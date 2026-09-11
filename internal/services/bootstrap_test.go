package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/core/database"
	storageconfig "github.com/syntrixbase/syntrix/internal/core/storage/config"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	indexerconfig "github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/puller"
	pullerconfig "github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/server"
	"gopkg.in/yaml.v3"
)

func bootstrapTestConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := &config.Config{Storage: storageconfig.DefaultConfig(), Indexer: indexerconfig.DefaultConfig(), Puller: pullerconfig.DefaultConfig()}
	cfg.Indexer.TemplatePath = t.TempDir()
	cfg.Indexer.Store.Path = filepath.Join(t.TempDir(), "indexes")
	cfg.Puller.Buffer.Path = filepath.Join(t.TempDir(), "events")
	return cfg
}

func TestBootstrapCaptureDefaultConfiguration(t *testing.T) {
	t.Run("raw defaults", func(t *testing.T) {
		cfg := &config.Config{Storage: storageconfig.DefaultConfig(), Puller: pullerconfig.DefaultConfig()}
		require.NoError(t, validateBootstrapCapture(cfg, []string{"default", "dynamic"}))
	})
	t.Run("shipped configuration", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("..", "..", "configs", "config.yml"))
		require.NoError(t, err)
		var shipped config.Config
		require.NoError(t, yaml.Unmarshal(data, &shipped))
		configDir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yml"), data, 0600))
		cfg := config.LoadConfigFrom(configDir)
		require.Equal(t, shipped.Puller.Backends, cfg.Puller.Backends)
		require.NoError(t, validateBootstrapCapture(cfg, []string{"default", "dynamic"}))
	})
}

func TestBootstrapCaptureAuthoritativeRouting(t *testing.T) {
	cfg := bootstrapTestConfig(t)
	cfg.Storage.Topology.Document.Strategy = "read_write_split"
	cfg.Storage.Topology.Document.Replica = "uncaptured-replica"
	require.NoError(t, validateBootstrapCapture(cfg, []string{"default", "dynamic"}))
	cfg.Storage.Backends["other"] = cfg.Storage.Backends["default_mongo"]
	cfg.Storage.Databases["tenant"] = storageconfig.DatabaseConfig{Backend: "other"}
	require.ErrorContains(t, validateBootstrapCapture(cfg, []string{"tenant"}), "not captured")
	cfg.Puller.Backends = append(cfg.Puller.Backends, pullerconfig.PullerBackendConfig{Name: "other", Collections: []string{"documents"}})
	require.ErrorContains(t, validateBootstrapCapture(cfg, []string{"tenant"}), `"sys"`)
	cfg.Puller.Backends[1].Collections = append(cfg.Puller.Backends[1].Collections, "sys")
	require.NoError(t, validateBootstrapCapture(cfg, []string{"tenant"}))
}

type bootstrapMetadata struct {
	database.DatabaseStore
	rows  []*database.Database
	err   error
	calls int
}

func (s *bootstrapMetadata) List(_ context.Context, opts database.ListOptions) ([]*database.Database, int, error) {
	s.calls++
	if opts.OwnerID != "" || opts.Status != "" {
		return nil, 0, errors.New("bootstrap must enumerate every owner and status")
	}
	if s.err != nil {
		return nil, 0, s.err
	}
	end := min(opts.Offset+opts.Limit, len(s.rows))
	return s.rows[opts.Offset:end], len(s.rows), nil
}

func TestBootstrapInventoryIncludesAllPagesAndConfiguration(t *testing.T) {
	cfg := bootstrapTestConfig(t)
	cfg.Indexer.Databases = []string{"index-only"}
	cfg.Storage.Databases["route-only"] = storageconfig.DatabaseConfig{Backend: "default_mongo"}
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Indexer.TemplatePath, "empty.yaml"), []byte("database: template-only\ntemplates: []\n"), 0600))
	metadata := &bootstrapMetadata{}
	for i := 0; i < 201; i++ {
		metadata.rows = append(metadata.rows, &database.Database{ID: fmt.Sprintf("db-%03d", i), Status: database.DatabaseStatus("deleting")})
	}
	ids, err := bootstrapDatabaseInventory(context.Background(), cfg, metadata)
	require.NoError(t, err)
	require.Len(t, ids, 205)
	require.Equal(t, 3, metadata.calls)
	for _, id := range []string{"default", "db-200", "index-only", "route-only", "template-only"} {
		require.Contains(t, ids, id)
	}
	_, err = bootstrapDatabaseInventory(context.Background(), cfg, nil)
	require.ErrorContains(t, err, "metadata storage")
	metadata.err = errors.New("metadata unavailable")
	_, err = bootstrapDatabaseInventory(context.Background(), cfg, metadata)
	require.ErrorIs(t, err, metadata.err)
}

