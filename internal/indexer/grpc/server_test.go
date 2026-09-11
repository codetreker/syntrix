package grpc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// mockLocalService implements LocalService for testing.
type mockLocalService struct {
	statsFn  func(context.Context) (manager.Stats, error)
	searchFn func(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error)
	healthFn func(ctx context.Context) (manager.Health, error)
	mgr      *manager.Manager
}

func (m *mockLocalService) Search(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, database, plan)
	}
	return nil, nil
}

func (m *mockLocalService) Health(ctx context.Context) (manager.Health, error) {
	if m.healthFn != nil {
		return m.healthFn(ctx)
	}
	return manager.Health{Status: "ok"}, nil
}

func (m *mockLocalService) Stats(ctx context.Context) (manager.Stats, error) {
	if m.statsFn != nil {
		return m.statsFn(ctx)
	}
	return manager.Stats{}, nil
}

func TestServer_Stats(t *testing.T) {
	t.Run("delegates complete service statistics", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		server := NewServer(&mockLocalService{statsFn: func(got context.Context) (manager.Stats, error) {
			assert.Equal(t, ctx, got)
			return manager.Stats{TemplateCount: 3, EventsApplied: 1<<53 + 1, LastEventTime: math.MaxInt64}, nil
		}})
		got, err := server.Stats(ctx, &indexerv1.StatsRequest{})
		require.NoError(t, err)
		assert.Equal(t, int64(3), got.TemplateCount)
		assert.Equal(t, int64(1<<53+1), got.EventsApplied)
		assert.Equal(t, int64(math.MaxInt64), got.LastEventTime)
	})
	for _, tc := range []struct {
		name string
		err  error
		code codes.Code
	}{
		{"canceled", context.Canceled, codes.Canceled},
		{"deadline", context.DeadlineExceeded, codes.DeadlineExceeded},
		{"wrapped cancellation", fmt.Errorf("collector: %w", context.Canceled), codes.Canceled},
		{"collector failure", assert.AnError, codes.Internal},
		{"status", status.Error(codes.Unavailable, "collector unavailable"), codes.Unavailable},
		{"wrapped status", fmt.Errorf("collector: %w", status.Error(codes.ResourceExhausted, "busy")), codes.ResourceExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(&mockLocalService{statsFn: func(context.Context) (manager.Stats, error) { return manager.Stats{}, tc.err }})
			got, err := server.Stats(context.Background(), &indexerv1.StatsRequest{})
			require.Error(t, err)
			assert.Nil(t, got)
			assert.Equal(t, tc.code, status.Code(err))
		})
	}
}

func (m *mockLocalService) Manager() *manager.Manager {
	return m.mgr
}

