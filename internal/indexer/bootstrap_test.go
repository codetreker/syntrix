package indexer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/puller"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/normalizer"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const bootstrapTemplates = `templates:
- name: by_score
  collectionPattern: users/{uid}/docs
  fields:
  - {field: score, order: asc}
- name: system
  collectionPattern: sys
  includeDeleted: true
  fields:
  - {field: id, order: asc}
`

type bootstrapPuller struct {
	marker  string
	invalid error
	stream  chan *puller.Event
	target  string
}

func (p *bootstrapPuller) Subscribe(context.Context, string, string) <-chan *puller.Event {
	panic("unverified subscription")
}
func (p *bootstrapPuller) BootstrapBoundary(context.Context) (string, error) {
	return p.marker, p.invalid
}
func (p *bootstrapPuller) ValidateBoundary(context.Context, string) error { return p.invalid }
func (p *bootstrapPuller) SubscribeReady(ctx context.Context, _ string, _ string, onReady func(string)) <-chan *puller.Event {
	target := p.target
	if target == "" {
		target = p.marker
	}
	out := make(chan *puller.Event, 16)
	go func() {
		defer close(out)
		for {
			select {
			case evt := <-p.stream:
				if evt == nil {
					return
				}
				out <- evt
			default:
				goto barrier
			}
		}
	barrier:
		select {
		case out <- &puller.Event{Ready: true, Progress: target}:
		case <-ctx.Done():
			return
		}
		if onReady != nil {
			onReady(target)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-p.stream:
				if !ok {
					return
				}
				select {
				case out <- evt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
func bootstrapMarker(id string) string {
	pm := cursor.NewProgressMarker()
	pm.Positions["source"] = id
	pm.Lineages = map[string]string{"source": "lineage"}
	return pm.Encode()
}

type bootstrapDocuments struct {
	docs    map[string]map[string][]*types.StoredDoc
	failure error
	mu      sync.Mutex
	scans   int
}

func (s *bootstrapDocuments) EnumerateCollections(_ context.Context, db, after string, limit int, opts ...types.CollectionEnumerationOptions) ([]string, error) {
	if len(opts) != 1 || !opts[0].IncludeSystem {
		return nil, fmt.Errorf("system scope omitted")
	}
	var collections []string
	for collection := range s.docs[db] {
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
func (s *bootstrapDocuments) ScanDocuments(_ context.Context, db string, request types.SourceScanRequest) (types.SourceScanPage, error) {
	s.mu.Lock()
	s.scans++
	s.mu.Unlock()
	if s.failure != nil {
		return types.SourceScanPage{}, s.failure
	}
	if request.Consistency != types.ReadAuthoritative {
		return types.SourceScanPage{}, fmt.Errorf("not authoritative")
	}
	result := types.SourceScanPage{Exhausted: true}
	for _, doc := range s.docs[db][request.Collection] {
		id, err := types.LogicalDocumentID(doc)
		if err != nil {
			return result, err
		}
		if id <= request.AfterID {
			continue
		}
		if len(result.Documents) == request.Limit {
			result.Exhausted = false
			break
		}
		result.Documents = append(result.Documents, doc)
		result.NextAfter = id
	}
	return result, nil
}

func newBootstrapTestService(t *testing.T, mode config.StorageMode, p *bootstrapPuller) *service {
	t.Helper()
	cfg := config.Config{StorageMode: mode, BootstrapBatchSize: 1, Databases: []string{"db", "empty"}, Store: config.StoreConfig{Path: t.TempDir(), BatchInterval: time.Hour}}
	svc, err := NewService(cfg, p, testLogger())
	require.NoError(t, err)
	s := svc.(*service)
	require.NoError(t, s.manager.LoadTemplatesFromBytes([]byte(bootstrapTemplates)))
	return s
}
func bootstrapTestDocuments() *bootstrapDocuments {
	a := types.NewStoredDoc("db", "users/a/docs", "one", map[string]any{"score": int64(1)})
	b := types.NewStoredDoc("db", "users/b/docs", "one", map[string]any{"score": int64(2)})
	tombstone := types.NewStoredDoc("db", "sys", "gone", nil)
	tombstone.Deleted = true
	return &bootstrapDocuments{docs: map[string]map[string][]*types.StoredDoc{"db": {"users/a/docs": {&a}, "users/b/docs": {&b}, "sys": {&tombstone}}}}
}

func TestMaintenanceBootstrapPublishesCompleteMemoryAndPersistentCatalogs(t *testing.T) {
	for _, mode := range []config.StorageMode{config.StorageModeMemory, config.StorageModePebble} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := &bootstrapPuller{marker: bootstrapMarker(""), stream: make(chan *puller.Event)}
			s := newBootstrapTestService(t, mode, p)
			defer s.Stop(context.Background())
			source := bootstrapTestDocuments()
			request := BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true, ExpectedBoundary: p.marker}
			_, err := s.OpenCandidates(ctx, "db", Plan{Collection: "users/a/docs"})
			require.ErrorIs(t, err, ErrIndexNotReady)
			require.NoError(t, s.Bootstrap(ctx, request))
			catalogs, err := s.store.ListBootstrapCatalogs()
			require.NoError(t, err)
			require.Len(t, catalogs, 2)
			require.Equal(t, catalogs[0].Generation, catalogs[1].Generation)
			for _, collection := range []string{"users/a/docs", "users/b/docs", "sys"} {
				matches := s.manager.MatchTemplatesForCollection(collection)
				require.Len(t, matches, 1)
				generation, exists, err := s.store.ReadGeneration("db", collection, matches[0].Template.Fingerprint())
				require.NoError(t, err)
				require.True(t, exists)
				require.True(t, generation.Ready)
				rows := readProjectionRows(t, s.store, store.QueryIndexRef{Database: "db", Collection: collection, TemplateFingerprint: matches[0].Template.Fingerprint(), Generation: generation.ID})
				require.Len(t, rows, 1)
			}
			fingerprint := s.manager.Templates()[0].Fingerprint()
			empty, exists, err := s.store.ReadGeneration("empty", "users/new/docs", fingerprint)
			require.NoError(t, err)
			require.True(t, exists)
			require.True(t, empty.Ready)
			require.NoError(t, s.Start(ctx))
			require.NoError(t, s.WaitReady(ctx))
			health, err := s.Health(ctx)
			require.NoError(t, err)
			require.Equal(t, HealthOK, health.Status)
			require.ErrorContains(t, s.Bootstrap(ctx, request), "stopped")
		})
	}
}

func TestMaintenanceBootstrapFailureNeverPublishesReadyCatalog(t *testing.T) {
	for _, phase := range []string{"attestation", "source", "inventory", "wrong-puller"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			p := &bootstrapPuller{marker: bootstrapMarker(""), stream: make(chan *puller.Event)}
			s := newBootstrapTestService(t, config.StorageModeMemory, p)
			defer s.Stop(context.Background())
			source := bootstrapTestDocuments()
			req := BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}
			switch phase {
			case "attestation":
				req.WritesQuiesced = false
			case "source":
				source.failure = errors.New("scan failure")
			case "inventory":
				req.ValidateInventory = func(context.Context) error { return errors.New("inventory changed") }
			case "wrong-puller":
				req.ExpectedBoundary = "different"
			}
			require.Error(t, s.Bootstrap(ctx, req))
			catalogs, err := s.store.ListBootstrapCatalogs()
			require.NoError(t, err)
			require.Empty(t, catalogs)
			require.Error(t, s.Start(ctx))
		})
	}
}

