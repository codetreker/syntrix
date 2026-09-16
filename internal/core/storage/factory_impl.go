package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	_ "github.com/lib/pq"
	"github.com/syntrixbase/syntrix/internal/core/database"
	dbpostgres "github.com/syntrixbase/syntrix/internal/core/database/postgres"
	"github.com/syntrixbase/syntrix/internal/core/storage/config"
	"github.com/syntrixbase/syntrix/internal/core/storage/mongo"
	"github.com/syntrixbase/syntrix/internal/core/storage/postgres"
	"github.com/syntrixbase/syntrix/internal/core/storage/router"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
)

// mongoProvider interface to allow mocking
type mongoProvider interface {
	Client() *mongodriver.Client
	DatabaseName() string
}

// Dependency injection for testing
var newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
	return mongo.NewProvider(ctx, uri, dbName)
}

// Dependency injection for postgres
var newPostgresDB = func(cfg config.PostgresConfig) (*sql.DB, error) {
	db, err := sql.Open("postgres", cfg.DSN)
	if err != nil {
		return nil, err
	}
	if cfg.MaxOpenConns > 0 {
		db.SetMaxOpenConns(cfg.MaxOpenConns)
	}
	if cfg.MaxIdleConns > 0 {
		db.SetMaxIdleConns(cfg.MaxIdleConns)
	}
	if cfg.ConnMaxLifetime > 0 {
		db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	}
	return db, nil
}

type factory struct {
	providers  map[string]Provider
	postgresDB *sql.DB
	docStore   types.DocumentStore
	usrStore   types.UserStore
	revStore   types.TokenRevocationStore
	dbStore    database.DatabaseStore
	mu         sync.Mutex
}

func NewFactory(ctx context.Context, cfg config.Config) (StorageFactory, error) {
	f := &factory{
		providers: make(map[string]Provider),
	}
	success := false
	defer func() {
		if !success {
			f.Close()
		}
	}()

	// 1. Initialize Providers
	for name, backendCfg := range cfg.Backends {
		switch backendCfg.Type {
		case "mongo":
			p, err := newMongoProvider(ctx, backendCfg.Mongo.URI, backendCfg.Mongo.DatabaseName)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize backend %s: %w", name, err)
			}
			f.providers[name] = p
		case "postgres":
			// Postgres is initialized lazily when needed for user store
			// We just validate the config here
			if backendCfg.Postgres.DSN == "" {
				return nil, fmt.Errorf("postgres backend %s: DSN is required", name)
			}
		default:
			return nil, fmt.Errorf("unsupported backend type: %s", backendCfg.Type)
		}
	}

	// 2. Initialize Document Store
	defaultDocRouter, err := f.createDocumentRouter(cfg.Topology.Document)
	if err != nil {
		return nil, err
	}

	databaseDocRouters := make(map[string]types.DocumentRouter)
	for tID, tCfg := range cfg.Databases {
		if tID == model.DefaultDatabase {
			continue
		}
		p, err := f.getMongoProvider(tCfg.Backend)
		if err != nil {
			return nil, err
		}
		store := mongo.NewDocumentStore(p.Client(), p.Client().Database(p.DatabaseName()), cfg.Topology.Document.DataCollection, cfg.Topology.Document.SysCollection, cfg.Topology.Document.SoftDeleteRetention)
		databaseDocRouters[tID] = router.NewSingleDocumentRouter(store)
	}
	f.docStore = router.NewRoutedDocumentStore(router.NewDatabaseDocumentRouter(defaultDocRouter, databaseDocRouters))

	// 3. Initialize User Store
	userStore, err := f.createUserStore(ctx, cfg)
	if err != nil {
		return nil, err
	}
	f.usrStore = userStore

	// 4. Initialize Revocation Store
	defaultRevRouter, err := f.createRevocationRouter(cfg.Topology.Revocation)
	if err != nil {
		return nil, err
	}

	databaseRevRouters := make(map[string]types.RevocationRouter)
	for tID, tCfg := range cfg.Databases {
		if tID == model.DefaultDatabase {
			continue
		}
		p, err := f.getMongoProvider(tCfg.Backend)
		if err != nil {
			return nil, err
		}
		store := mongo.NewRevocationStore(p.Client().Database(p.DatabaseName()), cfg.Topology.Revocation.Collection)
		databaseRevRouters[tID] = router.NewSingleRevocationRouter(store)
	}
	f.revStore = router.NewRoutedRevocationStore(router.NewDatabaseRevocationRouter(defaultRevRouter, databaseRevRouters))

	// 5. Initialize Database Store (uses same postgres as user store)
	if f.postgresDB != nil {
		// Ensure databases table exists
		if err := dbpostgres.EnsureSchema(ctx, f.postgresDB); err != nil {
			return nil, fmt.Errorf("failed to ensure databases schema: %w", err)
		}
		f.dbStore = dbpostgres.NewStore(f.postgresDB, "databases")
	}

	success = true

	return f, nil
}