func TestServer_Search(t *testing.T) {
	ctx := context.Background()

	t.Run("successful search", func(t *testing.T) {
		mock := &mockLocalService{
			searchFn: func(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error) {
				assert.Equal(t, "testdb", database)
				assert.Equal(t, "users/alice/chats", plan.Collection)
				assert.Equal(t, 10, plan.Limit)
				return []manager.DocRef{
					{ID: "doc1", OrderKey: []byte{0x01, 0x02}},
					{ID: "doc2", OrderKey: []byte{0x03, 0x04}},
				}, nil
			},
			mgr: manager.New(mem_store.New()),
		}

		server := NewServer(mock)
		resp, err := server.Search(ctx, &indexerv1.SearchRequest{
			Database:   "testdb",
			Collection: "users/alice/chats",
			Limit:      10,
		})

		require.NoError(t, err)
		require.NotNil(t, resp)
		assert.Len(t, resp.Docs, 2)
		assert.Equal(t, "doc1", resp.Docs[0].Id)
		assert.Equal(t, base64.StdEncoding.EncodeToString([]byte{0x01, 0x02}), resp.Docs[0].OrderKey)
		assert.Equal(t, "doc2", resp.Docs[1].Id)
	})

	t.Run("search with filters", func(t *testing.T) {
		mock := &mockLocalService{
			searchFn: func(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error) {
				assert.Len(t, plan.Filters, 1)
				assert.Equal(t, "status", plan.Filters[0].Field)
				assert.Equal(t, manager.FilterEq, plan.Filters[0].Op)
				assert.Equal(t, "active", plan.Filters[0].Value)
				return nil, nil
			},
			mgr: manager.New(mem_store.New()),
		}

		server := NewServer(mock)
		valueBytes, _ := json.Marshal("active")
		_, err := server.Search(ctx, &indexerv1.SearchRequest{
			Database:   "testdb",
			Collection: "users/alice/chats",
			Filters: []*indexerv1.Filter{
				{Field: "status", Op: "eq", Value: valueBytes},
			},
		})

		require.NoError(t, err)
	})

	t.Run("search with order by", func(t *testing.T) {
		mock := &mockLocalService{
			searchFn: func(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error) {
				assert.Len(t, plan.OrderBy, 1)
				assert.Equal(t, "timestamp", plan.OrderBy[0].Field)
				return nil, nil
			},
			mgr: manager.New(mem_store.New()),
		}

		server := NewServer(mock)
		_, err := server.Search(ctx, &indexerv1.SearchRequest{
			Database:   "testdb",
			Collection: "users/alice/chats",
			OrderBy: []*indexerv1.OrderByField{
				{Field: "timestamp", Direction: "desc"},
			},
		})

		require.NoError(t, err)
	})

	t.Run("search with no matching index", func(t *testing.T) {
		mock := &mockLocalService{
			searchFn: func(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error) {
				return nil, manager.ErrNoMatchingIndex
			},
			mgr: manager.New(mem_store.New()),
		}

		server := NewServer(mock)
		_, err := server.Search(ctx, &indexerv1.SearchRequest{
			Database:   "testdb",
			Collection: "unknown/path",
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "no matching index")
	})
}

func TestServer_Health(t *testing.T) {
	ctx := context.Background()

	t.Run("healthy status", func(t *testing.T) {
		mock := &mockLocalService{
			healthFn: func(ctx context.Context) (manager.Health, error) {
				return manager.Health{
					Status: "ok",
				}, nil
			},
			mgr: manager.New(mem_store.New()),
		}

		server := NewServer(mock)
		resp, err := server.Health(ctx, &indexerv1.HealthRequest{})

		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Status)
	})

	t.Run("with index health", func(t *testing.T) {
		mock := &mockLocalService{
			healthFn: func(ctx context.Context) (manager.Health, error) {
				return manager.Health{
					Status: "ok",
					Indexes: map[string]manager.IndexHealth{
						"idx1": {State: "healthy", DocCount: 10},
						"idx2": {State: "healthy", DocCount: 20},
					},
				}, nil
			},
		}

		server := NewServer(mock)
		resp, err := server.Health(ctx, &indexerv1.HealthRequest{})

		require.NoError(t, err)
		assert.Equal(t, "ok", resp.Status)
		assert.Len(t, resp.Indexes, 2)
	})
}