type blockedProjectionStore struct {
	store.Store
	entered chan struct{}
	release chan struct{}
}

func (s *blockedProjectionStore) ApplyDocumentProjection(projections []store.Projection, progress string) error {
	close(s.entered)
	<-s.release
	return s.Store.ApplyDocumentProjection(projections, progress)
}
func TestVerifiedReplayReadyWaitsForAppliedProjectionAndFailsClosed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &bootstrapPuller{marker: bootstrapMarker(""), target: bootstrapMarker("1-1-a"), stream: make(chan *puller.Event, 1)}
	s := newBootstrapTestService(t, config.StorageModeMemory, p)
	defer s.Stop(context.Background())
	source := &bootstrapDocuments{docs: map[string]map[string][]*types.StoredDoc{}}
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	blocked := &blockedProjectionStore{Store: s.store, entered: make(chan struct{}), release: make(chan struct{})}
	s.store = blocked
	// Keep the manager's loaded template definitions while wrapping the service store.
	doc := types.NewStoredDoc("db", "users/a/docs", "one", map[string]any{"score": int64(1)})
	p.stream <- &puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &doc}, Progress: p.target}
	require.NoError(t, s.Start(ctx))
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err := s.OpenCandidates(ctx, "db", Plan{Collection: doc.Collection})
	require.ErrorIs(t, err, ErrIndexNotReady)
	close(blocked.release)
	require.NoError(t, s.WaitReady(ctx))
	close(p.stream)
	require.Eventually(t, func() bool { health, _ := s.Health(ctx); return health.Status == HealthUnhealthy }, time.Second, time.Millisecond)
	_, err = s.OpenCandidates(ctx, "db", Plan{Collection: doc.Collection})
	require.ErrorIs(t, err, ErrIndexNotReady)
}

