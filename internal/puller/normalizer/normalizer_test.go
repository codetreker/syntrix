package normalizer

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func makeRawEvent(opType string, clusterT, clusterI uint32, fullDoc bson.M, docKey bson.M, db, coll string) *RawEvent {
	raw := &RawEvent{
		OperationType: opType,
		ClusterTime:   primitive.Timestamp{T: clusterT, I: clusterI},
		FullDocument:  fullDoc,
		DocumentKey:   docKey,
	}
	raw.Namespace.DB = db
	raw.Namespace.Coll = coll
	return raw
}

func TestNormalizer_Normalize(t *testing.T) {
	t.Parallel()
	n := New()

	tests := []struct {
		name    string
		raw     *RawEvent
		wantErr bool
		check   func(*testing.T, *events.StoreChangeEvent)
	}{
		{
			name: "insert operation",
			raw: makeRawEvent(
				"insert",
				1234567890, 1,
				bson.M{"_id": "doc1", "database": "database-1", "fullpath": "/path"},
				bson.M{"_id": "doc1"},
				"testdb", "testcoll",
			),
			wantErr: false,
			check: func(t *testing.T, evt *events.StoreChangeEvent) {
				if evt.OpType != events.StoreOperationInsert {
					t.Errorf("OpType = %s, want insert", evt.OpType)
				}
				if evt.MgoDocID != "doc1" {
					t.Errorf("MgoDocID = %s, want doc1", evt.MgoDocID)
				}
				if evt.MgoColl != "testcoll" {
					t.Errorf("MgoColl = %s, want testcoll", evt.MgoColl)
				}
				if evt.Database != "database-1" {
					t.Errorf("Database = %s, want database-1", evt.Database)
				}
				if evt.FullDocument == nil {
					t.Error("FullDocument should not be nil")
				} else if evt.FullDocument.Id != "doc1" {
					t.Errorf("FullDocument.Id = %s, want doc1", evt.FullDocument.Id)
				}
			},
		},
		{
			name: "update operation with fullDocument",
			raw: makeRawEvent(
				"update",
				1234567890, 2,
				bson.M{"_id": "doc2", "database": "database-2", "fullpath": "/path2"},
				bson.M{"_id": "doc2"},
				"testdb", "testcoll",
			),
			wantErr: false,
			check: func(t *testing.T, evt *events.StoreChangeEvent) {
				if evt.OpType != events.StoreOperationUpdate {
					t.Errorf("OpType = %s, want update", evt.OpType)
				}
				if evt.Database != "database-2" {
					t.Errorf("Database = %s, want database-2", evt.Database)
				}
			},
		},
		{
			name: "update operation missing fullDocument",
			raw: makeRawEvent(
				"update",
				1234567890, 2,
				nil,
				bson.M{"_id": "doc2"},
				"testdb", "testcoll",
			),
			wantErr: true, // Should fail
			check:   nil,
		},
		{
			name: "delete operation",
			raw: makeRawEvent(
				"delete",
				1234567890, 3,
				nil,
				bson.M{"_id": "doc3"},
				"testdb", "testcoll",
			),
			wantErr: false,
			check: func(t *testing.T, evt *events.StoreChangeEvent) {
				if evt.OpType != events.StoreOperationDelete {
					t.Errorf("OpType = %s, want delete", evt.OpType)
				}
				if evt.FullDocument != nil {
					t.Error("FullDocument should be nil for delete")
				}
				if evt.Database != "" {
					t.Errorf("Database should be empty for delete, got %s", evt.Database)
				}
			},
		},
		{
			name: "unknown operation type",
			raw: makeRawEvent(
				"unknown",
				1234567890, 1,
				nil,
				bson.M{"_id": "doc1"},
				"testdb", "testcoll",
			),
			wantErr: true,
		},
		{
			name: "ObjectID document key",
			raw: func() *RawEvent {
				oid := primitive.NewObjectID()
				return makeRawEvent(
					"insert",
					1234567890, 1,
					bson.M{"_id": oid, "database": "t1", "fullpath": "/p"},
					bson.M{"_id": oid},
					"testdb", "testcoll",
				)
			}(),
			wantErr: false,
			check: func(t *testing.T, evt *events.StoreChangeEvent) {
				if evt.MgoDocID == "" {
					t.Error("MgoDocID should not be empty")
				}
			},
		},
		{
			name: "With TxnNumber",
			raw: func() *RawEvent {
				raw := makeRawEvent(
					"insert",
					1234567890, 1,
					bson.M{"_id": "doc1", "database": "t1", "fullpath": "/p"},
					bson.M{"_id": "doc1"},
					"testdb", "testcoll",
				)
				txn := int64(100)
				raw.TxnNumber = &txn
				return raw
			}(),
			wantErr: false,
			check: func(t *testing.T, evt *events.StoreChangeEvent) {
				if evt.TxnNumber == nil || *evt.TxnNumber != 100 {
					t.Error("TxnNumber not preserved")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt, err := n.Normalize(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Errorf("Normalize() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && tt.check != nil {
				tt.check(t, evt)
			}
		})
	}
}

func TestNormalizer_UpdateDescription(t *testing.T) {
	t.Parallel()
	n := New()

	raw := &RawEvent{
		OperationType: "update",
		ClusterTime:   primitive.Timestamp{T: 1234567890, I: 1},
		DocumentKey:   bson.M{"_id": "doc1"},
		FullDocument:  bson.M{"_id": "doc1", "database": "t1", "fullpath": "/p"},
		UpdateDescription: bson.M{
			"updatedFields": bson.M{"name": "updated"},
			"removedFields": bson.A{"oldField"},
			"truncatedArrays": bson.A{
				bson.M{"field": "items", "newSize": int32(10)},
			},
		},
	}
	raw.Namespace.DB = "testdb"
	raw.Namespace.Coll = "testcoll"

	evt, err := n.Normalize(raw)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	if evt.UpdateDesc == nil {
		t.Fatal("UpdateDesc is nil")
	}

	if evt.UpdateDesc.UpdatedFields["name"] != "updated" {
		t.Errorf("UpdatedFields.name = %v, want 'updated'", evt.UpdateDesc.UpdatedFields["name"])
	}

	if len(evt.UpdateDesc.RemovedFields) != 1 || evt.UpdateDesc.RemovedFields[0] != "oldField" {
		t.Errorf("RemovedFields = %v, want ['oldField']", evt.UpdateDesc.RemovedFields)
	}

	if len(evt.UpdateDesc.TruncatedArrays) != 1 {
		t.Errorf("len(TruncatedArrays) = %d, want 1", len(evt.UpdateDesc.TruncatedArrays))
	} else {
		ta := evt.UpdateDesc.TruncatedArrays[0]
		if ta.Field != "items" || ta.NewSize != 10 {
			t.Errorf("TruncatedArray = %+v, want {Field: items, NewSize: 10}", ta)
		}
	}
}

func TestExtractDocumentID_StringID(t *testing.T) {
	t.Parallel()
	docKey := bson.M{"_id": "my-string-id"}
	id, err := extractDocumentID(docKey)
	if err != nil {
		t.Fatalf("extractDocumentID() error = %v", err)
	}
	if id != "my-string-id" {
		t.Errorf("extractDocumentID() = %q, want 'my-string-id'", id)
	}
}

func TestExtractDocumentID_ObjectID(t *testing.T) {
	t.Parallel()
	oid := primitive.NewObjectID()
	docKey := bson.M{"_id": oid}
	id, err := extractDocumentID(docKey)
	if err != nil {
		t.Fatalf("extractDocumentID() error = %v", err)
	}
	if id != oid.Hex() {
		t.Errorf("extractDocumentID() = %q, want %q", id, oid.Hex())
	}
}

func TestExtractDocumentID_IntID(t *testing.T) {
	t.Parallel()
	docKey := bson.M{"_id": int64(12345)}
	id, err := extractDocumentID(docKey)
	if err != nil {
		t.Fatalf("extractDocumentID() error = %v", err)
	}
	if id != "12345" {
		t.Errorf("extractDocumentID() = %q, want '12345'", id)
	}
}

func TestExtractDocumentID_Int32ID(t *testing.T) {
	t.Parallel()
	docKey := bson.M{"_id": int32(999)}
	id, err := extractDocumentID(docKey)
	if err != nil {
		t.Fatalf("extractDocumentID() error = %v", err)
	}
	if id != "999" {
		t.Errorf("extractDocumentID() = %q, want '999'", id)
	}
}

func TestExtractDocumentID_IntPlainID(t *testing.T) {
	docKey := bson.M{"_id": int(42)}
	id, err := extractDocumentID(docKey)
	if err != nil {
		t.Fatalf("extractDocumentID() error = %v", err)
	}
	if id != "42" {
		t.Errorf("extractDocumentID() = %q, want '42'", id)
	}
}

func TestExtractDocumentID_OtherType(t *testing.T) {
	docKey := bson.M{"_id": 3.14}
	id, err := extractDocumentID(docKey)
	if err != nil {
		t.Fatalf("extractDocumentID() error = %v", err)
	}
	// Should use %v formatting
	if id == "" {
		t.Error("extractDocumentID() returned empty string")
	}
}

func TestExtractDocumentID_NilDocKey(t *testing.T) {
	_, err := extractDocumentID(nil)
	if err == nil {
		t.Error("extractDocumentID(nil) should return error")
	}
}

func TestExtractDocumentID_CompoundKey(t *testing.T) {
	// No _id field, should use compound key formatting
	docKey := bson.M{"field1": "value1", "field2": "value2"}
	id, err := extractDocumentID(docKey)
	if err != nil {
		t.Fatalf("extractDocumentID() error = %v", err)
	}
	// Should return a hash of the document key
	if id == "" {
		t.Error("extractDocumentID() returned empty string for compound key")
	}
	// Hash should be 32 hex characters (16 bytes)
	if len(id) != 32 {
		t.Errorf("compound key hash length = %d, want 32", len(id))
	}
}

func TestFormatCompoundKey(t *testing.T) {
	// Use single-key map to avoid non-deterministic map iteration order
	docKey := bson.M{"key": "value"}
	result := formatCompoundKey(docKey)

	if result == "" {
		t.Error("formatCompoundKey() returned empty string")
	}
	// Should be a hex string (32 chars for 16 bytes)
	if len(result) != 32 {
		t.Errorf("formatCompoundKey() length = %d, want 32", len(result))
	}

	// Same single-key input should produce same output
	docKey2 := bson.M{"key": "value"}
	result2 := formatCompoundKey(docKey2)
	if result != result2 {
		t.Error("formatCompoundKey() should be deterministic for same input")
	}
}

func TestFormatID(t *testing.T) {
	tests := []struct {
		name string
		id   any
		want string
	}{
		{
			name: "ObjectID",
			id:   primitive.NewObjectIDFromTimestamp(time.Unix(1234567890, 0)),
			want: "", // Will check it's not empty
		},
		{
			name: "string",
			id:   "my-id",
			want: "my-id",
		},
		{
			name: "int",
			id:   123,
			want: "123",
		},
		{
			name: "int32",
			id:   int32(456),
			want: "456",
		},
		{
			name: "int64",
			id:   int64(789),
			want: "789",
		},
		{
			name: "float",
			id:   3.14,
			want: "3.14",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatID(tt.id)
			if tt.want == "" {
				// Just check it's not empty
				if result == "" {
					t.Error("formatID() returned empty string")
				}
			} else if result != tt.want {
				t.Errorf("formatID() = %q, want %q", result, tt.want)
			}
		})
	}
}

func TestNormalizer_NilDocumentKey(t *testing.T) {
	n := New()

	raw := &RawEvent{
		OperationType: "insert",
		ClusterTime:   primitive.Timestamp{T: 1234567890, I: 1},
		DocumentKey:   nil, // nil document key
		FullDocument:  bson.M{"_id": "doc1", "name": "test"},
	}
	raw.Namespace.DB = "testdb"
	raw.Namespace.Coll = "testcoll"

	_, err := n.Normalize(raw)
	if err == nil {
		t.Error("Normalize() should fail with nil DocumentKey")
	}
}

func TestParseEventID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantT   uint32
		wantI   uint32
		wantErr bool
	}{
		{
			name:    "valid id",
			id:      "1234567890-1-abcdef12",
			wantT:   1234567890,
			wantI:   1,
			wantErr: false,
		},
		{
			name:    "invalid format",
			id:      "invalid",
			wantErr: true,
		},
		{
			name:    "missing hash",
			id:      "1234567890-1",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct, err := ParseEventID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseEventID() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if ct.T != tt.wantT {
					t.Errorf("ParseEventID() T = %v, want %v", ct.T, tt.wantT)
				}
				if ct.I != tt.wantI {
					t.Errorf("ParseEventID() I = %v, want %v", ct.I, tt.wantI)
				}
			}
		})
	}
}

