package authn

import (
	"github.com/codetreker/syntrix/internal/core/identity/config"
	"os"
	"sync"
	"testing"
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

func newTestAccountService(cfg config.AuthNConfig, users UserStore, revocations TokenRevocationStore) (*accountService, error) {
	accounts, _, _, err := NewServices(cfg, users, revocations)
	if err != nil {
		return nil, err
	}
	return accounts.(*accountService), nil
}
