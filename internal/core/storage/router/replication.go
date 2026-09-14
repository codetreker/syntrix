package router

import (
	"context"
	"fmt"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
)

var _ types.ReplicationSource = (*RoutedDocumentStore)(nil)

func (s *RoutedDocumentStore) replicationSource(database, collection string) (types.ReplicationSource, error) {
	store, err := s.router.Select(database, types.OpWatch)
	if err != nil {
		return nil, err
	}
	source, ok := store.(types.ReplicationSource)
	if !ok {
		return nil, &types.ReplicationError{Code: types.ReplicationUnsupported, Database: database, Collection: collection, Cause: fmt.Errorf("selected authoritative store does not support replication")}
	}
	return source, nil
}

func (s *RoutedDocumentStore) BeginBootstrap(ctx context.Context, database, collection string, budget types.ReplicationBudget) (types.ReplicationPosition, error) {
	budget, err := resolveReplicationRequest(ctx, database, collection, budget)
	if err != nil {
		return types.ReplicationPosition{}, err
	}
	source, err := s.replicationSource(database, collection)
	if err != nil {
		return types.ReplicationPosition{}, err
	}
	return source.BeginBootstrap(ctx, database, collection, budget)
}

func (s *RoutedDocumentStore) ReadBootstrapPage(ctx context.Context, database, collection string, after types.ReplicationPosition, budget types.ReplicationBudget) (types.ReplicationPage, error) {
	return s.replicationPage(ctx, database, collection, after, budget, types.ReplicationScan)
}

func (s *RoutedDocumentStore) ReadChangesPage(ctx context.Context, database, collection string, after types.ReplicationPosition, budget types.ReplicationBudget) (types.ReplicationPage, error) {
	return s.replicationPage(ctx, database, collection, after, budget, types.ReplicationChanges)
}

func (s *RoutedDocumentStore) replicationPage(ctx context.Context, database, collection string, after types.ReplicationPosition, budget types.ReplicationBudget, phase types.ReplicationPhase) (types.ReplicationPage, error) {
	budget, err := resolveReplicationRequest(ctx, database, collection, budget)
	if err != nil {
		return types.ReplicationPage{}, err
	}
	if err := after.Validate(phase); err != nil {
		return types.ReplicationPage{}, err
	}
	source, err := s.replicationSource(database, collection)
	if err != nil {
		return types.ReplicationPage{}, err
	}
	if phase == types.ReplicationScan {
		return source.ReadBootstrapPage(ctx, database, collection, after, budget)
	}
	return source.ReadChangesPage(ctx, database, collection, after, budget)
}

func resolveReplicationRequest(ctx context.Context, database, collection string, budget types.ReplicationBudget) (types.ReplicationBudget, error) {
	if err := ctx.Err(); err != nil {
		return types.ReplicationBudget{}, err
	}
	if err := types.ValidateReplicationScope(database, collection); err != nil {
		return types.ReplicationBudget{}, err
	}
	return types.ResolveReplicationBudget(budget)
}
