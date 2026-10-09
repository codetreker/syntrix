package helper

import (
	"testing"

	"github.com/codetreker/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
)

func TestValidatePathSyntax_MaxLength(t *testing.T) {
	// Save original config
	originalCfg := validationConfig

	// Set a small max path length
	SetValidationConfig(ValidationConfig{
		MaxPathLength: 20,
	})

	// Test path within limit
	err := CheckPath("short/path")
	assert.NoError(t, err)

	// Test path exceeding limit
	err = CheckPath("this/is/a/very/long/path/that/exceeds/limit")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "20")

	// Restore original config
	SetValidationConfig(originalCfg)
}

func TestValidatePathSyntax(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid path", "users/alice", false},
		{"valid nested path", "rooms/room1/messages/msg1", false},
		{"empty path", "", true},
		{"invalid chars", "users/alice!", true},
		{"starts with slash", "/users/alice", true},
		{"ends with slash", "users/alice/", true},
		{"double slash", "users//alice", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckPath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateDocumentPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid document path", "users/alice", false},
		{"valid nested document path", "rooms/room1/messages/msg1", false},
		{"invalid collection path", "users", true},
		{"invalid nested collection path", "rooms/room1/messages", true},
		{"invalid syntax", "/users/alice", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckDocumentPath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateCollection(t *testing.T) {
	tests := []struct {
		name       string
		collection string
		wantErr    bool
	}{
		{"valid collection", "users", false},
		{"valid nested collection", "rooms/room1/messages", false},
		{"invalid document path", "users/alice", true},
		{"invalid nested document path", "rooms/room1/messages/msg1", true},
		{"invalid syntax", "/users", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckCollectionPath(tt.collection)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidateData(t *testing.T) {
	tests := []struct {
		name    string
		data    model.Document
		wantErr bool
	}{
		{"valid data", model.Document{"key": "value"}, false},
		{"nil data", nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.data.ValidateDocument()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestExplodeFullpath(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		wantCollection string
		wantDocID      string
		wantErr        bool
	}{
		{"document path", "users/alice", "users", "alice", false},
		{"nested document path", "rooms/room1/messages/msg1", "rooms/room1/messages", "msg1", false},
		{"collection path", "users", "users", "", false},
		{"nested collection path", "rooms/room1/messages", "rooms/room1/messages", "", false},
		{"invalid path syntax", "/users/alice", "", "", true},
		{"empty path", "", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collection, docID, err := ExplodeFullpath(tt.path)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantCollection, collection)
				assert.Equal(t, tt.wantDocID, docID)
			}
		})
	}
}

func TestExplodeFullpath_LongID(t *testing.T) {
	originalCfg := validationConfig
	SetValidationConfig(ValidationConfig{
		MaxIDLength: 10,
	})
	defer SetValidationConfig(originalCfg)

	_, _, err := ExplodeFullpath("users/verylongdocumentid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds maximum length")
}
