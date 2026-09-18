package core

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func conditionedCreate(condition storage.CreateCondition) storage.ReplicationPushRequest {
	req := pushRequest(storage.PushCreate, nil)
	req.Changes[0].CreateCondition = condition
	if condition == storage.CreateIfTombstone {
		req.Changes[0].BaseVersion = ptr(7)
	}
	return req
}

func pushCreateCurrent(state string) *storage.StoredDoc {
	if state == "missing" {
		return nil
	}
	doc := storage.NewStoredDoc("db", "items", "alice", nil)
	doc.Deleted = state != "live"
	doc.Version = 7
	if state == "new_tombstone" {
		doc.Version = 8
	}
	return &doc
}

func TestPushCreateConditionsInitialState(t *testing.T) {
	for _, condition := range []storage.CreateCondition{storage.CreateIfAbsent, storage.CreateIfTombstone} {
		for _, state := range []string{"missing", "live", "tombstone", "new_tombstone"} {
			t.Run(fmt.Sprintf("%s/%s", condition, state), func(t *testing.T) {
				store := new(routedReadStorage)
				req := conditionedCreate(condition)
				current := pushCreateCurrent(state)
				var readErr error
				if current == nil {
					readErr = model.ErrNotFound
				}
				store.On("Get", mock.Anything, "db", "items/alice", []storage.ReadOptions{{Consistency: storage.ReadAuthoritative, ShowDeleted: true}}).Return(current, readErr).Once()
				matched := condition == storage.CreateIfAbsent && state == "missing" || condition == storage.CreateIfTombstone && state == "tombstone"
				if matched {
					store.On("Create", mock.Anything, "db", mock.MatchedBy(func(doc storage.StoredDoc) bool {
						return doc.Fullpath == "items/alice" && !doc.Deleted && doc.Version == 1
					}), []storage.CreateOptions{{Condition: condition, ExpectedVersion: req.Changes[0].BaseVersion}}).Return(nil).Once()
				}
				response, err := New(store, nil).Push(context.Background(), "db", req)
				require.NoError(t, err)
				if matched {
					require.Empty(t, response.Conflicts)
				} else {
					reason := storage.PushTombstoned
					if state == "missing" {
						reason = storage.PushMissing
					} else if state == "live" {
						reason = storage.PushAlreadyExists
					}
					require.Equal(t, []storage.ReplicationPushConflict{{ID: "alice", Reason: reason, Current: current}}, response.Conflicts)
				}
				store.AssertExpectations(t)
				store.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			})
		}
	}
}

func TestPushCreateConditionAtomicFailure(t *testing.T) {
	for _, condition := range []storage.CreateCondition{storage.CreateIfAbsent, storage.CreateIfTombstone} {
		for _, writeErr := range []error{model.ErrExists, model.ErrPreconditionFailed, model.ErrNotFound} {
			for _, state := range []string{"missing", "live", "tombstone", "new_tombstone"} {
				t.Run(fmt.Sprintf("%s/%s/%s", condition, writeErr, state), func(t *testing.T) {
					store := new(routedReadStorage)
					req := conditionedCreate(condition)
					initial := pushCreateCurrent("tombstone")
					var initialErr error
					if condition == storage.CreateIfAbsent {
						initial = nil
						initialErr = model.ErrNotFound
					}
					reads := []storage.ReadOptions{{Consistency: storage.ReadAuthoritative, ShowDeleted: true}}
					store.On("Get", mock.Anything, "db", "items/alice", reads).Return(initial, initialErr).Once()
					store.On("Create", mock.Anything, "db", mock.Anything, []storage.CreateOptions{{Condition: condition, ExpectedVersion: req.Changes[0].BaseVersion}}).Return(fmt.Errorf("condition race: %w", writeErr)).Once()
					latest := pushCreateCurrent(state)
					var readErr error
					if latest == nil {
						readErr = model.ErrNotFound
					}
					store.On("Get", mock.Anything, "db", "items/alice", reads).Return(latest, readErr).Once()
					response, err := New(store, nil).Push(context.Background(), "db", req)
					require.NoError(t, err)
					reason := storage.PushTombstoned
					if latest == nil {
						reason = storage.PushMissing
					} else if !latest.Deleted {
						reason = storage.PushAlreadyExists
					}
					require.Equal(t, []storage.ReplicationPushConflict{{ID: "alice", Reason: reason, Current: latest}}, response.Conflicts)
					store.AssertExpectations(t)
					store.AssertNumberOfCalls(t, "Create", 1)
					store.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
				})
			}
		}
	}
}

func TestPushCreateConditionFailurePropagation(t *testing.T) {
	for _, stage := range []string{"initial_read", "create", "conflict_read"} {
		t.Run(stage, func(t *testing.T) {
			store := new(MockStorageBackend)
			failure := errors.New("storage unavailable")
			if stage == "initial_read" {
				store.On("Get", mock.Anything, "db", "items/alice").Return(nil, failure).Once()
			} else {
				store.On("Get", mock.Anything, "db", "items/alice").Return(nil, model.ErrNotFound).Once()
				writeErr := failure
				if stage == "conflict_read" {
					writeErr = model.ErrExists
					store.On("Get", mock.Anything, "db", "items/alice").Return(nil, failure).Once()
				}
				store.On("Create", mock.Anything, "db", mock.Anything, []storage.CreateOptions{{Condition: storage.CreateIfAbsent}}).Return(writeErr).Once()
			}
			response, err := New(store, nil).Push(context.Background(), "db", conditionedCreate(storage.CreateIfAbsent))
			require.ErrorIs(t, err, failure)
			require.Nil(t, response)
			store.AssertExpectations(t)
		})
	}
}

func TestPushRejectsInvalidCreateConditionsBeforeStorage(t *testing.T) {
	tests := []struct {
		action    storage.PushAction
		condition storage.CreateCondition
		version   *int64
	}{
		{storage.PushUpdate, storage.CreateIfAbsent, nil},
		{storage.PushDelete, storage.CreateIfTombstone, ptr(1)},
		{storage.PushCreate, "unknown", nil},
		{storage.PushCreate, storage.CreateIfAbsent, ptr(0)},
		{storage.PushCreate, storage.CreateIfAbsent, ptr(1)},
		{storage.PushCreate, storage.CreateIfTombstone, nil},
		{storage.PushCreate, storage.CreateIfTombstone, ptr(-1)},
	}
	for i, test := range tests {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			store := new(MockStorageBackend)
			req := pushRequest(storage.PushUpdate, nil)
			bad := pushRequest(test.action, test.version).Changes[0]
			bad.CreateCondition = test.condition
			req.Changes = append(req.Changes, bad)
			response, err := New(store, nil).Push(context.Background(), "db", req)
			require.ErrorIs(t, err, model.ErrInvalidQuery)
			require.Nil(t, response)
			require.Empty(t, store.Calls)
		})
	}
}

func TestPushCreateTombstoneVersionZero(t *testing.T) {
	store := new(MockStorageBackend)
	req := conditionedCreate(storage.CreateIfTombstone)
	req.Changes[0].BaseVersion = ptr(0)
	current := pushCreateCurrent("tombstone")
	current.Version = 0
	store.On("Get", mock.Anything, "db", "items/alice").Return(current, nil).Once()
	store.On("Create", mock.Anything, "db", mock.Anything, []storage.CreateOptions{{Condition: storage.CreateIfTombstone, ExpectedVersion: ptr(0)}}).Return(nil).Once()
	response, err := New(store, nil).Push(context.Background(), "db", req)
	require.NoError(t, err)
	require.Empty(t, response.Conflicts)
	store.AssertExpectations(t)
}