func TestConvertBsonValue(t *testing.T) {
	t.Parallel()

	oid := primitive.NewObjectID()
	now := time.Now().UTC().Truncate(time.Millisecond)
	ts := primitive.Timestamp{T: 123, I: 456}

	tests := []struct {
		name  string
		input any
		want  any
	}{
		{
			name:  "string",
			input: "test",
			want:  "test",
		},
		{
			name:  "int",
			input: 123,
			want:  123,
		},
		{
			name:  "bson.M",
			input: bson.M{"key": "value"},
			want:  map[string]any{"key": "value"},
		},
		{
			name:  "bson.A",
			input: bson.A{"item1", 123},
			want:  []any{"item1", 123},
		},
		{
			name:  "ObjectID",
			input: oid,
			want:  oid.Hex(),
		},
		{
			name:  "DateTime",
			input: primitive.NewDateTimeFromTime(now),
			want:  now.Local(),
		},
		{
			name:  "Timestamp",
			input: ts,
			want:  map[string]uint32{"T": 123, "I": 456},
		},
		{
			name: "Nested",
			input: bson.M{
				"array": bson.A{
					bson.M{"nested": "value"},
				},
			},
			want: map[string]any{
				"array": []any{
					map[string]any{"nested": "value"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertBsonValue(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizer_FixTimestamps(t *testing.T) {
	n := New()

	now := time.Now()
	pNow := primitive.NewDateTimeFromTime(now)

	raw := makeRawEvent(
		"insert",
		1, 1,
		bson.M{
			"_id":        "t1",
			"database":   "T",
			"created_at": pNow,
			"updated_at": now,
		},
		bson.M{"_id": "t1"},
		"db", "coll",
	)

	evt, err := n.Normalize(raw)
	assert.NoError(t, err)

	// fixTimestamps logic converts them to int64
	assert.Equal(t, now.UnixMilli(), evt.FullDocument.CreatedAt)
	assert.Equal(t, now.UnixMilli(), evt.FullDocument.UpdatedAt)
}

func TestNormalizer_MetadataTimestampDeltasUseMilliseconds(t *testing.T) {
	t.Parallel()
	deadline := time.UnixMilli(1789102091123).UTC()
	for _, value := range []any{primitive.NewDateTimeFromTime(deadline), deadline} {
		raw := makeRawEvent("update", 1, 1, bson.M{
			"_id": "physical-id", "database": "db", "collection": "items", "fullpath": "items/logical-id", "data": bson.M{}, "deleted": true,
		}, bson.M{"_id": "physical-id"}, "source", "data")
		raw.UpdateDescription = bson.M{"updatedFields": bson.M{
			"created_at": value, "updated_at": value, "sys_expires_at": value,
			"data": bson.M{}, "deleted": true, "version": int64(9007199254740993),
		}}
		evt, err := New().Normalize(raw)
		require.NoError(t, err)
		encoded, err := events.MarshalEvent(evt)
		require.NoError(t, err)
		decoded, err := events.UnmarshalEvent(encoded)
		require.NoError(t, err)
		for _, field := range []string{"created_at", "updated_at", "sys_expires_at"} {
			require.Equal(t, deadline.UnixMilli(), decoded.UpdateDesc.UpdatedFields[field])
		}
		require.Equal(t, int64(9007199254740993), decoded.UpdateDesc.UpdatedFields["version"])
		require.Equal(t, map[string]any{}, decoded.FullDocument.Data)
	}
}

func TestNormalizer_MetadataConversionDoesNotCoerceBusinessDates(t *testing.T) {
	t.Parallel()
	raw := makeRawEvent("update", 1, 1, bson.M{
		"_id": "physical-id", "database": "db", "collection": "items", "fullpath": "items/logical-id", "data": bson.M{},
	}, bson.M{"_id": "physical-id"}, "source", "data")
	raw.UpdateDescription = bson.M{"updatedFields": bson.M{
		"data.sys_expires_at": primitive.DateTime(1234),
	}}
	evt, err := New().Normalize(raw)
	require.NoError(t, err)
	_, err = events.MarshalEvent(evt)
	require.ErrorContains(t, err, "unsupported value type time.Time")
}
