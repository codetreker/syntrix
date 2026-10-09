// Package generator provides test data generation for benchmark operations.
package generator

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// DocumentGenerator generates random documents for testing.
type DocumentGenerator struct {
	fieldsCount  int
	documentSize int // Approximate size in bytes
}

// NewDocumentGenerator creates a new document generator.
func NewDocumentGenerator(fieldsCount int, documentSize string) (*DocumentGenerator, error) {
	size, err := parseSizeString(documentSize)
	if err != nil {
		return nil, fmt.Errorf("invalid document size: %w", err)
	}

	return &DocumentGenerator{
		fieldsCount:  fieldsCount,
		documentSize: size,
	}, nil
}

// Generate creates a random document with the configured number of fields.
func (g *DocumentGenerator) Generate() (map[string]interface{}, error) {
	doc := make(map[string]interface{}, g.fieldsCount)

	// Calculate approximate size per field
	sizePerField := g.documentSize / g.fieldsCount
	if sizePerField < 10 {
		sizePerField = 10 // Minimum size per field
	}

	for i := 0; i < g.fieldsCount; i++ {
		fieldName := fmt.Sprintf("field_%d", i)
		fieldValue := g.generateFieldValue(sizePerField, i)
		doc[fieldName] = fieldValue
	}

	// Add some standard fields
	doc["created_at"] = time.Now().Unix()
	doc["benchmark_id"] = generateRandomString(8)

	return doc, nil
}

// generateFieldValue generates a field value of approximately the given size.
func (g *DocumentGenerator) generateFieldValue(targetSize int, fieldIndex int) interface{} {
	// Vary field types to make data more realistic
	switch fieldIndex % 5 {
	case 0:
		// String field
		return generateRandomString(targetSize / 2) // UTF-8 chars are ~2 bytes
	case 1:
		// Number field
		n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
		return n.Int64()
	case 2:
		// Boolean field
		n, _ := rand.Int(rand.Reader, big.NewInt(2))
		return n.Int64() == 1
	case 3:
		// Float field
		n, _ := rand.Int(rand.Reader, big.NewInt(100000))
		return float64(n.Int64()) / 100.0
	default:
		// Nested object (every 5th field)
		return map[string]interface{}{
			"nested_string": generateRandomString(targetSize / 4),
			"nested_number": fieldIndex * 100,
		}
	}
}

// GenerateBatch generates multiple documents at once.
func (g *DocumentGenerator) GenerateBatch(count int) ([]map[string]interface{}, error) {
	docs := make([]map[string]interface{}, count)
	for i := 0; i < count; i++ {
		doc, err := g.Generate()
		if err != nil {
			return nil, fmt.Errorf("failed to generate document %d: %w", i, err)
		}
		docs[i] = doc
	}
	return docs, nil
}

// generateRandomString generates a random alphanumeric string of the given length.
func generateRandomString(length int) string {
	if length <= 0 {
		return ""
	}

	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	for i := range b {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		b[i] = charset[n.Int64()]
	}
	return string(b)
}

// GenerateRandomID generates a random document ID.
func GenerateRandomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.URLEncoding.EncodeToString(b)[:22] // 22 chars, URL-safe
}

// parseSizeString parses size strings like "1KB", "10MB" into bytes.
func parseSizeString(sizeStr string) (int, error) {
	sizeStr = strings.TrimSpace(strings.ToUpper(sizeStr))

	if sizeStr == "" {
		return 1024, nil // Default 1KB
	}

	// Extract number and unit
	var num int
	var unit string

	if _, err := fmt.Sscanf(sizeStr, "%d%s", &num, &unit); err != nil {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	multiplier := 1
	switch unit {
	case "B", "BYTES":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit: %s", unit)
	}

	return num * multiplier, nil
}

// IDGenerator generates sequential or random IDs for documents.
type IDGenerator struct {
	prefix  string
	counter int
	random  bool
}

// NewIDGenerator creates a new ID generator.
func NewIDGenerator(prefix string, random bool) *IDGenerator {
	return &IDGenerator{
		prefix:  prefix,
		counter: 0,
		random:  random,
	}
}

// Next generates the next ID.
func (g *IDGenerator) Next() string {
	if g.random {
		return g.prefix + GenerateRandomID()
	}

	g.counter++
	return fmt.Sprintf("%s%06d", g.prefix, g.counter)
}

// BatchNext generates multiple IDs at once.
func (g *IDGenerator) BatchNext(count int) []string {
	ids := make([]string, count)
	for i := 0; i < count; i++ {
		ids[i] = g.Next()
	}
	return ids
}
