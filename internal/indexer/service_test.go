package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"github.com/syntrixbase/syntrix/internal/puller"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

// mockPullerService is a mock implementation of puller.Service for testing.
type mockPullerService struct {
	mu          sync.Mutex
	events      chan *puller.Event
	subscribers []mockSubscription
}

type mockSubscription struct {
	consumerID string
	after      string
	ch         chan *puller.Event
}

func newMockPullerService() *mockPullerService {
	return &mockPullerService{
		events: make(chan *puller.Event, 100),
	}
}

func (m *mockPullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *puller.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	ch := make(chan *puller.Event, 100)
	m.subscribers = append(m.subscribers, mockSubscription{
		consumerID: consumerID,
		after:      after,
		ch:         ch,
	})

	// Forward events from main channel to subscriber
	go func() {
		for {
			select {
			case <-ctx.Done():
				close(ch)
				return
			case evt, ok := <-m.events:
				if !ok {
					close(ch)
					return
				}
				select {
				case ch <- evt:
				case <-ctx.Done():
					close(ch)
					return
				}
			}
		}
	}()

	return ch
}

func (m *mockPullerService) SendEvent(evt *puller.Event) {
	m.events <- evt
}

func (m *mockPullerService) Close() {
	close(m.events)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestService creates a new service for testing, panics on error.
func newTestService(cfg config.Config, pullerSvc puller.Service, logger *slog.Logger) LocalService {
	svc, err := NewService(cfg, pullerSvc, logger)
	if err != nil {
		panic(fmt.Sprintf("failed to create test service: %v", err))
	}
	return svc
}

func TestNewService(t *testing.T) {
	t.Run("with defaults", func(t *testing.T) {
		cfg := config.Config{}
		svc := newTestService(cfg, nil, testLogger())

		require.NotNil(t, svc)
		s := svc.(*service)
		assert.Equal(t, "indexer", s.cfg.ConsumerID)
		assert.Equal(t, "data/indexer/progress", s.cfg.ProgressPath)
	})

	t.Run("with custom config", func(t *testing.T) {
		cfg := config.Config{
			ConsumerID:   "my-indexer",
			ProgressPath: "/custom/path",
		}
		svc := newTestService(cfg, nil, testLogger())

		s := svc.(*service)
		assert.Equal(t, "my-indexer", s.cfg.ConsumerID)
		assert.Equal(t, "/custom/path", s.cfg.ProgressPath)
	})
}

func TestService_StartStop(t *testing.T) {
	t.Run("start without puller", func(t *testing.T) {
		cfg := config.Config{}
		svc := newTestService(cfg, nil, testLogger())

		ctx := context.Background()
		err := svc.Start(ctx)
		require.NoError(t, err)

		// Verify running
		health, err := svc.Health(ctx)
		require.NoError(t, err)
		assert.Equal(t, HealthOK, health.Status)

		// Stop
		err = svc.Stop(ctx)
		require.NoError(t, err)
	})

	t.Run("start already running", func(t *testing.T) {
		cfg := config.Config{}
		svc := newTestService(cfg, nil, testLogger())

		ctx := context.Background()
		err := svc.Start(ctx)
		require.NoError(t, err)
		defer svc.Stop(ctx)

		// Try to start again - returns error
		err = svc.Start(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already running")
	})

	t.Run("stop not running", func(t *testing.T) {
		cfg := config.Config{}
		svc := newTestService(cfg, nil, testLogger())

		ctx := context.Background()
		err := svc.Stop(ctx)
		require.NoError(t, err)
	})

	t.Run("start with puller", func(t *testing.T) {
		mockPuller := newMockPullerService()
		cfg := config.Config{}
		svc := newTestService(cfg, mockPuller, testLogger())

		ctx := context.Background()
		err := svc.Start(ctx)
		require.NoError(t, err)

		// Give subscription loop time to start
		time.Sleep(10 * time.Millisecond)

		err = svc.Stop(ctx)
		require.NoError(t, err)
	})
}

func publishTestGeneration(t *testing.T, svc LocalService, database, collection string) []store.QueryIndexRef {
	t.Helper()
	var refs []store.QueryIndexRef
	for _, match := range svc.Manager().MatchTemplatesForCollection(collection) {
		ref := store.QueryIndexRef{Database: database, Collection: collection, TemplateFingerprint: match.Template.Fingerprint(), Generation: "test-generation"}
		require.NoError(t, svc.Manager().Store().PublishGeneration(ref, ""))
		refs = append(refs, ref)
	}
	return refs
}

func readProjectionRows(t *testing.T, st store.Store, ref store.QueryIndexRef) []store.DocRef {
	t.Helper()
	view, err := st.ReadView(context.Background(), ref, store.ReadBudget{MaxOverlayReplacements: 1000, MaxOverlayBytes: 4 << 20})
	require.NoError(t, err)
	defer view.Close()
	iterator, err := view.Scan(context.Background(), store.SearchOptions{})
	require.NoError(t, err)
	defer iterator.Close()
	var rows []store.DocRef
	for {
		row, ok, err := iterator.Next()
		require.NoError(t, err)
		if !ok {
			break
		}
		rows = append(rows, row)
	}
	return rows
}

func TestService_ApplyEvent(t *testing.T) {
	svc := newTestService(config.Config{}, nil, testLogger())
	defer svc.Manager().Store().Close()
	ctx := context.Background()
	require.NoError(t, svc.ApplyEvent(ctx, nil, ""))
	require.Error(t, svc.ApplyEvent(ctx, &ChangeEvent{Database: "db"}, "bad"))
	require.NoError(t, svc.Manager().LoadTemplatesFromBytes([]byte(`templates:
- name: by_tags
  collectionPattern: users/{uid}/docs
  fields:
  - { field: tags, mode: membership }
  - { field: createdAt, order: desc }
- name: by_created
  collectionPattern: users/{uid}/docs
  fields:
  - { field: createdAt, order: desc }
`)))
	refs := publishTestGeneration(t, svc, "db", "users/alice/docs")
	doc := types.NewStoredDoc("db", "users/alice/docs", "aa", map[string]any{"tags": []any{"one", "one", "two"}})
	event := &ChangeEvent{Database: "db", FullDocument: &doc}
	require.NoError(t, svc.ApplyEvent(ctx, event, "p1"))
	assert.Len(t, readProjectionRows(t, svc.Manager().Store(), refs[0]), 2)
	assert.Len(t, readProjectionRows(t, svc.Manager().Store(), refs[1]), 1)
	doc.Data["tags"] = []any{}
	require.NoError(t, svc.ApplyEvent(ctx, event, "p2"))
	assert.Empty(t, readProjectionRows(t, svc.Manager().Store(), refs[0]))
	assert.Len(t, readProjectionRows(t, svc.Manager().Store(), refs[1]), 1)
	doc.Deleted = true
	require.NoError(t, svc.ApplyEvent(ctx, event, "p3"))
	for _, ref := range refs {
		assert.Empty(t, readProjectionRows(t, svc.Manager().Store(), ref))
	}
	progress, err := svc.Manager().Store().LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "p3", progress)
}

func TestBuildDocumentProjection(t *testing.T) {
	tmpl := template.Template{CollectionPattern: "docs", IncludeDeleted: true, Fields: []template.Field{{Field: "tags", Mode: template.Membership}, {Field: "createdAt", Order: template.Desc}}}
	doc := types.NewStoredDoc("db", "docs", "aa", map[string]any{"id": "shadow", "createdAt": int64(999), "tags": []any{int64(1), float64(1), nil, "x", []any{"x"}, map[string]any{"tag": "x"}}})
	doc.CreatedAt = 42
	projection, err := BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
	require.NoError(t, err)
	assert.Equal(t, "aa", projection.DocumentID)
	assert.Len(t, projection.PostingKeys, 3)
	for _, member := range []any{nil, int64(1), "x"} {
		expected, err := encoding.Encode([]encoding.Field{{Value: member}, {Value: int64(42), Direction: encoding.Desc}}, "aa")
		require.NoError(t, err)
		assert.Contains(t, projection.PostingKeys, expected)
	}
	for _, value := range []any{nil, "x", []any{}} {
		doc.Data["tags"] = value
		projection, err := BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
		require.NoError(t, err)
		assert.Empty(t, projection.PostingKeys)
	}
	doc.Data["tags"] = []any{"kept"}
	doc.Deleted = true
	projection, err = BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
	require.NoError(t, err)
	assert.Empty(t, projection.PostingKeys)
	tmpl.Fields = []template.Field{{Field: "deleted", Order: template.Asc}, {Field: "business", Order: template.Asc}, {Field: "id", Order: template.Asc}}
	projection, err = BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
	require.NoError(t, err)
	expected, err := encoding.Encode([]encoding.Field{{Value: true}, {Missing: true}, {Value: "aa"}}, "aa")
	require.NoError(t, err)
	assert.Equal(t, [][]byte{expected}, projection.PostingKeys)
	doc.Deleted = false
	doc.Data["business"] = nil
	explicitNull, err := BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
	require.NoError(t, err)
	delete(doc.Data, "business")
	missing, err := BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
	require.NoError(t, err)
	assert.NotEqual(t, explicitNull.PostingKeys, missing.PostingKeys)
	doc.Data["business"] = []any{}
	_, err = BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
	require.Error(t, err)
}

func TestProjectionExpansionLimits(t *testing.T) {
	doc := types.NewStoredDoc("db", "docs", "id", map[string]any{"tags": []any{"a", "b", "c"}})
	tmpl := template.Template{CollectionPattern: "docs", Fields: []template.Field{{Field: "tags", Mode: template.Membership}}}
	for _, limits := range []ProjectionLimits{{2, 10, 1000, 4096}, {10, 2, 1000, 4096}, {10, 10, 3, 4096}, {10, 10, 1000, 3}} {
		_, err := BuildDocumentProjection(&doc, &tmpl, "gen", limits)
		require.ErrorIs(t, err, store.ErrWorkLimit)
	}
	doc.Data["tags"] = []any{uint64(1)}
	_, err := BuildDocumentProjection(&doc, &tmpl, "gen", DefaultProjectionLimits())
	require.Error(t, err)
}

func TestService_Search(t *testing.T) {
	svc := newTestService(config.Config{}, nil, testLogger())
	s := svc.(*service)

	templateYAML := `
templates:
  - name: chats_by_timestamp
    collectionPattern: users/{uid}/chats
    fields:
      - { field: timestamp, order: desc }
`
	err := s.manager.LoadTemplatesFromBytes([]byte(templateYAML))
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop(context.Background())

	// Add some documents
	for i := 1; i <= 5; i++ {
		docID := "doc" + string(rune('0'+i))
		evt := &ChangeEvent{
			EventID:  "evt" + string(rune('0'+i)),
			Database: "testdb",
			FullDocument: &storage.StoredDoc{
				Database:   "testdb",
				Id:         docID,
				Collection: "users/alice/chats",
				Fullpath:   "users/alice/chats/" + docID,
				Data: map[string]any{
					"id":        docID,
					"timestamp": float64(i * 1000),
				},
			},
		}
		publishTestGeneration(t, svc, "testdb", evt.FullDocument.Collection)
		err := svc.ApplyEvent(context.Background(), evt, "")
		require.NoError(t, err)
	}

	t.Run("basic search", func(t *testing.T) {
		results, err := svc.Search(context.Background(), "testdb", Plan{
			Collection: "users/alice/chats",
			OrderBy: []OrderField{
				{Field: "timestamp", Direction: 1}, // Desc
			},
			Limit: 10,
		})
		require.NoError(t, err)
		assert.Len(t, results, 5)
	})

	t.Run("search with limit", func(t *testing.T) {
		results, err := svc.Search(context.Background(), "testdb", Plan{
			Collection: "users/alice/chats",
			OrderBy: []OrderField{
				{Field: "timestamp", Direction: 1}, // Desc
			},
			Limit: 2,
		})
		require.NoError(t, err)
		assert.Len(t, results, 2)
	})

	t.Run("search non-existent database", func(t *testing.T) {
		results, err := svc.Search(context.Background(), "nonexistent", Plan{
			Collection: "users/alice/chats",
			OrderBy: []OrderField{
				{Field: "timestamp", Direction: 1},
			},
		})
		assert.ErrorIs(t, err, ErrIndexNotReady)
		assert.Empty(t, results)
	})
}

func TestService_Health(t *testing.T) {
	t.Run("healthy when running", func(t *testing.T) {
		svc := newTestService(config.Config{}, nil, testLogger())
		err := svc.Start(context.Background())
		require.NoError(t, err)
		defer svc.Stop(context.Background())

		health, err := svc.Health(context.Background())
		require.NoError(t, err)
		assert.Equal(t, HealthOK, health.Status)
	})

	t.Run("unhealthy when not running", func(t *testing.T) {
		svc := newTestService(config.Config{}, nil, testLogger())

		health, err := svc.Health(context.Background())
		require.NoError(t, err)
		assert.Equal(t, HealthUnhealthy, health.Status)
	})
}

func TestService_Stats(t *testing.T) {
	svc := newTestService(config.Config{}, nil, testLogger())
	s := svc.(*service)

	templateYAML := `
templates:
  - name: chats_by_timestamp
    collectionPattern: users/{uid}/chats
    fields:
      - { field: timestamp, order: desc }
`
	err := s.manager.LoadTemplatesFromBytes([]byte(templateYAML))
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop(context.Background())

	stats, err := svc.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, stats.TemplateCount)
	assert.Equal(t, int64(0), stats.EventsApplied)

	// Add an event
	evt := &ChangeEvent{
		EventID:  "evt1",
		Database: "testdb",
		FullDocument: &storage.StoredDoc{
			Database:   "testdb",
			Id:         "doc1",
			Collection: "users/alice/chats",
			Fullpath:   "users/alice/chats/doc1",
			Data: map[string]any{
				"id":        "doc1",
				"timestamp": float64(1000),
			},
		},
	}
	publishTestGeneration(t, svc, "testdb", evt.FullDocument.Collection)
	err = svc.ApplyEvent(context.Background(), evt, "")
	require.NoError(t, err)

	stats, err = svc.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), stats.EventsApplied)
	assert.Greater(t, stats.LastEventTime, int64(0))
}

