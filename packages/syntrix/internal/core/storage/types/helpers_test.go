package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCalculateDatabase(t *testing.T) {
	id1 := CalculateDatabase("database1", "/path/to/doc1")
	id2 := CalculateDatabase("database1", "/path/to/doc1")
	id3 := CalculateDatabase("database2", "/path/to/doc1")
	id4 := CalculateDatabase("database1", "/path/to/doc2")

	assert.Equal(t, id1, id2)
	assert.NotEqual(t, id1, id3)
	assert.NotEqual(t, id1, id4)
	assert.True(t, strings.HasPrefix(id1, "database1:"))
}

func TestCalculateID(t *testing.T) {
	id1 := CalculateID("/path/to/doc1")
	id2 := CalculateID("/path/to/doc1")
	id3 := CalculateID("/path/to/doc2")

	assert.Equal(t, id1, id2, "Same path should generate same ID")
	assert.NotEqual(t, id1, id3, "Different paths should generate different IDs")
	assert.NotEmpty(t, id1)
}

func TestCalculateCollectionHash(t *testing.T) {
	h1 := CalculateCollectionHash("users")
	h2 := CalculateCollectionHash("users")
	h3 := CalculateCollectionHash("orders")

	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h1, h3)
	assert.NotEmpty(t, h1)
}

func TestNewDocument(t *testing.T) {
	data := map[string]interface{}{
		"key": "value",
	}
	doc := NewStoredDoc("database1", "users", "123", data)

	assert.Equal(t, "database1", doc.Database)
	assert.Equal(t, "users/123", doc.Fullpath)
	assert.Equal(t, "users", doc.Collection)
	assert.Equal(t, data, doc.Data)
	assert.NotEmpty(t, doc.Id)
	assert.Equal(t, CalculateDatabase("database1", "users/123"), doc.Id)
	assert.Equal(t, CalculateCollectionHash("users"), doc.CollectionHash)
	assert.NotZero(t, doc.CreatedAt)
	assert.NotZero(t, doc.UpdatedAt)
}

func TestNewDocument_NoSlashCollection(t *testing.T) {
	data := map[string]interface{}{"key": "value"}
	doc := NewStoredDoc("database1", "root", "", data)

	assert.Equal(t, "root", doc.Collection)
	assert.Empty(t, doc.Parent)
}

