package indexer

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/persist_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/indexer/template"
	"github.com/syntrixbase/syntrix/internal/puller"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// service implements LocalService.
type service struct {
	cfg          config.Config
	logger       *slog.Logger
	manager      *manager.Manager
	store        store.Store
	projectionMu sync.Mutex

	// Puller subscription
	pullerSvc puller.Service

	// State
	mu              sync.RWMutex
	running         bool
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	progress        string // last progress marker
	ready           bool
	retrying        bool
	bootstrapping   bool
	bootstrapCancel context.CancelFunc
	starting        bool
	startCancel     context.CancelFunc
	closed          bool
	lifecycleErr    error
	readyChanged    chan struct{}
	closeOnce       sync.Once
	closeErr        error

	// Stats
	eventsApplied atomic.Int64
	lastEventTime atomic.Int64
}

// NewService creates a new Indexer service.
// pullerSvc can be nil if not subscribing to Puller (for testing).
func NewService(cfg config.Config, pullerSvc puller.Service, logger *slog.Logger) (LocalService, error) {
	if logger == nil {
		logger = slog.Default()
	}
	defaults := config.DefaultConfig()
	if cfg.StartupTimeout == 0 {
		cfg.StartupTimeout = defaults.StartupTimeout
	}
	if cfg.BootstrapBatchSize == 0 {
		cfg.BootstrapBatchSize = defaults.BootstrapBatchSize
	}
	if cfg.BootstrapPageBytes == 0 {
		cfg.BootstrapPageBytes = defaults.BootstrapPageBytes
	}
	if cfg.StartupTimeout < 0 || cfg.BootstrapBatchSize < 0 || cfg.BootstrapPageBytes < 0 {
		return nil, fmt.Errorf("indexer maintenance limits must be positive")
	}

	if cfg.ConsumerID == "" {
		cfg.ConsumerID = "indexer"
	}
	if cfg.ProgressPath == "" {
		cfg.ProgressPath = "data/indexer/progress"
	}

	// Create store based on storage mode
	st, err := newStore(cfg, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to create store: %w", err)
	}

	return &service{
		cfg:          cfg,
		logger:       logger.With("component", "indexer"),
		manager:      manager.New(st),
		store:        st,
		pullerSvc:    pullerSvc,
		readyChanged: make(chan struct{}),
	}, nil
}

// newStore creates a store based on the storage mode configuration.
func newStore(cfg config.Config, logger *slog.Logger) (store.Store, error) {
	switch cfg.StorageMode {
	case config.StorageModeMemory, "":
		return mem_store.New(), nil
	case config.StorageModePebble:
		return persist_store.NewPebbleStore(cfg.Store, logger)
	default:
		return nil, fmt.Errorf("unknown storage mode: %s", cfg.StorageMode)
	}
}

// Start verifies persistent coverage before subscribing. WaitReady also waits for
// the replay prefix announced by Puller to be applied and flushed locally.
func (s *service) Start(ctx context.Context) error {
	validationCtx, cancel := context.WithTimeout(ctx, s.cfg.StartupTimeout)
	defer cancel()
	return s.StartWithValidationContext(ctx, validationCtx)
}