type lifecycleErrorStore struct {
	store.Store
	loadErr, closeErr error
}

func (s *lifecycleErrorStore) LoadProgress() (string, error) { return "", s.loadErr }
func (s *lifecycleErrorStore) Close() error                  { return errors.Join(s.Store.Close(), s.closeErr) }
func TestIndexerLifecyclePropagatesStorageErrors(t *testing.T) {
	ctx := context.Background()
	svc, err := NewService(config.Config{}, nil, testLogger())
	require.NoError(t, err)
	s := svc.(*service)
	loadFailure := errors.New("read failure")
	closeFailure := errors.New("close failure")
	s.store = &lifecycleErrorStore{Store: s.store, loadErr: loadFailure, closeErr: closeFailure}
	s.manager = manager.New(s.store)
	require.ErrorIs(t, s.Start(ctx), loadFailure)
	require.ErrorIs(t, s.Stop(ctx), closeFailure)
}

type cancelingBootstrapSource struct {
	*bootstrapDocuments
	entered chan struct{}
}

func (s *cancelingBootstrapSource) ScanDocuments(ctx context.Context, _ string, _ types.SourceScanRequest) (types.SourceScanPage, error) {
	close(s.entered)
	<-ctx.Done()
	return types.SourceScanPage{}, ctx.Err()
}
func TestStopCancelsBootstrapBeforeClosingStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &bootstrapPuller{marker: bootstrapMarker(""), stream: make(chan *puller.Event)}
	s := newBootstrapTestService(t, config.StorageModeMemory, p)
	source := &cancelingBootstrapSource{bootstrapDocuments: bootstrapTestDocuments(), entered: make(chan struct{})}
	completed := make(chan error, 1)
	go func() {
		completed <- s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true})
	}()
	select {
	case <-source.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, s.Stop(ctx))
	require.ErrorIs(t, <-completed, context.Canceled)
	require.Error(t, s.Start(ctx))
}

func TestCanceledSubscriptionUnblocksIndependentReadinessWaiter(t *testing.T) {
	lifeCtx, cancelLife := context.WithCancel(context.Background())
	p := &bootstrapPuller{marker: bootstrapMarker(""), stream: make(chan *puller.Event)}
	s := newBootstrapTestService(t, config.StorageModeMemory, p)
	defer s.Stop(context.Background())
	source := &bootstrapDocuments{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	require.NoError(t, s.Start(lifeCtx))
	require.NoError(t, s.WaitReady(ctx))
	cancelLife()
	require.Eventually(t, func() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.lifecycleErr != nil }, time.Second, time.Millisecond)
	require.ErrorIs(t, s.WaitReady(ctx), context.Canceled)
}

type recoverySubscription struct {
	after  string
	events chan *puller.Event
}
type recoveryBootstrapPuller struct {
	bootstrapPuller
	connections chan recoverySubscription
}

