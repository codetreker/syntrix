package authn

import (
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
