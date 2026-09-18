package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func pushRequest(action storage.PushAction, version *int64) storage.ReplicationPushRequest {
	return storage.ReplicationPushRequest{Collection: "items", Changes: []storage.ReplicationPushChange{{Action: action, BaseVersion: version, Doc: &storage.StoredDoc{Fullpath: "items/alice", Data: map[string]interface{}{"value": "new"}}}}}
}

func TestPushMissingAndTombstoneMatrix(t *testing.T) {
	for _, tombstone := range []bool{false, true} {
		for _, action := range []storage.PushAction{storage.PushCreate, storage.PushUpdate, storage.PushDelete} {
			versions := []*int64{nil, ptr(0), ptr(1)}
			if action == storage.PushCreate {
				versions = append(versions, ptr(9), ptr(math.MaxInt64))
			}
			for _, version := range versions {
				versionName := "omitted"
				if version != nil {
					versionName = fmt.Sprint(*version)
				}
				t.Run(fmt.Sprintf("deleted=%v/action=%s/version=%s", tombstone, action, versionName), func(t *testing.T) {
					store := new(routedReadStorage)
					var current *storage.StoredDoc
					readErr := model.ErrNotFound
					if tombstone {
						current = &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Deleted: true, Version: 9, UpdatedAt: 123}
						readErr = nil
					}
					store.On("Get", mock.Anything, "db", "items/alice", []storage.ReadOptions{{Consistency: storage.ReadAuthoritative, ShowDeleted: true}}).Return(current, readErr).Once()
					creating := action == storage.PushCreate || action == storage.PushUpdate && version == nil
					if creating {
						store.On("Create", mock.Anything, "db", mock.MatchedBy(func(doc storage.StoredDoc) bool {
							return doc.Database == "db" && doc.Fullpath == "items/alice" && doc.Version == 1 && !doc.Deleted && doc.Data["id"] == "alice"
						})).Return(nil).Once()
					}
					resp, err := New(store, nil).Push(context.Background(), "db", pushRequest(action, version))
					require.NoError(t, err)
					if !creating && version != nil {
						require.Len(t, resp.Conflicts, 1)
						want := storage.PushMissing
						if tombstone {
							want = storage.PushTombstoned
						}
						require.Equal(t, storage.ReplicationPushConflict{ChangeIndex: 0, ID: "alice", Reason: want, Current: current}, resp.Conflicts[0])
					} else {
						require.Empty(t, resp.Conflicts)
					}
					store.AssertExpectations(t)
					store.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
					store.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
				})
			}
		}
	}
}

func TestPushCreateNeverOverwritesLiveDocument(t *testing.T) {
	versions := map[string]*int64{"omitted": nil, "zero": ptr(0), "matching": ptr(9), "different": ptr(1), "max": ptr(math.MaxInt64)}
	for name, version := range versions {
		for _, identical := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/identical=%v", name, identical), func(t *testing.T) {
				store := new(routedReadStorage)
				current := &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Id: "stored-hash", Version: 9, CreatedAt: 123, UpdatedAt: 456, Data: map[string]interface{}{"id": "alice", "value": "original"}}
				before := *current
				req := pushRequest(storage.PushCreate, version)
				if identical {
					req.Changes[0].Doc.Data = map[string]interface{}{"id": "alice", "value": "original"}
				}
				input := *req.Changes[0].Doc
				store.On("Get", mock.Anything, "db", "items/alice", []storage.ReadOptions{{Consistency: storage.ReadAuthoritative, ShowDeleted: true}}).Return(current, nil).Once()
				resp, err := New(store, nil).Push(context.Background(), "db", req)
				require.NoError(t, err)
				require.Equal(t, []storage.ReplicationPushConflict{{ID: "alice", Reason: storage.PushAlreadyExists, Current: current}}, resp.Conflicts)
				require.Equal(t, before, *current)
				require.Equal(t, map[string]interface{}{"id": "alice", "value": "original"}, current.Data)
				require.Equal(t, input, *req.Changes[0].Doc)
				store.AssertExpectations(t)
				require.Len(t, store.Calls, 1)
			})
		}
	}
}