func TestArchiveDerivedStoresValidatesAllPathsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, "indexes")
	require.NoError(t, os.Mkdir(indexPath, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(indexPath, "data"), []byte("preserved"), 0600))
	for _, unsafe := range []string{"", "/", ".", "..", indexPath, filepath.Join(indexPath, "child")} {
		require.Error(t, archiveDerivedStores([]string{indexPath, unsafe}))
		require.FileExists(t, filepath.Join(indexPath, "data"))
	}
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(indexPath, link))
	require.ErrorContains(t, archiveDerivedStores([]string{indexPath, filepath.Join(link, "child")}), "symlink")
	require.NoError(t, archiveDerivedStores([]string{indexPath, filepath.Join(root, "missing")}))
	require.NoDirExists(t, indexPath)
	archives, err := filepath.Glob(indexPath + ".pre-bootstrap-*")
	require.NoError(t, err)
	require.Len(t, archives, 1)
	data, err := os.ReadFile(filepath.Join(archives[0], "data"))
	require.NoError(t, err)
	require.Equal(t, "preserved", string(data))
}

type archiveRecordHandler struct {
	slog.Handler
	record func(slog.Record)
}

func (h archiveRecordHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h archiveRecordHandler) Handle(_ context.Context, record slog.Record) error {
	h.record(record)
	return nil
}

func TestArchiveDerivedStoresRestoresEarlierArchivesOnFailure(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("rollback-conflict=%t", conflict), func(t *testing.T) {
			root := t.TempDir()
			first := filepath.Join(root, "indexes")
			// The source basename is valid, but its timestamped archive exceeds NAME_MAX.
			second := filepath.Join(root, strings.Repeat("b", 240))
			for _, dir := range []string{first, second} {
				require.NoError(t, os.Mkdir(dir, 0700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "data"), []byte("original"), 0600))
			}
			var archive string
			var conflictErr error
			previous := slog.Default()
			previousWriter, previousFlags := log.Writer(), log.Flags()
			defer func() {
				slog.SetDefault(previous)
				log.SetOutput(previousWriter)
				log.SetFlags(previousFlags)
			}()
			slog.SetDefault(slog.New(archiveRecordHandler{Handler: previous.Handler(), record: func(record slog.Record) {
				if record.Message != "Archived derived store before bootstrap" {
					return
				}
				var path, backup string
				record.Attrs(func(attr slog.Attr) bool {
					switch attr.Key {
					case "path":
						path = attr.Value.String()
					case "archive":
						backup = attr.Value.String()
					}
					return true
				})
				if path != first {
					return
				}
				archive = backup
				if conflict {
					// Simulate another process claiming the original path before rollback.
					conflictErr = os.Mkdir(first, 0700)
					if conflictErr == nil {
						conflictErr = os.WriteFile(filepath.Join(first, "competitor"), []byte("keep"), 0600)
					}
				}
			}}))
			err := archiveDerivedStores([]string{first, second})
			require.ErrorContains(t, err, "archive derived store")
			require.ErrorContains(t, err, second)
			require.NoError(t, conflictErr)
			require.NotEmpty(t, archive)
			data, readErr := os.ReadFile(filepath.Join(second, "data"))
			require.NoError(t, readErr)
			require.Equal(t, "original", string(data))
			if conflict {
				require.ErrorContains(t, err, "restore")
				require.ErrorContains(t, err, archive)
				data, readErr = os.ReadFile(filepath.Join(archive, "data"))
				require.NoError(t, readErr)
				require.Equal(t, "original", string(data))
				data, readErr = os.ReadFile(filepath.Join(first, "competitor"))
				require.NoError(t, readErr)
				require.Equal(t, "keep", string(data))
			} else {
				require.NoDirExists(t, archive)
				data, readErr = os.ReadFile(filepath.Join(first, "data"))
				require.NoError(t, readErr)
				require.Equal(t, "original", string(data))
			}
		})
	}
}