func (p *recoveryBootstrapPuller) SubscribeReady(ctx context.Context, _ string, after string, _ func(string)) <-chan *puller.Event {
	connection := recoverySubscription{after: after, events: make(chan *puller.Event, 8)}
	select {
	case p.connections <- connection:
	case <-ctx.Done():
	}
	return connection.events
}
func TestVerifiedSubscriptionRecoversOnlyTransientFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &recoveryBootstrapPuller{bootstrapPuller: bootstrapPuller{marker: "opaque-bootstrap"}, connections: make(chan recoverySubscription, 4)}
	base := newBootstrapTestService(t, config.StorageModeMemory, &p.bootstrapPuller)
	base.pullerSvc = p
	defer base.Stop(context.Background())
	source := &bootstrapDocuments{}
	require.NoError(t, base.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	require.NoError(t, base.Start(ctx))
	first := <-p.connections
	first.events <- &puller.Event{Ready: true, Progress: first.after}
	require.NoError(t, base.WaitReady(ctx))
	doc := types.NewStoredDoc("db", "users/a/docs", "one", map[string]any{"score": int64(1)})
	first.events <- &puller.Event{Change: &ChangeEvent{Database: "db", FullDocument: &doc}, Progress: "opaque-applied"}
	first.events <- &puller.Event{Error: status.Error(codes.Unavailable, "network failure"), Retryable: true}
	require.Eventually(t, func() bool { health, _ := base.Health(ctx); return health.Status == HealthUnhealthy }, time.Second, time.Millisecond)
	var second recoverySubscription
	select {
	case second = <-p.connections:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Equal(t, "opaque-applied", second.after)
	persisted, err := base.store.LoadProgress()
	require.NoError(t, err)
	require.Equal(t, "opaque-applied", persisted)
	_, err = base.OpenCandidates(ctx, "db", Plan{Collection: doc.Collection})
	require.ErrorIs(t, err, ErrIndexNotReady)
	second.events <- &puller.Event{Ready: true, Progress: second.after}
	require.NoError(t, base.WaitReady(ctx))
	permanent := errors.New("invalid event wire version")
	second.events <- &puller.Event{Error: permanent}
	require.Eventually(t, func() bool { base.mu.RLock(); defer base.mu.RUnlock(); return errors.Is(base.lifecycleErr, permanent) }, time.Second, time.Millisecond)
	require.ErrorIs(t, base.WaitReady(ctx), permanent)
	require.Empty(t, p.connections)
}

type blockingValidationPuller struct {
	bootstrapPuller
	entered chan struct{}
}

func (p *blockingValidationPuller) ValidateBoundary(ctx context.Context, _ string) error {
	close(p.entered)
	<-ctx.Done()
	return ctx.Err()
}
func TestStopCancelsStartupValidation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &blockingValidationPuller{bootstrapPuller: bootstrapPuller{marker: "opaque-bootstrap"}, entered: make(chan struct{})}
	s := newBootstrapTestService(t, config.StorageModeMemory, &p.bootstrapPuller)
	s.pullerSvc = p
	catalogs := []store.BootstrapCatalog{}
	for _, db := range []string{"db", "empty"} {
		catalogs = append(catalogs, store.BootstrapCatalog{Database: db, Generation: "build", BootstrapProgress: p.marker, TemplateFingerprints: s.templateFingerprints(db)})
	}
	require.NoError(t, s.store.PublishBootstrapCatalogs(catalogs, p.marker))
	completed := make(chan error, 1)
	go func() { completed <- s.StartWithValidationContext(ctx, ctx) }()
	select {
	case <-p.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.NoError(t, s.Stop(ctx))
	require.ErrorIs(t, <-completed, context.Canceled)
}

func TestBootstrapAndLiveProjectionUseDatabaseScopedTemplates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	dir := t.TempDir()
	for db, field := range map[string]string{"first": "score", "second": "name"} {
		content := fmt.Sprintf("database: %s\ntemplates:\n- name: scoped\n  collectionPattern: docs\n  fields:\n  - {field: %s, order: asc}\n", db, field)
		require.NoError(t, os.WriteFile(filepath.Join(dir, db+".yaml"), []byte(content), 0600))
	}
	p := &bootstrapPuller{marker: "opaque-bootstrap", stream: make(chan *puller.Event)}
	svc, err := NewService(config.Config{TemplatePath: dir}, p, testLogger())
	require.NoError(t, err)
	s := svc.(*service)
	defer s.Stop(context.Background())
	source := &bootstrapDocuments{}
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"first", "second"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	for db, field := range map[string]string{"first": "score", "second": "name"} {
		doc := types.NewStoredDoc(db, "docs", "one", map[string]any{field: "value"})
		require.NoError(t, s.ApplyEvent(ctx, &ChangeEvent{Database: db, FullDocument: &doc}, "opaque-"+db))
		fingerprint := s.templateFingerprints(db)[0]
		generation, exists, err := s.store.ReadGeneration(db, "docs", fingerprint)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, readProjectionRows(t, s.store, store.QueryIndexRef{Database: db, Collection: "docs", TemplateFingerprint: fingerprint, Generation: generation.ID}), 1)
	}
}

