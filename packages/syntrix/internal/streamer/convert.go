package streamer

import (
	"encoding/json"

	pb "github.com/codetreker/syntrix/api/gen/streamer/v1"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/pkg/model"
)

// --- Proto to Domain conversions ---

// protoToEventDelivery converts a proto EventDelivery to domain type.
func protoToEventDelivery(d *pb.EventDelivery) *EventDelivery {
	if d == nil {
		return nil
	}
	return &EventDelivery{
		SubscriptionIDs: d.SubscriptionIds,
		Event:           protoToEvent(d.Event),
	}
}

// protoToEvent converts a proto StreamerEvent to domain Event.
// The Document field is returned as model.Document (user-facing format).
func protoToEvent(e *pb.StreamerEvent) *Event {
	if e == nil {
		return nil
	}

	var doc model.Document
	if len(e.DocumentData) > 0 {
		var data map[string]interface{}
		if err := json.Unmarshal(e.DocumentData, &data); err == nil {
			// Build model.Document from proto data
			// Proto DocumentData contains the user data fields
			doc = make(model.Document)
			for k, v := range data {
				doc[k] = v
			}
			// Add system fields
			doc["id"] = e.DocumentId
			doc["collection"] = e.Collection
		}
	}

	return &Event{
		EventID:    e.EventId,
		Database:   e.Database,
		Collection: e.Collection,
		DocumentID: e.DocumentId,
		Operation:  protoToOperationType(e.Operation),
		Document:   doc,
		Timestamp:  e.Timestamp,
	}
}

// protoToOperationType converts proto OperationType to domain type.
func protoToOperationType(op pb.OperationType) OperationType {
	switch op {
	case pb.OperationType_OPERATION_TYPE_INSERT:
		return OperationInsert
	case pb.OperationType_OPERATION_TYPE_UPDATE:
		return OperationUpdate
	case pb.OperationType_OPERATION_TYPE_DELETE:
		return OperationDelete
	default:
		return OperationUnspecified
	}
}

// --- Domain to Proto conversions ---

// filtersToProto converts a slice of domain Filters to proto.
func filtersToProto(filters []model.Filter) []*pb.Filter {
	result := make([]*pb.Filter, 0, len(filters))
	for _, f := range filters {
		result = append(result, filterToProto(f))
	}
	return result
}

// filterToProto converts domain Filter to proto.
func filterToProto(f model.Filter) *pb.Filter {
	return &pb.Filter{
		Field: f.Field,
		Op:    string(f.Op),
		Value: anyToProtoValue(f.Value),
	}
}

// anyToProtoValue converts Go any type to proto Value.
func anyToProtoValue(v any) *pb.Value {
	if v == nil {
		return nil
	}
	switch val := v.(type) {
	case string:
		return &pb.Value{Kind: &pb.Value_StringValue{StringValue: val}}
	case int:
		return &pb.Value{Kind: &pb.Value_IntValue{IntValue: int64(val)}}
	case int32:
		return &pb.Value{Kind: &pb.Value_IntValue{IntValue: int64(val)}}
	case int64:
		return &pb.Value{Kind: &pb.Value_IntValue{IntValue: val}}
	case float32:
		return &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: float64(val)}}
	case float64:
		return &pb.Value{Kind: &pb.Value_DoubleValue{DoubleValue: val}}
	case bool:
		return &pb.Value{Kind: &pb.Value_BoolValue{BoolValue: val}}
	case []any:
		values := make([]*pb.Value, 0, len(val))
		for _, item := range val {
			values = append(values, anyToProtoValue(item))
		}
		return &pb.Value{Kind: &pb.Value_ListValue{ListValue: &pb.ListValue{Values: values}}}
	default:
		return nil
	}
}

// eventToProto converts domain Event to proto StreamerEvent.
// model.Document is serialized to DocumentData, excluding system fields.
func eventToProto(e *Event) *pb.StreamerEvent {
	if e == nil {
		return nil
	}

	var docData []byte
	if e.Document != nil {
		// Extract user data fields (exclude system fields for wire format)
		userData := make(map[string]interface{})
		for k, v := range e.Document {
			// System fields are handled separately in proto message
			if k != "id" && k != "collection" {
				userData[k] = v
			}
		}
		if len(userData) > 0 {
			docData, _ = json.Marshal(userData)
		}
	}

	return &pb.StreamerEvent{
		EventId:      e.EventID,
		Database:     e.Database,
		Collection:   e.Collection,
		DocumentId:   e.DocumentID,
		Operation:    operationTypeToProto(e.Operation),
		DocumentData: docData,
		Timestamp:    e.Timestamp,
	}
}

// operationTypeToProto converts domain OperationType to proto.
func operationTypeToProto(op OperationType) pb.OperationType {
	switch op {
	case OperationInsert:
		return pb.OperationType_OPERATION_TYPE_INSERT
	case OperationUpdate:
		return pb.OperationType_OPERATION_TYPE_UPDATE
	case OperationDelete:
		return pb.OperationType_OPERATION_TYPE_DELETE
	default:
		return pb.OperationType_OPERATION_TYPE_UNSPECIFIED
	}
}

// eventDeliveryToProto converts domain EventDelivery to proto.
func eventDeliveryToProto(d *EventDelivery) *pb.EventDelivery {
	if d == nil {
		return nil
	}
	return &pb.EventDelivery{
		SubscriptionIds: d.SubscriptionIDs,
		Event:           eventToProto(d.Event),
	}
}

// protoEventToDelivery creates a domain EventDelivery directly from a proto StreamerEvent.
// This avoids unnecessary proto->proto->domain conversion.
func protoEventToDelivery(proto *pb.StreamerEvent, subscriptionIDs []string) *EventDelivery {
	return &EventDelivery{
		SubscriptionIDs: subscriptionIDs,
		Event:           protoToEvent(proto),
	}
}

// syntrixEventToDelivery creates a domain EventDelivery from a SyntrixChangeEvent.
// This is used when processing events internally without going through proto.
func syntrixEventToDelivery(event events.SyntrixChangeEvent, doc model.Document, subscriptionIDs []string) *EventDelivery {
	return &EventDelivery{
		SubscriptionIDs: subscriptionIDs,
		Event: &Event{
			EventID:    event.Id,
			Database:   event.Database,
			Collection: event.Document.Collection,
			DocumentID: doc.GetID(),
			Operation:  eventTypeToOperationType(event.Type),
			Document:   doc,
			Timestamp:  event.Timestamp,
		},
	}
}

// eventTypeToOperationType converts events.EventType to domain OperationType.
func eventTypeToOperationType(t events.EventType) OperationType {
	switch t {
	case events.EventCreate:
		return OperationInsert
	case events.EventUpdate:
		return OperationUpdate
	case events.EventDelete:
		return OperationDelete
	default:
		return OperationUnspecified
	}
}
