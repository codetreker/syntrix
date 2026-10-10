package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/core/storage/mongo"
	_ "github.com/lib/pq"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
)

type mongoProvider interface {
	Client() *mongodriver.Client
	DatabaseName() string
}

var newMongoProvider = func(ctx context.Context, uri, dbName string) (Provider, error) {
	return mongo.NewProvider(ctx, uri, dbName)
}

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

// Backends owns physical connections shared by the instance's store builders.
// Consumers borrow its handles and must finish before Close is called.
type Backends struct {
	providers  map[string]Provider
	postgresDB *sql.DB
	closeOnce  sync.Once
	closeErr   error
}

// NewBackends opens configured Mongo backends and the PostgreSQL backend selected
// by the existing user topology. Failure closes allocated connections and
// preserves both initialization and cleanup errors. It performs no schema DDL.
func NewBackends(ctx context.Context, cfg config.Config) (_ *Backends, err error) {
	b := &Backends{providers: make(map[string]Provider)}
	defer func() {
		if err != nil {
			err = errors.Join(err, b.Close())
		}
	}()

	names := make([]string, 0, len(cfg.Backends))
	for name := range cfg.Backends {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		backendCfg := cfg.Backends[name]
		switch backendCfg.Type {
		case "mongo":
			p, openErr := newMongoProvider(ctx, backendCfg.Mongo.URI, backendCfg.Mongo.DatabaseName)
			if openErr != nil {
				return nil, fmt.Errorf("failed to initialize backend %s: %w", name, openErr)
			}
			b.providers[name] = p
		case "postgres":
			if backendCfg.Postgres.DSN == "" {
				return nil, fmt.Errorf("postgres backend %s: DSN is required", name)
			}
		default:
			return nil, fmt.Errorf("unsupported backend type: %s", backendCfg.Type)
		}
	}

	backendName := cfg.Topology.User.Primary
	backendCfg, ok := cfg.Backends[backendName]
	if !ok {
		return nil, fmt.Errorf("backend not found: %s", backendName)
	}
	if backendCfg.Type != "postgres" {
		return nil, fmt.Errorf("unsupported backend type for user store: %s (only postgres is supported)", backendCfg.Type)
	}
	b.postgresDB, err = newPostgresDB(backendCfg.Postgres)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	if err := b.postgresDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping postgres: %w", err)
	}
	return b, nil
}

func (b *Backends) GetMongoClient(name string) (*mongodriver.Client, string, error) {
	p, ok := b.providers[name]
	if !ok {
		return nil, "", fmt.Errorf("backend not found: %s", name)
	}
	mp, ok := p.(mongoProvider)
	if !ok {
		return nil, "", fmt.Errorf("backend %s is not a mongo provider", name)
	}
	return mp.Client(), mp.DatabaseName(), nil
}

func (b *Backends) PostgresDB() *sql.DB {
	return b.postgresDB
}

// Close releases each owned connection once and returns the same aggregated
// result to repeated callers. Borrowed stores never close these resources.
func (b *Backends) Close() error {
	b.closeOnce.Do(func() {
		var errs []error
		for name, p := range b.providers {
			if err := p.Close(context.Background()); err != nil {
				errs = append(errs, fmt.Errorf("close backend %s: %w", name, err))
			}
		}
		if b.postgresDB != nil {
			if err := b.postgresDB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close postgres: %w", err))
			}
		}
		b.closeErr = errors.Join(errs...)
	})
	return b.closeErr
}