func stateTestManager(t *testing.T) *manager.Manager {
	t.Helper()
	st := mem_store.New()
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	mgr := manager.New(st)
	require.NoError(t, mgr.LoadTemplatesFromBytes([]byte(`templates:
- name: chats_by_timestamp
  collectionPattern: users/{uid}/chats
  fields: [{field: timestamp, order: desc}]
- name: chats_by_name
  collectionPattern: users/{uid}/chats
  fields: [{field: name, order: asc}]
- name: messages_by_timestamp
  collectionPattern: rooms/{rid}/messages
  fields: [{field: timestamp, order: desc}]
`)))
	return mgr
}
func seedStateIndex(t *testing.T, mgr *manager.Manager, database, collection, templateName string) store.QueryIndexRef {
	t.Helper()
	for _, tmpl := range mgr.Templates() {
		if tmpl.Name != templateName {
			continue
		}
		ref := store.QueryIndexRef{Database: database, Collection: collection, TemplateFingerprint: tmpl.Fingerprint(), Generation: "active"}
		field := encoding.Field{Value: int64(1), Direction: encoding.Desc}
		if templateName == "chats_by_name" {
			field = encoding.Field{Value: "Alice", Direction: encoding.Asc}
		}
		key, err := encoding.Encode([]encoding.Field{field}, "doc1")
		require.NoError(t, err)
		require.NoError(t, mgr.Store().ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "doc1", PostingKeys: [][]byte{key}}}, ""))
		require.NoError(t, mgr.Store().PublishGeneration(ref, ""))
		return ref
	}
	t.Fatalf("template %q not found", templateName)
	return store.QueryIndexRef{}
}
func TestServer_GetState(t *testing.T) {
	ctx := context.Background()
	t.Run("returns actual state", func(t *testing.T) {
		mgr := stateTestManager(t)
		seedStateIndex(t, mgr, "db1", "users/alice/chats", "chats_by_timestamp")
		seedStateIndex(t, mgr, "db2", "rooms/main/messages", "messages_by_timestamp")
		server := NewServer(&mockLocalService{mgr: mgr})
		resp, err := server.GetState(ctx, &indexerv1.GetStateRequest{})
		require.NoError(t, err)
		require.Len(t, resp.Actual, 2)
		for _, actual := range resp.Actual {
			assert.Equal(t, "healthy", actual.State)
			assert.EqualValues(t, -1, actual.DocCount)
		}
	})
	t.Run("filters by database", func(t *testing.T) {
		mgr := stateTestManager(t)
		seedStateIndex(t, mgr, "db1", "users/alice/chats", "chats_by_timestamp")
		seedStateIndex(t, mgr, "db2", "rooms/main/messages", "messages_by_timestamp")
		server := NewServer(&mockLocalService{mgr: mgr})
		resp, err := server.GetState(ctx, &indexerv1.GetStateRequest{Database: "db1"})
		require.NoError(t, err)
		require.Len(t, resp.Actual, 1)
		assert.Equal(t, "db1", resp.Actual[0].Database)
	})
	t.Run("filters by pattern", func(t *testing.T) {
		mgr := stateTestManager(t)
		seedStateIndex(t, mgr, "db1", "users/alice/chats", "chats_by_timestamp")
		seedStateIndex(t, mgr, "db1", "rooms/main/messages", "messages_by_timestamp")
		server := NewServer(&mockLocalService{mgr: mgr})
		resp, err := server.GetState(ctx, &indexerv1.GetStateRequest{Pattern: "users/*/chats"})
		require.NoError(t, err)
		require.Len(t, resp.Actual, 1)
		assert.Equal(t, "users/alice/chats", resp.Actual[0].Pattern)
		assert.Equal(t, "chats_by_timestamp", resp.Actual[0].TemplateId)
	})
}
func TestServer_InvalidateIndex(t *testing.T) {
	ctx := context.Background()
	t.Run("invalidates specific index", func(t *testing.T) {
		mgr := stateTestManager(t)
		ref := seedStateIndex(t, mgr, "db1", "users/alice/chats", "chats_by_timestamp")
		other := seedStateIndex(t, mgr, "db1", "rooms/main/messages", "messages_by_timestamp")
		server := NewServer(&mockLocalService{mgr: mgr})
		resp, err := server.InvalidateIndex(ctx, &indexerv1.InvalidateIndexRequest{Database: "db1", Pattern: "users/*/chats", TemplateId: "chats_by_timestamp"})
		require.NoError(t, err)
		assert.EqualValues(t, 1, resp.IndexesInvalidated)
		generation, found, err := mgr.Store().ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
		require.NoError(t, err)
		require.True(t, found)
		assert.False(t, generation.Ready)
		assert.NotEmpty(t, generation.Failure)
		generation, found, err = mgr.Store().ReadGeneration(other.Database, other.Collection, other.TemplateFingerprint)
		require.NoError(t, err)
		require.True(t, found)
		assert.True(t, generation.Ready)
	})
	t.Run("invalidates all indexes for pattern", func(t *testing.T) {
		mgr := stateTestManager(t)
		first := seedStateIndex(t, mgr, "db1", "users/alice/chats", "chats_by_timestamp")
		second := seedStateIndex(t, mgr, "db1", "users/alice/chats", "chats_by_name")
		seedStateIndex(t, mgr, "db1", "rooms/main/messages", "messages_by_timestamp")
		server := NewServer(&mockLocalService{mgr: mgr})
		resp, err := server.InvalidateIndex(ctx, &indexerv1.InvalidateIndexRequest{Database: "db1", Pattern: "users/*/chats"})
		require.NoError(t, err)
		assert.EqualValues(t, 2, resp.IndexesInvalidated)
		for _, ref := range []store.QueryIndexRef{first, second} {
			generation, found, err := mgr.Store().ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
			require.NoError(t, err)
			require.True(t, found)
			assert.False(t, generation.Ready)
			assert.NotEmpty(t, generation.Failure)
		}
	})
}

