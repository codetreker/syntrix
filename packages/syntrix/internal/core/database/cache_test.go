package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStore implements DatabaseStore for testing
type mockStore struct {
	databases    map[string]*Database
	slugIndex    map[string]string
	getCalls     int
	getSlugCalls int
}

func newMockStore() *mockStore {
	return &mockStore{
		databases: make(map[string]*Database),
		slugIndex: make(map[string]string),
	}
}

func (m *mockStore) Create(ctx context.Context, db *Database) error {
	if _, exists := m.databases[db.ID]; exists {
		return ErrDatabaseExists
	}
	if db.Slug != nil {
		if _, exists := m.slugIndex[*db.Slug]; exists {
			return ErrSlugExists
		}
		m.slugIndex[*db.Slug] = db.ID
	}
	m.databases[db.ID] = db
	return nil
}

func (m *mockStore) Get(ctx context.Context, id string) (*Database, error) {
	m.getCalls++
	if db, ok := m.databases[id]; ok {
		return db, nil
	}
	return nil, ErrDatabaseNotFound
}

func (m *mockStore) GetBySlug(ctx context.Context, slug string) (*Database, error) {
	m.getSlugCalls++
	if id, ok := m.slugIndex[slug]; ok {
		return m.databases[id], nil
	}
	return nil, ErrDatabaseNotFound
}

func (m *mockStore) List(ctx context.Context, opts ListOptions) ([]*Database, int, error) {
	var result []*Database
	for _, db := range m.databases {
		if opts.Status != "" && db.Status != opts.Status {
			continue
		}
		if opts.OwnerID != "" && db.OwnerID != opts.OwnerID {
			continue
		}
		result = append(result, db)
	}
	return result, len(result), nil
}

func (m *mockStore) Update(ctx context.Context, db *Database) error {
	if _, ok := m.databases[db.ID]; !ok {
		return ErrDatabaseNotFound
	}
	m.databases[db.ID] = db
	if db.Slug != nil {
		m.slugIndex[*db.Slug] = db.ID
	}
	return nil
}

func (m *mockStore) Delete(ctx context.Context, id string) error {
	db, ok := m.databases[id]
	if !ok {
		return ErrDatabaseNotFound
	}
	if db.Slug != nil {
		delete(m.slugIndex, *db.Slug)
	}
	delete(m.databases, id)
	return nil
}

func (m *mockStore) CountByOwner(ctx context.Context, ownerID string) (int, error) {
	count := 0
	for _, db := range m.databases {
		if db.OwnerID == ownerID {
			count++
		}
	}
	return count, nil
}

func (m *mockStore) Exists(ctx context.Context, id string) (bool, error) {
	_, ok := m.databases[id]
	return ok, nil
}

func (m *mockStore) Close(ctx context.Context) error {
	return nil
}

func TestCache_GetByID(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	slug := "my-app"
	db := &Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}
	require.NoError(t, store.Create(context.Background(), db))

	// First call should hit store
	result, err := cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.Equal(t, 1, store.getCalls)

	// Second call should hit cache
	result, err = cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.Equal(t, 1, store.getCalls) // Still 1, didn't hit store
}

func TestCache_GetByID_NotFound(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	// First call should hit store
	_, err := cache.GetByID(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
	assert.Equal(t, 1, store.getCalls)

	// Second call should hit negative cache
	_, err = cache.GetByID(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
	assert.Equal(t, 1, store.getCalls) // Still 1, didn't hit store
}

func TestCache_GetBySlug(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	slug := "my-app"
	db := &Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}
	require.NoError(t, store.Create(context.Background(), db))

	// First call should hit store
	result, err := cache.GetBySlug(context.Background(), "my-app")
	assert.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.Equal(t, 1, store.getSlugCalls)

	// Second call should hit cache
	result, err = cache.GetBySlug(context.Background(), "my-app")
	assert.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.Equal(t, 1, store.getSlugCalls) // Still 1, didn't hit store
}

func TestCache_GetBySlug_NotFound(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	// First call should hit store
	_, err := cache.GetBySlug(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
	assert.Equal(t, 1, store.getSlugCalls)

	// Second call should hit negative cache
	_, err = cache.GetBySlug(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
	assert.Equal(t, 1, store.getSlugCalls) // Still 1, didn't hit store
}

func TestCache_GetByID_ThenGetBySlug(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	slug := "my-app"
	db := &Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}
	require.NoError(t, store.Create(context.Background(), db))

	// Get by ID first
	result, err := cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.Equal(t, 1, store.getCalls)

	// Get by slug should use cached entry (through slug -> ID mapping)
	result, err = cache.GetBySlug(context.Background(), "my-app")
	assert.NoError(t, err)
	assert.Equal(t, "a1b2c3d4e5f67890", result.ID)
	assert.Equal(t, 0, store.getSlugCalls) // Slug lookup used ID cache
}

func TestCache_Invalidate(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	slug := "my-app"
	db := &Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}
	require.NoError(t, store.Create(context.Background(), db))

	// Populate cache
	_, err := cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, 1, store.getCalls)

	// Invalidate
	cache.Invalidate(db)

	// Next call should hit store again
	_, err = cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, 2, store.getCalls)
}

