package authn

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

func init() {
	// Reduce Argon2id cost for testing
	argonTime = 1
	argonMemory = 1 * 1024
	argonThreads = 1
}

func TestHashPassword(t *testing.T) {
	t.Parallel()
	password := "mysecretpassword"
	hash, algo, err := HashPassword(password)
	require.NoError(t, err)
	assert.Equal(t, AlgoArgon2id, algo)
	assert.NotEmpty(t, hash)
	assert.Contains(t, hash, "$argon2id$")
}

func TestVerifyPassword_Argon2id(t *testing.T) {
	t.Parallel()
	password := "mysecretpassword"
	hash, algo, err := HashPassword(password)
	require.NoError(t, err)

	// Correct password
	valid, err := VerifyPassword(password, hash, algo)
	require.NoError(t, err)
	assert.True(t, valid)

	// Wrong password
	valid, err = VerifyPassword("wrongpassword", hash, algo)
	require.NoError(t, err)
	assert.False(t, valid)
}

func TestVerifyPassword_Bcrypt(t *testing.T) {
	t.Parallel()
	password := "mysecretpassword"
	// Manually create a bcrypt hash
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	require.NoError(t, err)
	hash := string(bytes)

	// Correct password
	valid, err := VerifyPassword(password, hash, AlgoBcrypt)
	require.NoError(t, err)
	assert.True(t, valid)

	// Wrong password
	valid, err = VerifyPassword("wrongpassword", hash, AlgoBcrypt)
	require.NoError(t, err)
	assert.False(t, valid)
}

func TestVerifyPassword_UnknownAlgo(t *testing.T) {
	t.Parallel()
	valid, err := VerifyPassword("pass", "hash", "unknown")
	assert.Error(t, err)
	assert.False(t, valid)
}
