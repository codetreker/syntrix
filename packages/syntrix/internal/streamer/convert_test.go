package streamer

import (
	"testing"

	pb "github.com/codetreker/syntrix/api/gen/streamer/v1"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/pkg/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Proto to Domain conversions ---

func TestProtoToEventDelivery(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := protoToEventDelivery(nil)
		assert.Nil(t, result)
	})

	t.Run("with event", func(t *testing.T) {
		result := protoToEventDelivery(&pb.EventDelivery{
			SubscriptionIds: []string{"sub1", "sub2"},
			Event: &pb.StreamerEvent{
				EventId:    "evt1",
				Database:   "database1",
				Collection: "users",
				DocumentId: "doc1",
				Operation:  pb.OperationType_OPERATION_TYPE_INSERT,
			},
		})
		require.NotNil(t, result)
		assert.Equal(t, []string{"sub1", "sub2"}, result.SubscriptionIDs)
		require.NotNil(t, result.Event)
		assert.Equal(t, "evt1", result.Event.EventID)
	})
}

func TestProtoToOperationType(t *testing.T) {
	tests := []struct {
		input    pb.OperationType
		expected OperationType
	}{
		{pb.OperationType_OPERATION_TYPE_INSERT, OperationInsert},
		{pb.OperationType_OPERATION_TYPE_UPDATE, OperationUpdate},
		{pb.OperationType_OPERATION_TYPE_DELETE, OperationDelete},
		{pb.OperationType_OPERATION_TYPE_UNSPECIFIED, OperationUnspecified},
		{pb.OperationType(999), OperationUnspecified},
	}

	for _, tt := range tests {
		t.Run(tt.input.String(), func(t *testing.T) {
			result := protoToOperationType(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// --- Domain to Proto conversions ---

func TestFilterToProto(t *testing.T) {
	result := filterToProto(model.Filter{
		Field: "status",
		Op:    model.OpEq,
		Value: "active",
	})
	assert.Equal(t, "status", result.Field)
	assert.Equal(t, "==", result.Op)
	assert.Equal(t, "active", result.Value.GetStringValue())
}

func TestAnyToProtoValue(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		result := anyToProtoValue(nil)
		assert.Nil(t, result)
	})

	t.Run("string", func(t *testing.T) {
		result := anyToProtoValue("hello")
		assert.Equal(t, "hello", result.GetStringValue())
	})

	t.Run("int", func(t *testing.T) {
		result := anyToProtoValue(42)
		assert.Equal(t, int64(42), result.GetIntValue())
	})

	t.Run("int32", func(t *testing.T) {
		result := anyToProtoValue(int32(42))
		assert.Equal(t, int64(42), result.GetIntValue())
	})

	t.Run("int64", func(t *testing.T) {
		result := anyToProtoValue(int64(42))
		assert.Equal(t, int64(42), result.GetIntValue())
	})

	t.Run("float32", func(t *testing.T) {
		result := anyToProtoValue(float32(3.14))
		assert.InDelta(t, 3.14, result.GetDoubleValue(), 0.001)
	})

	t.Run("float64", func(t *testing.T) {
		result := anyToProtoValue(float64(3.14))
		assert.Equal(t, 3.14, result.GetDoubleValue())
	})

	t.Run("bool", func(t *testing.T) {
		result := anyToProtoValue(true)
		assert.True(t, result.GetBoolValue())
	})

	t.Run("list", func(t *testing.T) {
		result := anyToProtoValue([]any{"a", int64(1)})
		list := result.GetListValue()
		require.NotNil(t, list)
		assert.Len(t, list.Values, 2)
	})

	t.Run("unknown type", func(t *testing.T) {
		result := anyToProtoValue(struct{}{})
		assert.Nil(t, result)
	})
}

func TestEventToProto(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := eventToProto(nil)
		assert.Nil(t, result)
	})

	t.Run("with document", func(t *testing.T) {
		result := eventToProto(&Event{
			EventID:    "evt1",
			Database:   "database1",
			Collection: "users",
			DocumentID: "doc1",
			Operation:  OperationInsert,
			Document: model.Document{
				"id":         "doc1",
				"collection": "users",
				"name":       "Alice",
			},
			Timestamp: 12345,
		})
		require.NotNil(t, result)
		assert.Equal(t, "evt1", result.EventId)
		assert.Equal(t, pb.OperationType_OPERATION_TYPE_INSERT, result.Operation)
		assert.NotEmpty(t, result.DocumentData)
	})

	t.Run("without document", func(t *testing.T) {
		result := eventToProto(&Event{
			EventID:   "evt1",
			Operation: OperationDelete,
		})
		require.NotNil(t, result)
		assert.Empty(t, result.DocumentData)
	})
}

func TestOperationTypeToProto(t *testing.T) {
	tests := []struct {
		input    OperationType
		expected pb.OperationType
	}{
		{OperationInsert, pb.OperationType_OPERATION_TYPE_INSERT},
		{OperationUpdate, pb.OperationType_OPERATION_TYPE_UPDATE},
		{OperationDelete, pb.OperationType_OPERATION_TYPE_DELETE},
		{OperationUnspecified, pb.OperationType_OPERATION_TYPE_UNSPECIFIED},
	}

	for _, tt := range tests {
		t.Run(tt.input.String(), func(t *testing.T) {
			result := operationTypeToProto(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestEventDeliveryToProto(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := eventDeliveryToProto(nil)
		assert.Nil(t, result)
	})

	t.Run("with delivery", func(t *testing.T) {
		result := eventDeliveryToProto(&EventDelivery{
			SubscriptionIDs: []string{"sub1"},
			Event: &Event{
				EventID:   "evt1",
				Operation: OperationUpdate,
			},
		})
		require.NotNil(t, result)
		assert.Equal(t, []string{"sub1"}, result.SubscriptionIds)
	})
}

func TestProtoToEvent(t *testing.T) {
	t.Run("nil input", func(t *testing.T) {
		result := protoToEvent(nil)
		assert.Nil(t, result)
	})

	t.Run("with document data", func(t *testing.T) {
		result := protoToEvent(&pb.StreamerEvent{
			EventId:      "evt1",
			Database:     "database1",
			Collection:   "users",
			DocumentId:   "doc1",
			Operation:    pb.OperationType_OPERATION_TYPE_INSERT,
			DocumentData: []byte(`{"name":"Alice","age":30}`),
			Timestamp:    12345,
		})
		require.NotNil(t, result)
		assert.Equal(t, "evt1", result.EventID)
		assert.Equal(t, "database1", result.Database)
		assert.Equal(t, "users", result.Collection)
		assert.Equal(t, "doc1", result.DocumentID)
		assert.Equal(t, OperationInsert, result.Operation)
		assert.Equal(t, int64(12345), result.Timestamp)
		require.NotNil(t, result.Document)
		assert.Equal(t, "doc1", result.Document["id"])
		assert.Equal(t, "users", result.Document["collection"])
		assert.Equal(t, "Alice", result.Document["name"])
		assert.Equal(t, float64(30), result.Document["age"])
	})

	t.Run("with empty document data", func(t *testing.T) {
		result := protoToEvent(&pb.StreamerEvent{
			EventId:      "evt2",
			DocumentData: []byte{},
		})
		require.NotNil(t, result)
		assert.Nil(t, result.Document)
	})

	t.Run("with invalid JSON document data", func(t *testing.T) {
		result := protoToEvent(&pb.StreamerEvent{
			EventId:      "evt3",
			DocumentData: []byte(`{invalid json}`),
		})
		require.NotNil(t, result)
		// Document should be nil when JSON parsing fails
		assert.Nil(t, result.Document)
	})
}

func TestProtoEventToDelivery(t *testing.T) {
	result := protoEventToDelivery(&pb.StreamerEvent{
		EventId:    "evt1",
		Database:   "database1",
		Collection: "users",
		DocumentId: "doc1",
		Operation:  pb.OperationType_OPERATION_TYPE_UPDATE,
	}, []string{"sub1", "sub2"})

	require.NotNil(t, result)
	assert.Equal(t, []string{"sub1", "sub2"}, result.SubscriptionIDs)
	require.NotNil(t, result.Event)
	assert.Equal(t, "evt1", result.Event.EventID)
	assert.Equal(t, OperationUpdate, result.Event.Operation)
}

func TestEventTypeToOperationType(t *testing.T) {
	tests := []struct {
		name     string
		input    events.EventType
		expected OperationType
	}{
		{"create", events.EventCreate, OperationInsert},
		{"update", events.EventUpdate, OperationUpdate},
		{"delete", events.EventDelete, OperationDelete},
		{"unknown", events.EventType("unknown"), OperationUnspecified},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := eventTypeToOperationType(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
