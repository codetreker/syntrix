package runtime

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/codetreker/syntrix/internal/core/storage"
	storageconfig "github.com/codetreker/syntrix/internal/core/storage/config"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/identity/authn"
	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/codetreker/syntrix/internal/identity/repository/mongo"
	"github.com/codetreker/syntrix/internal/identity/repository/postgres"
	"github.com/codetreker/syntrix/internal/identity/repository/router"
)

// Module borrows physical connections and owns instance account composition.
// Closing physical connections remains the instance backend owner's responsibility.
type Module struct {
	accounts identity.AccountService
	verifier *identity.Verifier
	issuer   identity.SystemTokenIssuer
	users    repository.UserStore
	admin    config.AdminConfig
}

// NewModule initializes Identity repositories and token capabilities without
// constructing document or catalog stores. Failure publishes no module.
func NewModule(ctx context.Context, cfg config.Config, persistence storageconfig.Config, backends *storage.Backends) (*Module, error) {
	if err := postgres.EnsureSchema(ctx, backends.PostgresDB()); err != nil {
		return nil, fmt.Errorf("failed to ensure postgres schema: %w", err)
	}
	users := postgres.NewUserStore(backends.PostgresDB(), persistence.Topology.User.Collection)
	defaultRouter, err := createRevocationRouter(backends, persistence.Topology.Revocation)
	if err != nil {
		return nil, err
	}
	databaseRouters := make(map[string]repository.RevocationRouter)
	for name, database := range persistence.Databases {
		if name == "default" {
			continue
		}
		client, dbName, err := backends.GetMongoClient(database.Backend)
		if err != nil {
			return nil, err
		}
		store := mongo.NewRevocationStore(client.Database(dbName), persistence.Topology.Revocation.Collection)
		databaseRouters[name] = router.NewSingleRevocationRouter(store)
	}
	revocations := router.NewRoutedRevocationStore(router.NewDatabaseRevocationRouter(defaultRouter, databaseRouters))
	return compose(cfg, users, revocations)
}

func compose(cfg config.Config, users repository.UserStore, revocations repository.TokenRevocationStore) (*Module, error) {
	accounts, verifier, issuer, err := authn.NewServices(cfg.AuthN, users, revocations)
	if err != nil {
		return nil, err
	}
	return &Module{accounts: accounts, verifier: verifier, issuer: issuer, users: users, admin: cfg.Admin}, nil
}

func createRevocationRouter(backends *storage.Backends, cfg config.CollectionTopology) (repository.RevocationRouter, error) {
	client, dbName, err := backends.GetMongoClient(cfg.Primary)
	if err != nil {
		return nil, err
	}
	primary := mongo.NewRevocationStore(client.Database(dbName), cfg.Collection)
	switch cfg.Strategy {
	case "single":
		return router.NewSingleRevocationRouter(primary), nil
	case "read_write_split":
		client, dbName, err := backends.GetMongoClient(cfg.Replica)
		if err != nil {
			return nil, err
		}
		return router.NewSplitRevocationRouter(primary, mongo.NewRevocationStore(client.Database(dbName), cfg.Collection)), nil
	}
	return nil, fmt.Errorf("unsupported strategy: %s", cfg.Strategy)
}

func (m *Module) Accounts() identity.AccountService             { return m.accounts }
func (m *Module) Verifier() *identity.Verifier                  { return m.verifier }
func (m *Module) SystemTokenIssuer() identity.SystemTokenIssuer { return m.issuer }

// EnsureAdmin preserves signup's configured-role rules and existing duplicate
// handling; an existing account is never promoted or reset by bootstrap.
func (m *Module) EnsureAdmin(ctx context.Context) error {
	if m.admin.Username == "" {
		slog.Debug("Admin user initialization skipped: no username configured")
		return nil
	}
	if m.admin.Password == "" {
		slog.Warn("Admin user initialization skipped: no password configured", "username", m.admin.Username)
		return nil
	}
	_, err := m.accounts.SignUp(ctx, identity.SignupRequest{Username: m.admin.Username, Password: m.admin.Password})
	if err != nil {
		if err.Error() == "user already exists" {
			slog.Debug("Admin user already exists", "username", m.admin.Username)
			return nil
		}
		return fmt.Errorf("failed to create admin user: %w", err)
	}
	slog.Info("Created admin user", "username", m.admin.Username)
	return nil
}

// ResolveOwner exposes only the existing account identity needed by catalog
// bootstrap. The catalog invokes it after checking whether the database exists.
func (m *Module) ResolveOwner(ctx context.Context, username string) (string, string, error) {
	user, err := m.users.GetUserByUsername(ctx, username)
	if err != nil {
		return "", "", err
	}
	return user.ID, user.Username, nil
}
