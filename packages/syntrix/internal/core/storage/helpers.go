package storage

import "github.com/codetreker/syntrix/internal/core/storage/types"

var (
	// CalculateDatabase calculates the database-aware document ID
	CalculateDatabase = types.CalculateDatabase

	// NewStoredDoc creates a new document instance with initialized metadata
	NewStoredDoc = types.NewStoredDoc
)
