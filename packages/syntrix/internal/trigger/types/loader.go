package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Errors for trigger loading
var (
	ErrEmptyDatabase     = errors.New("database field cannot be empty")
	ErrDuplicateDatabase = errors.New("duplicate database definition")
	ErrNotDirectory      = errors.New("path is not a directory")
)

// TriggerFile represents a trigger configuration file with file-level database.
// The file format is:
//
//	database: mydb
//	retryPolicy:        # optional, default for all triggers in this file
//	  maxAttempts: 3
//	  initialBackoff: 1s
//	  maxBackoff: 30s
//	triggers:
//	  trigger-id:
//	    collection: users
//	    events: [create, update]
//	    retryPolicy:    # optional, overrides file-level default
//	      maxAttempts: 5
//	    ...
type TriggerFile struct {
	Database    string              `json:"database" yaml:"database"`
	RetryPolicy *RetryPolicy        `json:"retryPolicy" yaml:"retryPolicy"`
	Triggers    map[string]*Trigger `json:"triggers" yaml:"triggers"`
}

// LoadTriggersFromDir loads triggers from all YAML/JSON files in a directory.
// Each file must have a database field. Same database in multiple files is rejected.
// Returns a map of database -> triggers.
func LoadTriggersFromDir(dirPath string) (map[string][]*Trigger, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: %s", ErrNotDirectory, dirPath)
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	result := make(map[string][]*Trigger)
	// Track database -> source file for conflict detection
	seen := make(map[string]string)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yml" && ext != ".yaml" && ext != ".json" {
			continue
		}

		filePath := filepath.Join(dirPath, name)
		triggerFile, err := loadTriggerFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("file %s: %w", name, err)
		}

		if triggerFile.Database == "" {
			return nil, fmt.Errorf("file %s: %w", name, ErrEmptyDatabase)
		}

		// Check for duplicate database
		if existingFile, ok := seen[triggerFile.Database]; ok {
			return nil, fmt.Errorf("%w: database %q defined in both %s and %s",
				ErrDuplicateDatabase, triggerFile.Database, existingFile, name)
		}
		seen[triggerFile.Database] = name

		// Convert map to slice and set ID and Database from file
		// Apply file-level retryPolicy as default if trigger doesn't have one
		triggers := make([]*Trigger, 0, len(triggerFile.Triggers))
		for id, trigger := range triggerFile.Triggers {
			trigger.ID = id
			trigger.Database = triggerFile.Database
			// Apply file-level retryPolicy if trigger doesn't have one set
			if triggerFile.RetryPolicy != nil && isRetryPolicyEmpty(trigger.RetryPolicy) {
				trigger.RetryPolicy = *triggerFile.RetryPolicy
			}
			triggers = append(triggers, trigger)
		}

		result[triggerFile.Database] = triggers
	}

	return result, nil
}

// isRetryPolicyEmpty checks if a RetryPolicy has no values set.
func isRetryPolicyEmpty(rp RetryPolicy) bool {
	return rp.MaxAttempts == 0 && rp.InitialBackoff == 0 && rp.MaxBackoff == 0
}

// loadTriggerFile loads a TriggerFile from a YAML or JSON file.
func loadTriggerFile(filename string) (*TriggerFile, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read trigger file: %w", err)
	}

	var triggerFile TriggerFile
	ext := strings.ToLower(filepath.Ext(filename))

	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &triggerFile); err != nil {
			return nil, fmt.Errorf("failed to parse YAML trigger file: %w", err)
		}
	case ".json":
		if err := json.Unmarshal(data, &triggerFile); err != nil {
			return nil, fmt.Errorf("failed to parse JSON trigger file: %w", err)
		}
	}

	return &triggerFile, nil
}
