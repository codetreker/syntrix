package wire

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const MaxPullCursorBytes = 256 << 10

type jsonPullEnvelope struct {
	Documents  []json.RawMessage `json:"documents"`
	Checkpoint string            `json:"checkpoint"`
	CaughtUp   bool              `json:"caughtUp"`
}

func pullFailure(code types.ReplicationErrorCode, message string) error {
	return &types.ReplicationError{Code: code, Cause: errors.New(message)}
}

func validatePullEnvelope(page *types.ReplicationPullResponse) error {
	if page == nil || page.Checkpoint == "" || !utf8.ValidString(page.Checkpoint) {
		return pullFailure(types.ReplicationInvalidState, "missing or invalid pull response checkpoint")
	}
	if len(page.Checkpoint) > MaxPullCursorBytes || len(page.Documents) > types.MaxReplicationLimit {
		return pullFailure(types.ReplicationBudgetExceeded, "pull response exceeds cursor or document count limit")
	}
	return nil
}

func validatePullDocument(doc model.Document) error {
	if doc == nil || doc.GetID() == "" || strings.ContainsAny(doc.GetID(), "/\x00") || doc.GetCollection() == "" {
		return pullFailure(types.ReplicationInvalidState, "pull document has invalid logical identity")
	}
	if value, exists := doc["deleted"]; exists {
		if _, ok := value.(bool); !ok {
			return pullFailure(types.ReplicationInvalidState, "pull document has invalid deletion metadata")
		}
	}
	return nil
}

// CheckPullPageSize accounts for already encoded documents without encoding them
// again. documentBytes is the sum of compact typed JSON lengths;
// protobufDocumentBytes includes each repeated Document field's tag and length.
func CheckPullPageSize(page *types.ReplicationPullResponse, documentBytes, protobufDocumentBytes int) error {
	if err := validatePullEnvelope(page); err != nil {
		return err
	}
	if documentBytes < 0 || protobufDocumentBytes < 0 {
		return pullFailure(types.ReplicationInvalidState, "negative pull response encoded size")
	}
	envelope, err := json.Marshal(jsonPullEnvelope{Documents: []json.RawMessage{}, Checkpoint: page.Checkpoint, CaughtUp: page.CaughtUp})
	if err != nil {
		return pullFailure(types.ReplicationInvalidState, "pull response envelope cannot be encoded")
	}
	jsonOverhead := len(envelope) + max(0, len(page.Documents)-1)
	protoOverhead := proto.Size(&pb.PullResponse{Checkpoint: page.Checkpoint, CaughtUp: page.CaughtUp, WireVersion: Version})
	if documentBytes > MaxPageBytes-jsonOverhead || protobufDocumentBytes > MaxGRPCBytes-protoOverhead {
		return pullFailure(types.ReplicationBudgetExceeded, "pull response exceeds transport page limit")
	}
	return nil
}

func encodePullDocuments(page *types.ReplicationPullResponse) (*pb.PullResponse, *jsonPullEnvelope, error) {
	if err := validatePullEnvelope(page); err != nil {
		return nil, nil, err
	}
	out := &pb.PullResponse{Checkpoint: page.Checkpoint, CaughtUp: page.CaughtUp, WireVersion: Version, Documents: make([]*pb.Document, 0, len(page.Documents))}
	envelope := &jsonPullEnvelope{Checkpoint: page.Checkpoint, CaughtUp: page.CaughtUp, Documents: make([]json.RawMessage, 0, len(page.Documents))}
	jsonBytes, protoBytes := 0, 0
	for _, doc := range page.Documents {
		if err := validatePullDocument(doc); err != nil {
			return nil, nil, err
		}
		data, err := model.EncodeTypedValue(map[string]any(doc))
		if err != nil {
			return nil, nil, pullFailure(types.ReplicationInvalidState, "pull document cannot be encoded")
		}
		encoded := &pb.Document{Data: data}
		jsonBytes += len(data)
		protoBytes += proto.Size(&pb.PullResponse{Documents: []*pb.Document{encoded}})
		if jsonBytes > MaxPageBytes || protoBytes > MaxGRPCBytes {
			return nil, nil, pullFailure(types.ReplicationBudgetExceeded, "pull response exceeds transport page limit")
		}
		out.Documents = append(out.Documents, encoded)
		envelope.Documents = append(envelope.Documents, data)
	}
	if err := CheckPullPageSize(page, jsonBytes, protoBytes); err != nil {
		return nil, nil, err
	}
	return out, envelope, nil
}

