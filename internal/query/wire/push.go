package wire

import (
	"fmt"
	"strings"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

var pushActions = map[types.PushAction]pb.PushAction{
	types.PushCreate: pb.PushAction_PUSH_ACTION_CREATE,
	types.PushUpdate: pb.PushAction_PUSH_ACTION_UPDATE,
	types.PushDelete: pb.PushAction_PUSH_ACTION_DELETE,
}

func EncodePushRequest(database string, req types.ReplicationPushRequest) (*pb.PushRequest, error) {
	out := &pb.PushRequest{Database: database, Collection: req.Collection}
	for i, c := range req.Changes {
		action, ok := pushActions[c.Action]
		if !ok || c.Doc == nil || (c.BaseVersion != nil && *c.BaseVersion < 0) {
			return nil, fmt.Errorf("%w: invalid push change %d", model.ErrInvalidQuery, i)
		}
		doc, err := encodePushDocument(c.Doc)
		if err != nil {
			return nil, fmt.Errorf("%w: change %d: %v", model.ErrInvalidQuery, i, err)
		}
		var condition *pb.PushCreateCondition
		switch c.CreateCondition {
		case "":
		case types.CreateIfAbsent:
			condition = pb.PushCreateCondition_PUSH_CREATE_CONDITION_ABSENT.Enum()
		case types.CreateIfTombstone:
			condition = pb.PushCreateCondition_PUSH_CREATE_CONDITION_TOMBSTONE.Enum()
		default:
			return nil, fmt.Errorf("%w: change %d has invalid create condition", model.ErrInvalidQuery, i)
		}
		out.Changes = append(out.Changes, &pb.PushChange{Action: action, Document: doc, BaseVersion: c.BaseVersion, CreateCondition: condition})
	}
	if proto.Size(out) > MaxGRPCBytes {
		return nil, fmt.Errorf("%w: encoded push request exceeds %d bytes", model.ErrInvalidQuery, MaxGRPCBytes)
	}
	return out, nil
}

func DecodePushRequest(req *pb.PushRequest) (types.ReplicationPushRequest, error) {
	if req == nil {
		return types.ReplicationPushRequest{}, fmt.Errorf("%w: missing push request", model.ErrInvalidQuery)
	}
	if proto.Size(req) > MaxGRPCBytes {
		return types.ReplicationPushRequest{}, fmt.Errorf("%w: encoded push request exceeds %d bytes", model.ErrInvalidQuery, MaxGRPCBytes)
	}
	out := types.ReplicationPushRequest{Collection: req.Collection}
	for i, c := range req.Changes {
		if c == nil || c.Document == nil || (c.BaseVersion != nil && *c.BaseVersion < 0) {
			return out, fmt.Errorf("%w: invalid push change %d", model.ErrInvalidQuery, i)
		}
		var action types.PushAction
		for key, value := range pushActions {
			if c.Action == value {
				action = key
			}
		}
		if action == "" {
			return out, fmt.Errorf("%w: invalid push action %d", model.ErrInvalidQuery, i)
		}
		doc, err := decodePushDocument(c.Document)
		if err != nil {
			return out, fmt.Errorf("%w: change %d: %v", model.ErrInvalidQuery, i, err)
		}
		var condition types.CreateCondition
		if c.CreateCondition != nil {
			switch *c.CreateCondition {
			case pb.PushCreateCondition_PUSH_CREATE_CONDITION_ABSENT:
				condition = types.CreateIfAbsent
			case pb.PushCreateCondition_PUSH_CREATE_CONDITION_TOMBSTONE:
				condition = types.CreateIfTombstone
			default:
				return out, fmt.Errorf("%w: change %d has invalid create condition", model.ErrInvalidQuery, i)
			}
		}
		out.Changes = append(out.Changes, types.ReplicationPushChange{Action: action, Doc: doc, BaseVersion: c.BaseVersion, CreateCondition: condition})
	}
	return out, nil
}

func encodePushDocument(doc *types.StoredDoc) (*pb.Document, error) {
	if doc == nil {
		return nil, nil
	}
	var value any
	if doc.Data != nil {
		value = doc.Data
	}
	data, err := model.EncodeTypedValue(value)
	if err != nil {
		return nil, err
	}
	return &pb.Document{Id: doc.Id, Database: doc.Database, Fullpath: doc.Fullpath, Collection: doc.Collection, CollectionHash: doc.CollectionHash, Parent: doc.Parent, UpdatedAt: doc.UpdatedAt, CreatedAt: doc.CreatedAt, Version: doc.Version, Data: data, Deleted: doc.Deleted}, nil
}

func decodePushDocument(doc *pb.Document) (*types.StoredDoc, error) {
	if doc == nil {
		return nil, nil
	}
	value, err := model.DecodeTypedValue(doc.Data)
	if err != nil {
		return nil, err
	}
	var data map[string]interface{}
	if value != nil {
		var ok bool
		data, ok = value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("push document data must be an object or null")
		}
	}
	return &types.StoredDoc{Id: doc.Id, Database: doc.Database, Fullpath: doc.Fullpath, Collection: doc.Collection, CollectionHash: doc.CollectionHash, Parent: doc.Parent, UpdatedAt: doc.UpdatedAt, CreatedAt: doc.CreatedAt, Version: doc.Version, Data: data, Deleted: doc.Deleted}, nil
}