func TestServer_Reload(t *testing.T) {
	ctx := context.Background()

	t.Run("returns current template count", func(t *testing.T) {
		mgr := manager.New(mem_store.New())
		mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: test
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
`))

		mock := &mockLocalService{mgr: mgr}
		server := NewServer(mock)

		resp, err := server.Reload(ctx, &indexerv1.ReloadRequest{})

		require.NoError(t, err)
		assert.Equal(t, int32(1), resp.TemplatesLoaded)
	})
}

func TestParseFilterOp(t *testing.T) {
	tests := []struct {
		input string
		want  manager.FilterOp
		err   bool
	}{
		{"eq", manager.FilterEq, false},
		{"gt", manager.FilterGt, false},
		{"lt", manager.FilterLt, false},
		{"gte", manager.FilterGte, false},
		{"lte", manager.FilterLte, false},
		{"invalid", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseFilterOp(tt.input)
			if tt.err {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestServer_ConvertError(t *testing.T) {
	server := NewServer(&mockLocalService{mgr: manager.New(mem_store.New())})

	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{"ErrIndexNotReady", manager.ErrIndexNotReady, "index not ready"},
		{"ErrIndexRebuilding", manager.ErrIndexRebuilding, "index is rebuilding"},
		{"ErrInvalidPlan", manager.ErrInvalidPlan, "invalid query plan"},
		{"unknown error", assert.AnError, "internal error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grpcErr := server.convertError(tt.err)
			assert.Contains(t, grpcErr.Error(), tt.wantMsg)
		})
	}
}

func TestServer_GetState_WithTemplates(t *testing.T) {
	ctx := context.Background()

	st := mem_store.New()
	mgr := manager.New(st)
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
      - field: priority
        order: asc
`))
	require.NoError(t, err)

	// Create an index by upserting a document
	st.Upsert("db1", "users/*/chats", "chat-ts", "doc1", []byte{0x01}, "")

	mock := &mockLocalService{mgr: mgr}
	server := NewServer(mock)

	resp, err := server.GetState(ctx, &indexerv1.GetStateRequest{})

	require.NoError(t, err)
	// Should have both desired and actual
	assert.Len(t, resp.Desired, 1)
	assert.Len(t, resp.Desired[0].Fields, 2)
	assert.Equal(t, "timestamp", resp.Desired[0].Fields[0].Field)
	assert.Equal(t, "desc", resp.Desired[0].Fields[0].Direction)
	assert.Equal(t, "priority", resp.Desired[0].Fields[1].Field)
	assert.Equal(t, "asc", resp.Desired[0].Fields[1].Direction)
}

func TestServer_Search_IndexStatus(t *testing.T) {
	ctx := context.Background()

	st := mem_store.New()
	mgr := manager.New(st)
	// Load template
	err := mgr.LoadTemplatesFromBytes([]byte(`
templates:
  - name: chat-ts
    collectionPattern: users/{uid}/chats
    fields:
      - field: timestamp
        order: desc
`))
	require.NoError(t, err)

	// Create index by upserting a document
	st.Upsert("db1", "users/*/chats", "chat-ts", "doc1", []byte{0x01}, "")

	mock := &mockLocalService{
		searchFn: func(ctx context.Context, database string, plan manager.Plan) ([]manager.DocRef, error) {
			return []manager.DocRef{{ID: "doc1", OrderKey: []byte{0x01}}}, nil
		},
		mgr: mgr,
	}

	server := NewServer(mock)
	resp, err := server.Search(ctx, &indexerv1.SearchRequest{
		Database:   "db1",
		Collection: "users/alice/chats",
		OrderBy: []*indexerv1.OrderByField{
			{Field: "timestamp", Direction: "desc"},
		},
	})

	require.NoError(t, err)
	assert.Len(t, resp.Docs, 1)
	assert.Equal(t, "doc1", resp.Docs[0].Id)
}