func TestService_Manager(t *testing.T) {
	svc := newTestService(config.Config{}, nil, testLogger())
	s := svc.(*service)

	mgr := svc.Manager()
	assert.Same(t, s.manager, mgr)
}

func TestService_PullerSubscription(t *testing.T) {
	t.Run("receives events from puller", func(t *testing.T) {
		mockPuller := newMockPullerService()
		svc := newTestService(config.Config{}, mockPuller, testLogger())
		s := svc.(*service)

		templateYAML := `
templates:
  - name: chats_by_timestamp
    collectionPattern: users/{uid}/chats
    fields:
      - { field: timestamp, order: desc }
`
		err := s.manager.LoadTemplatesFromBytes([]byte(templateYAML))
		require.NoError(t, err)

		err = svc.Start(context.Background())
		require.NoError(t, err)
		defer svc.Stop(context.Background())

		// Give subscription loop time to start
		time.Sleep(50 * time.Millisecond)

		publishTestGeneration(t, svc, "testdb", "users/alice/chats")
		// Send an event through puller
		mockPuller.SendEvent(&puller.Event{
			Change: &events.StoreChangeEvent{
				EventID:  "evt1",
				Database: "testdb",
				FullDocument: &storage.StoredDoc{
					Database:   "testdb",
					Id:         "doc1",
					Collection: "users/alice/chats",
					Fullpath:   "users/alice/chats/doc1",
					Data: map[string]any{
						"id":        "doc1",
						"timestamp": float64(1000),
					},
				},
			},
			Progress: "progress-1",
		})

		// Wait for event to be processed
		time.Sleep(100 * time.Millisecond)

		// Verify event was applied
		stats, err := svc.Stats(context.Background())
		require.NoError(t, err)
		assert.Equal(t, int64(1), stats.EventsApplied)

		// Verify progress was updated
		s.mu.RLock()
		progress := s.progress
		s.mu.RUnlock()
		assert.Equal(t, "progress-1", progress)
	})
}

