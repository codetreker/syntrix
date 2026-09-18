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
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const MaxPullCursorBytes = 256 << 10
const MaxPullLimit = 1000

type jsonPullEnvelope struct {
	Documents  []json.RawMessage `json:"documents"`
	Checkpoint string            `json:"checkpoint"`
	CaughtUp   bool              `json:"caughtUp"`
}

func pullFailure(code types.WatchErrorCode, message string) error {
	return &types.WatchError{Code: code, Cause: errors.New(message)}
}

func validatePullEnvelope(page *types.ReplicationPullResponse) error {
	if page != nil && page.Mode == "replace" {
		return validateWindowEnvelope(page)
	}
	if page == nil || page.Checkpoint == "" || !utf8.ValidString(page.Checkpoint) {
		return pullFailure(types.WatchInvalidEvent, "missing or invalid pull response checkpoint")
	}
	if len(page.Checkpoint) > MaxPullCursorBytes || len(page.Documents) > MaxPullLimit {
		return model.ErrQueryWorkLimit
	}
	if page.ProtocolVersion != 0 {
		return validateSourceEnvelope(page)
	}
	if page.Mode != "" || page.DatabaseIdentity != "" || page.SourceHash != "" || page.GenerationID != "" || page.Phase != "" || page.BootstrapComplete || len(page.Events) != 0 || page.RequestID != nil || page.Complete != nil || len(page.EffectiveOrder) != 0 {
		return pullFailure(types.WatchInvalidEvent, "unexpected query replication envelope")
	}
	return nil
}

func validatePullDocument(doc model.Document) error {
	if doc == nil || doc.GetID() == "" || strings.ContainsAny(doc.GetID(), "/\x00") || doc.GetCollection() == "" {
		return pullFailure(types.WatchInvalidEvent, "pull document has invalid logical identity")
	}
	if value, exists := doc["deleted"]; exists {
		if _, ok := value.(bool); !ok {
			return pullFailure(types.WatchInvalidEvent, "pull document has invalid deletion metadata")
		}
	}
	return nil
}

