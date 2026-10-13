package runtime

import (
	"context"

	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/identity/repository/mongo"
	"github.com/codetreker/syntrix/internal/identity/repository/postgres"
)

func InitializationPlacements(cfg storageconfig.Config, backends *storage.Backends) ([]storage.MongoPlacement, error) {
	topology := cfg.Topology.Revocation
	names, err := storage.RoutedMongoNames(cfg, topology.Primary, topology.Replica, topology.Strategy)
	if err != nil {
		return nil, err
	}
	return storage.MongoPlacements(cfg, backends, "revocations", topology.Collection, names, nil, mongo.RevocationIndexDefinitions())
}

func Prepare(ctx context.Context, cfg storageconfig.Config, backends *storage.Backends) error {
	return PrepareIn(ctx, cfg, backends, "")
}

func PrepareIn(ctx context.Context, cfg storageconfig.Config, backends *storage.Backends, namespace string) error {
	_, err := InitializationPlacements(cfg, backends)
	if err != nil {
		return err
	}
	if err := postgres.EnsureSchemaIn(ctx, backends.PostgresDB(), namespace); err != nil {
		return err
	}
	return nil
}