func EncodeJSONPullPage(page *types.ReplicationPullResponse) ([]byte, error) {
	_, envelope, err := encodePullDocuments(page)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, pullFailure(types.ReplicationInvalidState, "pull response cannot be encoded")
	}
	return out, nil
}

func EncodePullPage(page *types.ReplicationPullResponse) (*pb.PullResponse, error) {
	out, _, err := encodePullDocuments(page)
	return out, err
}

func DecodePullPage(in *pb.PullResponse) (*types.ReplicationPullResponse, error) {
	if in == nil || in.WireVersion != Version {
		return nil, pullFailure(types.ReplicationInvalidState, "unsupported pull response version")
	}
	if proto.Size(in) > MaxGRPCBytes {
		return nil, pullFailure(types.ReplicationBudgetExceeded, "pull response exceeds transport page limit")
	}
	out := &types.ReplicationPullResponse{Checkpoint: in.Checkpoint, CaughtUp: in.CaughtUp, Documents: make([]model.Document, 0, len(in.Documents))}
	if len(in.Documents) > types.MaxReplicationLimit {
		return nil, pullFailure(types.ReplicationBudgetExceeded, "pull response exceeds document count limit")
	}
	if err := validatePullEnvelope(out); err != nil {
		return nil, err
	}
	for _, doc := range in.Documents {
		if doc == nil {
			return nil, pullFailure(types.ReplicationInvalidState, "missing pull document")
		}
		// Metadata lives inside the typed object; a second representation would
		// leave clients with conflicting document identities or versions.
		if !proto.Equal(doc, &pb.Document{Data: doc.Data}) {
			return nil, pullFailure(types.ReplicationInvalidState, "unexpected pull document metadata")
		}
		value, err := model.DecodeTypedValue(doc.Data)
		if err != nil {
			return nil, pullFailure(types.ReplicationInvalidState, "pull document has invalid typed encoding")
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, pullFailure(types.ReplicationInvalidState, "pull document is not an object")
		}
		if err := validatePullDocument(model.Document(object)); err != nil {
			return nil, err
		}
		out.Documents = append(out.Documents, model.Document(object))
	}
	if _, _, err := encodePullDocuments(out); err != nil {
		return nil, err
	}
	return out, nil
}

func replicationStatusCode(code types.ReplicationErrorCode) (codes.Code, bool) {
	switch code {
	case types.ReplicationInvalidCursor, types.ReplicationScopeMismatch:
		return codes.InvalidArgument, true
	case types.ReplicationSourceMismatch, types.ReplicationHistoryUnavailable, types.ReplicationIdentityUnavailable:
		return codes.FailedPrecondition, true
	case types.ReplicationUnsupported:
		return codes.Unimplemented, true
	case types.ReplicationUnavailable:
		return codes.Unavailable, true
	case types.ReplicationPermissionDenied:
		return codes.PermissionDenied, true
	case types.ReplicationBudgetExceeded:
		return codes.ResourceExhausted, true
	case types.ReplicationInvalidState:
		return codes.Internal, true
	default:
		return codes.Internal, false
	}
}

// ReplicationErrorToStatus exports recovery categories without exposing source
// causes, document values, or opaque checkpoints in the transport status.
func ReplicationErrorToStatus(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "replication request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "replication request deadline exceeded")
	}
	var failure *types.ReplicationError
	if errors.As(err, &failure) {
		if code, known := replicationStatusCode(failure.Code); known {
			st, detailErr := status.New(code, "replication "+string(failure.Code)).WithDetails(&errdetails.ErrorInfo{Domain: "syntrix.replication", Reason: string(failure.Code)})
			if detailErr == nil {
				return st.Err()
			}
		}
	}
	return status.Error(codes.Internal, "replication request failed")
}

func ReplicationStatusToError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	st, ok := status.FromError(err)
	if ok {
		for _, detail := range st.Details() {
			info, ok := detail.(*errdetails.ErrorInfo)
			if !ok || info.Domain != "syntrix.replication" {
				continue
			}
			code := types.ReplicationErrorCode(info.Reason)
			if expected, known := replicationStatusCode(code); known && st.Code() == expected {
				return pullFailure(code, "remote replication request failed")
			}
		}
		if st.Code() == codes.Unavailable {
			return pullFailure(types.ReplicationUnavailable, "remote replication source unavailable")
		}
	}
	return pullFailure(types.ReplicationInvalidState, fmt.Sprintf("remote replication request failed (%s)", status.Code(err)))
}
