package runtime

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type accountStub struct {
	identity.AccountService
	called int
	req    identity.SignupRequest
	err    error
}

func (s *accountStub) SignUp(_ context.Context, req identity.SignupRequest) (*identity.TokenPair, error) {
	s.called++
	s.req = req
	return nil, s.err
}

func TestModuleAdminBootstrapGates(t *testing.T) {
	failure := errors.New("storage failed")
	for _, tc := range []struct {
		name    string
		admin   config.AdminConfig
		err     error
		called  int
		wantErr error
	}{
		{name: "no username", admin: config.AdminConfig{Password: "password"}},
		{name: "no password", admin: config.AdminConfig{Username: "admin"}},
		{name: "create", admin: config.AdminConfig{Username: "admin", Password: "password"}, called: 1},
		{name: "existing", admin: config.AdminConfig{Username: "admin", Password: "password"}, err: identity.ErrUserExists, called: 1},
		{name: "same duplicate text", admin: config.AdminConfig{Username: "admin", Password: "password"}, err: errors.New("user already exists"), called: 1},
		{name: "wrapped duplicate preserves error", admin: config.AdminConfig{Username: "admin", Password: "password"}, err: errors.Join(failure, identity.ErrUserExists), called: 1, wantErr: failure},
		{name: "failure", admin: config.AdminConfig{Username: "admin", Password: "password"}, err: failure, called: 1, wantErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accounts := &accountStub{err: tc.err}
			module := &Module{accounts: accounts, admin: tc.admin}
			err := module.EnsureAdmin(context.Background())
			assert.Equal(t, tc.called, accounts.called)
			if tc.called != 0 {
				assert.Equal(t, identity.SignupRequest{Username: tc.admin.Username, Password: tc.admin.Password}, accounts.req)
			}
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

type userMemory struct {
	repository.UserStore
	users     map[string]*repository.UserRecord
	lookupErr error
	createErr error
	creates   int
}

func (s *userMemory) GetUserByUsername(_ context.Context, name string) (*repository.UserRecord, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	if user, ok := s.users[name]; ok {
		return user, nil
	}
	return nil, identity.ErrUserNotFound
}
func (s *userMemory) CreateUser(_ context.Context, user *repository.UserRecord) error {
	s.creates++
	if s.createErr != nil {
		return s.createErr
	}
	s.users[user.Username] = user
	return nil
}

func TestModuleCompositionPreservesAdminRoleAndExistingAccount(t *testing.T) {
	for _, name := range []string{"designated", "different"} {
		t.Run(name, func(t *testing.T) {
			cfg := config.DefaultConfig()
			cfg.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "key.pem")
			cfg.AuthN.AdminUsername = "designated"
			cfg.Admin = config.AdminConfig{Username: name, Password: "ValidPassword123!"}
			users := &userMemory{users: map[string]*repository.UserRecord{}}
			module, err := compose(cfg, users, nil)
			require.NoError(t, err)
			require.NoError(t, module.EnsureAdmin(context.Background()))
			user := users.users[name]
			require.NotNil(t, user)
			if name == "designated" {
				assert.Equal(t, []string{"admin"}, user.Roles)
			} else {
				assert.Equal(t, []string{"user"}, user.Roles)
			}
			assert.NotEmpty(t, user.ID)
			assert.NotEmpty(t, user.PasswordHash)
			before := *user
			require.NoError(t, module.EnsureAdmin(context.Background()))
			assert.Equal(t, before, *user)
			assert.Equal(t, 1, users.creates)
			assert.NotNil(t, module.Accounts())
			assert.NotNil(t, module.Verifier())
			assert.NotNil(t, module.SystemTokenIssuer())
			id, username, err := module.ResolveOwner(context.Background(), name)
			require.NoError(t, err)
			assert.Equal(t, user.ID, id)
			assert.Equal(t, name, username)
		})
	}
}

func TestModuleOwnerResolverPreservesErrors(t *testing.T) {
	for _, cause := range []error{identity.ErrUserNotFound, errors.New("lookup failed")} {
		module := &Module{users: &userMemory{lookupErr: cause}}
		id, name, err := module.ResolveOwner(context.Background(), "admin")
		assert.Empty(t, id)
		assert.Empty(t, name)
		assert.Same(t, cause, err)
	}
}

func TestModuleCompositionFailurePublishesNothing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AuthN.PrivateKeyFile = t.TempDir()
	module, err := compose(cfg, nil, nil)
	assert.Nil(t, module)
	assert.Error(t, err)
}

func TestModuleConcurrentUserCreateErrorPreserved(t *testing.T) {
	rawUnique := errors.New("duplicate key value violates unique constraint")
	cfg := config.DefaultConfig()
	cfg.AuthN.PrivateKeyFile = filepath.Join(t.TempDir(), "key.pem")
	cfg.Admin = config.AdminConfig{Username: "admin", Password: "ValidPassword123!"}
	cfg.AuthN.AccessTokenTTL = time.Hour
	users := &userMemory{users: map[string]*repository.UserRecord{}, createErr: rawUnique}
	module, err := compose(cfg, users, nil)
	require.NoError(t, err)
	assert.ErrorIs(t, module.EnsureAdmin(context.Background()), rawUnique)
}
