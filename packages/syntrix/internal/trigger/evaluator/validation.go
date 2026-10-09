package evaluator

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"

	"github.com/codetreker/syntrix/internal/trigger/types"
)

var (
	validNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
)

// ValidateTrigger checks if the trigger configuration is valid.
func ValidateTrigger(t *types.Trigger) error {
	if t.ID == "" {
		return errors.New("trigger id is required")
	}
	if !validNameRegex.MatchString(t.ID) {
		return fmt.Errorf("invalid trigger id: %s", t.ID)
	}
	if t.Timeout < 0 {
		return errors.New("timeout must not be negative")
	}

	if t.Database == "" {
		return errors.New("database is required")
	}
	if !validNameRegex.MatchString(t.Database) {
		return fmt.Errorf("invalid database: %s", t.Database)
	}
	if len(t.Database) > 128 {
		return fmt.Errorf("database name too long: %s", t.Database)
	}

	if t.Collection == "" {
		return errors.New("collection is required")
	}
	// Collection can be a glob, so we don't strictly enforce alphanumeric.
	// But we should check length.
	if len(t.Collection) > 128 {
		return fmt.Errorf("collection name too long: %s", t.Collection)
	}

	if len(t.Events) == 0 {
		return errors.New("at least one event is required")
	}
	for _, evt := range t.Events {
		switch evt {
		case "create", "update", "delete":
		default:
			return fmt.Errorf("invalid event type: %s", evt)
		}
	}

	if t.URL == "" {
		return errors.New("url is required")
	}

	// Validate URL format
	parsedURL, err := url.Parse(t.URL)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("url must use http or https scheme: %s", t.URL)
	}
	if parsedURL.Host == "" {
		return fmt.Errorf("url must have a host: %s", t.URL)
	}

	return nil
}
