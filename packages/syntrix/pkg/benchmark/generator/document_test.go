package generator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDocumentGenerator(t *testing.T) {
	tests := []struct {
		name         string
		fieldsCount  int
		documentSize string
		expectError  bool
	}{
		{"valid 1KB", 10, "1KB", false},
		{"valid 10KB", 20, "10KB", false},
		{"valid 1MB", 50, "1MB", false},
		{"valid bytes", 5, "512B", false},
		{"invalid format", 10, "invalid", true},
		{"empty size (default)", 10, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gen, err := NewDocumentGenerator(tt.fieldsCount, tt.documentSize)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, gen)
				assert.Equal(t, tt.fieldsCount, gen.fieldsCount)
			}
		})
	}
}

func TestDocumentGenerator_Generate(t *testing.T) {
	gen, err := NewDocumentGenerator(10, "1KB")
	require.NoError(t, err)

	doc, err := gen.Generate()
	require.NoError(t, err)
	require.NotNil(t, doc)

	// Check that document has the expected fields plus standard fields
	assert.GreaterOrEqual(t, len(doc), 10)

	// Check standard fields exist
	assert.Contains(t, doc, "created_at")
	assert.Contains(t, doc, "benchmark_id")

	// Check field types
	createdAt, ok := doc["created_at"].(int64)
	assert.True(t, ok)
	assert.Greater(t, createdAt, int64(0))

	benchmarkID, ok := doc["benchmark_id"].(string)
	assert.True(t, ok)
	assert.NotEmpty(t, benchmarkID)

	// Check that fields have different types
	foundString := false
	foundNumber := false
	foundBool := false
	foundFloat := false
	foundObject := false

	for key, value := range doc {
		if key == "created_at" || key == "benchmark_id" {
			continue
		}

		switch value.(type) {
		case string:
			foundString = true
		case int64:
			foundNumber = true
		case bool:
			foundBool = true
		case float64:
			foundFloat = true
		case map[string]interface{}:
			foundObject = true
		}
	}

	// We should have at least some variety in field types
	assert.True(t, foundString || foundNumber || foundBool || foundFloat || foundObject,
		"Document should have varied field types")
}

func TestDocumentGenerator_GenerateBatch(t *testing.T) {
	gen, err := NewDocumentGenerator(5, "512B")
	require.NoError(t, err)

	count := 10
	docs, err := gen.GenerateBatch(count)
	require.NoError(t, err)
	assert.Len(t, docs, count)

	// Check that all documents are unique (different benchmark_id)
	ids := make(map[string]bool)
	for _, doc := range docs {
		benchmarkID, ok := doc["benchmark_id"].(string)
		assert.True(t, ok)
		assert.NotContains(t, ids, benchmarkID, "Document IDs should be unique")
		ids[benchmarkID] = true
	}
}

func TestGenerateRandomString(t *testing.T) {
	tests := []struct {
		name   string
		length int
	}{
		{"zero length", 0},
		{"small", 10},
		{"medium", 100},
		{"large", 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			str := generateRandomString(tt.length)
			if tt.length == 0 {
				assert.Empty(t, str)
			} else {
				assert.Equal(t, tt.length, len(str))
				// Check that string contains only alphanumeric characters
				for _, ch := range str {
					assert.True(t, (ch >= 'a' && ch <= 'z') ||
						(ch >= 'A' && ch <= 'Z') ||
						(ch >= '0' && ch <= '9'),
						"String should contain only alphanumeric characters")
				}
			}
		})
	}

	// Test uniqueness
	str1 := generateRandomString(20)
	str2 := generateRandomString(20)
	assert.NotEqual(t, str1, str2, "Random strings should be different")
}

func TestGenerateRandomID(t *testing.T) {
	id1 := GenerateRandomID()
	id2 := GenerateRandomID()

	assert.NotEmpty(t, id1)
	assert.NotEmpty(t, id2)
	assert.Len(t, id1, 22)
	assert.Len(t, id2, 22)
	assert.NotEqual(t, id1, id2, "IDs should be unique")
}

func TestParseSizeString(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    int
		expectError bool
	}{
		{"bytes", "100B", 100, false},
		{"kilobytes", "1KB", 1024, false},
		{"megabytes", "2MB", 2 * 1024 * 1024, false},
		{"gigabytes", "1GB", 1024 * 1024 * 1024, false},
		{"lowercase kb", "5kb", 5 * 1024, false},
		{"with spaces", " 10 KB ", 10 * 1024, false},
		{"just number", "10K", 10 * 1024, false},
		{"empty (default)", "", 1024, false},
		{"invalid format", "abc", 0, true},
		{"invalid unit", "10XB", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseSizeString(tt.input)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestIDGenerator_Sequential(t *testing.T) {
	gen := NewIDGenerator("test-", false)

	id1 := gen.Next()
	id2 := gen.Next()
	id3 := gen.Next()

	assert.Equal(t, "test-000001", id1)
	assert.Equal(t, "test-000002", id2)
	assert.Equal(t, "test-000003", id3)
}

func TestIDGenerator_Random(t *testing.T) {
	gen := NewIDGenerator("doc-", true)

	id1 := gen.Next()
	id2 := gen.Next()

	assert.True(t, len(id1) > 4)
	assert.True(t, len(id2) > 4)
	assert.NotEqual(t, id1, id2, "Random IDs should be different")
	assert.Contains(t, id1, "doc-")
	assert.Contains(t, id2, "doc-")
}

func TestIDGenerator_BatchNext(t *testing.T) {
	gen := NewIDGenerator("batch-", false)

	ids := gen.BatchNext(5)
	assert.Len(t, ids, 5)

	// Check sequential
	assert.Equal(t, "batch-000001", ids[0])
	assert.Equal(t, "batch-000002", ids[1])
	assert.Equal(t, "batch-000003", ids[2])
	assert.Equal(t, "batch-000004", ids[3])
	assert.Equal(t, "batch-000005", ids[4])
}

func TestIDGenerator_BatchNextRandom(t *testing.T) {
	gen := NewIDGenerator("rand-", true)

	ids := gen.BatchNext(10)
	assert.Len(t, ids, 10)

	// Check all IDs are unique
	uniqueIDs := make(map[string]bool)
	for _, id := range ids {
		assert.NotContains(t, uniqueIDs, id, "IDs should be unique")
		uniqueIDs[id] = true
		assert.Contains(t, id, "rand-")
	}
}

func TestDocumentGenerator_FieldVariety(t *testing.T) {
	gen, err := NewDocumentGenerator(20, "2KB")
	require.NoError(t, err)

	doc, err := gen.Generate()
	require.NoError(t, err)

	// Count different field types
	stringCount := 0
	numberCount := 0
	boolCount := 0
	floatCount := 0
	objectCount := 0

	for key, value := range doc {
		if key == "created_at" || key == "benchmark_id" {
			continue
		}

		switch value.(type) {
		case string:
			stringCount++
		case int64:
			numberCount++
		case bool:
			boolCount++
		case float64:
			floatCount++
		case map[string]interface{}:
			objectCount++
		}
	}

	// With 20 fields, we should have at least 3 of each type (except objects which appear less frequently)
	assert.GreaterOrEqual(t, stringCount, 3, "Should have multiple string fields")
	assert.GreaterOrEqual(t, numberCount, 3, "Should have multiple number fields")
	assert.GreaterOrEqual(t, boolCount, 3, "Should have multiple boolean fields")
	assert.GreaterOrEqual(t, floatCount, 3, "Should have multiple float fields")
	assert.GreaterOrEqual(t, objectCount, 1, "Should have at least one nested object")
}
