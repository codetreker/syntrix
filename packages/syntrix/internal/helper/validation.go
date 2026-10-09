package helper

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	pathRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-\./]+$`)
)

// ValidationConfig holds configurable limits for validation
type ValidationConfig struct {
	MaxPathLength int // Maximum allowed path length (default: 1024)
	MaxIDLength   int // Maximum allowed document ID length (default: 64)
}

// DefaultValidationConfig returns the default validation configuration
func DefaultValidationConfig() ValidationConfig {
	return ValidationConfig{
		MaxPathLength: 1024,
		MaxIDLength:   128,
	}
}

// validationConfig is the package-level configuration used by validation functions
var validationConfig = DefaultValidationConfig()

// SetValidationConfig updates the validation configuration
func SetValidationConfig(cfg ValidationConfig) {
	if cfg.MaxPathLength <= 0 {
		cfg.MaxPathLength = DefaultValidationConfig().MaxPathLength
	}
	if cfg.MaxIDLength <= 0 {
		cfg.MaxIDLength = DefaultValidationConfig().MaxIDLength
	}
	validationConfig = cfg
}

func CheckPath(path string) error {
	if path == "" {
		return errors.New("path cannot be empty")
	}

	if len(path) > validationConfig.MaxPathLength {
		return fmt.Errorf("path length cannot exceed %d characters", validationConfig.MaxPathLength)
	}

	if !pathRegex.MatchString(path) {
		return errors.New("path contains invalid characters")
	}

	// Ensure path does not start or end with /
	if strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return errors.New("path cannot start or end with /")
	}

	// Ensure no double slashes
	if strings.Contains(path, "//") {
		return errors.New("path cannot contain empty segments")
	}

	return nil
}

func CheckDocumentPath(path string) error {
	if err := CheckPath(path); err != nil {
		return err
	}
	parts := strings.Split(path, "/")
	if len(parts)%2 != 0 || len(parts) < 2 {
		return errors.New("invalid document path: must have even number of segments (e.g. collection/doc)")
	}

	for i := 1; i < len(parts); i += 2 {
		if len(parts[i]) > validationConfig.MaxIDLength {
			return fmt.Errorf("document ID '%s' exceeds maximum length of %d characters", parts[i], validationConfig.MaxIDLength)
		}
	}
	return nil
}

func CheckCollectionPath(collection string) error {
	if err := CheckPath(collection); err != nil {
		return err
	}
	if len(strings.Split(collection, "/"))%2 != 0 {
		return nil
	}
	return errors.New("invalid collection path: must have odd number of segments (e.g. collection or collection/doc/subcollection)")
}

func ExplodeFullpath(path string) (collection string, docId string, err error) {
	if err := CheckPath(path); err != nil {
		return "", "", err
	}

	parts := strings.Split(path, "/")

	for i := 1; i < len(parts); i += 2 {
		if len(parts[i]) > validationConfig.MaxIDLength {
			return "", "", fmt.Errorf("document ID '%s' exceeds maximum length of %d characters", parts[i], validationConfig.MaxIDLength)
		}
	}

	if len(parts)%2 != 0 {
		return path, "", nil // It's a collection path
	}

	// It's a document path
	collection = strings.Join(parts[:len(parts)-1], "/")
	docId = parts[len(parts)-1]
	return collection, docId, nil
}
