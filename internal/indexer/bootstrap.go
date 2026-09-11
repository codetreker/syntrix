package indexer

import (
	"context"
	"fmt"
	"slices"
	"sort"

	"github.com/google/uuid"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"github.com/syntrixbase/syntrix/internal/puller"
)

// BootstrapService owns a write-quiesced complete rebuild and its serving handoff.
type BootstrapService interface {
	Bootstrap(context.Context, BootstrapRequest) error
	StartWithValidationContext(lifeCtx, validationCtx context.Context) error
	WaitReady(context.Context) error
}

type BootstrapRequest struct {
	Databases  []string
	Scanner    types.DocumentScanner
	Enumerator types.DocumentCollectionEnumerator
	// Writers include API clients, workers, direct database clients, and Mongo TTL.
	WritesQuiesced    bool
	ExpectedBoundary  string
	ValidateInventory func(context.Context) error
}

func (s *service) databaseTemplates(database string) []template.Template {
	grouped := s.manager.DatabaseTemplates()
	if len(grouped) != 0 {
		return grouped[database]
	}
	return s.manager.Templates()
}

func (s *service) templateFingerprints(database string) []string {
	templates := s.databaseTemplates(database)
	fingerprints := make([]string, 0, len(templates))
	for i := range templates {
		fingerprints = append(fingerprints, templates[i].Fingerprint())
	}
	slices.Sort(fingerprints)
	return slices.Compact(fingerprints)
}

func (s *service) loadTemplates() error {
	if s.cfg.TemplatePath != "" {
		return s.manager.LoadTemplatesFromDir(s.cfg.TemplatePath)
	}
	return nil
}

func (s *service) requiredDatabases() []string {
	databases := slices.Clone(s.cfg.Databases)
	for database := range s.manager.DatabaseTemplates() {
		databases = append(databases, database)
	}
	slices.Sort(databases)
	return slices.Compact(databases)
}

// Bootstrap publishes no complete catalog until every source scan has finished.
// Incomplete attempts require an explicit derived-data reset before retrying.
func (s *service) Bootstrap(ctx context.Context, request BootstrapRequest) (resultErr error) {
	observation := &bootstrapObservation{operationID: uuid.NewString(), generation: uuid.NewString(), scopeID: indexIdentity(request.Databases...), phase: "validation", databases: len(request.Databases)}
	s.logBootstrap(ctx, observation, "validating", nil)
	defer func() {
		state := "ready"
		if resultErr != nil {
			state = "failed"
		}
		s.logBootstrap(ctx, observation, state, resultErr)
	}()
	if !request.WritesQuiesced {
		return fmt.Errorf("bootstrap requires all source writers and TTL deletion to remain quiesced")
	}
	if request.Scanner == nil || request.Enumerator == nil {
		return fmt.Errorf("bootstrap requires authoritative source scanning and collection enumeration")
	}
	boundarySource, ok := s.pullerSvc.(puller.BoundaryService)
	if !ok {
		return fmt.Errorf("bootstrap requires verified native Puller boundaries")
	}
	s.mu.Lock()
	if s.running || s.bootstrapping || s.starting || s.closed {
		s.mu.Unlock()
		return fmt.Errorf("indexer must be stopped and open before bootstrap")
	}
	operationCtx, cancel := context.WithCancel(ctx)
	ctx = operationCtx
	s.bootstrapCancel = cancel
	s.wg.Add(1)
	s.bootstrapping = true
	s.ready = false
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.bootstrapping = false
		s.bootstrapCancel = nil
		s.mu.Unlock()
		s.wg.Done()
	}()
	if err := s.loadTemplates(); err != nil {
		return err
	}
	databases := slices.Clone(request.Databases)
	slices.Sort(databases)
	databases = slices.Compact(databases)
	if len(databases) == 0 || databases[0] == "" {
		return fmt.Errorf("bootstrap requires a complete nonempty database inventory")
	}
	for _, database := range s.requiredDatabases() {
		if !slices.Contains(databases, database) {
			return fmt.Errorf("bootstrap inventory does not cover configured database %q", database)
		}
	}
	catalogs, err := s.store.ListBootstrapCatalogs()
	if err != nil {
		return err
	}
	indexes, err := s.store.ListQueryIndexes()
	if err != nil {
		return err
	}
	if len(catalogs) != 0 || len(indexes) != 0 {
		return fmt.Errorf("index store is not empty; explicit derived-data reset required")
	}
	observation.phase = "capture_boundary"
	boundary, err := boundarySource.BootstrapBoundary(ctx)
	if err != nil {
		return fmt.Errorf("establish bootstrap boundary: %w", err)
	}
	if request.ExpectedBoundary != "" && boundary != request.ExpectedBoundary {
		return fmt.Errorf("configured Puller does not own the maintenance capture boundary")
	}
	generation := observation.generation
	observation.phase = "scan"
	observation.databases = len(databases)
	observation.scopeID = indexIdentity(databases...)
	s.logBootstrap(ctx, observation, "scanning", nil)
	for _, database := range databases {
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			collections, err := request.Enumerator.EnumerateCollections(ctx, database, after, s.cfg.BootstrapBatchSize, types.CollectionEnumerationOptions{IncludeSystem: true})
			if err != nil {
				return fmt.Errorf("enumerate database %q: %w", database, err)
			}
			if len(collections) > s.cfg.BootstrapBatchSize {
				return fmt.Errorf("source enumeration exceeded its page bound")
			}
			for _, collection := range collections {
				if collection <= after {
					return fmt.Errorf("source collection enumeration did not advance")
				}
				after = collection
				matches := template.MatchTemplates(collection, s.databaseTemplates(database))
				if len(matches) == 0 {
					continue
				}
				observation.collections++
				if err := s.bootstrapCollection(ctx, request.Scanner, database, collection, generation, matches, observation); err != nil {
					return err
				}
			}
			if len(collections) < s.cfg.BootstrapBatchSize {
				break
			}
		}
	}
	observation.phase = "publication"
	s.logBootstrap(ctx, observation, "publishing", nil)
	if err := s.store.Flush(); err != nil {
		return fmt.Errorf("flush bootstrap projections: %w", err)
	}
	if err := boundarySource.ValidateBoundary(ctx, boundary); err != nil {
		return fmt.Errorf("validate completed bootstrap boundary: %w", err)
	}
	if request.ValidateInventory != nil {
		if err := request.ValidateInventory(ctx); err != nil {
			return err
		}
	}
	catalogs = make([]store.BootstrapCatalog, 0, len(databases))
	for _, database := range databases {
		catalogs = append(catalogs, store.BootstrapCatalog{Database: database, Generation: generation, TemplateFingerprints: s.templateFingerprints(database), BootstrapProgress: boundary})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.store.PublishBootstrapCatalogs(catalogs, boundary); err != nil {
		return fmt.Errorf("publish complete bootstrap catalog: %w", err)
	}
	s.mu.Lock()
	s.cfg.Databases = databases
	s.progress = boundary
	s.mu.Unlock()
	observation.phase = "completed"
	return nil
}

