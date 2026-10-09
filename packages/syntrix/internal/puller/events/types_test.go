package events

import (
	"encoding/json"
	"testing"

	"github.com/codetreker/syntrix/internal/core/storage"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestOperationType_IsValid(t *testing.T) {
	tests := []struct {
		op    StoreOperationType
		valid bool
	}{
		{StoreOperationInsert, true},
		{StoreOperationUpdate, true},
		{StoreOperationReplace, true},
		{StoreOperationDelete, true},
		{"INSERT", false}, // uppercase is invalid
		{"unknown", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(string(tt.op), func(t *testing.T) {
			if got := tt.op.IsValid(); got != tt.valid {
				t.Errorf("OperationType(%q).IsValid() = %v, want %v", tt.op, got, tt.valid)
			}
		})
	}
}

func TestClusterTime_Compare(t *testing.T) {
	tests := []struct {
		name string
		a, b ClusterTime
		want int
	}{
		{"equal", ClusterTime{100, 1}, ClusterTime{100, 1}, 0},
		{"a.T < b.T", ClusterTime{99, 5}, ClusterTime{100, 1}, -1},
		{"a.T > b.T", ClusterTime{101, 1}, ClusterTime{100, 5}, 1},
		{"same T, a.I < b.I", ClusterTime{100, 1}, ClusterTime{100, 2}, -1},
		{"same T, a.I > b.I", ClusterTime{100, 3}, ClusterTime{100, 2}, 1},
		{"zero vs non-zero", ClusterTime{0, 0}, ClusterTime{1, 0}, -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Compare(tt.b); got != tt.want {
				t.Errorf("ClusterTime.Compare() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClusterTime_IsZero(t *testing.T) {
	tests := []struct {
		ct   ClusterTime
		zero bool
	}{
		{ClusterTime{0, 0}, true},
		{ClusterTime{1, 0}, false},
		{ClusterTime{0, 1}, false},
		{ClusterTime{100, 5}, false},
	}

	for _, tt := range tests {
		if got := tt.ct.IsZero(); got != tt.zero {
			t.Errorf("ClusterTime{%d,%d}.IsZero() = %v, want %v", tt.ct.T, tt.ct.I, got, tt.zero)
		}
	}
}

func TestClusterTime_PrimitiveConversion(t *testing.T) {
	orig := primitive.Timestamp{T: 1234567890, I: 42}
	ct := ClusterTimeFromPrimitive(orig)

	if ct.T != orig.T || ct.I != orig.I {
		t.Errorf("ClusterTimeFromPrimitive() = {%d,%d}, want {%d,%d}", ct.T, ct.I, orig.T, orig.I)
	}

	back := ct.ToPrimitive()
	if back.T != orig.T || back.I != orig.I {
		t.Errorf("ToPrimitive() = {%d,%d}, want {%d,%d}", back.T, back.I, orig.T, orig.I)
	}
}

func TestChangeEvent_JSON(t *testing.T) {
	evt := &StoreChangeEvent{
		EventID:     "evt-123",
		Database:    "database-abc",
		MgoColl:     "users",
		MgoDocID:    "doc-456",
		OpType:      StoreOperationInsert,
		ClusterTime: ClusterTime{T: 1735567890, I: 1},
		Timestamp:   1735567890000,
		FullDocument: &storage.StoredDoc{
			Id:       "doc-456",
			Database: "database-abc",
		},
	}

	// Marshal
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Check JSON field names (should use short names)
	jsonStr := string(data)
	if !contains(jsonStr, `"database"`) {
		t.Errorf("JSON should contain 'database', got: %s", jsonStr)
	}
	if !contains(jsonStr, `"mgoDocId"`) {
		t.Errorf("JSON should contain 'mgoDocId', got: %s", jsonStr)
	}
	if !contains(jsonStr, `"mgoColl"`) {
		t.Errorf("JSON should contain 'mgoColl', got: %s", jsonStr)
	}
	if !contains(jsonStr, `"opType"`) {
		t.Errorf("JSON should contain 'opType', got: %s", jsonStr)
	}
	if !contains(jsonStr, `"fullDoc"`) {
		t.Errorf("JSON should contain 'fullDoc', got: %s", jsonStr)
	}

	// Unmarshal
	var decoded StoreChangeEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.EventID != evt.EventID {
		t.Errorf("EventID = %q, want %q", decoded.EventID, evt.EventID)
	}
	if decoded.Database != evt.Database {
		t.Errorf("Database = %q, want %q", decoded.Database, evt.Database)
	}
	if decoded.OpType != evt.OpType {
		t.Errorf("OpType = %q, want %q", decoded.OpType, evt.OpType)
	}
	if decoded.FullDocument.Id != evt.FullDocument.Id {
		t.Errorf("FullDocument.Id = %q, want %q", decoded.FullDocument.Id, evt.FullDocument.Id)
	}
}

func TestPullerEvent_JSON(t *testing.T) {
	change := &StoreChangeEvent{
		EventID: "evt-123",
		OpType:  StoreOperationInsert,
	}
	evt := &PullerEvent{
		Change:   change,
		Progress: "resume-token-123",
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	jsonStr := string(data)
	if !contains(jsonStr, `"change_event"`) {
		t.Errorf("JSON should contain 'change_event', got: %s", jsonStr)
	}
	if !contains(jsonStr, `"progress"`) {
		t.Errorf("JSON should contain 'progress', got: %s", jsonStr)
	}

	var decoded PullerEvent
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if decoded.Progress != evt.Progress {
		t.Errorf("Progress = %q, want %q", decoded.Progress, evt.Progress)
	}
	if decoded.Change.EventID != evt.Change.EventID {
		t.Errorf("Change.EventID = %q, want %q", decoded.Change.EventID, evt.Change.EventID)
	}
}

func TestBufferKey(t *testing.T) {
	tests := []struct {
		name    string
		ct      ClusterTime
		eventID string
		want    string
	}{
		{
			name:    "normal",
			ct:      ClusterTime{T: 1735567890, I: 1},
			eventID: "evt-123",
			want:    "1735567890-0000000001-evt-123",
		},
		{
			name:    "zero increment",
			ct:      ClusterTime{T: 1735567890, I: 0},
			eventID: "evt-456",
			want:    "1735567890-0000000000-evt-456",
		},
		{
			name:    "large increment",
			ct:      ClusterTime{T: 1735567890, I: 999999999},
			eventID: "evt-789",
			want:    "1735567890-0999999999-evt-789",
		},
		{
			name:    "small T",
			ct:      ClusterTime{T: 1, I: 1},
			eventID: "a",
			want:    "0000000001-0000000001-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evt := &StoreChangeEvent{
				EventID:     tt.eventID,
				ClusterTime: tt.ct,
			}
			got := evt.BufferKey()
			if got != tt.want {
				t.Errorf("BufferKey() = %q, want %q", got, tt.want)
			}

			// Also test FormatBufferKey directly
			got2 := FormatBufferKey(tt.ct, tt.eventID)
			if got2 != tt.want {
				t.Errorf("FormatBufferKey() = %q, want %q", got2, tt.want)
			}
		})
	}
}

func TestBufferKey_Ordering(t *testing.T) {
	// Ensure lexicographic ordering matches temporal ordering
	events := []struct {
		ct      ClusterTime
		eventID string
	}{
		{ClusterTime{100, 1}, "a"},
		{ClusterTime{100, 2}, "b"},
		{ClusterTime{101, 0}, "c"},
		{ClusterTime{1000, 1}, "d"},
	}

	var keys []string
	for _, e := range events {
		keys = append(keys, FormatBufferKey(e.ct, e.eventID))
	}

	// Verify keys are in ascending order
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Errorf("Keys not in order: %q >= %q", keys[i-1], keys[i])
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
