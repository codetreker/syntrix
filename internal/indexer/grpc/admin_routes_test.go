package grpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"github.com/syntrixbase/syntrix/internal/indexer/store"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAdminRoutesPreserveContextFailures(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cause error
		code  codes.Code
	}{{"cancellation", context.Canceled, codes.Canceled}, {"deadline", context.DeadlineExceeded, codes.DeadlineExceeded}} {
		t.Run(tc.name, func(t *testing.T) {
			st := mem_store.New()
			t.Cleanup(func() { require.NoError(t, st.Close()) })
			wrapped := &failingAdminStore{Store: st, listError: fmt.Errorf("index inventory: %w", tc.cause)}
			server := NewServer(&mockLocalService{mgr: manager.New(wrapped)})
			state, err := server.GetState(context.Background(), &indexerv1.GetStateRequest{})
			require.Error(t, err)
			assert.Nil(t, state)
			assert.Equal(t, tc.code, status.Code(err))
			invalidated, err := server.InvalidateIndex(context.Background(), &indexerv1.InvalidateIndexRequest{Database: "db"})
			require.Error(t, err)
			assert.Nil(t, invalidated)
			assert.Equal(t, tc.code, status.Code(err))
		})
	}
}

func TestAdminRoutesUseDatabaseTemplateScope(t *testing.T) {
	st := mem_store.New()
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	mgr := manager.New(st)
	dir := t.TempDir()
	for database, field := range map[string]string{"alpha": "score", "beta": "rank"} {
		data := fmt.Sprintf("database: %s\ntemplates:\n- name: %s_index\n  collectionPattern: items/{owner}\n  fields: [{field: %s, order: asc}]\n", database, field, field)
		require.NoError(t, os.WriteFile(filepath.Join(dir, database+".yml"), []byte(data), 0600))
	}
	require.NoError(t, mgr.LoadTemplatesFromDir(dir))
	alpha := mgr.TemplatesForDatabase("alpha")[0]
	beta := mgr.TemplatesForDatabase("beta")[0]
	alphaRef := store.QueryIndexRef{Database: "alpha", Collection: "items/alice", TemplateFingerprint: alpha.Fingerprint(), Generation: "active"}
	betaRef := store.QueryIndexRef{Database: "beta", Collection: "items/alice", TemplateFingerprint: beta.Fingerprint(), Generation: "active"}
	require.NoError(t, st.PublishGeneration(alphaRef, ""))
	require.NoError(t, st.PublishGeneration(betaRef, ""))
	server := NewServer(&mockLocalService{mgr: mgr})
	state, err := server.GetState(context.Background(), &indexerv1.GetStateRequest{Database: "alpha", Pattern: "items/alice"})
	require.NoError(t, err)
	require.Len(t, state.Desired, 1)
	assert.Equal(t, "score_index", state.Desired[0].TemplateId)
	require.Len(t, state.Desired[0].Fields, 1)
	assert.Equal(t, "score", state.Desired[0].Fields[0].Field)
	require.Len(t, state.Actual, 1)
	assert.Equal(t, "score_index", state.Actual[0].TemplateId)
	unknown, err := server.GetState(context.Background(), &indexerv1.GetStateRequest{Database: "unknown"})
	require.NoError(t, err)
	assert.Empty(t, unknown.Desired)
	assert.Empty(t, unknown.Actual)
	wrongTemplate, err := server.InvalidateIndex(context.Background(), &indexerv1.InvalidateIndexRequest{Database: "alpha", Pattern: "items/*", TemplateId: "rank_index"})
	require.NoError(t, err)
	assert.Zero(t, wrongTemplate.IndexesInvalidated)
	invalidated, err := server.InvalidateIndex(context.Background(), &indexerv1.InvalidateIndexRequest{Database: "alpha", Pattern: "items/*", TemplateId: "score_index"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, invalidated.IndexesInvalidated)
	generation, found, err := st.ReadGeneration(betaRef.Database, betaRef.Collection, betaRef.TemplateFingerprint)
	require.NoError(t, err)
	require.True(t, found)
	assert.True(t, generation.Ready)
}