func (s *service) bootstrapCollection(ctx context.Context, scanner types.DocumentScanner, database, collection, generation string, matches []template.MatchResult, observation *bootstrapObservation) error {
	after := ""
	for {
		page, err := scanner.ScanDocuments(ctx, database, types.SourceScanRequest{Collection: collection, AfterID: after, Limit: s.cfg.BootstrapBatchSize, MaxBytes: s.cfg.BootstrapPageBytes, Consistency: types.ReadAuthoritative})
		if err != nil {
			return fmt.Errorf("scan %s/%s: %w", database, collection, err)
		}
		observation.scanned += int64(len(page.Documents))
		if len(page.Documents) > s.cfg.BootstrapBatchSize {
			return fmt.Errorf("source scan exceeded its page bound")
		}
		previous := after
		for _, doc := range page.Documents {
			if err := ctx.Err(); err != nil {
				return err
			}
			id, err := types.LogicalDocumentID(doc)
			if err != nil {
				return err
			}
			if doc.Database != database || doc.Collection != collection || id <= after {
				return fmt.Errorf("source scan returned an out-of-scope or unordered document")
			}
			projections := make([]store.Projection, 0, len(matches))
			for _, match := range matches {
				projection, err := BuildDocumentProjection(doc, match.Template, generation, DefaultProjectionLimits())
				if err != nil {
					ref := store.QueryIndexRef{Database: database, Collection: collection, TemplateFingerprint: match.Template.Fingerprint(), Generation: generation}
					s.logProjectionFailure(ctx, observation.operationID, &ChangeEvent{Database: database, FullDocument: doc}, ref, "invalid_projection", err)
					return fmt.Errorf("project %s/%s/%s: %w", database, collection, id, err)
				}
				projections = append(projections, projection)
			}
			if err := s.store.ApplyDocumentProjection(projections, ""); err != nil {
				return err
			}
			observation.projected++
			for _, projection := range projections {
				observation.postings += int64(len(projection.PostingKeys))
			}
			after = id
		}
		if len(page.Documents) > 0 && page.NextAfter != after {
			return fmt.Errorf("source scan continuation does not match its last document")
		}
		if page.Exhausted {
			return nil
		}
		if after == previous {
			return fmt.Errorf("source scan did not advance")
		}
	}
}

func (s *service) validateCatalogs() error {
	catalogs, err := s.store.ListBootstrapCatalogs()
	if err != nil {
		return err
	}
	if len(catalogs) == 0 {
		required := s.requiredDatabases()
		if len(required) == 0 {
			return fmt.Errorf("index bootstrap catalog missing; maintenance bootstrap required")
		}
		for _, database := range required {
			retired, err := s.store.IsDatabaseRetired(database)
			if err != nil {
				return err
			}
			if !retired {
				return fmt.Errorf("index bootstrap catalog missing; maintenance bootstrap required")
			}
		}
		return nil
	}
	byDatabase := make(map[string]store.BootstrapCatalog, len(catalogs))
	generation, boundary := catalogs[0].Generation, catalogs[0].BootstrapProgress
	for _, catalog := range catalogs {
		if catalog.Generation == "" || catalog.BootstrapProgress == "" || catalog.Generation != generation || catalog.BootstrapProgress != boundary {
			return fmt.Errorf("index bootstrap catalogs are incomplete or inconsistent")
		}
		expected := s.templateFingerprints(catalog.Database)
		actual := slices.Clone(catalog.TemplateFingerprints)
		sort.Strings(actual)
		if !slices.Equal(actual, expected) {
			return fmt.Errorf("index bootstrap templates changed for database %q", catalog.Database)
		}
		byDatabase[catalog.Database] = catalog
	}
	for _, database := range s.requiredDatabases() {
		if _, exists := byDatabase[database]; !exists {
			retired, err := s.store.IsDatabaseRetired(database)
			if err != nil {
				return err
			}
			if !retired {
				return fmt.Errorf("index bootstrap catalog missing for database %q", database)
			}
		}
	}
	return nil
}

func (s *service) WaitReady(ctx context.Context) error {
	for {
		s.mu.RLock()
		ready, running, failure, changed, retrying := s.ready, s.running, s.lifecycleErr, s.readyChanged, s.retrying
		s.mu.RUnlock()
		if failure != nil && !retrying {
			return failure
		}
		if ready && running {
			return nil
		}
		if !running {
			return fmt.Errorf("%w: indexer is not running", ErrIndexNotReady)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
