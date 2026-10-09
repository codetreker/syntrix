package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()

	// IDs should be 16 characters
	assert.Len(t, id1, 16)
	assert.Len(t, id2, 16)

	// IDs should be unique
	assert.NotEqual(t, id1, id2)

	// IDs should be valid
	assert.NoError(t, ValidateID(id1))
	assert.NoError(t, ValidateID(id2))
}

func TestValidateID(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr error
	}{
		{
			name:    "valid ID",
			id:      "a1b2c3d4e5f67890",
			wantErr: nil,
		},
		{
			name:    "valid ID all zeros",
			id:      "0000000000000000",
			wantErr: nil,
		},
		{
			name:    "valid ID all letters",
			id:      "abcdefabcdefabcd",
			wantErr: nil,
		},
		{
			name:    "too short",
			id:      "a1b2c3d4e5f6789",
			wantErr: ErrInvalidDatabaseID,
		},
		{
			name:    "too long",
			id:      "a1b2c3d4e5f678901",
			wantErr: ErrInvalidDatabaseID,
		},
		{
			name:    "contains uppercase",
			id:      "A1b2c3d4e5f67890",
			wantErr: ErrInvalidDatabaseID,
		},
		{
			name:    "contains invalid char",
			id:      "g1b2c3d4e5f67890",
			wantErr: ErrInvalidDatabaseID,
		},
		{
			name:    "empty string",
			id:      "",
			wantErr: ErrInvalidDatabaseID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateID(tt.id)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateSlug(t *testing.T) {
	tests := []struct {
		name    string
		slug    string
		wantErr error
	}{
		{
			name:    "valid slug",
			slug:    "my-app-prod",
			wantErr: nil,
		},
		{
			name:    "valid slug with numbers",
			slug:    "my-app-2024",
			wantErr: nil,
		},
		{
			name:    "valid slug minimum length",
			slug:    "abc",
			wantErr: nil,
		},
		{
			name:    "valid slug maximum length",
			slug:    "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz1", // 63 chars
			wantErr: nil,
		},
		{
			name:    "empty string is valid",
			slug:    "",
			wantErr: nil,
		},
		{
			name:    "reserved slug default",
			slug:    "default",
			wantErr: ErrReservedSlug,
		},
		{
			name:    "reserved slug admin",
			slug:    "admin",
			wantErr: ErrReservedSlug,
		},
		{
			name:    "reserved slug system",
			slug:    "system",
			wantErr: ErrReservedSlug,
		},
		{
			name:    "reserved slug api",
			slug:    "api",
			wantErr: ErrReservedSlug,
		},
		{
			name:    "reserved slug auth",
			slug:    "auth",
			wantErr: ErrReservedSlug,
		},
		{
			name:    "too short",
			slug:    "ab",
			wantErr: ErrInvalidSlugLength,
		},
		{
			name:    "too long",
			slug:    "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz12", // 64 chars
			wantErr: ErrInvalidSlugLength,
		},
		{
			name:    "starts with number",
			slug:    "1my-app",
			wantErr: ErrInvalidSlugFormat,
		},
		{
			name:    "starts with hyphen",
			slug:    "-my-app",
			wantErr: ErrInvalidSlugFormat,
		},
		{
			name:    "contains uppercase",
			slug:    "My-App",
			wantErr: ErrInvalidSlugFormat,
		},
		{
			name:    "contains underscore",
			slug:    "my_app",
			wantErr: ErrInvalidSlugFormat,
		},
		{
			name:    "contains space",
			slug:    "my app",
			wantErr: ErrInvalidSlugFormat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSlug(tt.slug)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateSlugForSystem(t *testing.T) {
	// ValidateSlugForSystem should allow reserved slugs
	err := ValidateSlugForSystem("default")
	assert.NoError(t, err)

	err = ValidateSlugForSystem("admin")
	assert.NoError(t, err)

	// But still validate format
	err = ValidateSlugForSystem("ab")
	assert.ErrorIs(t, err, ErrInvalidSlugLength)

	err = ValidateSlugForSystem("1my-app")
	assert.ErrorIs(t, err, ErrInvalidSlugFormat)
}

func TestParseIdentifier(t *testing.T) {
	tests := []struct {
		name       string
		identifier string
		wantID     string
		wantSlug   string
		wantIsID   bool
	}{
		{
			name:       "ID with prefix",
			identifier: "id:a1b2c3d4e5f67890",
			wantID:     "a1b2c3d4e5f67890",
			wantSlug:   "",
			wantIsID:   true,
		},
		{
			name:       "slug without prefix",
			identifier: "my-app-prod",
			wantID:     "",
			wantSlug:   "my-app-prod",
			wantIsID:   false,
		},
		{
			name:       "default slug",
			identifier: "default",
			wantID:     "",
			wantSlug:   "default",
			wantIsID:   false,
		},
		{
			name:       "empty identifier",
			identifier: "",
			wantID:     "",
			wantSlug:   "",
			wantIsID:   false,
		},
		{
			name:       "just id: prefix",
			identifier: "id:",
			wantID:     "",
			wantSlug:   "",
			wantIsID:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, gotSlug, gotIsID := ParseIdentifier(tt.identifier)
			assert.Equal(t, tt.wantID, gotID)
			assert.Equal(t, tt.wantSlug, gotSlug)
			assert.Equal(t, tt.wantIsID, gotIsID)
		})
	}
}

func TestIsReservedSlug(t *testing.T) {
	assert.True(t, IsReservedSlug("default"))
	assert.True(t, IsReservedSlug("admin"))
	assert.True(t, IsReservedSlug("system"))
	assert.True(t, IsReservedSlug("api"))
	assert.True(t, IsReservedSlug("auth"))
	assert.False(t, IsReservedSlug("my-app"))
	assert.False(t, IsReservedSlug(""))
}

func TestIDUniqueness(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 1000; i++ {
		id := GenerateID()
		require.False(t, seen[id], "duplicate ID generated: %s", id)
		seen[id] = true
	}
}
