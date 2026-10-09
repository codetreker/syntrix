package database

import (
	"context"
	"log/slog"

	"github.com/codetreker/syntrix/internal/core/storage/types"
)

const (
	// DefaultDatabaseSlug is the slug for the default database
	DefaultDatabaseSlug = "default"
	// DefaultDatabaseDisplayName is the display name for the default database
	DefaultDatabaseDisplayName = "Default Database"
)

// BootstrapConfig contains configuration for bootstrapping the default database
type BootstrapConfig struct {
	// AdminUsername is the username of the admin user who will own the default database
	AdminUsername string
}

// EnsureDefaultDatabase creates the default database if it doesn't exist.
// The default database is owned by the system admin user.
func EnsureDefaultDatabase(ctx context.Context, store DatabaseStore, userStore types.UserStore, config BootstrapConfig) error {
	if config.AdminUsername == "" {
		slog.Debug("Default database bootstrap skipped: no admin username configured")
		return nil
	}

	// Check if default database already exists
	_, err := store.GetBySlug(ctx, DefaultDatabaseSlug)
	if err == nil {
		slog.Debug("Default database already exists", "slug", DefaultDatabaseSlug)
		return nil
	}
	if err != ErrDatabaseNotFound {
		return err
	}

	// Get the admin user
	adminUser, err := userStore.GetUserByUsername(ctx, config.AdminUsername)
	if err != nil {
		if err == types.ErrUserNotFound {
			slog.Warn("Default database bootstrap skipped: admin user not found",
				"username", config.AdminUsername)
			return nil
		}
		return err
	}

	// Create the default database
	slug := DefaultDatabaseSlug
	db := &Database{
		ID:          GenerateID(),
		Slug:        &slug,
		DisplayName: DefaultDatabaseDisplayName,
		OwnerID:     adminUser.ID,
		Status:      StatusActive,
	}

	if err := store.Create(ctx, db); err != nil {
		// Handle race condition where another process created it
		if err == ErrSlugExists || err == ErrDatabaseExists {
			slog.Debug("Default database was created by another process", "slug", DefaultDatabaseSlug)
			return nil
		}
		return err
	}

	slog.Info("Created default database",
		"id", db.ID,
		"slug", DefaultDatabaseSlug,
		"owner", adminUser.Username,
	)

	return nil
}