func TestResolveReadOptions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		options  []ReadOptions
		expected ReadOptions
		invalid  bool
	}{
		{name: "omitted"},
		{name: "empty", options: []ReadOptions{}},
		{name: "zero value", options: []ReadOptions{{}}},
		{name: "explicit default", options: []ReadOptions{{Consistency: ReadDefault}}},
		{name: "authoritative", options: []ReadOptions{{Consistency: ReadAuthoritative}}, expected: ReadOptions{Consistency: ReadAuthoritative}},
		{name: "bounded authoritative", options: []ReadOptions{{Consistency: ReadAuthoritative, ShowDeleted: true, MaxBytes: 1024}}, expected: ReadOptions{Consistency: ReadAuthoritative, ShowDeleted: true, MaxBytes: 1024}},
		{name: "negative byte budget", options: []ReadOptions{{MaxBytes: -1}}, invalid: true},
		{name: "unknown consistency", options: []ReadOptions{{Consistency: 255}}, invalid: true},
		{name: "duplicate default", options: []ReadOptions{{}, {}}, invalid: true},
		{name: "duplicate authoritative", options: []ReadOptions{{Consistency: ReadAuthoritative}, {Consistency: ReadAuthoritative}}, invalid: true},
		{name: "conflicting", options: []ReadOptions{{}, {Consistency: ReadAuthoritative}}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := ResolveReadOptions(tc.options)
			if tc.invalid {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestLogicalDocumentID(t *testing.T) {
	doc := NewStoredDoc("app", "users", "real", map[string]interface{}{})
	doc.Data["id"] = "business"
	doc.Id = "backend-specific-key"
	id, err := LogicalDocumentID(&doc)
	assert.NoError(t, err)
	assert.Equal(t, "real", id)
	doc.Deleted, doc.Data = true, nil
	id, err = LogicalDocumentID(&doc)
	assert.NoError(t, err)
	assert.Equal(t, "real", id)
	for name, mutate := range map[string]func(*StoredDoc){
		"database":   func(d *StoredDoc) { d.Database = "" },
		"scope":      func(d *StoredDoc) { d.Collection = "other" },
		"missing ID": func(d *StoredDoc) { d.Fullpath = "users/" },
		"nested ID":  func(d *StoredDoc) { d.Fullpath = "users/a/b" },
		"wildcard":   func(d *StoredDoc) { d.Collection = "*" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := doc
			mutate(&invalid)
			_, err := LogicalDocumentID(&invalid)
			assert.Error(t, err)
		})
	}
	_, err = LogicalDocumentID(nil)
	assert.Error(t, err)
}

func TestSourceScanRequestValidation(t *testing.T) {
	valid := SourceScanRequest{Collection: "groups/a/items", Limit: 10, AfterID: "a"}
	assert.NoError(t, valid.Validate("app"))
	for name, mutate := range map[string]func(*SourceScanRequest){
		"wildcard":               func(r *SourceScanRequest) { r.Collection = "users/*" },
		"empty segment":          func(r *SourceScanRequest) { r.Collection = "users//items" },
		"empty collection":       func(r *SourceScanRequest) { r.Collection = "" },
		"zero limit":             func(r *SourceScanRequest) { r.Limit = 0 },
		"negative bytes":         func(r *SourceScanRequest) { r.MaxBytes = -1 },
		"invalid consistency":    func(r *SourceScanRequest) { r.Consistency = -1 },
		"full path continuation": func(r *SourceScanRequest) { r.AfterID = "users/a" },
	} {
		t.Run(name, func(t *testing.T) { invalid := valid; mutate(&invalid); assert.Error(t, invalid.Validate("app")) })
	}
	assert.Error(t, valid.Validate(""))
}

func TestCollectionEnumerationAdmission(t *testing.T) {
	ordinary, err := ResolveCollectionEnumerationOptions("app", "", 1, nil)
	assert.NoError(t, err)
	assert.False(t, ordinary.IncludeSystem)

	allScopes, err := ResolveCollectionEnumerationOptions("app", "groups/a/items", 1, []CollectionEnumerationOptions{{IncludeSystem: true}})
	assert.NoError(t, err)
	assert.True(t, allScopes.IncludeSystem)

	for _, tc := range []struct {
		name     string
		database string
		after    string
		limit    int
		options  []CollectionEnumerationOptions
	}{
		{name: "database-wide enumeration requires explicit database", limit: 1},
		{name: "zero budget must not become an unbounded scan", database: "app"},
		{name: "negative budget", database: "app", limit: -1},
		{name: "cursor cannot select a wildcard scope", database: "app", after: "groups/*/items", limit: 1},
		{name: "cursor must be a canonical collection path", database: "app", after: "groups//items", limit: 1},
		{name: "conflicting visibility options", database: "app", limit: 1, options: []CollectionEnumerationOptions{{}, {IncludeSystem: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ResolveCollectionEnumerationOptions(tc.database, tc.after, tc.limit, tc.options)
			assert.Error(t, err)
		})
	}
}

func TestStoredDocumentBytes(t *testing.T) {
	missing, err := StoredDocumentBytes(nil)
	assert.NoError(t, err)
	assert.Zero(t, missing)
	doc := NewStoredDoc("app", "items", "a", map[string]interface{}{"number": int64(9007199254740993)})
	size, err := StoredDocumentBytes(&doc)
	assert.NoError(t, err)
	assert.Positive(t, size)
	doc.Data["number"] = int64(9007199254740992)
	adjacentSize, err := StoredDocumentBytes(&doc)
	assert.NoError(t, err)
	assert.Equal(t, size, adjacentSize)
	assert.Equal(t, int64(9007199254740992), doc.Data["number"])
	doc.Data["invalid"] = make(chan int)
	_, err = StoredDocumentBytes(&doc)
	assert.Error(t, err)
}
