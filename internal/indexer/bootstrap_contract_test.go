package indexer

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/puller"
)

type contractScanner struct {
	page types.SourceScanPage
	err  error
}

func (s contractScanner) ScanDocuments(context.Context, string, types.SourceScanRequest) (types.SourceScanPage, error) {
	return s.page, s.err
}

func TestBootstrapRejectsSourceViolationsWithoutPublishingCompleteness(t *testing.T) {
	for _, scenario := range []string{"oversized-page", "invalid-identity", "wrong-scope", "unordered", "wrong-continuation", "no-progress", "unsupported-scalar", "storage-failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			p := &bootstrapPuller{marker: "opaque-bootstrap", stream: make(chan *puller.Event)}
			s := newBootstrapTestService(t, config.StorageModeMemory, p)
			defer s.Stop(context.Background())
			s.cfg.BootstrapBatchSize = 2
			doc := types.NewStoredDoc("db", "users/a/docs", "one", map[string]any{"score": int64(1)})
			enumeration := &bootstrapDocuments{docs: map[string]map[string][]*types.StoredDoc{"db": {doc.Collection: {&doc}}}}
			page := types.SourceScanPage{Documents: []*types.StoredDoc{&doc}, NextAfter: "one", Exhausted: true}
			failure := errors.New("projection persistence failed")
			switch scenario {
			case "oversized-page":
				page.Documents = []*types.StoredDoc{&doc, &doc, &doc}
			case "invalid-identity":
				doc.Fullpath = ""
			case "wrong-scope":
				doc.Database = "different"
			case "unordered":
				page.Documents = []*types.StoredDoc{&doc, &doc}
			case "wrong-continuation":
				page.NextAfter = "other"
			case "no-progress":
				page = types.SourceScanPage{}
			case "unsupported-scalar":
				doc.Data["score"] = []any{int64(1)}
			case "storage-failure":
				s.store = &failingObservationStore{Store: s.store, failure: failure}
			}
			err := s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: contractScanner{page: page}, Enumerator: enumeration, WritesQuiesced: true})
			require.Error(t, err)
			if scenario == "storage-failure" {
				require.ErrorIs(t, err, failure)
			}
			catalogs, readErr := s.store.ListBootstrapCatalogs()
			require.NoError(t, readErr)
			require.Empty(t, catalogs)
			progress, readErr := s.store.LoadProgress()
			require.NoError(t, readErr)
			require.Empty(t, progress)
			require.Error(t, s.Start(ctx))
			_, queryErr := s.OpenCandidates(ctx, "db", Plan{Collection: "users/a/docs", OrderBy: []OrderField{{Field: "score", Direction: Asc}}})
			require.ErrorIs(t, queryErr, ErrIndexNotReady)
		})
	}
}

type catalogReadFault struct {
	store.Store
	catalogs      []store.BootstrapCatalog
	err           error
	retirementErr error
}

func (s *catalogReadFault) ListBootstrapCatalogs() ([]store.BootstrapCatalog, error) {
	return s.catalogs, s.err
}
func (s *catalogReadFault) IsDatabaseRetired(db string) (bool, error) {
	if s.retirementErr != nil {
		return false, s.retirementErr
	}
	return s.Store.IsDatabaseRetired(db)
}

func TestStartupRejectsIncompleteCorruptAndUnreadableCatalogs(t *testing.T) {
	for _, scenario := range []string{"catalog-read", "inconsistent-generation", "changed-templates", "missing-database", "retirement-read"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			p := &bootstrapPuller{marker: "opaque-bootstrap", stream: make(chan *puller.Event)}
			s := newBootstrapTestService(t, config.StorageModeMemory, p)
			defer s.Stop(context.Background())
			source := &bootstrapDocuments{}
			require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
			catalogs, err := s.store.ListBootstrapCatalogs()
			require.NoError(t, err)
			fault := &catalogReadFault{Store: s.store, catalogs: catalogs}
			failure := errors.New("metadata unreadable")
			switch scenario {
			case "catalog-read":
				fault.err = failure
			case "inconsistent-generation":
				fault.catalogs[1].Generation = "incomplete-replacement"
			case "changed-templates":
				fault.catalogs[0].TemplateFingerprints = []string{"unrecognized-definition"}
			case "missing-database":
				fault.catalogs = fault.catalogs[:1]
			case "retirement-read":
				fault.catalogs = nil
				fault.retirementErr = failure
			}
			s.store = fault
			err = s.Start(ctx)
			require.Error(t, err)
			if scenario == "catalog-read" || scenario == "retirement-read" {
				require.ErrorIs(t, err, failure)
			}
			require.Error(t, s.WaitReady(ctx))
			_, err = s.OpenCandidates(ctx, "db", Plan{Collection: "users/a/docs"})
			require.ErrorIs(t, err, ErrIndexNotReady)
		})
	}
}

