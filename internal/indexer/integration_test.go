package indexer_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/puller"
)

type mockPuller struct {
	events      chan *puller.Event
	mu          sync.Mutex
	documents   map[string]map[string]map[string]*types.StoredDoc
	progress    string
	validated   string
	pushed      int64
	appliedBase int64
	cfg         config.Config
	closeOnce   sync.Once
}

func newMockPuller(bufferSize int) *mockPuller {
	return &mockPuller{events: make(chan *puller.Event, bufferSize), documents: make(map[string]map[string]map[string]*types.StoredDoc), progress: "fixture-bootstrap"}
}
func (m *mockPuller) Subscribe(context.Context, string, string) <-chan *puller.Event {
	panic("integration fixture requires verified replay")
}
func (m *mockPuller) BootstrapBoundary(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.progress, nil
}
func (m *mockPuller) ValidateBoundary(_ context.Context, after string) error {
	if after == "" {
		return fmt.Errorf("empty fixture replay boundary")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.validated = after
	return nil
}
func (m *mockPuller) SubscribeReady(ctx context.Context, _ string, after string, onReady func(string)) <-chan *puller.Event {
	m.mu.Lock()
	validated := m.validated
	m.mu.Unlock()
	if after != validated {
		panic("subscription boundary was not validated")
	}
	output := make(chan *puller.Event)
	go func() {
		defer close(output)
		select {
		case output <- &puller.Event{Ready: true, Progress: after}:
			if onReady != nil {
				onReady(after)
			}
		case <-ctx.Done():
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-m.events:
				if !ok {
					return
				}
				select {
				case output <- evt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return output
}
func (m *mockPuller) pushEvent(evt *puller.ChangeEvent, progress string) {
	m.mu.Lock()
	if doc := evt.FullDocument; doc != nil {
		id, err := types.LogicalDocumentID(doc)
		if err != nil {
			panic(err)
		}
		if m.documents[doc.Database] == nil {
			m.documents[doc.Database] = make(map[string]map[string]*types.StoredDoc)
		}
		if m.documents[doc.Database][doc.Collection] == nil {
			m.documents[doc.Database][doc.Collection] = make(map[string]*types.StoredDoc)
		}
		m.documents[doc.Database][doc.Collection][id] = doc
	}
	m.progress = progress
	m.pushed++
	m.mu.Unlock()
	m.events <- &puller.Event{Change: evt, Progress: progress}
}
func (m *mockPuller) close() { m.closeOnce.Do(func() { close(m.events) }) }
func (m *mockPuller) EnumerateCollections(_ context.Context, database, after string, limit int, options ...types.CollectionEnumerationOptions) ([]string, error) {
	if len(options) != 1 || !options[0].IncludeSystem {
		return nil, fmt.Errorf("bootstrap omitted system collections")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var collections []string
	for collection := range m.documents[database] {
		if collection > after {
			collections = append(collections, collection)
		}
	}
	slices.Sort(collections)
	if len(collections) > limit {
		collections = collections[:limit]
	}
	return collections, nil
}
func (m *mockPuller) ScanDocuments(_ context.Context, database string, request types.SourceScanRequest) (types.SourceScanPage, error) {
	if request.Consistency != types.ReadAuthoritative {
		return types.SourceScanPage{}, fmt.Errorf("bootstrap used non-authoritative source")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var ids []string
	for id := range m.documents[database][request.Collection] {
		if id > request.AfterID {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	page := types.SourceScanPage{Exhausted: len(ids) <= request.Limit}
	if len(ids) > request.Limit {
		ids = ids[:request.Limit]
	}
	for _, id := range ids {
		page.Documents = append(page.Documents, m.documents[database][request.Collection][id])
		page.NextAfter = id
	}
	return page, nil
}
func testStoredDoc(database, collection, id string, data map[string]any) *storage.StoredDoc {
	createdAt, hasCreatedAt := data["createdAt"]
	doc := storage.NewStoredDoc(database, collection, id, data)
	if hasCreatedAt {
		doc.CreatedAt = createdAt.(int64)
	}
	return &doc
}
func testDeletedDoc(database, collection, id string, data map[string]any) *storage.StoredDoc {
	doc := testStoredDoc(database, collection, id, data)
	doc.Deleted = true
	return doc
}
func createTestEvent(database, collection, docID string, data map[string]any) *puller.ChangeEvent {
	return &puller.ChangeEvent{EventID: "evt-" + docID, Database: database, OpType: puller.OperationInsert,
		FullDocument: testStoredDoc(database, collection, docID, data),
		ClusterTime:  puller.ClusterTime{T: uint32(time.Now().Unix()), I: 1}, Timestamp: time.Now().UnixMilli()}
}

const templateYAML = `
database: default
templates:
  - name: users_by_timestamp
    collectionPattern: "users"
    fields:
      - field: timestamp
        order: desc

  - name: chats_by_priority
    collectionPattern: "users/{userId}/chats"
    fields:
      - field: priority
        order: desc
      - field: timestamp
        order: desc

  - name: orders_by_amount
    collectionPattern: "orders"
    fields:
      - field: amount
        order: desc

  - name: products_by_price
    collectionPattern: "products"
    fields:
      - field: price
        order: asc
`

func setupIndexerService(t *testing.T, p *mockPuller) (indexer.LocalService, context.Context, context.CancelFunc) {
	t.Helper()
	return setupIntegrationService(t, p, config.Config{}, templateYAML)
}
func setupIndexerServiceWithPebble(t *testing.T, p *mockPuller, dataDir string) (indexer.LocalService, context.Context, context.CancelFunc) {
	t.Helper()
	return setupIntegrationService(t, p, config.Config{StorageMode: config.StorageModePebble, Store: config.StoreConfig{Path: dataDir, BatchSize: 10, BatchInterval: 10 * time.Millisecond, QueueSize: 10000}}, templateYAML)
}
func setupIntegrationService(t *testing.T, p *mockPuller, cfg config.Config, templates string) (indexer.LocalService, context.Context, context.CancelFunc) {
	t.Helper()
	cfg.ConsumerID = "test-indexer"
	cfg.Databases = []string{"default", "db0", "db1", "db2", "db3", "db4"}
	svc, err := indexer.NewService(cfg, p, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	require.NoError(t, err)
	require.NoError(t, svc.Manager().LoadTemplatesFromBytes([]byte(templates)))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		require.NoError(t, svc.Stop(stopCtx))
	})
	bootstrap := svc.(indexer.BootstrapService)
	catalogs, err := svc.Manager().Store().ListBootstrapCatalogs()
	require.NoError(t, err)
	if len(catalogs) == 0 {
		require.NoError(t, bootstrap.Bootstrap(ctx, indexer.BootstrapRequest{Databases: cfg.Databases, Scanner: p, Enumerator: p, WritesQuiesced: true}))
	}
	p.mu.Lock()
	p.cfg = cfg
	p.appliedBase = p.pushed
	p.mu.Unlock()
	require.NoError(t, svc.Start(ctx))
	readyCtx, readyCancel := context.WithTimeout(ctx, 5*time.Second)
	defer readyCancel()
	require.NoError(t, bootstrap.WaitReady(readyCtx))
	return svc, ctx, cancel
}
func waitIntegrationEvents(t *testing.T, svc indexer.LocalService, p *mockPuller) {
	t.Helper()
	p.mu.Lock()
	expected := p.pushed - p.appliedBase
	p.mu.Unlock()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		stats, err := svc.Stats(context.Background())
		if assert.NoError(c, err) {
			assert.Equal(c, expected, stats.EventsApplied)
		}
	}, 5*time.Second, time.Millisecond)
}
func reloadIntegrationTemplates(t *testing.T, svc indexer.LocalService, p *mockPuller, templates string) (indexer.LocalService, context.Context, context.CancelFunc) {
	t.Helper()
	waitIntegrationEvents(t, svc, p)
	p.mu.Lock()
	cfg := p.cfg
	p.mu.Unlock()
	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, svc.Stop(stopCtx))
	if cfg.StorageMode == config.StorageModePebble {
		require.NoError(t, os.RemoveAll(cfg.Store.Path))
	}
	return setupIntegrationService(t, p, cfg, templates)
}
func stopService(t *testing.T, svc indexer.LocalService, p *mockPuller) {
	t.Helper()
	p.close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, svc.Stop(ctx))
}
