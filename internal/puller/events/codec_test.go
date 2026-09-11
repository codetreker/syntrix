package events

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
)

func TestEventCodecPreservesIdentityAndTypedValues(t *testing.T) {
	t.Parallel()
	for _, deleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "tombstone"}[deleted], func(t *testing.T) {
			data := map[string]any{"id": "business-value", "before": int64(9007199254740991), "at": int64(9007199254740992), "after": int64(9007199254740993), "max": int64(math.MaxInt64), "float": float64(1.5), "nested": []any{map[string]any{"n": int64(math.MinInt64)}}}
			if deleted {
				data = map[string]any{}
			}
			doc := storage.NewStoredDoc("database", "parents/p1/children", "logical-id", nil)
			doc.Data, doc.Deleted = data, deleted
			txn := int64(math.MaxInt64)
			evt := &StoreChangeEvent{EventID: "source-event", Database: "database", MgoColl: "physical-collection", MgoDocID: "physical-id", OpType: StoreOperationUpdate, FullDocument: &doc, UpdateDesc: &UpdateDescription{UpdatedFields: map[string]any{"data.n": int64(9007199254740993)}, RemovedFields: []string{"old"}, TruncatedArrays: []TruncatedArray{{Field: "list", NewSize: 2}}}, ClusterTime: ClusterTime{T: 1, I: 2}, TxnNumber: &txn, Timestamp: 123, Backend: "backend"}
			encoded, err := MarshalEvent(evt)
			require.NoError(t, err)
			decoded, err := UnmarshalEvent(encoded)
			require.NoError(t, err)
			require.Equal(t, evt, decoded)
			require.Equal(t, evt.BufferKey(), decoded.BufferKey())
			public, err := json.Marshal(doc)
			require.NoError(t, err)
			require.NotContains(t, string(public), "fullpath")
		})
	}
}

func TestEventCodecRejectsIncompatibleOrInvalidDocuments(t *testing.T) {
	t.Parallel()
	_, err := UnmarshalEvent([]byte(`{"eventId":"legacy","fullDoc":{"data":{"n":9007199254740993}}}`))
	require.ErrorContains(t, err, "unsupported event wire version")
	_, err = UnmarshalDocument([]byte(`{"data":{}}`))
	require.ErrorContains(t, err, "unsupported document wire version")
	_, err = MarshalDocument(&storage.StoredDoc{Database: "db", Collection: "items", Data: map[string]any{"id": "guessed"}})
	require.Error(t, err)
	doc := storage.NewStoredDoc("db", "items", "real", nil)
	encoded, err := MarshalDocument(&doc)
	require.NoError(t, err)
	var wire documentWire
	require.NoError(t, json.Unmarshal(encoded, &wire))
	wire.LogicalID = "business-id"
	encoded, err = json.Marshal(wire)
	require.NoError(t, err)
	_, err = UnmarshalDocument(encoded)
	require.ErrorContains(t, err, "logical identity disagrees")
	doc.Data["unsupported"] = math.NaN()
	_, err = MarshalDocument(&doc)
	require.Error(t, err)
}

func TestEventCodecPhysicalDeleteHasNoDocument(t *testing.T) {
	t.Parallel()
	evt := &StoreChangeEvent{EventID: "physical-delete", OpType: StoreOperationDelete, MgoDocID: "physical-id"}
	encoded, err := MarshalEvent(evt)
	require.NoError(t, err)
	decoded, err := UnmarshalEvent(encoded)
	require.NoError(t, err)
	require.Equal(t, evt, decoded)
}

func TestEventCodecSizeAdmission(t *testing.T) {
	evt := &StoreChangeEvent{EventID: "bounded"}
	header, err := json.Marshal(eventEnvelope(evt))
	require.NoError(t, err)
	storage := make([]byte, MaxEventBytes+1)
	remaining := MaxEventBytes - len(header) - len(`,"fullDoc":`) - len(`,"updateDesc":`)
	require.NoError(t, ValidateEncodedEventSize(evt, storage[:remaining-2], []byte(`{}`)))
	require.ErrorIs(t, ValidateEncodedEventSize(evt, storage[:remaining-1], []byte(`{}`)), ErrEventTooLarge)
	_, err = UnmarshalEvent(storage)
	require.ErrorIs(t, err, ErrEventTooLarge)
	_, err = MarshalEvent(&StoreChangeEvent{EventID: strings.Repeat("x", MaxEventBytes)})
	require.ErrorIs(t, err, ErrEventTooLarge)
}

func TestUpdateDescriptionCodecRejectsInvalidDeltas(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		payload string
		message string
	}{
		{name: "malformed envelope", payload: `{"wireVersion":`, message: "unexpected end"},
		{name: "legacy envelope", payload: `{"updatedFields":{"data.n":9007199254740993}}`, message: "unsupported update description wire version"},
		{name: "invalid typed value", payload: `{"wireVersion":2,"updatedFields":{"type":"object","value":{"data.n":{"type":"int64","value":"1.5"}}}}`, message: "invalid syntax"},
		{name: "nonobject fields", payload: `{"wireVersion":2,"updatedFields":{"type":"array","value":[]}}`, message: "document fields must be an object"},
	} {
		t.Run(test.name, func(t *testing.T) {
			desc, err := UnmarshalUpdateDescription([]byte(test.payload))
			require.ErrorContains(t, err, test.message)
			require.Nil(t, desc)
		})
	}
	encoded, err := MarshalUpdateDescription(&UpdateDescription{UpdatedFields: map[string]any{"data.unsupported": make(chan int)}})
	require.ErrorContains(t, err, "unsupported value type")
	require.Nil(t, encoded)
}

func TestUpdateDescriptionCodecAllowsAbsentUpdatedFields(t *testing.T) {
	t.Parallel()
	desc, err := UnmarshalUpdateDescription([]byte(`{"wireVersion":2,"updatedFields":{"type":"null"},"removedFields":["data.old"]}`))
	require.NoError(t, err)
	require.Nil(t, desc.UpdatedFields)
	require.Equal(t, []string{"data.old"}, desc.RemovedFields)
}