// StartWithValidationContext bounds startup validation separately from the
// lifetime of the subscription installed after successful validation.
func (s *service) StartWithValidationContext(lifeCtx, validationCtx context.Context) error {
	if err := validationCtx.Err(); err != nil {
		return err
	}
	if err := lifeCtx.Err(); err != nil {
		return err
	}
	validationCtx, cancel := context.WithCancel(validationCtx)
	s.mu.Lock()
	if s.running || s.bootstrapping || s.starting || s.closed {
		s.mu.Unlock()
		cancel()
		return fmt.Errorf("indexer is already running, building, or closed")
	}
	s.starting = true
	s.startCancel = cancel
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		s.starting = false
		s.startCancel = nil
		s.mu.Unlock()
		s.wg.Done()
	}()
	if err := s.loadTemplates(); err != nil {
		return err
	}
	progress, err := s.store.LoadProgress()
	if err != nil {
		return fmt.Errorf("load index progress: %w", err)
	}
	if boundary, ok := s.pullerSvc.(puller.BoundaryService); ok {
		if err := s.validateCatalogs(); err != nil {
			return err
		}
		if progress == "" {
			return fmt.Errorf("index bootstrap progress missing")
		}
		if err := boundary.ValidateBoundary(validationCtx, progress); err != nil {
			return fmt.Errorf("validate index resume boundary: %w", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validationCtx.Err(); err != nil {
		return err
	}
	if err := lifeCtx.Err(); err != nil {
		return err
	}
	if s.closed {
		return fmt.Errorf("indexer was closed during startup")
	}
	s.progress = progress
	s.running = true
	s.lifecycleErr = nil
	s.retrying = false
	s.setReadyLocked(s.pullerSvc == nil, nil)
	if s.pullerSvc != nil {
		subCtx, cancel := context.WithCancel(lifeCtx)
		s.cancel = cancel
		s.wg.Add(1)
		go s.subscriptionLoop(subCtx)
	}
	return nil
}

func (s *service) setReadyLocked(ready bool, err error) {
	s.ready = ready
	if ready {
		s.retrying = false
	}
	s.lifecycleErr = err
	if s.readyChanged != nil {
		close(s.readyChanged)
	}
	s.readyChanged = make(chan struct{})
}

func (s *service) failSubscription(err error) {
	s.mu.Lock()
	s.retrying = false
	s.setReadyLocked(false, err)
	s.mu.Unlock()
	if err != nil {
		s.logger.Error("index subscription unavailable", "state", "unavailable", "category", indexFailureCategory(err, "subscription_failure"))
	}
}

func (s *service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	if s.startCancel != nil {
		s.startCancel()
	}
	if s.bootstrapCancel != nil {
		s.bootstrapCancel()
	}
	if s.cancel != nil {
		s.cancel()
	}
	s.running = false
	s.retrying = false
	s.setReadyLocked(false, s.lifecycleErr)
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	s.closeOnce.Do(func() {
		err := s.store.Close()
		s.mu.Lock()
		s.closeErr = err
		s.closed = true
		s.mu.Unlock()
	})
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closeErr
}

func temporaryBoundaryError(err error) bool {
	if errors.Is(err, puller.ErrCaptureUnavailable) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		return true
	default:
		return false
	}
}

func (s *service) subscriptionLoop(ctx context.Context) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		failure := s.lifecycleErr
		if failure == nil {
			failure = ctx.Err()
			if failure == nil {
				failure = fmt.Errorf("index subscription terminated")
			}
		}
		s.retrying = false
		s.setReadyLocked(false, failure)
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Unlock()
	}()
	boundary, verified := s.pullerSvc.(puller.BoundaryService)
	var stream <-chan *puller.Event
	var attemptCancel context.CancelFunc
	attach := func(progress string) {
		if attemptCancel != nil {
			attemptCancel()
		}
		var attemptCtx context.Context
		attemptCtx, attemptCancel = context.WithCancel(ctx)
		if verified {
			stream = boundary.SubscribeReady(attemptCtx, s.cfg.ConsumerID, progress, nil)
		} else {
			stream = s.pullerSvc.Subscribe(attemptCtx, s.cfg.ConsumerID, progress)
			s.mu.Lock()
			s.setReadyLocked(true, nil)
			s.mu.Unlock()
		}
	}
	defer func() {
		if attemptCancel != nil {
			attemptCancel()
		}
	}()
	s.mu.RLock()
	initial := s.progress
	s.mu.RUnlock()
	attach(initial)
	retries := 0
	reconnect := func(cause error) bool {
		attemptCancel()
		s.mu.Lock()
		s.retrying = true
		s.setReadyLocked(false, cause)
		s.mu.Unlock()
		if err := s.store.Flush(); err != nil {
			s.failSubscription(err)
			return false
		}
		progress, err := s.store.LoadProgress()
		if err != nil {
			s.failSubscription(err)
			return false
		}
		s.mu.Lock()
		s.progress = progress
		s.mu.Unlock()
		for retries < 10 {
			retries++
			select {
			case <-ctx.Done():
				return false
			case <-time.After(time.Second):
			}
			if verified {
				validationCtx, cancel := context.WithTimeout(ctx, s.cfg.StartupTimeout)
				err := boundary.ValidateBoundary(validationCtx, progress)
				cancel()
				if err != nil {
					if ctx.Err() != nil {
						return false
					}
					if !temporaryBoundaryError(err) {
						s.failSubscription(err)
						return false
					}
					cause = err
					s.mu.Lock()
					s.setReadyLocked(false, err)
					s.mu.Unlock()
					continue
				}
			}
			attach(progress)
			return true
		}
		s.failSubscription(fmt.Errorf("Puller reconnect attempts exhausted: %w", cause))
		return false
	}
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-stream:
			if ctx.Err() != nil {
				return
			}
			if !ok {
				if reconnect(fmt.Errorf("%w: Puller subscription closed", ErrIndexNotReady)) {
					continue
				}
				return
			}
			if evt == nil {
				s.failSubscription(fmt.Errorf("Puller returned a nil event"))
				return
			}
			if evt.Error != nil {
				if evt.Change != nil || evt.Ready {
					s.failSubscription(fmt.Errorf("invalid Puller error control event"))
					return
				}
				if evt.Retryable {
					if reconnect(evt.Error) {
						continue
					}
					return
				}
				s.failSubscription(evt.Error)
				return
			}
			if evt.Ready {
				if !verified || evt.Change != nil || evt.Progress == "" {
					s.failSubscription(fmt.Errorf("invalid Puller readiness barrier"))
					return
				}
				s.mu.RLock()
				applied := s.progress
				s.mu.RUnlock()
				if applied != evt.Progress {
					s.failSubscription(fmt.Errorf("Puller readiness barrier differs from applied progress"))
					return
				}
				if err := s.store.Flush(); err != nil {
					s.failSubscription(err)
					return
				}
				if ctx.Err() != nil {
					return
				}
				retries = 0
				s.mu.Lock()
				s.setReadyLocked(true, nil)
				s.mu.Unlock()
				continue
			}
			if evt.Change != nil {
				if err := s.ApplyEvent(ctx, evt.Change, evt.Progress); err != nil {
					s.failSubscription(err)
					return
				}
				if !verified {
					retries = 0
				}
			}
		}
	}
}