func TestService_StartWithTemplatePath(t *testing.T) {
	t.Run("valid template directory", func(t *testing.T) {
		// Create temp template directory
		tmpDir, err := os.MkdirTemp("", "templates-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		templateYAML := `
database: default
templates:
  - name: test_template
    collectionPattern: users/{uid}/docs
    fields:
      - { field: timestamp, order: desc }
`
		err = os.WriteFile(tmpDir+"/default.yml", []byte(templateYAML), 0644)
		require.NoError(t, err)

		cfg := config.Config{
			TemplatePath: tmpDir,
		}
		svc := newTestService(cfg, nil, testLogger())

		err = svc.Start(context.Background())
		require.NoError(t, err)
		defer svc.Stop(context.Background())

		// Verify templates were loaded
		stats, err := svc.Stats(context.Background())
		require.NoError(t, err)
		assert.Equal(t, 1, stats.TemplateCount)
	})

	t.Run("invalid template directory", func(t *testing.T) {
		cfg := config.Config{
			TemplatePath: "/nonexistent/path/templates",
		}
		svc := newTestService(cfg, nil, testLogger())

		err := svc.Start(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load templates")
	})
}

func TestService_StopWithTimeout(t *testing.T) {
	t.Run("stop with context timeout", func(t *testing.T) {
		// Create a mock puller that keeps subscription alive
		mockPuller := &slowPullerService{}
		svc := newTestService(config.Config{}, mockPuller, testLogger())

		err := svc.Start(context.Background())
		require.NoError(t, err)

		// Give subscription loop time to start
		time.Sleep(10 * time.Millisecond)

		// Stop with an already canceled context
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		// Stop returns context.Canceled because context is already cancelled
		// However, since subscriptionLoop might return immediately (before context check in Stop),
		// we might get nil (successful stop). We accept both.
		err = svc.Stop(ctx)
		if err != nil {
			assert.Equal(t, context.Canceled, err)
		}

		// Clean up with a proper context
		svc.Stop(context.Background())
	})
}

// slowPullerService is a mock that blocks on Subscribe
type slowPullerService struct{}

func (s *slowPullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *puller.Event {
	ch := make(chan *puller.Event)
	go func() {
		// Block until context is done
		<-ctx.Done()
		// Wait a bit before closing to simulate slow cleanup
		time.Sleep(100 * time.Millisecond)
		close(ch)
	}()
	return ch
}

func TestService_SubscriptionReconnect(t *testing.T) {
	t.Run("reconnects on channel close", func(t *testing.T) {
		reconnectPuller := &reconnectablePullerService{
			reconnectCount: 0,
		}
		svc := newTestService(config.Config{}, reconnectPuller, testLogger())
		s := svc.(*service)

		templateYAML := `
templates:
  - name: test_template
    collectionPattern: users/{uid}/docs
    fields:
      - { field: timestamp, order: desc }
`
		err := s.manager.LoadTemplatesFromBytes([]byte(templateYAML))
		require.NoError(t, err)

		err = svc.Start(context.Background())
		require.NoError(t, err)

		// Give subscription loop time to start
		time.Sleep(50 * time.Millisecond)

		// Close the first channel to trigger reconnect
		reconnectPuller.CloseCurrentChannel()

		// Wait for reconnect (1 second backoff + some margin)
		time.Sleep(1200 * time.Millisecond)

		// Verify reconnect happened
		reconnectPuller.mu.Lock()
		count := reconnectPuller.reconnectCount
		reconnectPuller.mu.Unlock()
		assert.GreaterOrEqual(t, count, 2, "should have reconnected at least once")

		svc.Stop(context.Background())
	})
}

// reconnectablePullerService tracks reconnection attempts
type reconnectablePullerService struct {
	mu             sync.Mutex
	reconnectCount int
	currentCh      chan *puller.Event
}

func (r *reconnectablePullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *puller.Event {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.reconnectCount++
	ch := make(chan *puller.Event, 10)
	r.currentCh = ch

	go func() {
		<-ctx.Done()
		r.mu.Lock()
		if r.currentCh == ch {
			close(ch)
		}
		r.mu.Unlock()
	}()

	return ch
}

func (r *reconnectablePullerService) CloseCurrentChannel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.currentCh != nil {
		close(r.currentCh)
		r.currentCh = nil
	}
}

func TestService_ProjectionFailurePreservesEventBoundary(t *testing.T) {
	svc := newTestService(config.Config{}, nil, testLogger())
	defer svc.Manager().Store().Close()
	require.NoError(t, svc.Manager().LoadTemplatesFromBytes([]byte(`templates:
- name: first
  collectionPattern: docs
  fields: [{field: value, order: asc}]
- name: second
  collectionPattern: docs
  fields: [{field: other, order: asc}]
`)))
	refs := publishTestGeneration(t, svc, "db", "docs")
	doc := types.NewStoredDoc("db", "docs", "id", map[string]any{"value": 1, "other": 2})
	event := &ChangeEvent{Database: "db", FullDocument: &doc}
	require.NoError(t, svc.ApplyEvent(context.Background(), event, "good"))
	before := make([][]store.DocRef, len(refs))
	for i, ref := range refs {
		before[i] = readProjectionRows(t, svc.Manager().Store(), ref)
	}
	doc.Data["value"] = 3
	doc.Data["other"] = []any{4}
	require.Error(t, svc.ApplyEvent(context.Background(), event, "bad"))
	progress, err := svc.Manager().Store().LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "good", progress)
	for i, ref := range refs {
		assert.Equal(t, before[i], readProjectionRows(t, svc.Manager().Store(), ref))
		generation, found, err := svc.Manager().Store().ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
		require.NoError(t, err)
		require.True(t, found)
		assert.NotEmpty(t, generation.Failure)
	}
	stats, err := svc.Stats(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats.EventsApplied)
}

func TestService_SubscriptionReconnect_ContextCanceled(t *testing.T) {
	t.Run("context canceled during reconnect backoff", func(t *testing.T) {
		// Create a puller that closes channel immediately
		closingPuller := &immediateClosePullerService{}
		svc := newTestService(config.Config{}, closingPuller, testLogger())

		ctx, cancel := context.WithCancel(context.Background())
		err := svc.Start(ctx)
		require.NoError(t, err)

		// Give subscription loop time to start and hit the reconnect path
		time.Sleep(50 * time.Millisecond)

		// Cancel context while in the reconnect backoff
		cancel()

		// Wait for the subscription loop to exit
		time.Sleep(100 * time.Millisecond)

		// Clean up
		svc.Stop(context.Background())
	})
}

// immediateClosePullerService closes the channel immediately to trigger reconnect
type immediateClosePullerService struct {
	mu        sync.Mutex
	callCount int
}

func (p *immediateClosePullerService) Subscribe(ctx context.Context, consumerID string, after string) <-chan *puller.Event {
	p.mu.Lock()
	p.callCount++
	count := p.callCount
	p.mu.Unlock()

	ch := make(chan *puller.Event)

	// First call: close immediately to trigger reconnect
	// Subsequent calls: wait for context
	if count == 1 {
		go func() {
			time.Sleep(10 * time.Millisecond)
			close(ch)
		}()
	} else {
		go func() {
			<-ctx.Done()
			close(ch)
		}()
	}

	return ch
}

// ============================================================================
// Pebble Storage Mode Tests
// ============================================================================

func TestNewService_PebbleMode(t *testing.T) {
	t.Run("valid pebble config", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "indexer-pebble-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		cfg := config.Config{
			StorageMode: "pebble",
			Store: config.StoreConfig{
				Path:           tmpDir + "/test.db",
				BatchSize:      100,
				BatchInterval:  50 * time.Millisecond,
				BlockCacheSize: 8 * 1024 * 1024,
			},
		}

		svc, err := NewService(cfg, nil, testLogger())
		require.NoError(t, err)
		require.NotNil(t, svc)

		// Start and stop to verify it works
		ctx := context.Background()
		err = svc.Start(ctx)
		require.NoError(t, err)

		err = svc.Stop(ctx)
		require.NoError(t, err)
	})

	t.Run("pebble mode without logger uses provided logger", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "indexer-pebble-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		cfg := config.Config{
			StorageMode: "pebble",
			Store: config.StoreConfig{
				Path:           tmpDir + "/test.db",
				BatchSize:      100,
				BatchInterval:  50 * time.Millisecond,
				BlockCacheSize: 8 * 1024 * 1024,
			},
		}

		logger := testLogger()
		svc, err := NewService(cfg, nil, logger)
		require.NoError(t, err)
		require.NotNil(t, svc)
		defer svc.Stop(context.Background())
	})

	t.Run("invalid storage mode", func(t *testing.T) {
		cfg := config.Config{
			StorageMode: "invalid_mode",
		}

		_, err := NewService(cfg, nil, testLogger())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown storage mode")
	})

	t.Run("pebble mode with empty path fails", func(t *testing.T) {
		cfg := config.Config{
			StorageMode: "pebble",
			Store: config.StoreConfig{
				Path: "", // Empty path should fail
			},
		}

		_, err := NewService(cfg, nil, testLogger())
		require.Error(t, err)
	})
}

func TestService_PebbleMode_ApplyEvent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "indexer-pebble-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	cfg := config.Config{
		StorageMode: "pebble",
		Store: config.StoreConfig{
			Path:           tmpDir + "/test.db",
			BatchSize:      100,
			BatchInterval:  50 * time.Millisecond,
			BlockCacheSize: 8 * 1024 * 1024,
		},
	}

	svc, err := NewService(cfg, nil, testLogger())
	require.NoError(t, err)
	require.NotNil(t, svc)

	s := svc.(*service)

	// Load templates
	templateYAML := `
templates:
  - name: chats_by_timestamp
    collectionPattern: users/{uid}/chats
    fields:
      - { field: timestamp, order: desc }
`
	err = s.manager.LoadTemplatesFromBytes([]byte(templateYAML))
	require.NoError(t, err)

	err = svc.Start(context.Background())
	require.NoError(t, err)
	defer svc.Stop(context.Background())

	// Apply an event with progress
	evt := &ChangeEvent{
		EventID:  "evt1",
		Database: "testdb",
		FullDocument: &storage.StoredDoc{
			Database:   "testdb",
			Id:         "doc1",
			Collection: "users/alice/chats",
			Fullpath:   "users/alice/chats/doc1",
			Data: map[string]any{
				"id":        "doc1",
				"timestamp": float64(1000),
			},
		},
	}
	publishTestGeneration(t, svc, "testdb", evt.FullDocument.Collection)
	err = svc.ApplyEvent(context.Background(), evt, "event-progress-1")
	require.NoError(t, err)

	// Verify event was applied
	stats, err := svc.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), stats.EventsApplied)

	// Verify progress was saved via store
	progress, err := s.store.LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "event-progress-1", progress)
}

func TestService_PebbleMode_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "indexer-pebble-test-*")
	require.NoError(t, err)
	defer os.RemoveAll(tmpDir)

	dbPath := tmpDir + "/test.db"
	templateYAML := `
templates:
  - name: chats_by_timestamp
    collectionPattern: users/{uid}/chats
    fields:
      - { field: timestamp, order: desc }
`

	// First service instance - insert data
	{
		cfg := config.Config{
			StorageMode: "pebble",
			Store: config.StoreConfig{
				Path:           dbPath,
				BatchSize:      100,
				BatchInterval:  50 * time.Millisecond,
				BlockCacheSize: 8 * 1024 * 1024,
			},
		}

		svc, err := NewService(cfg, nil, testLogger())
		require.NoError(t, err)

		s := svc.(*service)
		err = s.manager.LoadTemplatesFromBytes([]byte(templateYAML))
		require.NoError(t, err)

		err = svc.Start(context.Background())
		require.NoError(t, err)

		// Apply event with progress
		evt := &ChangeEvent{
			EventID:  "evt1",
			Database: "testdb",
			FullDocument: &storage.StoredDoc{
				Database:   "testdb",
				Id:         "doc1",
				Collection: "users/alice/chats",
				Fullpath:   "users/alice/chats/doc1",
				Data: map[string]any{
					"id":        "doc1",
					"timestamp": float64(1000),
				},
			},
		}
		publishTestGeneration(t, svc, "testdb", evt.FullDocument.Collection)
		err = svc.ApplyEvent(context.Background(), evt, "persist-test-progress")
		require.NoError(t, err)

		// Close the service
		err = svc.Stop(context.Background())
		require.NoError(t, err)
	}

	// Second service instance - verify data persisted
	{
		cfg := config.Config{
			StorageMode: "pebble",
			Store: config.StoreConfig{
				Path:           dbPath,
				BatchSize:      100,
				BatchInterval:  50 * time.Millisecond,
				BlockCacheSize: 8 * 1024 * 1024,
			},
		}

		svc, err := NewService(cfg, nil, testLogger())
		require.NoError(t, err)

		s := svc.(*service)
		err = s.manager.LoadTemplatesFromBytes([]byte(templateYAML))
		require.NoError(t, err)

		err = svc.Start(context.Background())
		require.NoError(t, err)
		defer svc.Stop(context.Background())

		// Verify progress was loaded from persistent store
		s.mu.RLock()
		progress := s.progress
		s.mu.RUnlock()
		assert.Equal(t, "persist-test-progress", progress)
	}
}

func TestService_InvalidateDatabase(t *testing.T) {
	cfg := config.Config{
		StorageMode: config.StorageModeMemory,
	}

	svc, err := NewService(cfg, nil, testLogger())
	require.NoError(t, err)

	// Insert some data
	s := svc.(*service)
	store := s.manager.Store().(*mem_store.Store)

	// Add entries for two databases
	err = store.Upsert("db1", "users/*", "tmpl1", "user1", []byte{0x01}, "")
	require.NoError(t, err)
	err = store.Upsert("db2", "posts/*", "tmpl2", "post1", []byte{0x02}, "")
	require.NoError(t, err)

	// Verify both databases have indexes
	indexes, err := store.ListIndexes("db1")
	require.NoError(t, err)
	assert.Len(t, indexes, 1)

	indexes, err = store.ListIndexes("db2")
	require.NoError(t, err)
	assert.Len(t, indexes, 1)

	// Invalidate db1
	err = svc.InvalidateDatabase(context.Background(), "db1")
	require.NoError(t, err)

	// Verify db1 indexes are gone (returns error for non-existent db)
	_, err = store.ListIndexes("db1")
	assert.Error(t, err) // db1 no longer exists

	// Verify db2 indexes remain
	indexes, err = store.ListIndexes("db2")
	require.NoError(t, err)
	assert.Len(t, indexes, 1)
}

func TestService_InvalidateDatabase_NonExistent(t *testing.T) {
	cfg := config.Config{
		StorageMode: config.StorageModeMemory,
	}

	svc, err := NewService(cfg, nil, testLogger())
	require.NoError(t, err)

	// Invalidating a non-existent database should not error
	err = svc.InvalidateDatabase(context.Background(), "nonexistent")
	require.NoError(t, err)
}

// mockStoreWithDeleteDatabaseError is a mock store that returns an error on DeleteDatabase.
type mockStoreWithDeleteDatabaseError struct {
	store.Store
	deleteDatabaseErr error
}

func (m *mockStoreWithDeleteDatabaseError) DeleteDatabase(db string) error {
	if m.deleteDatabaseErr != nil {
		return m.deleteDatabaseErr
	}
	return m.Store.DeleteDatabase(db)
}

func TestService_InvalidateDatabase_StoreError(t *testing.T) {
	// Create a regular service first
	cfg := config.Config{
		StorageMode: config.StorageModeMemory,
	}

	svc, err := NewService(cfg, nil, testLogger())
	require.NoError(t, err)

	s := svc.(*service)

	// Wrap the manager's store with our error-returning mock
	originalStore := s.manager.Store()
	mockStore := &mockStoreWithDeleteDatabaseError{
		Store:             originalStore,
		deleteDatabaseErr: errors.New("mock delete database error"),
	}

	// Create a new manager with the mock store
	s.manager = manager.New(mockStore)

	// InvalidateDatabase should return error
	err = svc.InvalidateDatabase(context.Background(), "testdb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete database indexes")
	assert.Contains(t, err.Error(), "mock delete database error")
}

func TestService_Stats_Context(t *testing.T) {
	svc := newTestService(config.Config{}, nil, testLogger())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := svc.Stats(ctx)
	require.ErrorIs(t, err, context.Canceled)
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	_, err = svc.Stats(expired)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestService_Stats_DoesNotReadStore(t *testing.T) {
	svc := &service{manager: manager.New(nil)}
	got, err := svc.Stats(context.Background())
	require.NoError(t, err)
	assert.Equal(t, Stats{}, got)
}

func TestService_Stats_EventCounting(t *testing.T) {
	svc := newTestService(config.Config{}, nil, testLogger())
	defer svc.Manager().Store().Close()
	require.NoError(t, svc.ApplyEvent(context.Background(), nil, ""))
	require.Error(t, svc.ApplyEvent(context.Background(), &ChangeEvent{}, ""))
	doc := types.NewStoredDoc("db", "unmatched", "id", nil)
	require.NoError(t, svc.ApplyEvent(context.Background(), &ChangeEvent{Database: "db", FullDocument: &doc}, "p1"))
	counted, err := svc.Stats(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, counted.EventsApplied)
	assert.Positive(t, counted.LastEventTime)
}

func TestSubscriptionStopsBeforeFailedProgress(t *testing.T) {
	source := newMockPullerService()
	svc := newTestService(config.Config{}, source, testLogger())
	require.NoError(t, svc.Manager().LoadTemplatesFromBytes([]byte(`templates:
- name: by_value
  collectionPattern: docs
  fields: [{field: value, order: asc}]
`)))
	refs := publishTestGeneration(t, svc, "db", "docs")
	good := types.NewStoredDoc("db", "docs", "id", map[string]any{"value": 1})
	require.NoError(t, svc.ApplyEvent(context.Background(), &ChangeEvent{Database: "db", FullDocument: &good}, "good"))
	require.NoError(t, svc.Start(context.Background()))
	defer svc.Stop(context.Background())
	bad := types.NewStoredDoc("db", "docs", "id", map[string]any{"value": []any{2}})
	source.SendEvent(&puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &bad}, Progress: "bad"})
	source.SendEvent(&puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &good}, Progress: "later"})
	require.Eventually(t, func() bool {
		generation, _, err := svc.Manager().Store().ReadGeneration("db", "docs", refs[0].TemplateFingerprint)
		return err == nil && generation.Failure != ""
	}, time.Second, time.Millisecond)
	finished := make(chan struct{})
	go func() { svc.(*service).wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("consumer continued after failed event")
	}
	progress, err := svc.Manager().Store().LoadProgress()
	require.NoError(t, err)
	assert.Equal(t, "good", progress)
	stats, err := svc.Stats(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, stats.EventsApplied)
}