func TestPrepareIndexBootstrapRequiresExplicitFence(t *testing.T) {
	cfg := bootstrapTestConfig(t)
	for _, opts := range []Options{
		{ResetDerived: true}, {WritesQuiesced: true},
		{BootstrapIndexes: true, BootstrapTimeout: time.Minute},
		{BootstrapIndexes: true, WritesQuiesced: true},
	} {
		require.Error(t, NewManager(cfg, opts).PrepareIndexBootstrap())
	}
	m := NewManager(cfg, Options{BootstrapIndexes: true, WritesQuiesced: true, BootstrapTimeout: time.Minute})
	require.NoError(t, m.PrepareIndexBootstrap())
	require.True(t, m.opts.RunPuller)
	require.True(t, m.opts.RunIndexer)
}

type bootstrapSource struct {
	types.DocumentStore
	rows []*types.StoredDoc
}

func (s bootstrapSource) ScanDocuments(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error) {
	return types.SourceScanPage{Documents: s.rows, Exhausted: true, NextAfter: "a"}, nil
}

func (s bootstrapSource) EnumerateCollections(context.Context, string, string, int, ...types.CollectionEnumerationOptions) ([]string, error) {
	if len(s.rows) == 0 {
		return nil, nil
	}
	return []string{"items"}, nil
}

type bootstrapPuller struct {
	puller.LocalService
	ctx              context.Context
	calls            *[]string
	handlerInstalled bool
	startErr         error
	boundaryErr      error
}

func (p *bootstrapPuller) Start(ctx context.Context) error {
	p.ctx = ctx
	*p.calls = append(*p.calls, "capture-start")
	return p.startErr
}

func (p *bootstrapPuller) BootstrapBoundary(context.Context) (string, error) {
	return bootstrapTestBoundary(), p.boundaryErr
}
func (p *bootstrapPuller) SetEventHandler(func(context.Context, string, *puller.ChangeEvent) error) {
	p.handlerInstalled = true
}
func (*bootstrapPuller) ValidateBoundary(context.Context, string) error { return nil }
func (*bootstrapPuller) SubscribeReady(_ context.Context, _, after string, onReady func(string)) <-chan *puller.Event {
	stream := make(chan *puller.Event, 1)
	stream <- &puller.Event{Ready: true, Progress: after}
	if onReady != nil {
		onReady(after)
	}
	return stream
}

func bootstrapTestBoundary() string {
	return (&cursor.ProgressMarker{Positions: map[string]string{"default_mongo": ""}, Lineages: map[string]string{"default_mongo": "test-lineage"}}).Encode()
}

type bootstrapIndexer struct {
	indexer.LocalService
	ctx           context.Context
	calls         *[]string
	failure       error
	request       indexer.BootstrapRequest
	beforePublish func()
	bootstrapHook func(context.Context) error
	startErr      error
}

func (s *bootstrapIndexer) Bootstrap(ctx context.Context, request indexer.BootstrapRequest) error {
	*s.calls = append(*s.calls, "scan-publish")
	s.request = request
	if s.bootstrapHook != nil {
		if err := s.bootstrapHook(ctx); err != nil {
			return err
		}
	}
	if s.beforePublish != nil {
		s.beforePublish()
	}
	return request.ValidateInventory(ctx)
}

func (s *bootstrapIndexer) Start(ctx context.Context) error {
	s.ctx = ctx
	*s.calls = append(*s.calls, "indexer-start")
	return s.startErr
}

func (s *bootstrapIndexer) StartWithValidationContext(lifeCtx, validationCtx context.Context) error {
	if err := validationCtx.Err(); err != nil {
		return err
	}
	return s.Start(lifeCtx)
}

func (s *bootstrapIndexer) WaitReady(context.Context) error {
	*s.calls = append(*s.calls, "applied-ready")
	return s.failure
}