// ProjectionLimits bound complete expansion before any storage mutation.
type ProjectionLimits struct {
	MaxArrayElements int
	MaxPostings      int
	MaxEncodedBytes  int
	MaxPositionBytes int
}

func DefaultProjectionLimits() ProjectionLimits {
	return ProjectionLimits{MaxArrayElements: 10000, MaxPostings: 1024, MaxEncodedBytes: 1 << 20, MaxPositionBytes: 4096}
}

// DocumentField reads reserved metadata from the envelope. Tombstones have no
// business fields even when a replay payload retains obsolete business data.
func DocumentField(doc *types.StoredDoc, logicalID, field string) (any, bool) {
	switch field {
	case "id":
		return logicalID, true
	case "collection":
		return doc.Collection, true
	case "version":
		return doc.Version, true
	case "createdAt":
		return doc.CreatedAt, true
	case "updatedAt":
		return doc.UpdatedAt, true
	case "deleted":
		return doc.Deleted, true
	default:
		if doc.Deleted {
			return nil, false
		}
		value, present := doc.Data[field]
		return value, present
	}
}

// BuildDocumentProjection is shared by live ingestion and maintenance bootstrap.
// A successful result replaces the document's entire previous posting set.
func BuildDocumentProjection(doc *types.StoredDoc, tmpl *template.Template, generation string, limits ProjectionLimits) (store.Projection, error) {
	projection := store.Projection{}
	if limits.MaxArrayElements <= 0 || limits.MaxPostings <= 0 || limits.MaxEncodedBytes <= 0 || limits.MaxPositionBytes <= 0 {
		return projection, fmt.Errorf("projection limits must be positive")
	}
	if err := template.ValidateTemplate(tmpl); err != nil {
		return projection, err
	}
	id, err := types.LogicalDocumentID(doc)
	if err != nil {
		return projection, err
	}
	if generation == "" {
		return projection, fmt.Errorf("projection generation is required")
	}
	projection.Index = store.QueryIndexRef{Database: doc.Database, Collection: doc.Collection, TemplateFingerprint: tmpl.Fingerprint(), Generation: generation}
	projection.DocumentID = id
	if doc.Deleted && !tmpl.IncludeDeleted {
		return projection, nil
	}
	fields := make([]encoding.Field, len(tmpl.Fields))
	membership := -1
	var members []any
	for i, field := range tmpl.Fields {
		value, present := DocumentField(doc, id, field.Field)
		direction := encoding.Asc
		if field.Order == template.Desc {
			direction = encoding.Desc
		}
		fields[i] = encoding.Field{Value: value, Missing: !present, Direction: direction}
		if field.Mode == template.Membership {
			membership = i
			if !present || value == nil {
				continue
			}
			source := reflect.ValueOf(value)
			if source.Kind() != reflect.Slice && source.Kind() != reflect.Array {
				if _, err := model.NormalizeValue(value); err != nil {
					return projection, fmt.Errorf("field %q: %w", field.Field, err)
				}
				continue
			}
			if source.Len() > limits.MaxArrayElements {
				return projection, &ProjectionLimitError{Kind: "membership_elements", Limit: limits.MaxArrayElements}
			}
			seen := make(map[string]struct{})
			for j := 0; j < source.Len(); j++ {
				member, err := model.NormalizeValue(source.Index(j).Interface())
				if err != nil {
					return projection, fmt.Errorf("membership field %q element %d: %w", field.Field, j, err)
				}
				switch member.(type) {
				case []any, map[string]any:
					continue
				}
				key, err := model.ScalarKey(member)
				if err != nil {
					return projection, err
				}
				if _, found := seen[string(key)]; found {
					continue
				}
				if len(members) == limits.MaxPostings {
					return projection, &ProjectionLimitError{Kind: "membership_postings", Limit: limits.MaxPostings}
				}
				seen[string(key)] = struct{}{}
				members = append(members, member)
			}
		} else if present {
			if _, err := model.ScalarKey(value); err != nil {
				return projection, fmt.Errorf("scalar index field %q: %w", field.Field, err)
			}
		}
	}
	scalarFields := make([]encoding.Field, 0, len(fields))
	for i, field := range fields {
		if i != membership {
			scalarFields = append(scalarFields, field)
		}
	}
	position, err := encoding.Encode(scalarFields, id)
	if err != nil {
		return projection, err
	}
	if len(position) > limits.MaxPositionBytes {
		return projection, &ProjectionLimitError{Kind: "position_bytes", Limit: limits.MaxPositionBytes}
	}
	if membership < 0 {
		members = []any{nil}
	}
	encodedBytes := 0
	for _, member := range members {
		if membership >= 0 {
			fields[membership] = encoding.Field{Value: member, Direction: encoding.Asc}
		}
		key, err := encoding.Encode(fields, id)
		if err != nil {
			return projection, err
		}
		if len(key) > limits.MaxEncodedBytes-encodedBytes {
			return projection, &ProjectionLimitError{Kind: "posting_bytes", Limit: limits.MaxEncodedBytes}
		}
		encodedBytes += len(key)
		projection.PostingKeys = append(projection.PostingKeys, key)
	}
	sort.Slice(projection.PostingKeys, func(i, j int) bool { return encoding.Compare(projection.PostingKeys[i], projection.PostingKeys[j]) < 0 })
	return projection, nil
}