func TestPhysicalCleanupAdvancesProgressWithoutStoppingReadyQueries(t *testing.T) {
	for _, mode := range []config.StorageMode{config.StorageModeMemory, config.StorageModePebble} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := &bootstrapPuller{marker: "bootstrap-boundary", stream: make(chan *puller.Event, 8)}
			s := newBootstrapTestService(t, mode, p)
			defer s.Stop(context.Background())
			source := bootstrapTestDocuments()
			require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
			require.NoError(t, s.Start(ctx))
			require.NoError(t, s.WaitReady(ctx))
			physicalID := source.docs["db"]["sys"][0].Id
			raw := normalizer.RawEvent{OperationType: "delete", DocumentKey: bson.M{"_id": physicalID}, ClusterTime: primitive.Timestamp{T: 2, I: 1}}
			raw.Namespace.Coll = "sys"
			deletion, err := normalizer.New().Normalize(&raw)
			require.NoError(t, err)
			require.Nil(t, deletion.FullDocument)
			require.Empty(t, deletion.Database)
			require.Equal(t, physicalID, deletion.MgoDocID)
			p.stream <- &puller.Event{Change: deletion, Progress: "after-physical-cleanup"}
			require.Eventually(t, func() bool { return s.eventsApplied.Load() == 1 }, time.Second, time.Millisecond)
			require.NoError(t, s.store.Flush())
			progress, err := s.store.LoadProgress()
			require.NoError(t, err)
			require.Equal(t, "after-physical-cleanup", progress)
			candidates, err := s.Search(ctx, "db", Plan{Collection: "users/a/docs", OrderBy: []OrderField{{Field: "score", Direction: Asc}}})
			require.NoError(t, err)
			require.Len(t, candidates, 1)
			require.Equal(t, "one", candidates[0].ID)
			next := types.NewStoredDoc("db", "users/a/docs", "two", map[string]any{"score": int64(2)})
			p.stream <- &puller.Event{Change: &ChangeEvent{Database: "db", OpType: puller.OperationInsert, FullDocument: &next}, Progress: "after-next-insert"}
			require.Eventually(t, func() bool { return s.eventsApplied.Load() == 2 }, time.Second, time.Millisecond)
			candidates, err = s.Search(ctx, "db", Plan{Collection: next.Collection, OrderBy: []OrderField{{Field: "score", Direction: Asc}}})
			require.NoError(t, err)
			require.Len(t, candidates, 2)
			health, err := s.Health(ctx)
			require.NoError(t, err)
			require.Equal(t, HealthOK, health.Status)
			for _, op := range []puller.OperationType{puller.OperationInsert, puller.OperationUpdate, puller.OperationReplace} {
				require.Error(t, s.ApplyEvent(ctx, &ChangeEvent{OpType: op}, "must-not-advance"))
			}
			require.NoError(t, s.store.Flush())
			progress, err = s.store.LoadProgress()
			require.NoError(t, err)
			require.Equal(t, "after-next-insert", progress)
		})
	}
}

