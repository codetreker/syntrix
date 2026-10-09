package database

import (
	"encoding/hex"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/zeebo/blake3"
)

// IDPrefix is used to distinguish database IDs from slugs in URL paths
const IDPrefix = "id:"

// slugRegexp matches valid slug format: starts with letter, 3-63 chars, lowercase letters, digits, hyphens
var slugRegexp = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)

// ReservedSlugs contains slugs that cannot be used by regular users
// Note: "default" is reserved for the system-created default database
var ReservedSlugs = map[string]bool{
	"default": true,
	"admin":   true,
	"system":  true,
	"api":     true,
	"auth":    true,
}

// GenerateID creates a new database ID using hex(blake3(uuid())[:8])
// This produces a 16-character hexadecimal string.
func GenerateID() string {
	u := uuid.New()
	hash := blake3.Sum256(u[:])
	return hex.EncodeToString(hash[:8])
}

// ValidateID checks if the given ID is a valid database ID format
func ValidateID(id string) error {
	if len(id) != 16 {
		return ErrInvalidDatabaseID
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return ErrInvalidDatabaseID
		}
	}
	return nil
}

// ValidateSlug checks if a slug is valid and not reserved
func ValidateSlug(slug string) error {
	if slug == "" {
		return nil // Empty is allowed (optional)
	}

	if !slugRegexp.MatchString(slug) {
		if len(slug) < 3 || len(slug) > 63 {
			return ErrInvalidSlugLength
		}
		return ErrInvalidSlugFormat
	}

	if ReservedSlugs[slug] {
		return ErrReservedSlug
	}

	return nil
}

// ValidateSlugForSystem validates slug format without checking reserved slugs
// This is used for system operations that need to create reserved slugs
func ValidateSlugForSystem(slug string) error {
	if slug == "" {
		return nil
	}

	if !slugRegexp.MatchString(slug) {
		if len(slug) < 3 || len(slug) > 63 {
			return ErrInvalidSlugLength
		}
		return ErrInvalidSlugFormat
	}

	return nil
}

// ParseIdentifier parses a URL path identifier and determines if it's an ID or slug.
// Returns (id, slug, isID).
// If the identifier starts with "id:", it's treated as an ID.
// Otherwise, it's treated as a slug.
func ParseIdentifier(identifier string) (id string, slug string, isID bool) {
	if strings.HasPrefix(identifier, IDPrefix) {
		return identifier[len(IDPrefix):], "", true
	}
	return "", identifier, false
}

// IsReservedSlug checks if the given slug is reserved
func IsReservedSlug(slug string) bool {
	return ReservedSlugs[slug]
}
