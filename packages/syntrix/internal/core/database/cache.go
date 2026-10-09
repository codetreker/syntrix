package database

import (
	"context"
	"sync"
	"time"
)

// CacheConfig contains configuration for the database cache
type CacheConfig struct {
	// Size is the maximum number of entries in each cache
	Size int
	// TTL is the time-to-live for positive cache entries
	TTL time.Duration
	// NegativeTTL is the time-to-live for negative cache entries
	NegativeTTL time.Duration
}

// DefaultCacheConfig returns default cache configuration
func DefaultCacheConfig() CacheConfig {
	return CacheConfig{
		Size:        1000,
		TTL:         5 * time.Minute,
		NegativeTTL: 30 * time.Second,
	}
}

// Cache provides a caching layer over DatabaseStore with dual lookup (ID and slug)
type Cache struct {
	store         DatabaseStore
	config        CacheConfig
	mu            sync.RWMutex
	idCache       map[string]*cacheEntry
	slugCache     map[string]string // slug -> ID mapping
	negativeCache map[string]time.Time
}

type cacheEntry struct {
	db        *Database
	expiresAt time.Time
}

// NewCache creates a new Cache with the given store and configuration
func NewCache(store DatabaseStore, config CacheConfig) *Cache {
	if config.Size <= 0 {
		config.Size = 1000
	}
	if config.TTL <= 0 {
		config.TTL = 5 * time.Minute
	}
	if config.NegativeTTL <= 0 {
		config.NegativeTTL = 30 * time.Second
	}

	return &Cache{
		store:         store,
		config:        config,
		idCache:       make(map[string]*cacheEntry),
		slugCache:     make(map[string]string),
		negativeCache: make(map[string]time.Time),
	}
}

// GetByID retrieves a database by ID, using cache when available
func (c *Cache) GetByID(ctx context.Context, id string) (*Database, error) {
	c.mu.RLock()
	if entry, ok := c.idCache[id]; ok {
		if time.Now().Before(entry.expiresAt) {
			c.mu.RUnlock()
			return entry.db, nil
		}
	}
	// Check negative cache
	cacheKey := "id:" + id
	if expiresAt, ok := c.negativeCache[cacheKey]; ok {
		if time.Now().Before(expiresAt) {
			c.mu.RUnlock()
			return nil, ErrDatabaseNotFound
		}
	}
	c.mu.RUnlock()

	// Fetch from store
	db, err := c.store.Get(ctx, id)
	if err == ErrDatabaseNotFound {
		c.mu.Lock()
		c.negativeCache[cacheKey] = time.Now().Add(c.config.NegativeTTL)
		c.evictIfNeeded()
		c.mu.Unlock()
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	// Add to cache
	c.addToCache(db)
	return db, nil
}

// GetBySlug retrieves a database by slug, using cache when available
func (c *Cache) GetBySlug(ctx context.Context, slug string) (*Database, error) {
	c.mu.RLock()
	if id, ok := c.slugCache[slug]; ok {
		if entry, ok := c.idCache[id]; ok {
			if time.Now().Before(entry.expiresAt) {
				c.mu.RUnlock()
				return entry.db, nil
			}
		}
	}
	// Check negative cache
	cacheKey := "slug:" + slug
	if expiresAt, ok := c.negativeCache[cacheKey]; ok {
		if time.Now().Before(expiresAt) {
			c.mu.RUnlock()
			return nil, ErrDatabaseNotFound
		}
	}
	c.mu.RUnlock()

	// Fetch from store
	db, err := c.store.GetBySlug(ctx, slug)
	if err == ErrDatabaseNotFound {
		c.mu.Lock()
		c.negativeCache[cacheKey] = time.Now().Add(c.config.NegativeTTL)
		c.evictIfNeeded()
		c.mu.Unlock()
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	// Add to cache
	c.addToCache(db)
	return db, nil
}

// addToCache adds a database to the cache (caller must hold write lock or will acquire it)
func (c *Cache) addToCache(db *Database) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.idCache[db.ID] = &cacheEntry{
		db:        db,
		expiresAt: time.Now().Add(c.config.TTL),
	}

	if db.Slug != nil {
		c.slugCache[*db.Slug] = db.ID
	}

	// Clear any negative cache entries
	delete(c.negativeCache, "id:"+db.ID)
	if db.Slug != nil {
		delete(c.negativeCache, "slug:"+*db.Slug)
	}

	c.evictIfNeeded()
}

// Invalidate removes a database from all caches
func (c *Cache) Invalidate(db *Database) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.idCache, db.ID)
	if db.Slug != nil {
		delete(c.slugCache, *db.Slug)
	}
}

// InvalidateByID removes a database from cache by ID
func (c *Cache) InvalidateByID(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.idCache[id]; ok {
		if entry.db.Slug != nil {
			delete(c.slugCache, *entry.db.Slug)
		}
		delete(c.idCache, id)
	}
}

// Warm preloads active databases into the cache
func (c *Cache) Warm(ctx context.Context) error {
	databases, _, err := c.store.List(ctx, ListOptions{
		Status: StatusActive,
		Limit:  c.config.Size,
	})
	if err != nil {
		return err
	}

	for _, db := range databases {
		c.addToCache(db)
	}

	return nil
}

// Clear removes all entries from the cache
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.idCache = make(map[string]*cacheEntry)
	c.slugCache = make(map[string]string)
	c.negativeCache = make(map[string]time.Time)
}

// Size returns the current number of entries in the ID cache
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.idCache)
}

// evictIfNeeded removes expired entries and evicts oldest if over capacity
// Caller must hold write lock
func (c *Cache) evictIfNeeded() {
	now := time.Now()

	// Remove expired entries from negative cache
	for key, expiresAt := range c.negativeCache {
		if now.After(expiresAt) {
			delete(c.negativeCache, key)
		}
	}

	// Remove expired entries from ID cache
	for id, entry := range c.idCache {
		if now.After(entry.expiresAt) {
			if entry.db.Slug != nil {
				delete(c.slugCache, *entry.db.Slug)
			}
			delete(c.idCache, id)
		}
	}

	// If still over capacity, remove oldest entries
	for len(c.idCache) > c.config.Size {
		var oldestID string
		var oldestTime time.Time

		for id, entry := range c.idCache {
			if oldestID == "" || entry.expiresAt.Before(oldestTime) {
				oldestID = id
				oldestTime = entry.expiresAt
			}
		}

		if oldestID != "" {
			if entry, ok := c.idCache[oldestID]; ok {
				if entry.db.Slug != nil {
					delete(c.slugCache, *entry.db.Slug)
				}
			}
			delete(c.idCache, oldestID)
		}
	}
}
