package grpc

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	indexerclient "github.com/syntrixbase/syntrix/internal/indexer/client"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/persist_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

const adminTemplatesYAML = `templates:
- name: by_score
  collectionPattern: items/{owner}
  fields: [{field: score, order: asc}]
- name: by_label
  collectionPattern: items/{owner}
  fields: [{field: label, order: asc}]
`

func adminStore(t *testing.T, backend string) store.Store {
	t.Helper()
	if backend == "memory" {
		st := mem_store.New()
		t.Cleanup(func() { require.NoError(t, st.Close()) })
		return st
	}
	cfg := config.DefaultStoreConfig()
	cfg.Path = filepath.Join(t.TempDir(), "index")
	cfg.BlockCacheSize = 1 << 20
	st, err := persist_store.NewPebbleStore(cfg, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func adminRPC(t *testing.T, mgr *manager.Manager) (indexerv1.IndexerServiceClient, *indexerclient.Client) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := googlegrpc.NewServer()
	indexerv1.RegisterIndexerServiceServer(server, NewServer(&readyCandidateService{mockLocalService: &mockLocalService{mgr: mgr}}))
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	conn, err := googlegrpc.NewClient(listener.Addr().String(), googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close()) })
	candidate, err := indexerclient.New(listener.Addr().String(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, candidate.Close()) })
	return indexerv1.NewIndexerServiceClient(conn), candidate
}

func TestAdminV2InvalidatesActiveGenerations(t *testing.T) {
	for _, backend := range []string{"memory", "pebble"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			st := adminStore(t, backend)
			mgr := manager.New(st)
			require.NoError(t, mgr.LoadTemplatesFromBytes([]byte(adminTemplatesYAML)))
			templates := mgr.Templates()
			seed := func(database, collection, generation string, templateIndex int) store.QueryIndexRef {
				ref := store.QueryIndexRef{Database: database, Collection: collection, TemplateFingerprint: templates[templateIndex].Fingerprint(), Generation: generation}
				key, err := encoding.Encode([]encoding.Field{{Value: int64(7)}}, "id")
				require.NoError(t, err)
				require.NoError(t, st.ApplyDocumentProjection([]store.Projection{{Index: ref, DocumentID: "id", PostingKeys: [][]byte{key}}}, ""))
				require.NoError(t, st.PublishGeneration(ref, ""))
				return ref
			}
			seed("db", "items/alice", "obsolete", 0)
			first := seed("db", "items/alice", "active", 0)
			second := seed("db", "items/bob", "active", 0)
			otherTemplate := seed("db", "items/alice", "active", 1)
			otherDatabase := seed("other", "items/alice", "active", 0)
			rpc, candidate := adminRPC(t, mgr)
			plan := manager.Plan{Collection: "items/alice", OrderBy: []manager.OrderField{{Field: "score", Direction: encoding.Asc}}}
			stream, err := candidate.OpenCandidates(ctx, "db", plan)
			require.NoError(t, err)
			group, more, err := stream.Next()
			require.NoError(t, err)
			require.True(t, more)
			assert.Equal(t, "id", group.ID)
			require.NoError(t, stream.Close())
			before, err := rpc.GetState(ctx, &indexerv1.GetStateRequest{Database: "db", Pattern: "items/*"})
			require.NoError(t, err)
			require.Len(t, before.Actual, 3)
			for _, actual := range before.Actual {
				assert.Equal(t, "healthy", actual.State)
				assert.EqualValues(t, -1, actual.DocCount)
				assert.Contains(t, []string{"items/alice", "items/bob"}, actual.Pattern)
			}
			invalidated, err := rpc.InvalidateIndex(ctx, &indexerv1.InvalidateIndexRequest{Database: "db", Pattern: "items/*", TemplateId: "by_score"})
			require.NoError(t, err)
			assert.EqualValues(t, 2, invalidated.IndexesInvalidated)
			_, err = candidate.OpenCandidates(ctx, "db", plan)
			require.ErrorIs(t, err, manager.ErrIndexNotReady)
			after, err := rpc.GetState(ctx, &indexerv1.GetStateRequest{Database: "db"})
			require.NoError(t, err)
			require.Len(t, after.Actual, 3)
			for _, actual := range after.Actual {
				if actual.TemplateId == "by_score" {
					assert.Equal(t, "failed", actual.State)
				} else {
					assert.Equal(t, "healthy", actual.State)
				}
			}
			for _, ref := range []store.QueryIndexRef{first, second, otherTemplate, otherDatabase} {
				generation, found, err := st.ReadGeneration(ref.Database, ref.Collection, ref.TemplateFingerprint)
				require.NoError(t, err)
				require.True(t, found)
				failed := ref.Database == "db" && ref.TemplateFingerprint == templates[0].Fingerprint()
				assert.Equal(t, !failed, generation.Ready)
			}
		})
	}
}