func TestPushCASFailureObservations(t *testing.T) {
	for _, action := range []storage.PushAction{storage.PushUpdate, storage.PushDelete} {
		for _, writeErr := range []error{model.ErrNotFound, model.ErrPreconditionFailed} {
			for _, state := range []string{"missing", "deleted", "new_version", "same_version"} {
				t.Run(fmt.Sprintf("%s/%s/%s", action, writeErr, state), func(t *testing.T) {
					store := new(MockStorageBackend)
					initial := &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 0}
					store.On("Get", mock.Anything, "db", "items/alice").Return(initial, nil).Once()
					pred := model.Filters{{Field: "version", Op: model.OpEq, Value: int64(0)}}
					if action == storage.PushDelete {
						store.On("Delete", mock.Anything, "db", "items/alice", pred).Return(fmt.Errorf("wrapped: %w", writeErr)).Once()
					} else {
						store.On("Update", mock.Anything, "db", "items/alice", mock.Anything, pred).Return(fmt.Errorf("wrapped: %w", writeErr)).Once()
					}
					var latest *storage.StoredDoc
					var readErr error
					want := storage.PushPreconditionFailed
					switch state {
					case "missing":
						readErr = model.ErrNotFound
						want = storage.PushMissing
					case "deleted":
						latest = &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Deleted: true, Version: 1}
						want = storage.PushTombstoned
					case "new_version":
						latest = &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 1}
						want = storage.PushVersionMismatch
					default:
						latest = &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 0}
					}
					store.On("Get", mock.Anything, "db", "items/alice").Return(latest, readErr).Once()
					resp, err := New(store, nil).Push(context.Background(), "db", pushRequest(action, ptr(0)))
					require.NoError(t, err)
					require.Equal(t, []storage.ReplicationPushConflict{{ID: "alice", Reason: want, Current: latest}}, resp.Conflicts)
					store.AssertExpectations(t)
				})
			}
		}
	}
}

