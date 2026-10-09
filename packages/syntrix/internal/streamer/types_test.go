package streamer

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOperationType_String(t *testing.T) {
	tests := []struct {
		op       OperationType
		expected string
	}{
		{OperationUnspecified, "unspecified"},
		{OperationInsert, "insert"},
		{OperationUpdate, "update"},
		{OperationDelete, "delete"},
		{OperationType(999), "unspecified"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.op.String())
		})
	}
}

func TestEvent(t *testing.T) {
	t.Run("event with model.Document", func(t *testing.T) {
		evt := Event{
			EventID:    "evt1",
			Database:   "database1",
			Collection: "users",
			DocumentID: "doc1",
			Operation:  OperationInsert,
			Document: map[string]interface{}{
				"id":         "doc1",
				"collection": "users",
				"name":       "Alice",
				"version":    int64(1),
			},
			Timestamp: 12345,
		}
		assert.Equal(t, "evt1", evt.EventID)
		assert.Equal(t, "doc1", evt.Document.GetID())
		assert.Equal(t, "users", evt.Document.GetCollection())
	})

	t.Run("event without document (delete)", func(t *testing.T) {
		evt := Event{
			EventID:    "evt2",
			Database:   "database1",
			Collection: "users",
			DocumentID: "doc1",
			Operation:  OperationDelete,
			Document:   nil,
			Timestamp:  12345,
		}
		assert.Nil(t, evt.Document)
		assert.Equal(t, OperationDelete, evt.Operation)
	})
}

func TestEventDelivery(t *testing.T) {
	t.Run("delivery with multiple subscription IDs", func(t *testing.T) {
		delivery := EventDelivery{
			SubscriptionIDs: []string{"sub1", "sub2", "sub3"},
			Event: &Event{
				EventID:   "evt1",
				Operation: OperationUpdate,
			},
		}
		assert.Len(t, delivery.SubscriptionIDs, 3)
		assert.NotNil(t, delivery.Event)
	})
}