// CheckPullPageSize accounts for documents in collection and window modes, and
// events in unbounded query mode. Totals include each JSON entry and protobuf
// field tag and length; the envelope and cursor are added here.
func CheckPullPageSize(page *types.ReplicationPullResponse, documentBytes, protobufDocumentBytes int) error {
	if err := validatePullEnvelope(page); err != nil {
		return err
	}
	if documentBytes < 0 || protobufDocumentBytes < 0 {
		return pullFailure(types.WatchInvalidEvent, "negative pull response encoded size")
	}
	var envelope []byte
	var err error
	count := len(page.Documents)
	protoOverhead := proto.Size(&pb.PullResponse{Checkpoint: page.Checkpoint, CaughtUp: page.CaughtUp, WireVersion: Version})
	if page.Mode == "replace" {
		envelope, err = json.Marshal(windowEnvelope(page))
		protoOverhead = proto.Size(windowProtoEnvelope(page))
	} else if page.ProtocolVersion != 0 {
		envelope, err = json.Marshal(sourceEnvelope(page))
		count = len(page.Events)
		protoOverhead = proto.Size(sourceProtoEnvelope(page))
	} else {
		envelope, err = json.Marshal(jsonPullEnvelope{Documents: []json.RawMessage{}, Checkpoint: page.Checkpoint, CaughtUp: page.CaughtUp})
	}
	if err != nil {
		return pullFailure(types.WatchInvalidEvent, "pull response envelope cannot be encoded")
	}
	jsonOverhead := len(envelope) + max(0, count-1)
	if documentBytes > MaxPageBytes-jsonOverhead || protobufDocumentBytes > MaxGRPCBytes-protoOverhead {
		return model.ErrQueryWorkLimit
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
			return nil, nil, pullFailure(types.WatchInvalidEvent, "pull document cannot be encoded")
		}
		encoded := &pb.Document{Data: data}
		jsonBytes += len(data)
		protoBytes += proto.Size(&pb.PullResponse{Documents: []*pb.Document{encoded}})
		if jsonBytes > MaxPageBytes || protoBytes > MaxGRPCBytes {
			return nil, nil, model.ErrQueryWorkLimit
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
	if page != nil && page.Mode == "replace" {
		_, envelope, err := encodeWindowPullPage(page)
		if err != nil {
			return nil, err
		}
		return json.Marshal(envelope)
	}
	if page != nil && page.ProtocolVersion != 0 {
		_, envelope, err := encodeSourcePullPage(page)
		if err != nil {
			return nil, err
		}
		return json.Marshal(envelope)
	}
	_, envelope, err := encodePullDocuments(page)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(envelope)
	if err != nil {
		return nil, pullFailure(types.WatchInvalidEvent, "pull response cannot be encoded")
	}
	return out, nil
}

func EncodePullPage(page *types.ReplicationPullResponse) (*pb.PullResponse, error) {
	if page != nil && page.Mode == "replace" {
		out, _, err := encodeWindowPullPage(page)
		return out, err
	}
	if page != nil && page.ProtocolVersion != 0 {
		out, _, err := encodeSourcePullPage(page)
		return out, err
	}
	out, _, err := encodePullDocuments(page)
	return out, err
}

func DecodePullPage(in *pb.PullResponse) (*types.ReplicationPullResponse, error) {
	if in == nil || in.WireVersion != Version {
		return nil, pullFailure(types.WatchInvalidEvent, "unsupported pull response version")
	}
	if proto.Size(in) > MaxGRPCBytes {
		return nil, model.ErrQueryWorkLimit
	}
	if in.Mode == "replace" {
		return decodeWindowPullPage(in)
	}
	if in.ProtocolVersion != 0 {
		return decodeSourcePullPage(in)
	}
	if in.Mode != "" || in.DatabaseIdentity != "" || in.SourceHash != "" || in.GenerationId != "" || in.Phase != "" || in.BootstrapComplete != nil || len(in.Events) != 0 || in.RequestId != nil || in.Complete != nil || len(in.EffectiveOrder) != 0 {
		return nil, pullFailure(types.WatchInvalidEvent, "unexpected query replication envelope")
	}
	out := &types.ReplicationPullResponse{Checkpoint: in.Checkpoint, CaughtUp: in.CaughtUp, Documents: make([]model.Document, 0, len(in.Documents))}
	if len(in.Documents) > MaxPullLimit {
		return nil, model.ErrQueryWorkLimit
	}
	if err := validatePullEnvelope(out); err != nil {
		return nil, err
	}
	for _, doc := range in.Documents {
		if doc == nil {
			return nil, pullFailure(types.WatchInvalidEvent, "missing pull document")
		}
		// Metadata lives inside the typed object; a second representation would
		// leave clients with conflicting document identities or versions.
		if !proto.Equal(doc, &pb.Document{Data: doc.Data}) {
			return nil, pullFailure(types.WatchInvalidEvent, "unexpected pull document metadata")
		}
		value, err := model.DecodeTypedValue(doc.Data)
		if err != nil {
			return nil, pullFailure(types.WatchInvalidEvent, "pull document has invalid typed encoding")
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, pullFailure(types.WatchInvalidEvent, "pull document is not an object")
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

func replicationStatusCode(code types.WatchErrorCode) (codes.Code, bool) {
	switch code {
	case types.WatchInvalidScope, types.WatchInvalidCheckpoint, types.WatchScopeMismatch:
		return codes.InvalidArgument, true
	case types.WatchSourceMismatch, types.WatchHistoryUnavailable, types.WatchPayloadUnavailable:
		return codes.FailedPrecondition, true
	case types.WatchUnsupported:
		return codes.Unimplemented, true
	case types.WatchSourceUnavailable:
		return codes.Unavailable, true
	case types.WatchPermissionDenied:
		return codes.PermissionDenied, true
	case types.WatchInvalidEvent:
		return codes.Internal, true
	default:
		return codes.Internal, false
	}
}

var replicationDomainErrors = []struct {
	err    error
	code   codes.Code
	reason string
}{
	{types.ErrReplicationWindowIncomplete, codes.Unavailable, "REPLICATION_WINDOW_INCOMPLETE"},
	{indexer.ErrNoMatchingIndex, codes.FailedPrecondition, "NO_MATCHING_INDEX"},
	{indexer.ErrIndexNotReady, codes.Unavailable, "INDEX_NOT_READY"},
	{indexer.ErrIndexRebuilding, codes.Unavailable, "INDEX_REBUILDING"},
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
	if errors.Is(err, types.ErrInvalidReplicationSource) {
		st, _ := status.New(codes.InvalidArgument, "invalid replication source").WithDetails(&errdetails.ErrorInfo{Domain: "syntrix.replication", Reason: "INVALID_REPLICATION_SOURCE"})
		return st.Err()
	}
	if errors.Is(err, model.ErrQueryWorkLimit) {
		st, _ := status.New(codes.ResourceExhausted, "pull page exceeds budget").WithDetails(&errdetails.ErrorInfo{Domain: "syntrix.replication", Reason: "PAGE_BUDGET"})
		return st.Err()
	}
	for _, item := range replicationDomainErrors {
		if errors.Is(err, item.err) {
			st, _ := status.New(item.code, item.err.Error()).WithDetails(&errdetails.ErrorInfo{Domain: "syntrix.replication", Reason: item.reason})
			return st.Err()
		}
	}
	var failure *types.WatchError
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
			if info.Reason == "INVALID_REPLICATION_SOURCE" && st.Code() == codes.InvalidArgument {
				return types.ErrInvalidReplicationSource
			}
			if info.Reason == "PAGE_BUDGET" && st.Code() == codes.ResourceExhausted {
				return model.ErrQueryWorkLimit
			}
			for _, item := range replicationDomainErrors {
				if info.Reason == item.reason && st.Code() == item.code {
					return item.err
				}
			}
			code := types.WatchErrorCode(info.Reason)
			if expected, known := replicationStatusCode(code); known && st.Code() == expected {
				return pullFailure(code, "remote replication request failed")
			}
		}
		if st.Code() == codes.Unavailable {
			return pullFailure(types.WatchSourceUnavailable, "remote replication source unavailable")
		}
	}
	return pullFailure(types.WatchInvalidEvent, fmt.Sprintf("remote replication request failed (%s)", status.Code(err)))
}