func TestPushValidatesEntireBatch(t *testing.T) {
	tests := map[string]func(*storage.ReplicationPushChange){
		"missing action":        func(c *storage.ReplicationPushChange) { c.Action = "" },
		"unknown action":        func(c *storage.ReplicationPushChange) { c.Action = "merge" },
		"negative version":      func(c *storage.ReplicationPushChange) { c.BaseVersion = ptr(-1) },
		"nil document":          func(c *storage.ReplicationPushChange) { c.Doc = nil },
		"database mismatch":     func(c *storage.ReplicationPushChange) { c.Doc.Database = "other" },
		"collection mismatch":   func(c *storage.ReplicationPushChange) { c.Doc.Collection = "other" },
		"foreign path":          func(c *storage.ReplicationPushChange) { c.Doc.Fullpath = "other/alice" },
		"nested path":           func(c *storage.ReplicationPushChange) { c.Doc.Fullpath = "items/alice/child/id" },
		"invalid path":          func(c *storage.ReplicationPushChange) { c.Doc.Fullpath = "items/*" },
		"missing identity":      func(c *storage.ReplicationPushChange) { c.Doc.Fullpath = "" },
		"contradictory ID":      func(c *storage.ReplicationPushChange) { c.Doc.Data["id"] = "bob" },
		"wrong ID type":         func(c *storage.ReplicationPushChange) { c.Doc.Data["id"] = 1 },
		"contradictory delete":  func(c *storage.ReplicationPushChange) { c.Doc.Deleted = true },
		"invalid payload":       func(c *storage.ReplicationPushChange) { c.Doc.Data["bad"] = math.NaN() },
		"invalid UTF8 key":      func(c *storage.ReplicationPushChange) { c.Doc.Data["\xff"] = true },
		"invalid UTF8 value":    func(c *storage.ReplicationPushChange) { c.Doc.Data["bad"] = "\xff" },
		"unsupported number":    func(c *storage.ReplicationPushChange) { c.Doc.Data["bad"] = uint64(1) },
		"out of domain integer": func(c *storage.ReplicationPushChange) { c.Doc.Data["bad"] = json.Number("9223372036854775808") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			store := new(MockStorageBackend)
			req := pushRequest(storage.PushUpdate, nil)
			bad := pushRequest(storage.PushUpdate, nil).Changes[0]
			mutate(&bad)
			req.Changes = append(req.Changes, bad)
			resp, err := New(store, nil).Push(context.Background(), "db", req)
			require.ErrorIs(t, err, model.ErrInvalidQuery)
			require.Nil(t, resp)
			require.Empty(t, store.Calls)
		})
	}
	for _, db := range []string{"", "bad\x00db", "\xff"} {
		require.ErrorIs(t, ValidatePushRequest(db, pushRequest(storage.PushUpdate, nil)), model.ErrInvalidQuery)
	}
	for _, collection := range []string{"", "*", "items/alice", "\xff", "items\x00"} {
		req := pushRequest(storage.PushUpdate, nil)
		req.Collection = collection
		require.ErrorIs(t, ValidatePushRequest("db", req), model.ErrInvalidQuery)
	}
	store := new(MockStorageBackend)
	resp, err := New(store, nil).Push(context.Background(), "db", storage.ReplicationPushRequest{Collection: "items"})
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	require.Nil(t, resp)
	require.Empty(t, store.Calls)
	req := pushRequest(storage.PushUpdate, nil)
	req.Changes[0].Doc.Data["bad"] = math.NaN()
	err = ValidatePushRequest("db", req)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	var marshalErr *json.UnsupportedValueError
	require.ErrorAs(t, err, &marshalErr)
}

