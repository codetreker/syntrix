package authz

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStripDatabasePrefix(t *testing.T) {
	// Case 1: With prefix
	assert.Equal(t, "users/123", stripDatabasePrefix("/projects/p1/databases/d1/documents/users/123"))

	// Case 2: Without prefix
	assert.Equal(t, "users/123", stripDatabasePrefix("users/123"))

	// Case 3: Empty
	assert.Equal(t, "", stripDatabasePrefix(""))
}
