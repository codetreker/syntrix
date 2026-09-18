package database

import (
	"context"
	"log/slog"
)

// ServiceConfig contains configuration for the database service
type ServiceConfig struct {
	// MaxDatabasesPerUser is the maximum number of databases a non-admin user can create (0 = unlimited)
	MaxDatabasesPerUser int
	// Cache configuration
	Cache CacheConfig
}

// DefaultServiceConfig returns default service configuration
func DefaultServiceConfig() ServiceConfig {
	return ServiceConfig{
		MaxDatabasesPerUser: 10,
		Cache:               DefaultCacheConfig(),
	}
}

// CreateRequest contains the data for creating a new database
type CreateRequest struct {
	Slug        *string
	DisplayName string
	Description *string
	OwnerID     string // Admin only - allows specifying owner
	Settings    *DatabaseSettings
}

// UpdateRequest contains the data for updating a database
type UpdateRequest struct {
	Slug        *string // Can only be set if currently nil
	DisplayName *string
	Description *string
	Status      *DatabaseStatus // Admin only
	Settings    *DatabaseSettings
}

// ListResult contains the result of listing databases
type ListResult struct {
	Databases []*Database
	Total     int
	Quota     *QuotaInfo
}

// QuotaInfo contains quota information for a user
type QuotaInfo struct {
	Used  int
	Limit int
}

// Service defines the interface for database management operations
type Service interface {
	// CreateDatabase creates a new database
	CreateDatabase(ctx context.Context, userID string, isAdmin bool, req CreateRequest) (*Database, error)

	// ListDatabases lists databases (filtered by owner for non-admin)
	ListDatabases(ctx context.Context, userID string, isAdmin bool, opts ListOptions) (*ListResult, error)

	// GetDatabase retrieves a database by identifier (slug or id:xxx)
	GetDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) (*Database, error)

	// UpdateDatabase updates a database
	UpdateDatabase(ctx context.Context, userID string, isAdmin bool, identifier string, req UpdateRequest) (*Database, error)

	// DeleteDatabase initiates database deletion (soft delete)
	DeleteDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) error

	// ResolveDatabase resolves an identifier to a database and checks status
	ResolveDatabase(ctx context.Context, identifier string) (*Database, error)

	// ResolveDatabaseAuthoritative bypasses metadata caches and checks the current status.
	ResolveDatabaseAuthoritative(ctx context.Context, identifier string) (*Database, error)

	// ValidateDatabase checks if a database exists and is active
	ValidateDatabase(ctx context.Context, identifier string) error
}

type service struct {
	store  DatabaseStore
	cache  *Cache
	config ServiceConfig
	logger *slog.Logger
}

// NewService creates a new database service
func NewService(store DatabaseStore, config ServiceConfig, logger *slog.Logger) Service {
	if logger == nil {
		logger = slog.Default()
	}

	cache := NewCache(store, config.Cache)

	return &service{
		store:  store,
		cache:  cache,
		config: config,
		logger: logger,
	}
}

func (s *service) CreateDatabase(ctx context.Context, userID string, isAdmin bool, req CreateRequest) (*Database, error) {
	// Validate display name
	if req.DisplayName == "" {
		return nil, ErrDisplayNameRequired
	}

	// Validate slug if provided
	if req.Slug != nil && *req.Slug != "" {
		if isAdmin {
			// Admin can use reserved slugs, but still validate format
			if err := ValidateSlugForSystem(*req.Slug); err != nil {
				return nil, err
			}
		} else {
			if err := ValidateSlug(*req.Slug); err != nil {
				return nil, err
			}
		}
	}

	// Determine owner
	ownerID := userID
	if isAdmin && req.OwnerID != "" {
		ownerID = req.OwnerID
	}

	// Check quota for non-admin users
	if !isAdmin && s.config.MaxDatabasesPerUser > 0 {
		count, err := s.store.CountByOwner(ctx, ownerID)
		if err != nil {
			return nil, err
		}
		if count >= s.config.MaxDatabasesPerUser {
			return nil, ErrQuotaExceeded
		}
	}

	// Generate ID
	id := GenerateID()

	// Create database
	db := &Database{
		ID:          id,
		Slug:        req.Slug,
		DisplayName: req.DisplayName,
		Description: req.Description,
		OwnerID:     ownerID,
		Status:      StatusActive,
	}

	// Apply settings if provided (admin only)
	if isAdmin && req.Settings != nil {
		db.MaxDocuments = req.Settings.MaxDocuments
		db.MaxStorageBytes = req.Settings.MaxStorageBytes
	}

	if err := s.store.Create(ctx, db); err != nil {
		return nil, err
	}

	// Add to cache (also clears any negative cache entries for this slug/id)
	s.cache.addToCache(db)

	s.logger.Info("Database created",
		"id", db.ID,
		"slug", db.Slug,
		"owner", db.OwnerID,
	)

	return db, nil
}

