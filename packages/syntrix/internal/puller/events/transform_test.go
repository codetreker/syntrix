package events

import (
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransform_Insert(t *testing.T) {
	pEvent := &PullerEvent{
		Change: &StoreChangeEvent{
			EventID:   "evt-1",
			Database:  "database1",
			OpType:    StoreOperationInsert,
			Timestamp: 12345,
			FullDocument: &storage.StoredDoc{
				Id:         "doc1",
				Collection: "users",
				Data:       map[string]interface{}{"name": "Alice"},
			},
		},
		Progress: "progress-1",
	}

	evt, err := Transform(pEvent)
	require.NoError(t, err)

	assert.Equal(t, "evt-1", evt.Id)
	assert.Equal(t, "database1", evt.Database)
	assert.Equal(t, EventCreate, evt.Type)
	assert.Equal(t, int64(12345), evt.Timestamp)
	assert.Equal(t, "progress-1", evt.Progress)
	assert.NotNil(t, evt.Document)
	assert.Equal(t, "doc1", evt.Document.Id)
}

func TestTransform_Update(t *testing.T) {
	pEvent := &PullerEvent{
		Change: &StoreChangeEvent{
			EventID:  "evt-2",
			Database: "database1",
			OpType:   StoreOperationUpdate,
			FullDocument: &storage.StoredDoc{
				Id:      "doc1",
				Deleted: false,
			},
			UpdateDesc: &UpdateDescription{
				UpdatedFields: map[string]interface{}{"name": "Bob"},
			},
		},
		Progress: "progress-2",
	}

	evt, err := Transform(pEvent)
	require.NoError(t, err)

	assert.Equal(t, EventUpdate, evt.Type)
	assert.NotNil(t, evt.Document)
	assert.NotNil(t, evt.UpdateDesc)
}

func TestTransform_Replace(t *testing.T) {
	pEvent := &PullerEvent{
		Change: &StoreChangeEvent{
			EventID:  "evt-3",
			Database: "database1",
			OpType:   StoreOperationReplace,
			FullDocument: &storage.StoredDoc{
				Id:      "doc1",
				Deleted: false,
			},
		},
		Progress: "progress-3",
	}

	evt, err := Transform(pEvent)
	require.NoError(t, err)

	// Replace transforms to EventCreate (recreation after soft delete)
	assert.Equal(t, EventCreate, evt.Type)
	assert.NotNil(t, evt.Document)
}

func TestTransform_SoftDelete_Update(t *testing.T) {
	pEvent := &PullerEvent{
		Change: &StoreChangeEvent{
			EventID:  "evt-4",
			Database: "database1",
			OpType:   StoreOperationUpdate,
			FullDocument: &storage.StoredDoc{
				Id:      "doc1",
				Deleted: true,
			},
		},
		Progress: "progress-4",
	}

	evt, err := Transform(pEvent)
	require.NoError(t, err)

	// Soft delete (UPDATE with Deleted=true) transforms to EventDelete
	assert.Equal(t, EventDelete, evt.Type)
	assert.NotNil(t, evt.Document)
	assert.True(t, evt.Document.Deleted)
}

func TestTransform_SoftDelete_Replace(t *testing.T) {
	pEvent := &PullerEvent{
		Change: &StoreChangeEvent{
			EventID:  "evt-5",
			Database: "database1",
			OpType:   StoreOperationReplace,
			FullDocument: &storage.StoredDoc{
				Id:      "doc1",
				Deleted: true,
			},
		},
		Progress: "progress-5",
	}

	evt, err := Transform(pEvent)
	require.NoError(t, err)

	// Soft delete via REPLACE also transforms to EventDelete
	assert.Equal(t, EventDelete, evt.Type)
}

func TestTransform_Delete_Ignored(t *testing.T) {
	pEvent := &PullerEvent{
		Change: &StoreChangeEvent{
			EventID:  "evt-6",
			Database: "database1",
			OpType:   StoreOperationDelete,
		},
		Progress: "progress-6",
	}

	_, err := Transform(pEvent)
	assert.ErrorIs(t, err, ErrDeleteOPIgnored)
}

func TestTransform_UnknownOpType(t *testing.T) {
	pEvent := &PullerEvent{
		Change: &StoreChangeEvent{
			EventID:  "evt-7",
			Database: "database1",
			OpType:   "unknown",
		},
		Progress: "progress-7",
	}

	_, err := Transform(pEvent)
	assert.ErrorIs(t, err, ErrUnknownOpType)
}