func (f *factory) createDocumentRouter(cfg config.DocumentTopology) (types.DocumentRouter, error) {
	primary, err := f.getMongoProvider(cfg.Primary)
	if err != nil {
		return nil, err
	}

	primaryStore := mongo.NewDocumentStore(primary.Client(), primary.Client().Database(primary.DatabaseName()), cfg.DataCollection, cfg.SysCollection, cfg.SoftDeleteRetention)

	switch cfg.Strategy {
	case "single":
		return router.NewSingleDocumentRouter(primaryStore), nil
	case "read_write_split":
		replica, err := f.getMongoProvider(cfg.Replica)
		if err != nil {
			return nil, err
		}
		replicaStore := mongo.NewDocumentStore(replica.Client(), replica.Client().Database(replica.DatabaseName()), cfg.DataCollection, cfg.SysCollection, cfg.SoftDeleteRetention)
		return router.NewSplitDocumentRouter(primaryStore, replicaStore), nil
	}

	return nil, fmt.Errorf("unsupported strategy: %s", cfg.Strategy)
}

func (f *factory) createUserStore(ctx context.Context, cfg config.Config) (types.UserStore, error) {
	backendName := cfg.Topology.User.Primary
	backendCfg, ok := cfg.Backends[backendName]
	if !ok {
		return nil, fmt.Errorf("backend not found: %s", backendName)
	}

	switch backendCfg.Type {
	case "postgres":
		db, err := newPostgresDB(backendCfg.Postgres)
		if err != nil {
			return nil, fmt.Errorf("failed to connect to postgres: %w", err)
		}
		if err := db.PingContext(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to ping postgres: %w", err)
		}
		// Ensure auth_users table exists
		if err := postgres.EnsureSchema(ctx, db); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to ensure postgres schema: %w", err)
		}
		f.postgresDB = db
		return postgres.NewUserStore(db, cfg.Topology.User.Collection), nil
	default:
		return nil, fmt.Errorf("unsupported backend type for user store: %s (only postgres is supported)", backendCfg.Type)
	}
}

func (f *factory) createRevocationRouter(cfg config.CollectionTopology) (types.RevocationRouter, error) {
	primary, err := f.getMongoProvider(cfg.Primary)
	if err != nil {
		return nil, err
	}

	primaryStore := mongo.NewRevocationStore(primary.Client().Database(primary.DatabaseName()), cfg.Collection)

	switch cfg.Strategy {
	case "single":
		return router.NewSingleRevocationRouter(primaryStore), nil
	case "read_write_split":
		replica, err := f.getMongoProvider(cfg.Replica)
		if err != nil {
			return nil, err
		}
		replicaStore := mongo.NewRevocationStore(replica.Client().Database(replica.DatabaseName()), cfg.Collection)
		return router.NewSplitRevocationRouter(primaryStore, replicaStore), nil
	}

	return nil, fmt.Errorf("unsupported strategy: %s", cfg.Strategy)
}

func (f *factory) getMongoProvider(name string) (mongoProvider, error) {
	p, ok := f.providers[name]
	if !ok {
		return nil, fmt.Errorf("backend not found: %s", name)
	}
	mp, ok := p.(mongoProvider)
	if !ok {
		return nil, fmt.Errorf("backend %s is not a mongo provider", name)
	}
	return mp, nil
}

func (f *factory) GetMongoClient(name string) (*mongodriver.Client, string, error) {
	p, err := f.getMongoProvider(name)
	if err != nil {
		return nil, "", err
	}
	return p.Client(), p.DatabaseName(), nil
}

func (f *factory) Document() types.DocumentStore {
	return f.docStore
}

func (f *factory) User() types.UserStore {
	return f.usrStore
}

func (f *factory) Revocation() types.TokenRevocationStore {
	return f.revStore
}

func (f *factory) Database() database.DatabaseStore {
	return f.dbStore
}

func (f *factory) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var errs []error
	for _, p := range f.providers {
		if err := p.Close(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	if f.postgresDB != nil {
		if err := f.postgresDB.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing providers: %v", errs)
	}
	return nil
}