func (s *service) ListDatabases(ctx context.Context, userID string, isAdmin bool, opts ListOptions) (*ListResult, error) {
	// Non-admin users can only see their own databases
	if !isAdmin {
		opts.OwnerID = userID
	}

	databases, total, err := s.store.List(ctx, opts)
	if err != nil {
		return nil, err
	}

	result := &ListResult{
		Databases: databases,
		Total:     total,
	}

	// Include quota info for non-admin users
	if !isAdmin && s.config.MaxDatabasesPerUser > 0 {
		count, err := s.store.CountByOwner(ctx, userID)
		if err != nil {
			return nil, err
		}
		result.Quota = &QuotaInfo{
			Used:  count,
			Limit: s.config.MaxDatabasesPerUser,
		}
	}

	return result, nil
}

func (s *service) GetDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) (*Database, error) {
	db, err := s.resolveIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}

	// Non-admin users can only access their own databases
	if !isAdmin && db.OwnerID != userID {
		return nil, ErrNotOwner
	}

	return db, nil
}

func (s *service) UpdateDatabase(ctx context.Context, userID string, isAdmin bool, identifier string, req UpdateRequest) (*Database, error) {
	db, err := s.resolveIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}

	// Non-admin users can only update their own databases
	if !isAdmin && db.OwnerID != userID {
		return nil, ErrNotOwner
	}

	// Handle slug update
	if req.Slug != nil && *req.Slug != "" {
		// Slug can only be set if currently nil
		if db.Slug != nil {
			return nil, ErrSlugImmutable
		}

		// Validate slug
		if isAdmin {
			if err := ValidateSlugForSystem(*req.Slug); err != nil {
				return nil, err
			}
		} else {
			if err := ValidateSlug(*req.Slug); err != nil {
				return nil, err
			}
		}
		db.Slug = req.Slug
	}

	// Update fields
	if req.DisplayName != nil {
		db.DisplayName = *req.DisplayName
	}
	if req.Description != nil {
		db.Description = req.Description
	}

	// Admin-only fields
	if isAdmin {
		if req.Status != nil {
			if !req.Status.IsValid() {
				return nil, ErrInvalidStatus
			}
			db.Status = *req.Status
		}
		if req.Settings != nil {
			db.MaxDocuments = req.Settings.MaxDocuments
			db.MaxStorageBytes = req.Settings.MaxStorageBytes
		}
	}

	if err := s.store.Update(ctx, db); err != nil {
		return nil, err
	}

	// Invalidate cache
	s.cache.Invalidate(db)

	s.logger.Info("Database updated",
		"id", db.ID,
		"slug", db.Slug,
	)

	return db, nil
}

func (s *service) DeleteDatabase(ctx context.Context, userID string, isAdmin bool, identifier string) error {
	db, err := s.resolveIdentifier(ctx, identifier)
	if err != nil {
		return err
	}

	// Non-admin users can only delete their own databases
	if !isAdmin && db.OwnerID != userID {
		return ErrNotOwner
	}

	// Check if database is protected
	if db.IsProtected() {
		return ErrProtectedDatabase
	}

	// Soft delete: set status to deleting
	db.Status = StatusDeleting
	if err := s.store.Update(ctx, db); err != nil {
		return err
	}

	// Invalidate cache
	s.cache.Invalidate(db)

	s.logger.Info("Database deletion initiated",
		"id", db.ID,
		"slug", db.Slug,
	)

	return nil
}

func (s *service) ResolveDatabase(ctx context.Context, identifier string) (*Database, error) {
	db, err := s.resolveIdentifier(ctx, identifier)
	if err != nil {
		return nil, err
	}

	return validateResolvedDatabase(db)
}

func (s *service) ResolveDatabaseAuthoritative(ctx context.Context, identifier string) (*Database, error) {
	id, slug, isID := ParseIdentifier(identifier)
	var db *Database
	var err error
	if isID {
		db, err = s.store.Get(ctx, id)
	} else {
		db, err = s.store.GetBySlug(ctx, slug)
	}
	if err != nil {
		return nil, err
	}
	return validateResolvedDatabase(db)
}

func validateResolvedDatabase(db *Database) (*Database, error) {
	switch db.Status {
	case StatusActive:
		return db, nil
	case StatusSuspended:
		return nil, ErrDatabaseSuspended
	case StatusDeleting:
		return nil, ErrDatabaseDeleting
	default:
		return nil, ErrDatabaseNotFound
	}
}

func (s *service) ValidateDatabase(ctx context.Context, identifier string) error {
	_, err := s.ResolveDatabase(ctx, identifier)
	return err
}

// resolveIdentifier resolves an identifier (slug or id:xxx) to a database
func (s *service) resolveIdentifier(ctx context.Context, identifier string) (*Database, error) {
	id, slug, isID := ParseIdentifier(identifier)

	if isID {
		return s.cache.GetByID(ctx, id)
	}
	return s.cache.GetBySlug(ctx, slug)
}
