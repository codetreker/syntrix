package wire

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

type jsonWindowEnvelope struct {
	ProtocolVersion  int               `json:"protocolVersion"`
	Mode             string            `json:"mode"`
	DatabaseIdentity string            `json:"databaseIdentity"`
	SourceHash       string            `json:"sourceHash"`
	RequestID        string            `json:"requestId"`
	GenerationID     string            `json:"generationId"`
	Complete         bool              `json:"complete"`
	EffectiveOrder   []model.Order     `json:"effectiveOrder"`
	Documents        []json.RawMessage `json:"documents"`
}

func windowEnvelope(page *types.ReplicationPullResponse) jsonWindowEnvelope {
	return jsonWindowEnvelope{ProtocolVersion: page.ProtocolVersion, Mode: page.Mode, DatabaseIdentity: page.DatabaseIdentity, SourceHash: page.SourceHash, RequestID: *page.RequestID, GenerationID: page.GenerationID, Complete: *page.Complete, EffectiveOrder: page.EffectiveOrder, Documents: []json.RawMessage{}}
}

func windowProtoEnvelope(page *types.ReplicationPullResponse) *pb.PullResponse {
	out := &pb.PullResponse{WireVersion: Version, ProtocolVersion: int32(page.ProtocolVersion), Mode: page.Mode, DatabaseIdentity: page.DatabaseIdentity, SourceHash: page.SourceHash, RequestId: page.RequestID, GenerationId: page.GenerationID, Complete: page.Complete}
	for _, order := range page.EffectiveOrder {
		out.EffectiveOrder = append(out.EffectiveOrder, &pb.OrderBy{Field: order.Field, Direction: order.Direction})
	}
	return out
}

func validateWindowEnvelope(page *types.ReplicationPullResponse) error {
	if page.ProtocolVersion != 1 || page.Mode != "replace" || page.RequestID == nil || page.Complete == nil || !*page.Complete || page.Checkpoint != "" || page.CaughtUp || page.BootstrapComplete || page.Phase != "" || len(page.Events) != 0 {
		return pullFailure(types.WatchInvalidEvent, "invalid replacement envelope")
	}
	for _, identity := range []string{page.DatabaseIdentity, page.SourceHash, page.GenerationID, *page.RequestID} {
		if identity == "" || !utf8.ValidString(identity) || strings.ContainsRune(identity, 0) {
			return pullFailure(types.WatchInvalidEvent, "invalid replacement identity")
		}
	}
	hash, err := hex.DecodeString(page.SourceHash)
	if err != nil || len(hash) != 32 || hex.EncodeToString(hash) != page.SourceHash {
		return pullFailure(types.WatchInvalidEvent, "invalid replacement source hash")
	}
	generation, err := uuid.Parse(page.GenerationID)
	if err != nil || generation == uuid.Nil || generation.String() != page.GenerationID {
		return pullFailure(types.WatchInvalidEvent, "invalid replacement generation")
	}
	if len(page.Documents) > MaxPullLimit {
		return model.ErrQueryWorkLimit
	}
	if len(page.EffectiveOrder) == 0 {
		return pullFailure(types.WatchInvalidEvent, "missing replacement order")
	}
	fields := map[string]bool{}
	for _, order := range page.EffectiveOrder {
		if order.Field == "" || !utf8.ValidString(order.Field) || strings.ContainsRune(order.Field, 0) || fields[order.Field] || (order.Direction != "asc" && order.Direction != "desc") {
			return pullFailure(types.WatchInvalidEvent, "invalid replacement order")
		}
		fields[order.Field] = true
	}
	seen := map[string]bool{}
	for _, doc := range page.Documents {
		if err := validatePullDocument(doc); err != nil {
			return err
		}
		if !utf8.ValidString(doc.GetID()) || !utf8.ValidString(doc.GetCollection()) {
			return pullFailure(types.WatchInvalidEvent, "invalid replacement document identity")
		}
		if deleted, _ := doc["deleted"].(bool); deleted || seen[doc.GetID()] {
			return pullFailure(types.WatchInvalidEvent, "replacement documents must be live and unique")
		}
		seen[doc.GetID()] = true
	}
	return nil
}

func encodeWindowPullPage(page *types.ReplicationPullResponse) (*pb.PullResponse, *jsonWindowEnvelope, error) {
	if err := validateWindowEnvelope(page); err != nil {
		return nil, nil, err
	}
	out := windowProtoEnvelope(page)
	envelope := windowEnvelope(page)
	jsonBytes, protoBytes := 0, 0
	for _, doc := range page.Documents {
		data, err := model.EncodeTypedValue(map[string]any(doc))
		if err != nil {
			return nil, nil, pullFailure(types.WatchInvalidEvent, "invalid replacement document encoding")
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
	return out, &envelope, nil
}

func decodeWindowPullPage(in *pb.PullResponse) (*types.ReplicationPullResponse, error) {
	if in.BootstrapComplete != nil {
		return nil, pullFailure(types.WatchInvalidEvent, "replacement contains event progress")
	}
	out := &types.ReplicationPullResponse{ProtocolVersion: int(in.ProtocolVersion), Mode: in.Mode, DatabaseIdentity: in.DatabaseIdentity, SourceHash: in.SourceHash, RequestID: in.RequestId, GenerationID: in.GenerationId, Complete: in.Complete, Checkpoint: in.Checkpoint, CaughtUp: in.CaughtUp, Phase: in.Phase, Documents: make([]model.Document, 0, len(in.Documents))}
	if len(in.Events) != 0 {
		return nil, pullFailure(types.WatchInvalidEvent, "replacement contains events")
	}
	for _, order := range in.EffectiveOrder {
		if order == nil {
			return nil, pullFailure(types.WatchInvalidEvent, "missing replacement order")
		}
		out.EffectiveOrder = append(out.EffectiveOrder, model.Order{Field: order.Field, Direction: order.Direction})
	}
	if len(in.Documents) > MaxPullLimit {
		return nil, model.ErrQueryWorkLimit
	}
	if err := validateWindowEnvelope(out); err != nil {
		return nil, err
	}
	for _, doc := range in.Documents {
		if doc == nil || !proto.Equal(doc, &pb.Document{Data: doc.Data}) {
			return nil, pullFailure(types.WatchInvalidEvent, "invalid replacement document metadata")
		}
		if err := ValidatePullSourceJSON(doc.Data); err != nil {
			return nil, pullFailure(types.WatchInvalidEvent, "invalid typed replacement document")
		}
		value, err := model.DecodeTypedValue(doc.Data)
		if err != nil {
			return nil, pullFailure(types.WatchInvalidEvent, "invalid typed replacement document")
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, pullFailure(types.WatchInvalidEvent, "replacement document is not an object")
		}
		out.Documents = append(out.Documents, model.Document(object))
	}
	if _, _, err := encodeWindowPullPage(out); err != nil {
		return nil, err
	}
	return out, nil
}
