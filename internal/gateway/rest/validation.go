package rest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/helper"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// validate is the singleton validator instance used across all handlers.
var validate *validator.Validate

func init() {
	validate = validator.New()
}

// ValidationError wraps validation errors with user-friendly messages.
type ValidationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// ValidationErrors contains multiple validation errors.
type ValidationErrors struct {
	Errors []ValidationError `json:"errors"`
}

func (v ValidationErrors) Error() string {
	var msgs []string
	for _, e := range v.Errors {
		msgs = append(msgs, fmt.Sprintf("%s: %s", e.Field, e.Message))
	}
	return strings.Join(msgs, "; ")
}

// translateValidationError converts a validator.FieldError to a user-friendly message.
func translateValidationError(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "This field is required"
	case "min":
		return fmt.Sprintf("Must be at least %s characters", fe.Param())
	case "max":
		return fmt.Sprintf("Must be at most %s characters", fe.Param())
	case "alphanum":
		return "Must contain only letters and numbers"
	case "email":
		return "Must be a valid email address"
	case "url":
		return "Must be a valid URL"
	case "gte":
		return fmt.Sprintf("Must be greater than or equal to %s", fe.Param())
	case "lte":
		return fmt.Sprintf("Must be less than or equal to %s", fe.Param())
	case "gt":
		return fmt.Sprintf("Must be greater than %s", fe.Param())
	case "lt":
		return fmt.Sprintf("Must be less than %s", fe.Param())
	case "oneof":
		return fmt.Sprintf("Must be one of: %s", fe.Param())
	default:
		return fmt.Sprintf("Failed validation: %s", fe.Tag())
	}
}

// formatValidationErrors converts validator errors to ValidationErrors.
func formatValidationErrors(err error) ValidationErrors {
	var ve validator.ValidationErrors
	if !errors.As(err, &ve) {
		return ValidationErrors{
			Errors: []ValidationError{{Field: "unknown", Message: err.Error()}},
		}
	}

	var valErrors []ValidationError
	for _, fe := range ve {
		valErrors = append(valErrors, ValidationError{
			Field:   strings.ToLower(fe.Field()),
			Message: translateValidationError(fe),
		})
	}
	return ValidationErrors{Errors: valErrors}
}

// decodeAndValidate decodes a JSON request body and validates it.
// Returns the validated request struct or an error.
func decodeAndValidate[T any](r *http.Request) (*T, error) {
	var req T
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, fmt.Errorf("invalid request body: %w", err)
	}
	if err := validate.Struct(&req); err != nil {
		return nil, formatValidationErrors(err)
	}
	return &req, nil
}

// validateStruct validates a struct and returns user-friendly errors.
func validateStruct(s interface{}) error {
	if err := validate.Struct(s); err != nil {
		return formatValidationErrors(err)
	}
	return nil
}

// ValidationConfig holds configurable limits for validation
type ValidationConfig struct {
	MaxQueryLimit       int // Maximum allowed limit for queries (default: 1000)
	MaxReplicationLimit int // Maximum allowed limit for replication (default: 1000)
	MaxPathLength       int // Maximum allowed path length (default: 1024)
	MaxIDLength         int // Maximum allowed document ID length (default: 64)
}

// DefaultValidationConfig returns the default validation configuration
func DefaultValidationConfig() ValidationConfig {
	return ValidationConfig{
		MaxQueryLimit:       1000,
		MaxReplicationLimit: 1000,
		MaxPathLength:       1024,
		MaxIDLength:         64,
	}
}

// validationConfig is the package-level configuration used by validation functions
var validationConfig = DefaultValidationConfig()

// SetValidationConfig updates the validation configuration
func SetValidationConfig(cfg ValidationConfig) {
	if cfg.MaxQueryLimit <= 0 {
		cfg.MaxQueryLimit = DefaultValidationConfig().MaxQueryLimit
	}
	if cfg.MaxReplicationLimit <= 0 {
		cfg.MaxReplicationLimit = DefaultValidationConfig().MaxReplicationLimit
	}
	if cfg.MaxPathLength <= 0 {
		cfg.MaxPathLength = DefaultValidationConfig().MaxPathLength
	}
	if cfg.MaxIDLength <= 0 {
		cfg.MaxIDLength = DefaultValidationConfig().MaxIDLength
	}
	validationConfig = cfg
}

func validateQuery(q model.Query) error {
	if err := helper.CheckCollectionPath(q.Collection); err != nil {
		return fmt.Errorf("invalid collection: %w", err)
	}
	if q.Limit < 0 {
		return errors.New("limit cannot be negative")
	}
	if q.Limit > validationConfig.MaxQueryLimit {
		return fmt.Errorf("limit cannot exceed %d", validationConfig.MaxQueryLimit)
	}
	for _, f := range q.Filters {
		if f.Field == "" {
			return errors.New("filter field cannot be empty")
		}
		if f.Op == "" {
			return errors.New("filter op cannot be empty")
		}
		if !f.Op.IsValid() {
			return fmt.Errorf("unsupported filter operator: %s", f.Op)
		}
	}
	for _, o := range q.OrderBy {
		if o.Field == "" {
			return errors.New("orderby field cannot be empty")
		}
		if o.Direction != "asc" && o.Direction != "desc" {
			return errors.New("orderby direction must be 'asc' or 'desc'")
		}
	}
	return nil
}

func validateReplicationPull(database string, req storage.ReplicationPullRequest) error {
	return querycore.ValidatePullRequest(database, req)
}

func validateReplicationPush(database string, req storage.ReplicationPushRequest) error {
	return querycore.ValidatePushRequest(database, req)
}