func TestBootstrapCoordinatorPreservesLifetimeAndFailure(t *testing.T) {
	for _, failure := range []error{nil, context.DeadlineExceeded} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			cfg := bootstrapTestConfig(t)
			m := NewManager(cfg, Options{Mode: ModeStandalone, BootstrapIndexes: true, WritesQuiesced: true})
			m.storageFactory = &fakeStorageFactory{docStore: bootstrapSource{}, dbStore: &bootstrapMetadata{}}
			m.storageFactoryOnce.Do(func() {})
			var calls []string
			p := &bootstrapPuller{calls: &calls}
			i := &bootstrapIndexer{calls: &calls, failure: failure}
			m.pullerService, m.indexerService = p, i
			lifeCtx, lifeCancel := context.WithCancel(context.Background())
			defer lifeCancel()
			opCtx, opCancel := context.WithTimeout(lifeCtx, time.Second)
			defer opCancel()
			err := m.BootstrapIndexes(lifeCtx, opCtx)
			if failure == nil {
				require.NoError(t, err)
				require.True(t, m.bootstrapReady)
			} else {
				require.ErrorIs(t, err, failure)
				require.False(t, m.bootstrapReady)
				m.Start(lifeCtx)
			}
			require.Equal(t, []string{"capture-start", "scan-publish", "indexer-start", "applied-ready"}, calls)
			require.Equal(t, bootstrapTestBoundary(), i.request.ExpectedBoundary)
			opCancel()
			require.NoError(t, p.ctx.Err())
			require.NoError(t, i.ctx.Err())
			require.True(t, m.pullerStarted)
			require.True(t, m.indexerStarted)
			require.ErrorContains(t, m.BootstrapIndexes(lifeCtx, lifeCtx), "already started")
		})
	}
}

func TestBootstrapCoordinatorKeepsMemoryGenerationServing(t *testing.T) {
	cfg := bootstrapTestConfig(t)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Indexer.TemplatePath, "default.yaml"), []byte("database: default\ntemplates:\n  - name: by-id\n    collectionPattern: items\n    fields:\n      - field: id\n        order: asc\n"), 0600))
	m := NewManager(cfg, Options{Mode: ModeStandalone, BootstrapIndexes: true, WritesQuiesced: true})
	source := bootstrapSource{rows: []*types.StoredDoc{{Database: "default", Collection: "items", Fullpath: "items/a", Data: map[string]any{"name": "preserved"}}}}
	m.storageFactory = &fakeStorageFactory{docStore: source, dbStore: &bootstrapMetadata{}}
	m.storageFactoryOnce.Do(func() {})
	var calls []string
	p := &bootstrapPuller{calls: &calls}
	i, err := indexer.NewService(cfg.Indexer, p, nil)
	require.NoError(t, err)
	m.pullerService, m.indexerService = p, i
	lifeCtx, lifeCancel := context.WithCancel(context.Background())
	defer lifeCancel()
	t.Cleanup(func() {
		lifeCancel()
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, i.Stop(stopCtx))
	})
	opCtx, opCancel := context.WithTimeout(lifeCtx, time.Second)
	defer opCancel()
	require.NoError(t, m.BootstrapIndexes(lifeCtx, opCtx))
	opCancel()
	require.NoError(t, i.(indexer.BootstrapService).WaitReady(lifeCtx))
	stream, err := i.(indexer.CandidateService).OpenCandidates(lifeCtx, "default", indexer.Plan{Collection: "items", OrderBy: []manager.OrderField{{Field: "id", Direction: encoding.Asc}}, Limit: 10})
	require.NoError(t, err)
	defer stream.Close()
	group, found, err := stream.Next()
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "a", group.ID)
}

func TestBootstrapCoordinatorRejectsChangedInventoryBeforePublication(t *testing.T) {
	cfg := bootstrapTestConfig(t)
	m := NewManager(cfg, Options{Mode: ModeStandalone, BootstrapIndexes: true, WritesQuiesced: true})
	metadata := &bootstrapMetadata{}
	m.storageFactory = &fakeStorageFactory{docStore: bootstrapSource{}, dbStore: metadata}
	m.storageFactoryOnce.Do(func() {})
	var calls []string
	p := &bootstrapPuller{calls: &calls}
	i := &bootstrapIndexer{calls: &calls, beforePublish: func() { metadata.rows = []*database.Database{{ID: "created-during-maintenance"}} }}
	m.pullerService, m.indexerService = p, i
	lifeCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := m.BootstrapIndexes(lifeCtx, lifeCtx)
	require.ErrorContains(t, err, "inventory changed")
	require.False(t, m.indexerStarted)
	require.False(t, m.bootstrapReady)
	m.Start(lifeCtx)
	require.Equal(t, []string{"capture-start", "scan-publish"}, calls)
}

type bootstrapListener struct {
	server.Service
	start func(context.Context) error
}

func (s bootstrapListener) Start(ctx context.Context) error { return s.start(ctx) }