func TestPushCreateRaceAndErrors(t *testing.T) {
	live := &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 1, Data: map[string]interface{}{"value": "other"}}
	tombstone := &storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 2, Deleted: true}
	diskFailure, readFailure := errors.New("disk failure"), errors.New("read failure")
	for _, tc := range []struct {
		name     string
		initial  *storage.StoredDoc
		writeErr error
		latest   *storage.StoredDoc
		readErr  error
		reason   storage.PushConflictReason
		wantErr  error
	}{
		{name: "concurrent insert", writeErr: model.ErrExists, latest: live, reason: storage.PushAlreadyExists},
		{name: "concurrent recreation", initial: tombstone, writeErr: model.ErrPreconditionFailed, latest: live, reason: storage.PushAlreadyExists},
		{name: "deleted before reread", writeErr: model.ErrExists, latest: tombstone, reason: storage.PushTombstoned},
		{name: "purged before reread", writeErr: model.ErrPreconditionFailed, readErr: model.ErrNotFound, reason: storage.PushMissing},
		{name: "insert conflict read failure", writeErr: model.ErrExists, readErr: readFailure, wantErr: readFailure},
		{name: "recreation conflict read failure", initial: tombstone, writeErr: model.ErrPreconditionFailed, readErr: readFailure, wantErr: readFailure},
		{name: "storage failure", writeErr: diskFailure, wantErr: diskFailure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := new(routedReadStorage)
			options := []storage.ReadOptions{{Consistency: storage.ReadAuthoritative, ShowDeleted: true}}
			var initialErr error
			if tc.initial == nil {
				initialErr = model.ErrNotFound
			}
			store.On("Get", mock.Anything, "db", "items/alice", options).Return(tc.initial, initialErr).Once()
			store.On("Create", mock.Anything, "db", mock.Anything).Return(fmt.Errorf("wrapped: %w", tc.writeErr)).Once()
			if tc.writeErr != diskFailure {
				store.On("Get", mock.Anything, "db", "items/alice", options).Return(tc.latest, tc.readErr).Once()
			}
			resp, err := New(store, nil).Push(context.Background(), "db", pushRequest(storage.PushCreate, ptr(1)))
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Nil(t, resp)
			} else {
				require.NoError(t, err)
				require.Equal(t, []storage.ReplicationPushConflict{{ID: "alice", Reason: tc.reason, Current: tc.latest}}, resp.Conflicts)
			}
			store.AssertExpectations(t)
			store.AssertNotCalled(t, "Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
			store.AssertNotCalled(t, "Delete", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestPushPreservesInputAndDuplicateOrder(t *testing.T) {
	store := new(MockStorageBackend)
	req := pushRequest(storage.PushUpdate, nil)
	req.Changes[0].Doc.Fullpath = ""
	req.Changes[0].Doc.Data["id"] = "alice"
	req.Changes[0].Doc.Data["version"] = int64(123)
	original := *req.Changes[0].Doc
	store.On("Get", mock.Anything, "db", "items/alice").Return(nil, model.ErrNotFound).Once()
	store.On("Create", mock.Anything, "db", mock.Anything).Return(nil).Once()
	for _, version := range []int64{1, 2} {
		change := pushRequest(storage.PushUpdate, ptr(0)).Changes[0]
		req.Changes = append(req.Changes, change)
		store.On("Get", mock.Anything, "db", "items/alice").Return(&storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: version}, nil).Once()
	}
	resp, err := New(store, nil).Push(context.Background(), "db", req)
	require.NoError(t, err)
	require.Equal(t, original, *req.Changes[0].Doc)
	require.Equal(t, int64(123), req.Changes[0].Doc.Data["version"])
	require.Len(t, resp.Conflicts, 2)
	require.Equal(t, 1, resp.Conflicts[0].ChangeIndex)
	require.Equal(t, 2, resp.Conflicts[1].ChangeIndex)
	require.Equal(t, int64(1), resp.Conflicts[0].Current.Version)
	require.Equal(t, int64(2), resp.Conflicts[1].Current.Version)
	store.AssertExpectations(t)
}

func TestPushLiveConditionalWritesPreservePayload(t *testing.T) {
	for _, action := range []storage.PushAction{storage.PushUpdate, storage.PushDelete} {
		for _, version := range []int64{0, 1} {
			t.Run(fmt.Sprintf("%s/%d", action, version), func(t *testing.T) {
				store := new(MockStorageBackend)
				req := pushRequest(action, ptr(version))
				req.Changes[0].Doc.Data["version"] = int64(999)
				store.On("Get", mock.Anything, "db", "items/alice").Return(&storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: version}, nil).Once()
				pred := model.Filters{{Field: "version", Op: model.OpEq, Value: version}}
				if action == storage.PushDelete {
					store.On("Delete", mock.Anything, "db", "items/alice", pred).Return(nil).Once()
				} else {
					store.On("Update", mock.Anything, "db", "items/alice", map[string]interface{}{"value": "new"}, pred).Return(nil).Once()
				}
				resp, err := New(store, nil).Push(context.Background(), "db", req)
				require.NoError(t, err)
				require.Empty(t, resp.Conflicts)
				require.Equal(t, int64(999), req.Changes[0].Doc.Data["version"])
				store.AssertExpectations(t)
			})
		}
	}
}

func TestPushUnversionedDeleteConcurrentTombstone(t *testing.T) {
	store := new(MockStorageBackend)
	store.On("Get", mock.Anything, "db", "items/alice").Return(&storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 1}, nil).Once()
	store.On("Delete", mock.Anything, "db", "items/alice", model.Filters{}).Return(model.ErrNotFound).Once()
	store.On("Get", mock.Anything, "db", "items/alice").Return(&storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 2, Deleted: true}, nil).Once()
	resp, err := New(store, nil).Push(context.Background(), "db", pushRequest(storage.PushDelete, nil))
	require.NoError(t, err)
	require.Empty(t, resp.Conflicts)
	store.AssertExpectations(t)
}