func TestCache_InvalidateByID(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	slug := "my-app"
	db := &Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}
	require.NoError(t, store.Create(context.Background(), db))

	// Populate cache
	_, err := cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, 1, store.getCalls)

	// Invalidate by ID
	cache.InvalidateByID("a1b2c3d4e5f67890")

	// Next call should hit store again
	_, err = cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, 2, store.getCalls)
}

func TestCache_Clear(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	slug := "my-app"
	db := &Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}
	require.NoError(t, store.Create(context.Background(), db))

	// Populate cache
	_, err := cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, 1, cache.Size())

	// Clear
	cache.Clear()
	assert.Equal(t, 0, cache.Size())
}

func TestCache_Warm(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, DefaultCacheConfig())

	// Create some databases
	for i := 0; i < 5; i++ {
		slug := "app-" + string(rune('a'+i))
		db := &Database{
			ID:          GenerateID(),
			Slug:        &slug,
			DisplayName: "App " + string(rune('A'+i)),
			OwnerID:     "user-123",
			Status:      StatusActive,
		}
		require.NoError(t, store.Create(context.Background(), db))
	}

	// Warm cache
	err := cache.Warm(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 5, cache.Size())
}

func TestCache_TTLExpiry(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, CacheConfig{
		Size:        100,
		TTL:         50 * time.Millisecond,
		NegativeTTL: 50 * time.Millisecond,
	})

	slug := "my-app"
	db := &Database{
		ID:          "a1b2c3d4e5f67890",
		Slug:        &slug,
		DisplayName: "My App",
		OwnerID:     "user-123",
		Status:      StatusActive,
	}
	require.NoError(t, store.Create(context.Background(), db))

	// First call
	_, err := cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, 1, store.getCalls)

	// Wait for TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Next call should hit store again
	_, err = cache.GetByID(context.Background(), "a1b2c3d4e5f67890")
	assert.NoError(t, err)
	assert.Equal(t, 2, store.getCalls)
}

func TestCache_NegativeTTLExpiry(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, CacheConfig{
		Size:        100,
		TTL:         5 * time.Minute,
		NegativeTTL: 50 * time.Millisecond,
	})

	// First call - not found
	_, err := cache.GetByID(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
	assert.Equal(t, 1, store.getCalls)

	// Wait for negative TTL to expire
	time.Sleep(100 * time.Millisecond)

	// Next call should hit store again
	_, err = cache.GetByID(context.Background(), "nonexistent")
	assert.ErrorIs(t, err, ErrDatabaseNotFound)
	assert.Equal(t, 2, store.getCalls)
}

func TestCache_Eviction(t *testing.T) {
	store := newMockStore()
	cache := NewCache(store, CacheConfig{
		Size:        5,
		TTL:         5 * time.Minute,
		NegativeTTL: 30 * time.Second,
	})

	// Create more databases than cache size
	for i := 0; i < 10; i++ {
		id := GenerateID()
		slug := "app-" + id[:4]
		db := &Database{
			ID:          id,
			Slug:        &slug,
			DisplayName: "App " + id[:4],
			OwnerID:     "user-123",
			Status:      StatusActive,
		}
		require.NoError(t, store.Create(context.Background(), db))
		_, err := cache.GetByID(context.Background(), id)
		assert.NoError(t, err)
	}

	// Cache size should be limited
	assert.LessOrEqual(t, cache.Size(), 5)
}

func TestNewCache_DefaultsOnZeroConfig(t *testing.T) {
	store := newMockStore()

	// Zero config - should use defaults
	cache := NewCache(store, CacheConfig{
		Size:        0,
		TTL:         0,
		NegativeTTL: 0,
	})

	assert.NotNil(t, cache)
	assert.Equal(t, 1000, cache.config.Size)
	assert.Equal(t, 5*time.Minute, cache.config.TTL)
	assert.Equal(t, 30*time.Second, cache.config.NegativeTTL)
}

func TestNewCache_DefaultsOnNegativeConfig(t *testing.T) {
	store := newMockStore()

	// Negative values - should use defaults
	cache := NewCache(store, CacheConfig{
		Size:        -1,
		TTL:         -1,
		NegativeTTL: -1,
	})

	assert.NotNil(t, cache)
	assert.Equal(t, 1000, cache.config.Size)
	assert.Equal(t, 5*time.Minute, cache.config.TTL)
	assert.Equal(t, 30*time.Second, cache.config.NegativeTTL)
}

func TestNewCache_CustomConfig(t *testing.T) {
	store := newMockStore()

	// Custom valid config should be preserved
	cache := NewCache(store, CacheConfig{
		Size:        500,
		TTL:         10 * time.Minute,
		NegativeTTL: 1 * time.Minute,
	})

	assert.NotNil(t, cache)
	assert.Equal(t, 500, cache.config.Size)
	assert.Equal(t, 10*time.Minute, cache.config.TTL)
	assert.Equal(t, 1*time.Minute, cache.config.NegativeTTL)
}