func TestBootstrapDistributedListenerOrderingAndFailure(t *testing.T) {
	for _, listenerFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("listener-failure=%t", listenerFailure), func(t *testing.T) {
			cfg := bootstrapTestConfig(t)
			m := NewManager(cfg, Options{Mode: ModeDistributed, BootstrapIndexes: true, WritesQuiesced: true})
			m.storageFactory = &fakeStorageFactory{docStore: bootstrapSource{}, dbStore: &bootstrapMetadata{}}
			m.storageFactoryOnce.Do(func() {})
			var calls []string
			p := &bootstrapPuller{calls: &calls}
			i := &bootstrapIndexer{calls: &calls}
			m.pullerService, m.indexerService = p, i
			m.pullerGRPC = puller.NewGRPCServerWithInit(cfg.Puller.GRPC, p, nil)
			defer m.pullerGRPC.Shutdown()
			lifeCtx, lifeCancel := context.WithCancel(context.Background())
			defer lifeCancel()
			opCtx, opCancel := context.WithTimeout(lifeCtx, time.Second)
			defer opCancel()
			listenerStarted := make(chan struct{})
			var starts atomic.Int32
			listenErr := errors.New("listener unavailable")
			previousServer := server.Default()
			defer server.SetDefault(previousServer)
			server.SetDefault(bootstrapListener{start: func(ctx context.Context) error {
				starts.Add(1)
				if !p.handlerInstalled {
					return errors.New("Puller handler must be installed before the listener")
				}
				close(listenerStarted)
				if listenerFailure {
					return listenErr
				}
				<-ctx.Done()
				return nil
			}})
			i.bootstrapHook = func(ctx context.Context) error {
				select {
				case <-listenerStarted:
				case <-ctx.Done():
					return ctx.Err()
				}
				if listenerFailure {
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			}
			err := m.BootstrapIndexes(lifeCtx, opCtx)
			if listenerFailure {
				require.ErrorIs(t, err, listenErr)
				require.False(t, m.bootstrapReady)
				require.False(t, m.indexerStarted)
			} else {
				require.NoError(t, err)
				require.True(t, m.bootstrapReady)
			}
			m.Start(lifeCtx)
			lifeCancel()
			m.wg.Wait()
			require.Equal(t, int32(1), starts.Load())
			if listenerFailure {
				require.Equal(t, []string{"capture-start", "scan-publish"}, calls)
			} else {
				require.Equal(t, []string{"capture-start", "scan-publish", "indexer-start", "applied-ready"}, calls)
			}
		})
	}
}

func TestBootstrapFailuresDoNotAdvanceLifecycle(t *testing.T) {
	for _, stage := range []string{"storage", "inventory", "capture-configuration", "capture-start", "boundary", "publication", "validation"} {
		t.Run(stage, func(t *testing.T) {
			cfg := bootstrapTestConfig(t)
			m := NewManager(cfg, Options{Mode: ModeStandalone, BootstrapIndexes: true, WritesQuiesced: true})
			metadata := &bootstrapMetadata{}
			m.storageFactory = &fakeStorageFactory{docStore: bootstrapSource{}, dbStore: metadata}
			m.storageFactoryOnce.Do(func() {})
			var calls []string
			p := &bootstrapPuller{calls: &calls}
			i := &bootstrapIndexer{calls: &calls}
			m.pullerService, m.indexerService = p, i
			failure := errors.New("maintenance dependency failed")
			var expectedCalls []string
			switch stage {
			case "storage":
				m.storageFactoryErr = failure
			case "inventory":
				metadata.err = failure
			case "capture-configuration":
				cfg.Puller.Backends = nil
			case "capture-start":
				p.startErr = failure
				expectedCalls = []string{"capture-start"}
			case "boundary":
				p.boundaryErr = failure
				expectedCalls = []string{"capture-start"}
			case "publication":
				i.bootstrapHook = func(context.Context) error { return failure }
				expectedCalls = []string{"capture-start", "scan-publish"}
			case "validation":
				i.startErr = failure
				expectedCalls = []string{"capture-start", "scan-publish", "indexer-start"}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := m.BootstrapIndexes(ctx, ctx)
			if stage == "capture-configuration" {
				require.ErrorContains(t, err, "not captured")
			} else {
				require.ErrorIs(t, err, failure)
			}
			require.False(t, m.bootstrapReady)
			require.False(t, m.indexerStarted)
			m.Start(ctx)
			require.Equal(t, expectedCalls, calls)
		})
	}
}
