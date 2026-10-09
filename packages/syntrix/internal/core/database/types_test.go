package database

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDatabaseStatus_IsValid(t *testing.T) {
	tests := []struct {
		name     string
		status   DatabaseStatus
		expected bool
	}{
		{"active is valid", StatusActive, true},
		{"suspended is valid", StatusSuspended, true},
		{"deleting is valid", StatusDeleting, true},
		{"empty is invalid", DatabaseStatus(""), false},
		{"unknown is invalid", DatabaseStatus("unknown"), false},
		{"pending is invalid", DatabaseStatus("pending"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.status.IsValid()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDatabase_IsProtected(t *testing.T) {
	tests := []struct {
		name     string
		slug     *string
		expected bool
	}{
		{"nil slug is not protected", nil, false},
		{"default slug is protected", strPtr("default"), true},
		{"other slug is not protected", strPtr("my-app"), false},
		{"empty slug is not protected", strPtr(""), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := &Database{
				ID:   "test-id",
				Slug: tt.slug,
			}
			result := db.IsProtected()
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDatabase_Settings(t *testing.T) {
	db := &Database{
		ID:              "test-id",
		DisplayName:     "Test Database",
		MaxDocuments:    1000,
		MaxStorageBytes: 1024 * 1024 * 100, // 100 MB
	}

	settings := db.Settings()

	assert.Equal(t, int64(1000), settings.MaxDocuments)
	assert.Equal(t, int64(1024*1024*100), settings.MaxStorageBytes)
}

func TestDatabase_Settings_ZeroValues(t *testing.T) {
	db := &Database{
		ID:          "test-id",
		DisplayName: "Test Database",
	}

	settings := db.Settings()

	assert.Equal(t, int64(0), settings.MaxDocuments)
	assert.Equal(t, int64(0), settings.MaxStorageBytes)
}

func strPtr(s string) *string {
	return &s
}
