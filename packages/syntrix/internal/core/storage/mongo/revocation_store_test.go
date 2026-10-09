package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRevocationStore(t *testing.T) (types.TokenRevocationStore, func()) {
	env := setupTestEnv(t)

	store := NewRevocationStore(env.DB, "")
	err := store.EnsureIndexes(context.Background())
	require.NoError(t, err)

	return store, func() {
		// Cleanup handled by setupTestEnv
	}
}

func TestRevocationStore_Revocation(t *testing.T) {
	s, teardown := setupTestRevocationStore(t)
	defer teardown()

	ctx := context.Background()
	jti := "token-123"
	expiresAt := time.Now().Add(1 * time.Hour)

	// 1. Check not revoked initially
	revoked, err := s.IsRevoked(ctx, jti, 0)
	require.NoError(t, err)
	assert.False(t, revoked)

	// 2. Revoke Token
	err = s.RevokeToken(ctx, jti, expiresAt)
	require.NoError(t, err)

	// 3. Check immediate revocation (grace period 0) -> Should be revoked
	revoked, err = s.IsRevoked(ctx, jti, 0)
	require.NoError(t, err)
	assert.True(t, revoked)

	// 4. Check with grace period -> Should NOT be revoked yet (within grace period)
	revoked, err = s.IsRevoked(ctx, jti, 1*time.Minute)
	require.NoError(t, err)
	assert.False(t, revoked)

	// 5. Revoke Duplicate (should not error)
	err = s.RevokeToken(ctx, jti, expiresAt)
	require.NoError(t, err)

	// 6. Revoke Immediate (Force Logout)
	jti2 := "token-456"
	err = s.RevokeTokenImmediate(ctx, jti2, expiresAt)
	require.NoError(t, err)

	// 7. Check Immediate with grace period -> Should be revoked (bypassed grace period)
	revoked, err = s.IsRevoked(ctx, jti2, 1*time.Minute)
	require.NoError(t, err)
	assert.True(t, revoked)
}

func TestRevocationStore_RevokeTokenImmediate_Duplicate(t *testing.T) {
	s, teardown := setupTestRevocationStore(t)
	defer teardown()

	ctx := context.Background()
	jti := "token-dup"
	expiresAt := time.Now().Add(1 * time.Hour)

	// First revocation
	err := s.RevokeTokenImmediate(ctx, jti, expiresAt)
	require.NoError(t, err)

	// Second revocation (should be idempotent)
	err = s.RevokeTokenImmediate(ctx, jti, expiresAt)
	require.NoError(t, err)
}

func TestRevocationStore_RevokeTokenIfNotRevoked(t *testing.T) {
	s, teardown := setupTestRevocationStore(t)
	defer teardown()

	ctx := context.Background()
	jti := "token-atomic"
	expiresAt := time.Now().Add(1 * time.Hour)

	// 1. First revocation should succeed
	err := s.RevokeTokenIfNotRevoked(ctx, jti, expiresAt, 0)
	require.NoError(t, err)

	// 2. Second revocation should fail with ErrTokenAlreadyRevoked
	err = s.RevokeTokenIfNotRevoked(ctx, jti, expiresAt, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrTokenAlreadyRevoked)
}

func TestRevocationStore_RevokeTokenIfNotRevoked_WithGracePeriod(t *testing.T) {
	s, teardown := setupTestRevocationStore(t)
	defer teardown()

	ctx := context.Background()
	jti := "token-atomic-grace"
	expiresAt := time.Now().Add(1 * time.Hour)

	// 1. First revocation should succeed
	err := s.RevokeTokenIfNotRevoked(ctx, jti, expiresAt, 1*time.Minute)
	require.NoError(t, err)

	// 2. Second revocation within grace period should still fail (atomic check)
	// Even though IsRevoked with grace period returns false, the atomic operation
	// should detect the existing record and return ErrTokenAlreadyRevoked
	err = s.RevokeTokenIfNotRevoked(ctx, jti, expiresAt, 1*time.Minute)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrTokenAlreadyRevoked)
}
