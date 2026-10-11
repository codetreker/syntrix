package config

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

type Developer struct {
	ID       string `yaml:"id"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type Config struct {
	Developers []Developer   `yaml:"developers"`
	SessionTTL time.Duration `yaml:"session_ttl"`
}

func DefaultConfig() Config {
	return Config{SessionTTL: 24 * time.Hour}
}

func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

func (c Config) Validate() error {
	if c.SessionTTL <= 0 || c.SessionTTL > 30*24*time.Hour {
		return fmt.Errorf("auth session_ttl must be positive and no greater than 30 days")
	}
	if len(c.Developers) == 0 {
		return fmt.Errorf("auth developers must contain at least one bootstrap account")
	}
	ids := make(map[string]struct{}, len(c.Developers))
	usernames := make(map[string]struct{}, len(c.Developers))
	for i, developer := range c.Developers {
		if developer.ID == "" || len(developer.ID) > 128 || !utf8.ValidString(developer.ID) || strings.TrimSpace(developer.ID) != developer.ID || strings.ContainsRune(developer.ID, 0) {
			return fmt.Errorf("auth developers[%d].id must be a UTF-8 identifier of 1 through 128 bytes without surrounding whitespace or NUL", i)
		}
		username := NormalizeUsername(developer.Username)
		if username == "" || len(username) > 128 || !utf8.ValidString(developer.Username) || strings.ContainsRune(username, 0) {
			return fmt.Errorf("auth developers[%d].username must normalize to 1 through 128 UTF-8 bytes without NUL", i)
		}
		if len(developer.Password) == 0 || len(developer.Password) > 72 {
			return fmt.Errorf("auth developers[%d].password must contain 1 through 72 bytes", i)
		}
		if _, exists := ids[developer.ID]; exists {
			return fmt.Errorf("auth developers[%d].id duplicates another bootstrap account", i)
		}
		if _, exists := usernames[username]; exists {
			return fmt.Errorf("auth developers[%d].username duplicates another normalized bootstrap account", i)
		}
		ids[developer.ID] = struct{}{}
		usernames[username] = struct{}{}
	}
	return nil
}