func TestPushNormalizesNestedValuesWithoutChangingInput(t *testing.T) {
	for _, creating := range []bool{false, true} {
		t.Run(fmt.Sprintf("create=%v", creating), func(t *testing.T) {
			store := new(MockStorageBackend)
			req := pushRequest(storage.PushUpdate, nil)
			data := req.Changes[0].Doc.Data
			nested := map[string]interface{}{"int": json.Number("9007199254740993"), "float": json.Number("1.0"), "values": []int32{1, 2}}
			data["nested"] = nested
			want := map[string]interface{}{"int": int64(9007199254740993), "float": float64(1), "values": []interface{}{int64(1), int64(2)}}
			if creating {
				store.On("Get", mock.Anything, "db", "items/alice").Return(nil, model.ErrNotFound).Once()
				store.On("Create", mock.Anything, "db", mock.Anything).Run(func(args mock.Arguments) {
					stored := args.Get(2).(storage.StoredDoc)
					require.Equal(t, want, stored.Data["nested"])
					stored.Data["nested"].(map[string]interface{})["int"] = int64(7)
				}).Return(nil).Once()
			} else {
				store.On("Get", mock.Anything, "db", "items/alice").Return(&storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 1}, nil).Once()
				store.On("Update", mock.Anything, "db", "items/alice", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					stored := args.Get(3).(map[string]interface{})
					require.Equal(t, want, stored["nested"])
					stored["nested"].(map[string]interface{})["int"] = int64(7)
				}).Return(nil).Once()
			}
			resp, err := New(store, nil).Push(context.Background(), "db", req)
			require.NoError(t, err)
			require.Empty(t, resp.Conflicts)
			require.Equal(t, json.Number("9007199254740993"), nested["int"])
			require.Equal(t, json.Number("1.0"), nested["float"])
			require.Equal(t, []int32{1, 2}, nested["values"])
			store.AssertExpectations(t)
		})
	}
}

func TestPushRejectsOversizedEncodedRequestBeforeStorage(t *testing.T) {
	store := new(MockStorageBackend)
	req := pushRequest(storage.PushUpdate, nil)
	oversized := pushRequest(storage.PushUpdate, nil).Changes[0]
	oversized.Doc.Data["large"] = strings.Repeat("x", wire.MaxGRPCBytes)
	req.Changes = append(req.Changes, oversized)
	response, err := New(store, nil).Push(context.Background(), "db", req)
	require.ErrorIs(t, err, model.ErrInvalidQuery)
	require.Nil(t, response)
	require.Empty(t, store.Calls)
}

func TestPushRejectsOversizedResponseAfterOrderedWrites(t *testing.T) {
	store := new(MockStorageBackend)
	req := pushRequest(storage.PushUpdate, nil)
	store.On("Get", mock.Anything, "db", "items/alice").Return(&storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 1}, nil).Once()
	store.On("Update", mock.Anything, "db", "items/alice", mock.Anything, model.Filters{}).Return(nil).Once()
	large := strings.Repeat("x", wire.MaxGRPCBytes/2)
	for range 2 {
		req.Changes = append(req.Changes, pushRequest(storage.PushUpdate, ptr(0)).Changes[0])
		store.On("Get", mock.Anything, "db", "items/alice").Return(&storage.StoredDoc{Database: "db", Collection: "items", Fullpath: "items/alice", Version: 1, Data: map[string]interface{}{"large": large}}, nil).Once()
	}
	response, err := New(store, nil).Push(context.Background(), "db", req)
	require.ErrorIs(t, err, model.ErrQueryWorkLimit)
	require.Nil(t, response)
	store.AssertExpectations(t)
	store.AssertNumberOfCalls(t, "Update", 1)
}
