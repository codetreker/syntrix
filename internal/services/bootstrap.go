package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/syntrixbase/syntrix/internal/config"
	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"github.com/syntrixbase/syntrix/internal/puller"
	"github.com/syntrixbase/syntrix/internal/server"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// PrepareIndexBootstrap validates maintenance options and archives explicitly
// selected derived stores before Init opens them. Source storage is never reset.
// Operators must stop other processes using these paths before calling it.
func (m *Manager) PrepareIndexBootstrap() error {
	if !m.opts.BootstrapIndexes {
		if m.opts.WritesQuiesced || m.opts.ResetDerived {
			return errors.New("--writes-quiesced and --reset-derived require --bootstrap-indexes")
		}
		return nil
	}
	if !m.opts.WritesQuiesced {
		return errors.New("index bootstrap requires --writes-quiesced: stop external writers and pause MongoDB tombstone TTL deletion")
	}
	if m.opts.BootstrapTimeout <= 0 {
		return errors.New("bootstrap timeout must be positive")
	}
	if m.opts.ResetDerived {
		return archiveDerivedStores([]string{m.cfg.Indexer.Store.Path, m.cfg.Puller.Buffer.Path})
	}
	return nil
}

// BootstrapIndexes keeps capture and consumption attached to lifeCtx while
// operationCtx bounds inventory, scanning, publication, and applied readiness.
// Call before Start so internal writers remain stopped throughout maintenance.
func (m *Manager) BootstrapIndexes(lifeCtx, operationCtx context.Context) error {
	if !m.opts.BootstrapIndexes || !m.opts.WritesQuiesced {
		return errors.New("index bootstrap requires maintenance mode and quiesced writes")
	}
	if m.pullerStarted || m.indexerStarted {
		return errors.New("index bootstrap has already started; restart maintenance after failure")
	}
	bootstrap, ok := m.indexerService.(indexer.BootstrapService)
	if !ok || m.pullerService == nil {
		return errors.New("index bootstrap requires local Puller and bootstrap-capable Indexer services")
	}
	sf, err := m.getStorageFactory(operationCtx)
	if err != nil {
		return err
	}
	databases, err := bootstrapDatabaseInventory(operationCtx, m.cfg, sf.Database())
	if err != nil {
		return err
	}
	if err := validateBootstrapCapture(m.cfg, databases); err != nil {
		return err
	}
	scanner, ok := sf.Document().(types.DocumentScanner)
	if !ok {
		return errors.New("document storage does not support authoritative bootstrap scanning")
	}
	enumerator, ok := sf.Document().(types.DocumentCollectionEnumerator)
	if !ok {
		return errors.New("document storage does not support authoritative collection enumeration")
	}
	if err := m.pullerService.Start(lifeCtx); err != nil {
		return fmt.Errorf("start bootstrap Puller: %w", err)
	}
	m.pullerStarted = true
	boundarySource, ok := m.pullerService.(puller.BoundaryService)
	if !ok {
		return errors.New("local Puller does not support bootstrap boundaries")
	}
	expectedBoundary, err := boundarySource.BootstrapBoundary(operationCtx)
	if err != nil {
		return fmt.Errorf("capture local bootstrap boundary: %w", err)
	}
	if m.opts.Mode.IsDistributed() {
		s := server.Default()
		if s == nil || m.pullerGRPC == nil {
			return errors.New("distributed index bootstrap requires the unified server and Puller gRPC service")
		}
		m.pullerGRPC.Init()
		m.pullerGRPCInitialized = true
		var cancel context.CancelCauseFunc
		operationCtx, cancel = context.WithCancelCause(operationCtx)
		defer cancel(nil)
		m.serverStarted = true
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			if err := s.Start(lifeCtx); err != nil {
				cancel(fmt.Errorf("bootstrap server failed: %w", err))
				slog.Error("Bootstrap server stopped", "error", err)
			}
		}()
	}
	if err := bootstrap.Bootstrap(operationCtx, indexer.BootstrapRequest{
		Databases: databases, Scanner: scanner, Enumerator: enumerator, WritesQuiesced: true,
		ExpectedBoundary: expectedBoundary,
		ValidateInventory: func(ctx context.Context) error {
			current, err := bootstrapDatabaseInventory(ctx, m.cfg, sf.Database())
			if err != nil {
				return err
			}
			if !slices.Equal(databases, current) {
				return errors.New("bootstrap database inventory changed; keep writes paused and rebuild")
			}
			return nil
		},
	}); err != nil {
		return fmt.Errorf("bootstrap indexes: %w", errors.Join(err, context.Cause(operationCtx)))
	}
	if err := bootstrap.StartWithValidationContext(lifeCtx, operationCtx); err != nil {
		return fmt.Errorf("start bootstrapped Indexer: %w", err)
	}
	m.indexerStarted = true
	if err := bootstrap.WaitReady(operationCtx); err != nil {
		return fmt.Errorf("wait for bootstrap subscription application: %w", err)
	}
	if err := operationCtx.Err(); err != nil {
		return context.Cause(operationCtx)
	}
	m.bootstrapReady = true
	return nil
}