func TestAdminV2ConcreteEmptyCatalog(t *testing.T) {
	for _, backend := range []string{"memory", "pebble"} {
		t.Run(backend, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			st := adminStore(t, backend)
			mgr := manager.New(st)
			require.NoError(t, mgr.LoadTemplatesFromBytes([]byte(adminTemplatesYAML)))
			fingerprint := mgr.Templates()[0].Fingerprint()
			require.NoError(t, st.PublishBootstrapCatalog(store.BootstrapCatalog{Database: "db", Generation: "catalog", TemplateFingerprints: []string{fingerprint}}))
			rpc, candidate := adminRPC(t, mgr)
			plan := manager.Plan{Collection: "items/empty", OrderBy: []manager.OrderField{{Field: "score", Direction: encoding.Asc}}}
			stream, err := candidate.OpenCandidates(ctx, "db", plan)
			require.NoError(t, err)
			_, more, err := stream.Next()
			require.NoError(t, err)
			assert.False(t, more)
			require.NoError(t, stream.Close())
			broad, err := rpc.InvalidateIndex(ctx, &indexerv1.InvalidateIndexRequest{Database: "db", Pattern: "items/*", TemplateId: "by_score"})
			require.NoError(t, err)
			assert.Zero(t, broad.IndexesInvalidated)
			concrete, err := rpc.InvalidateIndex(ctx, &indexerv1.InvalidateIndexRequest{Database: "db", Pattern: "items/empty", TemplateId: "by_score"})
			require.NoError(t, err)
			assert.EqualValues(t, 1, concrete.IndexesInvalidated)
			_, err = candidate.OpenCandidates(ctx, "db", plan)
			require.ErrorIs(t, err, manager.ErrIndexNotReady)
			actual, err := rpc.GetState(ctx, &indexerv1.GetStateRequest{Database: "db", Pattern: "items/empty"})
			require.NoError(t, err)
			require.Len(t, actual.Actual, 1)
			require.Len(t, actual.Desired, 2)
			assert.Equal(t, "failed", actual.Actual[0].State)
		})
	}
}

type failingAdminStore struct {
	store.Store
	listError, readError, writeError error
}

func (s *failingAdminStore) ListQueryIndexes() ([]store.QueryIndexRef, error) {
	if s.listError != nil {
		return nil, s.listError
	}
	return s.Store.ListQueryIndexes()
}
func (s *failingAdminStore) ReadGeneration(db, collection, fingerprint string) (store.Generation, bool, error) {
	if s.readError != nil {
		return store.Generation{}, false, s.readError
	}
	return s.Store.ReadGeneration(db, collection, fingerprint)
}
func (s *failingAdminStore) SetFailure(ref store.QueryIndexRef, failure string) error {
	if s.writeError != nil {
		return s.writeError
	}
	return s.Store.SetFailure(ref, failure)
}

func TestAdminV2PropagatesStorageFailures(t *testing.T) {
	for _, operation := range []string{"list", "read", "write"} {
		t.Run(operation, func(t *testing.T) {
			st := mem_store.New()
			defer st.Close()
			ref := store.QueryIndexRef{Database: "db", Collection: "items/alice", TemplateFingerprint: "fingerprint", Generation: "active"}
			require.NoError(t, st.PublishGeneration(ref, ""))
			wrapped := &failingAdminStore{Store: st}
			failure := errors.New("admin storage failure")
			switch operation {
			case "list":
				wrapped.listError = failure
			case "read":
				wrapped.readError = failure
			case "write":
				wrapped.writeError = failure
			}
			server := NewServer(&mockLocalService{mgr: manager.New(wrapped)})
			response, err := server.InvalidateIndex(context.Background(), &indexerv1.InvalidateIndexRequest{Database: "db"})
			require.Error(t, err)
			assert.Nil(t, response)
			assert.Equal(t, codes.Internal, status.Code(err))
			assert.Contains(t, err.Error(), failure.Error())
			if operation != "write" {
				response, err := server.GetState(context.Background(), &indexerv1.GetStateRequest{})
				require.Error(t, err)
				assert.Nil(t, response)
				assert.Equal(t, codes.Internal, status.Code(err))
			}
		})
	}
}
