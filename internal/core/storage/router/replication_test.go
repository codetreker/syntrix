package router

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
)

type replicationTestStore struct {
	types.DocumentStore
	calls   []string
	budgets []types.ReplicationBudget
	err     error
}

func (s *replicationTestStore) BeginBootstrap(_ context.Context, database, collection string, budget types.ReplicationBudget) (types.ReplicationPosition, error) {
	s.calls = append(s.calls, "begin:"+database+":"+collection)
	s.budgets = append(s.budgets, budget)
	return types.ReplicationPosition{Phase: types.ReplicationScan, Opaque: "primary"}, s.err
}

func (s *replicationTestStore) ReadBootstrapPage(_ context.Context, database, collection string, after types.ReplicationPosition, budget types.ReplicationBudget) (types.ReplicationPage, error) {
	s.calls = append(s.calls, "scan:"+database+":"+collection+":"+after.Opaque)
	s.budgets = append(s.budgets, budget)
	return types.ReplicationPage{End: types.ReplicationPosition{Phase: types.ReplicationChanges, Opaque: "primary-changes"}, EndReason: types.ReplicationEndScan}, s.err
}

func (s *replicationTestStore) ReadChangesPage(_ context.Context, database, collection string, after types.ReplicationPosition, budget types.ReplicationBudget) (types.ReplicationPage, error) {
	s.calls = append(s.calls, "changes:"+database+":"+collection+":"+after.Opaque)
	s.budgets = append(s.budgets, budget)
	return types.ReplicationPage{End: after, CaughtUp: true, EndReason: types.ReplicationEndWatermark}, s.err
}

func TestReplicationRoutingUsesPrimaryAcrossInstances(t *testing.T) {
	primary, replica := &replicationTestStore{}, &replicationTestStore{}
	router := NewSplitDocumentRouter(primary, replica)
	first := NewRoutedDocumentStore(router).(types.ReplicationSource)
	second := NewRoutedDocumentStore(router).(types.ReplicationSource)
	budget, err := types.ResolveReplicationBudget(types.ReplicationBudget{Limit: 7})
	require.NoError(t, err)
	start, err := first.BeginBootstrap(context.Background(), "db", "users", budget)
	require.NoError(t, err)
	scan, err := second.ReadBootstrapPage(context.Background(), "db", "users", start, budget)
	require.NoError(t, err)
	page, err := first.ReadChangesPage(context.Background(), "db", "users", scan.End, budget)
	require.NoError(t, err)
	require.True(t, page.CaughtUp)
	require.Equal(t, []string{"begin:db:users", "scan:db:users:primary", "changes:db:users:primary-changes"}, primary.calls)
	require.Empty(t, replica.calls)
	for _, got := range primary.budgets {
		require.Equal(t, budget, got)
	}
}

func TestReplicationRoutingRejectsInvalidRequestsBeforeSelection(t *testing.T) {
	noSelection := &mockDocRouter{}
	source := NewRoutedDocumentStore(noSelection).(types.ReplicationSource)
	_, err := source.BeginBootstrap(context.Background(), "", "users", types.ReplicationBudget{})
	require.Error(t, err)
	_, err = source.BeginBootstrap(context.Background(), "db", "users", types.ReplicationBudget{Limit: -1})
	require.Error(t, err)
	_, err = source.ReadBootstrapPage(context.Background(), "db", "users", types.ReplicationPosition{Phase: types.ReplicationChanges, Opaque: "token"}, types.ReplicationBudget{})
	require.Error(t, err)
	_, err = source.ReadChangesPage(context.Background(), "db", "users", types.ReplicationPosition{}, types.ReplicationBudget{})
	require.Error(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = source.BeginBootstrap(ctx, "db", "users", types.ReplicationBudget{})
	require.ErrorIs(t, err, context.Canceled)
	noSelection.AssertExpectations(t)
}

func TestReplicationRoutingDoesNotFallbackOnUnsupportedOrFailure(t *testing.T) {
	replica := &replicationTestStore{}
	source := NewRoutedDocumentStore(NewSplitDocumentRouter(&fakeDocumentStore{}, replica)).(types.ReplicationSource)
	_, err := source.BeginBootstrap(context.Background(), "db", "users", types.ReplicationBudget{})
	var failure *types.ReplicationError
	require.ErrorAs(t, err, &failure)
	require.Equal(t, types.ReplicationUnsupported, failure.Code)
	require.Empty(t, replica.calls)

	expected := &types.ReplicationError{Code: types.ReplicationHistoryUnavailable, Cause: errors.New("history expired")}
	primary := &replicationTestStore{err: expected}
	source = NewRoutedDocumentStore(NewSplitDocumentRouter(primary, replica)).(types.ReplicationSource)
	_, err = source.ReadChangesPage(context.Background(), "db", "users", types.ReplicationPosition{Phase: types.ReplicationChanges, Opaque: "old-position"}, types.ReplicationBudget{})
	require.Same(t, expected, err)
	require.Empty(t, replica.calls)

	router := &mockDocRouter{}
	router.On("Select", "db", types.OpWatch).Return(nil, expected).Once()
	source = NewRoutedDocumentStore(router).(types.ReplicationSource)
	_, err = source.BeginBootstrap(context.Background(), "db", "users", types.ReplicationBudget{})
	require.Same(t, expected, err)
	router.AssertExpectations(t)
}