func TestEntireRetiredInventoryCanRestartWithoutInventingNewCatalogs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	p := &bootstrapPuller{marker: "opaque-bootstrap", stream: make(chan *puller.Event)}
	s := newBootstrapTestService(t, config.StorageModeMemory, p)
	defer s.Stop(context.Background())
	source := &bootstrapDocuments{}
	require.NoError(t, s.Bootstrap(ctx, BootstrapRequest{Databases: []string{"db", "empty"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	require.NoError(t, s.InvalidateDatabase(ctx, "db"))
	require.NoError(t, s.InvalidateDatabase(ctx, "empty"))
	require.NoError(t, s.Start(ctx))
	require.NoError(t, s.WaitReady(ctx))
	catalogs, err := s.store.ListBootstrapCatalogs()
	require.NoError(t, err)
	require.Empty(t, catalogs)
	_, err = s.OpenCandidates(ctx, "db", Plan{Collection: "users/a/docs", OrderBy: []OrderField{{Field: "score", Direction: Asc}}})
	require.ErrorIs(t, err, ErrIndexNotReady)
}

type healthReadFault struct {
	store.Store
	listErr, readErr error
}

func (s *healthReadFault) ListQueryIndexes() ([]store.QueryIndexRef, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.Store.ListQueryIndexes()
}
func (s *healthReadFault) ReadGeneration(db, collection, fingerprint string) (store.Generation, bool, error) {
	if s.readErr != nil {
		return store.Generation{}, false, s.readErr
	}
	return s.Store.ReadGeneration(db, collection, fingerprint)
}

func TestHealthReportsActualV2GenerationStatesAndMetadataFailures(t *testing.T) {
	ctx := context.Background()
	svc, err := NewService(config.Config{}, nil, testLogger())
	require.NoError(t, err)
	s := svc.(*service)
	defer s.Stop(ctx)
	require.NoError(t, s.Start(ctx))
	healthy := store.QueryIndexRef{Database: "db", Collection: "healthy", TemplateFingerprint: "definition", Generation: "current"}
	staged := store.QueryIndexRef{Database: "db", Collection: "building", TemplateFingerprint: "definition", Generation: "building"}
	old := healthy
	old.Generation = "obsolete"
	require.NoError(t, s.store.ApplyDocumentProjection([]store.Projection{{Index: old, DocumentID: "doc", PostingKeys: [][]byte{[]byte("old")}}, {Index: staged, DocumentID: "doc", PostingKeys: [][]byte{[]byte("building")}}}, ""))
	require.NoError(t, s.store.PublishGeneration(healthy, ""))
	health, err := s.Health(ctx)
	require.NoError(t, err)
	require.Equal(t, HealthDegraded, health.Status)
	require.Len(t, health.Indexes, 2)
	healthyKey := "db|healthy|definition|current"
	stagedKey := "db|building|definition|building"
	require.Equal(t, "healthy", health.Indexes[healthyKey].State)
	require.Equal(t, "rebuilding", health.Indexes[stagedKey].State)
	require.Equal(t, int64(-1), health.Indexes[healthyKey].DocCount)
	require.NoError(t, s.store.SetFailure(healthy, "invalid projection"))
	health, err = s.Health(ctx)
	require.NoError(t, err)
	require.Equal(t, HealthUnhealthy, health.Status)
	require.Equal(t, "failed", health.Indexes[healthyKey].State)
	for _, phase := range []string{"enumeration", "generation"} {
		failure := errors.New("health metadata failed")
		fault := &healthReadFault{Store: s.store}
		if phase == "enumeration" {
			fault.listErr = failure
		} else {
			fault.readErr = failure
		}
		s.manager = manager.New(fault)
		health, err = s.Health(ctx)
		require.ErrorIs(t, err, failure)
		require.Equal(t, HealthUnhealthy, health.Status)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	s.manager = manager.New(s.store)
	health, err = s.Health(canceled)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, HealthUnhealthy, health.Status)
}
