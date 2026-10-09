package rest

import (
	"time"

	"github.com/codetreker/syntrix/internal/core/database"
)

// CreateDatabaseRequest represents the request body for creating a database
type CreateDatabaseRequest struct {
	Slug        *string                  `json:"slug,omitempty"`
	DisplayName string                   `json:"display_name"`
	Description *string                  `json:"description,omitempty"`
	OwnerID     string                   `json:"owner_id,omitempty"` // Admin only
	Settings    *DatabaseSettingsRequest `json:"settings,omitempty"` // Admin only
}

// UpdateDatabaseRequest represents the request body for updating a database
type UpdateDatabaseRequest struct {
	Slug        *string                  `json:"slug,omitempty"` // Can only be set if currently nil
	DisplayName *string                  `json:"display_name,omitempty"`
	Description *string                  `json:"description,omitempty"`
	Status      *string                  `json:"status,omitempty"`   // Admin only
	Settings    *DatabaseSettingsRequest `json:"settings,omitempty"` // Admin only
}

// DatabaseSettingsRequest represents quota settings in requests
type DatabaseSettingsRequest struct {
	MaxDocuments    *int64 `json:"max_documents,omitempty"`
	MaxStorageBytes *int64 `json:"max_storage_bytes,omitempty"`
}

// DatabaseResponse represents a database in API responses
type DatabaseResponse struct {
	ID          string                    `json:"id"`
	Slug        *string                   `json:"slug"`
	DisplayName string                    `json:"display_name"`
	Description *string                   `json:"description,omitempty"`
	OwnerID     string                    `json:"owner_id"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
	Status      string                    `json:"status"`
	Settings    *DatabaseSettingsResponse `json:"settings,omitempty"`
}

// DatabaseSettingsResponse represents quota settings in responses
type DatabaseSettingsResponse struct {
	MaxDocuments    int64 `json:"max_documents"`
	MaxStorageBytes int64 `json:"max_storage_bytes"`
}

// DatabaseSummaryResponse represents a database summary for list responses
type DatabaseSummaryResponse struct {
	ID          string    `json:"id"`
	Slug        *string   `json:"slug"`
	DisplayName string    `json:"display_name"`
	OwnerID     string    `json:"owner_id"`
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
}

// ListDatabasesResponse represents the response for listing databases
type ListDatabasesResponse struct {
	Databases []*DatabaseSummaryResponse `json:"databases"`
	Total     int                        `json:"total"`
	Quota     *QuotaResponse             `json:"quota,omitempty"` // User API only
}

// QuotaResponse represents quota information in responses
type QuotaResponse struct {
	Used  int `json:"used"`
	Limit int `json:"limit"`
}

// DeleteDatabaseResponse represents the response for deleting a database
type DeleteDatabaseResponse struct {
	ID      string  `json:"id"`
	Slug    *string `json:"slug"`
	Status  string  `json:"status"`
	Message string  `json:"message"`
}

// toDatabaseResponse converts a database.Database to DatabaseResponse
func toDatabaseResponse(db *database.Database) *DatabaseResponse {
	return &DatabaseResponse{
		ID:          db.ID,
		Slug:        db.Slug,
		DisplayName: db.DisplayName,
		Description: db.Description,
		OwnerID:     db.OwnerID,
		CreatedAt:   db.CreatedAt,
		UpdatedAt:   db.UpdatedAt,
		Status:      string(db.Status),
		Settings: &DatabaseSettingsResponse{
			MaxDocuments:    db.MaxDocuments,
			MaxStorageBytes: db.MaxStorageBytes,
		},
	}
}

// toDatabaseSummary converts a database.Database to DatabaseSummaryResponse
func toDatabaseSummary(db *database.Database) *DatabaseSummaryResponse {
	return &DatabaseSummaryResponse{
		ID:          db.ID,
		Slug:        db.Slug,
		DisplayName: db.DisplayName,
		OwnerID:     db.OwnerID,
		CreatedAt:   db.CreatedAt,
		Status:      string(db.Status),
	}
}

// toCreateRequest converts CreateDatabaseRequest to database.CreateRequest
func (r *CreateDatabaseRequest) toCreateRequest() database.CreateRequest {
	req := database.CreateRequest{
		Slug:        r.Slug,
		DisplayName: r.DisplayName,
		Description: r.Description,
		OwnerID:     r.OwnerID,
	}
	if r.Settings != nil {
		req.Settings = &database.DatabaseSettings{}
		if r.Settings.MaxDocuments != nil {
			req.Settings.MaxDocuments = *r.Settings.MaxDocuments
		}
		if r.Settings.MaxStorageBytes != nil {
			req.Settings.MaxStorageBytes = *r.Settings.MaxStorageBytes
		}
	}
	return req
}

// toUpdateRequest converts UpdateDatabaseRequest to database.UpdateRequest
func (r *UpdateDatabaseRequest) toUpdateRequest() database.UpdateRequest {
	req := database.UpdateRequest{
		Slug:        r.Slug,
		DisplayName: r.DisplayName,
		Description: r.Description,
	}
	if r.Status != nil {
		status := database.DatabaseStatus(*r.Status)
		req.Status = &status
	}
	if r.Settings != nil {
		req.Settings = &database.DatabaseSettings{}
		if r.Settings.MaxDocuments != nil {
			req.Settings.MaxDocuments = *r.Settings.MaxDocuments
		}
		if r.Settings.MaxStorageBytes != nil {
			req.Settings.MaxStorageBytes = *r.Settings.MaxStorageBytes
		}
	}
	return req
}