func bootstrapDatabaseInventory(ctx context.Context, cfg *config.Config, store database.DatabaseStore) ([]string, error) {
	if store == nil {
		return nil, errors.New("index bootstrap requires database metadata storage to enumerate every database")
	}
	templates, err := template.LoadFromDir(cfg.Indexer.TemplatePath)
	if err != nil {
		return nil, fmt.Errorf("load bootstrap template inventory: %w", err)
	}
	ids := map[string]struct{}{model.DefaultDatabase: {}}
	for id := range cfg.Storage.Databases {
		ids[id] = struct{}{}
	}
	for _, id := range cfg.Indexer.Databases {
		ids[id] = struct{}{}
	}
	for id := range templates {
		ids[id] = struct{}{}
	}
	seen := make(map[string]struct{})
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, total, err := store.List(ctx, database.ListOptions{Limit: 100, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("list bootstrap databases: %w", err)
		}
		for _, db := range page {
			if db == nil || db.ID == "" {
				return nil, errors.New("database inventory contains an invalid record")
			}
			if _, duplicate := seen[db.ID]; duplicate {
				return nil, fmt.Errorf("database inventory repeated %q; verify metadata writes are stopped", db.ID)
			}
			seen[db.ID] = struct{}{}
			ids[db.ID] = struct{}{}
		}
		offset += len(page)
		if offset == total {
			break
		}
		if len(page) == 0 || offset > total {
			return nil, errors.New("database inventory count changed; verify metadata writes are stopped")
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		if strings.TrimSpace(id) == "" {
			return nil, errors.New("bootstrap database ID must not be empty")
		}
		result = append(result, id)
	}
	slices.Sort(result)
	return result, nil
}

func validateBootstrapCapture(cfg *config.Config, databases []string) error {
	capture := make(map[string][]string)
	for _, backend := range cfg.Puller.Backends {
		if _, duplicate := capture[backend.Name]; duplicate {
			return fmt.Errorf("duplicate Puller backend %q", backend.Name)
		}
		capture[backend.Name] = backend.Collections
	}
	for _, id := range databases {
		backendName := cfg.Storage.Topology.Document.Primary
		if route, ok := cfg.Storage.Databases[id]; ok {
			backendName = route.Backend
		}
		backend, ok := cfg.Storage.Backends[backendName]
		if !ok || backend.Type != "mongo" {
			return fmt.Errorf("bootstrap database %q requires a configured MongoDB write source", id)
		}
		collections, ok := capture[backendName]
		if !ok {
			return fmt.Errorf("bootstrap database %q write source %q is not captured by Puller", id, backendName)
		}
		for _, collection := range []string{cfg.Storage.Topology.Document.DataCollection, cfg.Storage.Topology.Document.SysCollection} {
			if collection == "" || !slices.Contains(collections, collection) {
				return fmt.Errorf("bootstrap database %q requires Puller backend %q to capture physical collection %q", id, backendName, collection)
			}
		}
	}
	return nil
}

func archiveDerivedStores(paths []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	resolved := make([]string, len(paths))
	for i, path := range paths {
		if strings.TrimSpace(path) == "" {
			return errors.New("derived store reset requires nonempty paths")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return err
		}
		if pathContains(absolute, cwd) {
			return fmt.Errorf("derived store path %q contains the working directory", path)
		}
		for current := absolute; ; current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err == nil && info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("derived store reset rejects symlink %q", current)
			}
			if current == absolute && err == nil && !info.IsDir() {
				return fmt.Errorf("derived store path %q is not a directory", path)
			}
			if current == filepath.Dir(current) {
				break
			}
		}
		for _, previous := range resolved[:i] {
			if pathContains(previous, absolute) || pathContains(absolute, previous) {
				return fmt.Errorf("derived store reset rejects overlapping paths %q and %q", previous, absolute)
			}
		}
		resolved[i] = absolute
	}
	archived := make(map[string]string)
	for _, path := range resolved {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		archive := fmt.Sprintf("%s.pre-bootstrap-%d", path, time.Now().UnixNano())
		if err := os.Rename(path, archive); err != nil {
			for original, backup := range archived {
				if rollbackErr := os.Rename(backup, original); rollbackErr != nil {
					err = errors.Join(err, fmt.Errorf("restore %q from %q: %w", original, backup, rollbackErr))
				}
			}
			return fmt.Errorf("archive derived store %q: %w", path, err)
		}
		archived[path] = archive
		slog.Info("Archived derived store before bootstrap", "path", path, "archive", archive)
	}
	return nil
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
