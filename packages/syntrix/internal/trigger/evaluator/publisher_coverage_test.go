package evaluator

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	pubsubtesting "github.com/codetreker/syntrix/internal/core/pubsub/testing"
	"github.com/codetreker/syntrix/internal/trigger/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTaskPublisher(t *testing.T) {
	t.Run("with mock publisher", func(t *testing.T) {
		mockPub := pubsubtesting.NewMockPublisher()
		pub := NewTaskPublisher(mockPub, "TRIGGERS", nil)
		assert.NotNil(t, pub)
	})

	t.Run("with nil metrics uses noop", func(t *testing.T) {
		mockPub := pubsubtesting.NewMockPublisher()
		pub := NewTaskPublisher(mockPub, "TRIGGERS", nil)
		assert.NotNil(t, pub)
	})

	t.Run("with empty stream name uses default", func(t *testing.T) {
		mockPub := pubsubtesting.NewMockPublisher()
		pub := NewTaskPublisher(mockPub, "", nil)
		assert.NotNil(t, pub)
	})
}

func TestTaskPublisher_Publish(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mockPub := pubsubtesting.NewMockPublisher()
		pub := NewTaskPublisher(mockPub, "TRIGGERS", nil)

		task := &types.DeliveryTask{
			TriggerID:  "trigger-1",
			Database:   "testdb",
			Collection: "testcol",
			DocumentID: "doc-1",
		}

		err := pub.Publish(context.Background(), task)
		require.NoError(t, err)

		msgs := mockPub.Messages()
		require.Len(t, msgs, 1)
		// Subject should be: <database>.<collection>.<base64_encoded_doc_id>
		assert.Contains(t, msgs[0].Subject, "testdb.testcol.")
	})

	t.Run("publish error", func(t *testing.T) {
		mockPub := pubsubtesting.NewMockPublisher()
		mockPub.SetError(errors.New("publish failed"))
		pub := NewTaskPublisher(mockPub, "TRIGGERS", nil)

		task := &types.DeliveryTask{
			TriggerID:  "trigger-1",
			Database:   "testdb",
			Collection: "testcol",
			DocumentID: "doc-1",
		}

		err := pub.Publish(context.Background(), task)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "publish failed")
	})

	t.Run("long subject gets hashed", func(t *testing.T) {
		mockPub := pubsubtesting.NewMockPublisher()
		pub := NewTaskPublisher(mockPub, "TRIGGERS", nil)

		// Create a very long document ID that will cause subject > 1024
		longDocID := make([]byte, 2000)
		for i := range longDocID {
			longDocID[i] = 'a'
		}

		task := &types.DeliveryTask{
			TriggerID:  "trigger-1",
			Database:   "testdb",
			Collection: "testcol",
			DocumentID: string(longDocID),
		}

		err := pub.Publish(context.Background(), task)
		require.NoError(t, err)

		msgs := mockPub.Messages()
		require.Len(t, msgs, 1)
		assert.Contains(t, msgs[0].Subject, "hashed.")
		assert.True(t, task.SubjectHashed)
	})

	t.Run("message data is valid json", func(t *testing.T) {
		mockPub := pubsubtesting.NewMockPublisher()
		pub := NewTaskPublisher(mockPub, "TRIGGERS", nil)

		task := &types.DeliveryTask{
			TriggerID:  "trigger-1",
			Database:   "testdb",
			Collection: "testcol",
			DocumentID: "doc-1",
		}

		err := pub.Publish(context.Background(), task)
		require.NoError(t, err)

		msgs := mockPub.Messages()
		require.Len(t, msgs, 1)

		var decoded types.DeliveryTask
		err = json.Unmarshal(msgs[0].Data, &decoded)
		require.NoError(t, err)
		assert.Equal(t, task.TriggerID, decoded.TriggerID)
		assert.Equal(t, task.Database, decoded.Database)
		assert.Equal(t, task.Collection, decoded.Collection)
		assert.Equal(t, task.DocumentID, decoded.DocumentID)
	})
}

func TestTaskPublisher_Close(t *testing.T) {
	mockPub := pubsubtesting.NewMockPublisher()
	pub := NewTaskPublisher(mockPub, "TRIGGERS", nil)

	err := pub.Close()
	assert.NoError(t, err)
	assert.True(t, mockPub.IsClosed())
}
