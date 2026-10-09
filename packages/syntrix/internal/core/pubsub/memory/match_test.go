package memory

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchSubject_Exact(t *testing.T) {
	assert.True(t, matchSubject("foo", "foo"))
	assert.True(t, matchSubject("foo.bar", "foo.bar"))
	assert.True(t, matchSubject("foo.bar.baz", "foo.bar.baz"))
}

func TestMatchSubject_NoMatch(t *testing.T) {
	assert.False(t, matchSubject("foo", "bar"))
	assert.False(t, matchSubject("foo.bar", "foo.baz"))
	assert.False(t, matchSubject("foo.bar", "foo.bar.baz"))
	assert.False(t, matchSubject("foo.bar.baz", "foo.bar"))
}

func TestMatchSubject_SingleWildcard(t *testing.T) {
	// * matches exactly one token
	assert.True(t, matchSubject("foo.*", "foo.bar"))
	assert.True(t, matchSubject("*.bar", "foo.bar"))
	assert.True(t, matchSubject("foo.*.baz", "foo.bar.baz"))

	// * does not match multiple tokens
	assert.False(t, matchSubject("foo.*", "foo.bar.baz"))
	assert.False(t, matchSubject("*", "foo.bar"))
}

func TestMatchSubject_MultiWildcard(t *testing.T) {
	// > matches one or more tokens
	assert.True(t, matchSubject("foo.>", "foo.bar"))
	assert.True(t, matchSubject("foo.>", "foo.bar.baz"))
	assert.True(t, matchSubject("foo.>", "foo.bar.baz.qux"))
	assert.True(t, matchSubject(">", "foo"))
	assert.True(t, matchSubject(">", "foo.bar.baz"))

	// > must match at least one token
	assert.False(t, matchSubject("foo.bar.>", "foo.bar"))
}

func TestMatchSubject_Mixed(t *testing.T) {
	// Combination of * and >
	assert.True(t, matchSubject("foo.*.>", "foo.bar.baz"))
	assert.True(t, matchSubject("foo.*.>", "foo.bar.baz.qux"))
	assert.True(t, matchSubject("*.bar.>", "foo.bar.baz"))

	assert.False(t, matchSubject("foo.*.>", "foo.bar"))
}

func TestMatchSubject_Empty(t *testing.T) {
	assert.False(t, matchSubject("", "foo"))
	assert.False(t, matchSubject("foo", ""))
	assert.False(t, matchSubject("", ""))
}

func TestMatchSubject_NATS_Examples(t *testing.T) {
	// Examples from plan
	assert.True(t, matchSubject("TRIGGERS.>", "TRIGGERS.db1.users.doc1"))
	assert.True(t, matchSubject("TRIGGERS.*", "TRIGGERS.db1"))
	assert.False(t, matchSubject("TRIGGERS.*", "TRIGGERS.db1.users"))
}