func TestRetiredDatabaseEventsCannotResurrectIndexesOrStopOtherDatabases(t *testing.T) {
	for _, mode := range []config.StorageMode{config.StorageModeMemory, config.StorageModePebble} {
		t.Run(string(mode), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := &bootstrapPuller{marker: "bootstrap-boundary", stream: make(chan *puller.Event, 8)}
			s := newBootstrapTestService(t, mode, p)
			source := bootstrapTestDocuments()
			require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
			require.NoError(t, s.Start(ctx))
			require.NoError(t, s.WaitReady(ctx))
			require.NoError(t, s.InvalidateDatabase(ctx, "db"))
			retired, err := s.store.IsDatabaseRetired("db")
			require.NoError(t, err)
			require.True(t, retired)
			old := types.NewStoredDoc("db", "users/a/docs", "late", map[string]any{"score": int64(1)})
			survivor := types.NewStoredDoc("empty", "users/a/docs", "current", map[string]any{"score": int64(2)})
			p.stream <- &puller.Event{Change: &ChangeEvent{Database: "db", OpType: puller.OperationInsert, FullDocument: &old}, Progress: "retired-event"}
			p.stream <- &puller.Event{Change: &ChangeEvent{Database: "empty", OpType: puller.OperationInsert, FullDocument: &survivor}, Progress: "surviving-event"}
			require.Eventually(t, func() bool { return s.eventsApplied.Load() == 2 }, time.Second, time.Millisecond)
			rows, err := s.Search(ctx, "empty", Plan{Collection: survivor.Collection, OrderBy: []OrderField{{Field: "score", Direction: Asc}}})
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, "current", rows[0].ID)
			refs, err := s.store.ListQueryIndexes()
			require.NoError(t, err)
			for _, ref := range refs {
				require.NotEqual(t, "db", ref.Database)
			}
			require.NoError(t, s.validateCatalogs())
			cfg := s.cfg
			require.NoError(t, s.Stop(ctx))
			if mode == config.StorageModePebble {
				reopened, err := NewService(cfg, p, testLogger())
				require.NoError(t, err)
				restarted := reopened.(*service)
				defer restarted.Stop(context.Background())
				require.NoError(t, restarted.manager.LoadTemplatesFromBytes([]byte(bootstrapTemplates)))
				require.NoError(t, restarted.validateCatalogs())
				require.NoError(t, restarted.ApplyEvent(ctx, &ChangeEvent{Database: "db", OpType: puller.OperationReplace, FullDocument: &old}, "retired-after-restart"))
				refs, err := restarted.store.ListQueryIndexes()
				require.NoError(t, err)
				for _, ref := range refs {
					require.NotEqual(t, "db", ref.Database)
				}
				retired, err = restarted.store.IsDatabaseRetired("db")
				require.NoError(t, err)
				require.True(t, retired)
			}
		})
	}
}

func TestDatabaseRetirementSerializesWithAnAdmittedProjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := &bootstrapPuller{marker: "opaque-bootstrap", stream: make(chan *puller.Event)}
	s := newBootstrapTestService(t, config.StorageModeMemory, p)
	defer s.Stop(context.Background())
	source := &bootstrapDocuments{}
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	blocked := &blockedProjectionStore{Store: s.store, entered: make(chan struct{}), release: make(chan struct{})}
	s.store = blocked
	doc := types.NewStoredDoc("db", "users/a/docs", "inflight", map[string]any{"score": int64(1)})
	applied := make(chan error, 1)
	go func() {
		applied <- s.ApplyEvent(ctx, &ChangeEvent{Database: "db", FullDocument: &doc}, "inflight-progress")
	}()
	select {
	case <-blocked.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	retired := make(chan error, 1)
	go func() { retired <- s.InvalidateDatabase(ctx, "db") }()
	close(blocked.release)
	require.NoError(t, <-applied)
	require.NoError(t, <-retired)
	refs, err := s.store.ListQueryIndexes()
	require.NoError(t, err)
	require.Empty(t, refs)
	isRetired, err := s.store.IsDatabaseRetired("db")
	require.NoError(t, err)
	require.True(t, isRetired)
}
