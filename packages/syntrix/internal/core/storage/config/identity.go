package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"unicode"

	"github.com/lib/pq"
)

// PostgresEndpointIdentity binds configured connection endpoints, independently
// of credentials and the current server selected by HA or DNS resolution.
func PostgresEndpointIdentity(dsn string) (string, error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := pq.ParseURL(dsn)
		if err != nil {
			return "", err
		}
		dsn = parsed
	}
	parameters, err := postgresParameters(dsn)
	if err != nil {
		return "", err
	}
	host := os.Getenv("PGHOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("PGPORT")
	if port == "" {
		port = "5432"
	}
	if configured, ok := parameters["host"]; ok {
		host = configured
	}
	if configured, ok := parameters["port"]; ok {
		port = configured
	}
	if !strings.HasPrefix(host, "/") {
		host = strings.ToLower(host)
	}
	hash := sha256.Sum256([]byte(strings.Join([]string{host, port, parameters["hostaddr"], parameters["service"]}, "\x00")))
	return hex.EncodeToString(hash[:]), nil
}

// lib/pq accepts whitespace around '=', single-quoted values, and backslash
// escapes in both quoted and unquoted values. Parse the full grammar so password
// contents cannot accidentally become part of an endpoint identity.
func postgresParameters(dsn string) (map[string]string, error) {
	runes := []rune(dsn)
	i := 0
	skip := func() {
		for i < len(runes) && unicode.IsSpace(runes[i]) {
			i++
		}
	}
	result := map[string]string{}
	for {
		skip()
		if i == len(runes) {
			return result, nil
		}
		start := i
		for i < len(runes) && !unicode.IsSpace(runes[i]) && runes[i] != '=' {
			i++
		}
		key := string(runes[start:i])
		skip()
		if key == "" || i == len(runes) || runes[i] != '=' {
			return nil, errors.New("invalid PostgreSQL connection options")
		}
		i++
		skip()
		value := []rune{}
		quoted := i < len(runes) && runes[i] == '\''
		if quoted {
			i++
		}
		closed := !quoted
		for i < len(runes) {
			ch := runes[i]
			if quoted && ch == '\'' {
				i++
				closed = true
				break
			}
			if !quoted && unicode.IsSpace(ch) {
				break
			}
			if ch == '\\' {
				i++
				if i == len(runes) {
					return nil, errors.New("invalid PostgreSQL connection escape")
				}
				ch = runes[i]
			}
			value = append(value, ch)
			i++
		}
		if !closed {
			return nil, errors.New("unterminated PostgreSQL connection value")
		}
		result[key] = string(value)
	}
}
