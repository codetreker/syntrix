package storage

import (
	"context"
	"fmt"

	"github.com/codetreker/syntrix/internal/core/database"
	dbpostgres "github.com/codetreker/syntrix/internal/core/database/postgres"
	"github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/core/storage/mongo"
	"github.com/codetreker/syntrix/internal/core/storage/router"
	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/codetreker/syntrix/pkg/model"
)

type factory struct {
	docStore types.DocumentStore
	dbStore  database.DatabaseStore
}

// NewFactory constructs document routing and initializes the catalog schema on
// borrowed connections. Failure does not close Backends; the caller owns cleanup.
func NewFactory(ctx context.Context, cfg config.Config, backends *Backends) (StorageFactory, error) {
	defaultDocRouter, err := createDocumentRouter(cfg.Topology.Document, backends)
	if err != nil {
		return nil, err
	}

	databaseDocRouters := make(map[string]types.DocumentRouter)
	for database, databaseCfg := range cfg.Databases {
		if database == model.DefaultDatabase {
			continue
		}
		client, dbName, err := backends.GetMongoClient(databaseCfg.Backend)
		if err != nil {
			return nil, err
		}
		store := mongo.NewDocumentStore(client, client.Database(dbName), cfg.Topology.Document.DataCollection, cfg.Topology.Document.SysCollection, cfg.Topology.Document.SoftDeleteRetention)
		databaseDocRouters[database] = router.NewSingleDocumentRouter(store)
	}
	docStore := router.NewRoutedDocumentStore(router.NewDatabaseDocumentRouter(defaultDocRouter, databaseDocRouters))

	db := backends.PostgresDB()
	if err := dbpostgres.EnsureSchema(ctx, db); err != nil {
		return nil, fmt.Errorf("failed to ensure databases schema: %w", err)
	}
	return &factory{
		docStore: docStore,
		dbStore:  dbpostgres.NewStore(db, "databases"),
	}, nil
}

func createDocumentRouter(cfg config.DocumentTopology, backends *Backends) (types.DocumentRouter, error) {
	client, dbName, err := backends.GetMongoClient(cfg.Primary)
	if err != nil {
		return nil, err
	}
	primaryStore := mongo.NewDocumentStore(client, client.Database(dbName), cfg.DataCollection, cfg.SysCollection, cfg.SoftDeleteRetention)

	switch cfg.Strategy {
	case "single":
		return router.NewSingleDocumentRouter(primaryStore), nil
	case "read_write_split":
		replicaClient, replicaDB, err := backends.GetMongoClient(cfg.Replica)
		if err != nil {
			return nil, err
		}
		replicaStore := mongo.NewDocumentStore(replicaClient, replicaClient.Database(replicaDB), cfg.DataCollection, cfg.SysCollection, cfg.SoftDeleteRetention)
		return router.NewSplitDocumentRouter(primaryStore, replicaStore), nil
	}
	return nil, fmt.Errorf("unsupported strategy: %s", cfg.Strategy)
}

func (f *factory) Document() types.DocumentStore {
	return f.docStore
}

func (f *factory) Database() database.DatabaseStore {
	return f.dbStore
}
