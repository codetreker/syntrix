package authn

import (
	"os"
	"sync"
	"testing"

	"github.com/codetreker/syntrix/internal/identity/config"
	"github.com/codetreker/syntrix/internal/identity/repository"
)

var (
	sharedKeyPath string
	sharedKeyOnce sync.Once
)

func getTestKeyPath(t *testing.T) string {
	sharedKeyOnce.Do(func() {
		f, err := os.CreateTemp("", "authn-test-key-*.pem")
		if err != nil {
			panic(err)
		}
		f.Close()
		sharedKeyPath = f.Name()

		key, err := GeneratePrivateKey()
		if err != nil {
			panic(err)
		}
		if err := SavePrivateKey(sharedKeyPath, key); err != nil {
			panic(err)
		}
	})
	return sharedKeyPath
}

func newTestAccountService(cfg config.AuthNConfig, users repository.UserStore, revocations repository.TokenRevocationStore) (*accountService, error) {
	accounts, _, _, err := NewServices(cfg, users, revocations)
	if err != nil {
		return nil, err
	}
	return accounts.(*accountService), nil
}
