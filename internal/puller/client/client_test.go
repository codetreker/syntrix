package client

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestConvertEvent(t *testing.T) {
	t.Parallel()
	c := &Client{
		logger: slog.Default(),
	}

	doc := storage.NewStoredDoc("database-1", "users", "doc-1", map[string]any{"name": "test"})
	fullDoc, err := events.MarshalDocument(&doc)
	require.NoError(t, err)
	updateDesc, err := events.MarshalUpdateDescription(&events.UpdateDescription{UpdatedFields: map[string]any{"name": "new"}, RemovedFields: []string{"old"}})
	require.NoError(t, err)
	tests := []struct {
		name     string
		input    *pullerv1.PullerEvent
		expected *events.PullerEvent
		wantErr  bool
	}{
		{
			name: "basic event",
			input: &pullerv1.PullerEvent{
				ChangeEvent: &pullerv1.ChangeEvent{
					EventId:   "evt-123",
					Database:  "database-1",
					MgoColl:   "users",
					MgoDocId:  "doc-1",
					OpType:    "insert",
					Timestamp: 1234567890,
				},
				Progress: "p1",
			},
			expected: &events.PullerEvent{
				Change: &events.StoreChangeEvent{
					EventID:   "evt-123",
					Database:  "database-1",
					MgoColl:   "users",
					MgoDocID:  "doc-1",
					OpType:    events.StoreOperationType("insert"),
					Timestamp: 1234567890,
				},
				Progress: "p1",
			},
		},
		{
			name: "event with cluster time",
			input: &pullerv1.PullerEvent{
				ChangeEvent: &pullerv1.ChangeEvent{
					EventId:  "evt-456",
					Database: "database-2",
					MgoColl:  "orders",
					MgoDocId: "doc-2",
					OpType:   "update",
					ClusterTime: &pullerv1.ClusterTime{
						T: 100,
						I: 5,
					},
				},
				Progress: "p2",
			},
			expected: &events.PullerEvent{
				Change: &events.StoreChangeEvent{
					EventID:  "evt-456",
					Database: "database-2",
					MgoColl:  "orders",
					MgoDocID: "doc-2",
					OpType:   events.StoreOperationType("update"),
					ClusterTime: events.ClusterTime{
						T: 100,
						I: 5,
					},
				},
				Progress: "p2",
			},
		},
		{
			name: "event without cluster time",
			input: &pullerv1.PullerEvent{
				ChangeEvent: &pullerv1.ChangeEvent{
					EventId: "evt-789",
					OpType:  "delete",
				},
			},
			expected: &events.PullerEvent{
				Change: &events.StoreChangeEvent{
					EventID: "evt-789",
					OpType:  events.StoreOperationType("delete"),
				},
			},
		},
		{
			name: "event with full document",
			input: &pullerv1.PullerEvent{
				ChangeEvent: &pullerv1.ChangeEvent{
					EventId: "evt-full",
					FullDoc: fullDoc,
				},
			},
			expected: &events.PullerEvent{
				Change: &events.StoreChangeEvent{
					EventID:      "evt-full",
					FullDocument: &doc,
				},
			},
		},
		{
			name: "event with update description",
			input: &pullerv1.PullerEvent{
				ChangeEvent: &pullerv1.ChangeEvent{
					EventId:    "evt-update",
					UpdateDesc: updateDesc,
				},
			},
			expected: &events.PullerEvent{
				Change: &events.StoreChangeEvent{
					EventID: "evt-update",
					UpdateDesc: &events.UpdateDescription{
						UpdatedFields: map[string]interface{}{"name": "new"},
						RemovedFields: []string{"old"},
					},
				},
			},
		},
		{
			name:    "event with invalid full document",
			wantErr: true,
			input: &pullerv1.PullerEvent{
				ChangeEvent: &pullerv1.ChangeEvent{
					EventId: "evt-invalid-doc",
					FullDoc: []byte(`{invalid-json}`),
				},
			},
			expected: &events.PullerEvent{
				Change: &events.StoreChangeEvent{
					EventID: "evt-invalid-doc",
				},
			},
		},
		{
			name:    "event with invalid update description",
			wantErr: true,
			input: &pullerv1.PullerEvent{
				ChangeEvent: &pullerv1.ChangeEvent{
					EventId:    "evt-invalid-desc",
					UpdateDesc: []byte(`{invalid-json}`),
				},
			},
			expected: &events.PullerEvent{
				Change: &events.StoreChangeEvent{
					EventID: "evt-invalid-desc",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := c.convertEvent(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)

			if result.Progress != tt.expected.Progress {
				t.Errorf("Progress = %v, want %v", result.Progress, tt.expected.Progress)
			}

			if result.Change == nil && tt.expected.Change == nil {
				return
			}
			if result.Change == nil || tt.expected.Change == nil {
				t.Errorf("Change mismatch: got %v, want %v", result.Change, tt.expected.Change)
				return
			}

			change := result.Change
			expChange := tt.expected.Change
			require.Equal(t, expChange.FullDocument, change.FullDocument)
			require.Equal(t, expChange.UpdateDesc, change.UpdateDesc)

			if change.EventID != expChange.EventID {
				t.Errorf("EventID = %v, want %v", change.EventID, expChange.EventID)
			}
			if change.Database != expChange.Database {
				t.Errorf("Database = %v, want %v", change.Database, expChange.Database)
			}
			if change.MgoColl != expChange.MgoColl {
				t.Errorf("MgoColl = %v, want %v", change.MgoColl, expChange.MgoColl)
			}
			if change.MgoDocID != expChange.MgoDocID {
				t.Errorf("MgoDocID = %v, want %v", change.MgoDocID, expChange.MgoDocID)
			}
			if change.OpType != expChange.OpType {
				t.Errorf("OpType = %v, want %v", change.OpType, expChange.OpType)
			}
			if change.Timestamp != expChange.Timestamp {
				t.Errorf("Timestamp = %v, want %v", change.Timestamp, expChange.Timestamp)
			}
			if change.ClusterTime.T != expChange.ClusterTime.T {
				t.Errorf("ClusterTime.T = %v, want %v", change.ClusterTime.T, expChange.ClusterTime.T)
			}
		})
	}
}
