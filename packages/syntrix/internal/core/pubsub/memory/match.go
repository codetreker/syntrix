package memory

import "strings"

// matchSubject checks if a subject matches a pattern.
// Supports NATS-style wildcards:
// - "*" matches a single token
// - ">" matches one or more tokens (must be last)
func matchSubject(pattern, subject string) bool {
	if pattern == "" || subject == "" {
		return false
	}

	patternParts := strings.Split(pattern, ".")
	subjectParts := strings.Split(subject, ".")

	for i, p := range patternParts {
		if p == ">" {
			// ">" must match at least one token
			return i < len(subjectParts)
		}
		if i >= len(subjectParts) {
			return false
		}
		if p != "*" && p != subjectParts[i] {
			return false
		}
	}
	return len(patternParts) == len(subjectParts)
}
