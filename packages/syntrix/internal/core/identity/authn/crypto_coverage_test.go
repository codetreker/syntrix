package authn

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

func TestVerifyArgon2id_Coverage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		password    string
		hash        string
		algo        string
		expected    bool
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Unsupported Algorithm",
			password:    "password",
			hash:        "somehash",
			algo:        "unknown",
			expected:    false,
			expectError: true,
			errorMsg:    "unsupported password algorithm",
		},
		{
			name:        "Invalid Format - Wrong number of parts",
			password:    "password",
			hash:        fmt.Sprintf("$argon2id$v=%d$m=65536,t=3,p=1$salt", argon2.Version), // Missing hash part
			algo:        AlgoArgon2id,
			expected:    false,
			expectError: true,
			errorMsg:    "invalid argon2id hash format",
		},
		{
			name:        "Invalid Format - Invalid version string",
			password:    "password",
			hash:        "$argon2id$v=xx$m=65536,t=3,p=1$salt$hash",
			algo:        AlgoArgon2id,
			expected:    false,
			expectError: true,
			// fmt.Sscanf error message might vary slightly but usually indicates input does not match format
		},
		{
			name:        "Incompatible Version",
			password:    "password",
			hash:        "$argon2id$v=99$m=65536,t=3,p=1$salt$hash",
			algo:        AlgoArgon2id,
			expected:    false,
			expectError: true,
			errorMsg:    "incompatible argon2 version",
		},
		{
			name:        "Invalid Format - Invalid params string",
			password:    "password",
			hash:        fmt.Sprintf("$argon2id$v=%d$m=xx,t=3,p=1$salt$hash", argon2.Version),
			algo:        AlgoArgon2id,
			expected:    false,
			expectError: true,
			// fmt.Sscanf error
		},
		{
			name:        "Invalid Format - Invalid salt base64",
			password:    "password",
			hash:        fmt.Sprintf("$argon2id$v=%d$m=65536,t=3,p=1$invalid-base64!$hash", argon2.Version),
			algo:        AlgoArgon2id,
			expected:    false,
			expectError: true,
			// base64 decode error
		},
		{
			name:        "Invalid Format - Invalid hash base64",
			password:    "password",
			hash:        fmt.Sprintf("$argon2id$v=%d$m=65536,t=3,p=1$c2FsdA$invalid-base64!", argon2.Version), // c2FsdA is "salt" in base64 (raw)
			algo:        AlgoArgon2id,
			expected:    false,
			expectError: true,
			// base64 decode error
		},
		{
			name:     "Valid Hash but Wrong Password",
			password: "wrongpassword",
			// Construct a valid hash manually or use a known one.
			// Let's use a known valid hash for "password"
			// $argon2id$v=19$m=65536,t=3,p=1$c2FsdA$hash...
			// Actually, easier to generate one first in a setup or just rely on the fact that we are testing error paths mostly.
			// But let's try to mock a "valid structure" that fails verification if we can easily calculate it,
			// or just rely on the fact that if we pass valid base64 but it doesn't match, it returns false, nil.
			// Let's use a dummy valid base64 for salt and hash.
			hash:        fmt.Sprintf("$argon2id$v=%d$m=65536,t=1,p=1$c2FsdA$aGFzaA", argon2.Version), // salt="salt", hash="hash"
			algo:        AlgoArgon2id,
			expected:    false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			valid, err := VerifyPassword(tt.password, tt.hash, tt.algo)
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, valid)
			}
		})
	}
}

func TestVerifyBcrypt_InvalidHash(t *testing.T) {
	t.Parallel()
	// Pass a string that is not a valid bcrypt hash
	valid, err := VerifyPassword("password", "invalid-bcrypt-hash", AlgoBcrypt)
	assert.False(t, valid)
	assert.Error(t, err)
	assert.NotEqual(t, bcrypt.ErrMismatchedHashAndPassword, err)
}