func EncodePushResponse(database string, req types.ReplicationPushRequest, resp *types.ReplicationPushResponse) (*pb.PushResponse, error) {
	if err := ValidatePushResponse(database, req, resp); err != nil {
		return nil, err
	}
	out := &pb.PushResponse{}
	for _, c := range resp.Conflicts {
		doc, err := encodePushDocument(c.Current)
		if err != nil {
			return nil, fmt.Errorf("encode push conflict: %w", err)
		}
		out.Conflicts = append(out.Conflicts, &pb.PushConflict{ChangeIndex: int32(c.ChangeIndex), Id: c.ID, Reason: string(c.Reason), Current: doc})
	}
	if proto.Size(out) > MaxGRPCBytes {
		return nil, model.ErrQueryWorkLimit
	}
	return out, nil
}

func DecodePushResponse(database string, req types.ReplicationPushRequest, resp *pb.PushResponse) (*types.ReplicationPushResponse, error) {
	if resp == nil {
		return nil, fmt.Errorf("missing push response")
	}
	if proto.Size(resp) > MaxGRPCBytes {
		return nil, model.ErrQueryWorkLimit
	}
	out := &types.ReplicationPushResponse{}
	for _, c := range resp.Conflicts {
		if c == nil {
			return nil, fmt.Errorf("missing push conflict")
		}
		doc, err := decodePushDocument(c.Current)
		if err != nil {
			return nil, fmt.Errorf("decode push conflict: %w", err)
		}
		out.Conflicts = append(out.Conflicts, types.ReplicationPushConflict{ChangeIndex: int(c.ChangeIndex), ID: c.Id, Reason: types.PushConflictReason(c.Reason), Current: doc})
	}
	if err := ValidatePushResponse(database, req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidatePushResponse checks conflict identity and state against the submitted batch.
func ValidatePushResponse(database string, req types.ReplicationPushRequest, resp *types.ReplicationPushResponse) error {
	if resp == nil {
		return fmt.Errorf("missing push response")
	}
	previous := -1
	for _, c := range resp.Conflicts {
		if c.ChangeIndex <= previous || c.ChangeIndex >= len(req.Changes) {
			return fmt.Errorf("invalid push conflict index")
		}
		previous = c.ChangeIndex
		change := req.Changes[c.ChangeIndex]
		if change.Doc == nil {
			return fmt.Errorf("missing push request document")
		}
		path := change.Doc.Fullpath
		if path == "" {
			id, _ := change.Doc.Data["id"].(string)
			path = req.Collection + "/" + id
		}
		if c.ID == "" || path != req.Collection+"/"+c.ID {
			return fmt.Errorf("push conflict identity mismatch")
		}
		if c.Current != nil {
			id, err := types.LogicalDocumentID(c.Current)
			if err != nil || c.Current.Database != database || c.Current.Collection != req.Collection || id != c.ID || c.Current.Version < 0 {
				return fmt.Errorf("push conflict current identity mismatch")
			}
		}
		valid := false
		switch c.Reason {
		case types.PushMissing:
			valid = c.Current == nil
		case types.PushTombstoned:
			valid = c.Current != nil && c.Current.Deleted
		case types.PushVersionMismatch:
			valid = change.CreateCondition == "" && c.Current != nil && !c.Current.Deleted && change.BaseVersion != nil && c.Current.Version != *change.BaseVersion
		case types.PushAlreadyExists:
			valid = c.Current != nil && !c.Current.Deleted && (change.Action == types.PushCreate || (change.Action == types.PushUpdate && change.BaseVersion == nil))
		case types.PushPreconditionFailed:
			valid = change.CreateCondition == "" && c.Current != nil && !c.Current.Deleted && (change.BaseVersion == nil || c.Current.Version == *change.BaseVersion)
		}
		if !valid || strings.ContainsAny(c.ID, "/\x00") {
			return fmt.Errorf("inconsistent push conflict state")
		}
	}
	return nil
}