// ApplyEvent constructs every matching replacement before publishing the event
// and its progress marker under one store visibility boundary.
func (s *service) ApplyEvent(ctx context.Context, evt *ChangeEvent, progress string) (resultErr error) {
	stage := "event_validation"
	var failureRef store.QueryIndexRef
	defer func() {
		if resultErr != nil {
			s.logProjectionFailure(ctx, "", evt, failureRef, stage, resultErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if evt == nil {
		return nil
	}
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	doc := evt.FullDocument
	if doc == nil && evt.OpType == puller.OperationDelete {
		// Physical cleanup has only a storage hash, which cannot identify logical
		// postings. Query rejects orphan candidates through authoritative hydration;
		// maintenance rebuilds reclaim them without inventing document identity.
		stage = "storage_failure"
		return s.commitProjection(nil, progress)
	}
	if doc == nil {
		return fmt.Errorf("index event requires the authoritative document envelope")
	}
	if evt.Database != doc.Database {
		return fmt.Errorf("index event database differs from document database")
	}
	stage = "storage_failure"
	retired, err := s.store.IsDatabaseRetired(doc.Database)
	if err != nil {
		return err
	}
	if retired {
		return s.commitProjection(nil, progress)
	}
	stage = "event_validation"
	if _, err := types.LogicalDocumentID(doc); err != nil {
		return err
	}
	matches := template.MatchTemplates(doc.Collection, s.databaseTemplates(doc.Database))
	replacements := make([]store.Projection, 0, len(matches))
	refs := make([]store.QueryIndexRef, 0, len(matches))
	for _, match := range matches {
		fingerprint := match.Template.Fingerprint()
		stage = "generation_unavailable"
		failureRef = store.QueryIndexRef{Database: doc.Database, Collection: doc.Collection, TemplateFingerprint: fingerprint}
		generation, found, err := s.store.ReadGeneration(doc.Database, doc.Collection, fingerprint)
		if err != nil {
			return err
		}
		if !found || !generation.Ready || generation.Failure != "" {
			return fmt.Errorf("index generation unavailable for collection %q", doc.Collection)
		}
		refs = append(refs, store.QueryIndexRef{Database: doc.Database, Collection: doc.Collection, TemplateFingerprint: fingerprint, Generation: generation.ID})
	}
	for i, match := range matches {
		stage = "invalid_projection"
		failureRef = refs[i]
		projection, err := BuildDocumentProjection(doc, match.Template, refs[i].Generation, DefaultProjectionLimits())
		if err != nil {
			return s.failEvent(refs, err)
		}
		replacements = append(replacements, projection)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	stage = "storage_failure"
	if err := s.commitProjection(replacements, progress); err != nil {
		return s.failEvent(refs, err)
	}
	return nil
}

func (s *service) commitProjection(replacements []store.Projection, progress string) error {
	if err := s.store.ApplyDocumentProjection(replacements, progress); err != nil {
		return err
	}
	s.mu.Lock()
	if progress != "" {
		s.progress = progress
	}
	s.mu.Unlock()
	s.eventsApplied.Add(1)
	s.lastEventTime.Store(time.Now().Unix())
	return nil
}

func (s *service) failEvent(refs []store.QueryIndexRef, cause error) error {
	result := cause
	for _, ref := range refs {
		if err := s.store.SetFailure(ref, cause.Error()); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

// Search returns bounded access candidates in index order. Query must hydrate
// and validate source documents; these references do not certify residual predicates.
func (s *service) Search(ctx context.Context, database string, plan Plan) (results []DocRef, resultErr error) {
	if err := s.queryReady(); err != nil {
		return nil, err
	}
	if plan.Limit < 0 {
		return nil, ErrInvalidPlan
	}
	if plan.StartAfter != "" {
		if len(plan.AfterPosition) != 0 {
			return nil, ErrInvalidPlan
		}
		position, err := base64.RawURLEncoding.DecodeString(plan.StartAfter)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid continuation", ErrInvalidPlan)
		}
		plan.AfterPosition = position
	}
	stream, err := s.manager.OpenCandidates(ctx, database, plan)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, stream.Close()) }()
	for plan.Limit == 0 || len(results) < plan.Limit {
		group, ok, err := stream.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		results = append(results, DocRef{ID: group.ID, OrderKey: group.Position})
	}
	return results, nil
}

func (s *service) queryReady() error {
	if s.pullerSvc == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.running || !s.ready {
		return fmt.Errorf("%w: verified replay is not applied", ErrIndexNotReady)
	}
	return nil
}

func (s *service) OpenCandidates(ctx context.Context, database string, plan Plan) (manager.CandidateStream, error) {
	if err := s.queryReady(); err != nil {
		return nil, err
	}
	return s.manager.OpenCandidates(ctx, database, plan)
}

// Health returns current health status of the indexer.
func (s *service) Health(ctx context.Context) (Health, error) {
	s.mu.RLock()
	running := s.running
	ready := s.ready
	failure := s.lifecycleErr
	s.mu.RUnlock()

	status := HealthOK
	if !running || (s.pullerSvc != nil && !ready) || failure != nil {
		status = HealthUnhealthy
	}

	st := s.manager.Store()
	indexes := make(map[string]manager.IndexHealth)
	refs, err := st.ListQueryIndexes()
	if err != nil {
		return Health{Status: HealthUnhealthy}, err
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return Health{Status: HealthUnhealthy}, err
		}
		generation, exists, err := st.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
		if err != nil {
			return Health{Status: HealthUnhealthy}, err
		}
		if exists && generation.ID != ref.Generation {
			continue
		}
		state := store.IndexStateRebuilding
		switch {
		case exists && generation.Failure != "":
			state = store.IndexStateFailed
			status = HealthUnhealthy
		case exists && generation.Ready:
			state = store.IndexStateHealthy
		default:
			if status == HealthOK {
				status = HealthDegraded
			}
		}
		key := ref.Database + "|" + ref.Collection + "|" + ref.TemplateFingerprint + "|" + ref.Generation
		indexes[key] = manager.IndexHealth{State: string(state), DocCount: -1}
	}

	return Health{
		Status:  status,
		Indexes: indexes,
		LastError: func() string {
			if failure != nil {
				return failure.Error()
			}
			return ""
		}(),
	}, nil
}

// Stats returns index statistics.
func (s *service) Stats(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	mgrStats := s.manager.Stats()

	// Augment with service-level stats
	mgrStats.EventsApplied = s.eventsApplied.Load()
	mgrStats.LastEventTime = s.lastEventTime.Load()

	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	return mgrStats, nil
}

// Manager returns the underlying index manager.
func (s *service) Manager() *manager.Manager {
	return s.manager
}

// InvalidateDatabase removes all index entries for a database.
func (s *service) InvalidateDatabase(ctx context.Context, database string) error {
	s.projectionMu.Lock()
	defer s.projectionMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	st := s.manager.Store()
	if err := st.DeleteDatabase(database); err != nil {
		s.logger.Error("failed to invalidate database indexes",
			"database", database,
			"category", "storage_failure")
		return fmt.Errorf("failed to delete database indexes: %w", err)
	}
	s.logger.Info("invalidated database indexes", "database", database)
	return nil
}
