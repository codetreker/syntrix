package types

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/syntrixbase/syntrix/pkg/model"
	"github.com/zeebo/blake3"
	"go.mongodb.org/mongo-driver/bson"
)

// ResolveReadOptions preserves default routing only when no other consistency is requested.
func ResolveReadOptions(opts []ReadOptions) (ReadOptions, error) {
	if len(opts) > 1 {
		return ReadOptions{}, fmt.Errorf("read accepts at most one ReadOptions value")
	}
	var resolved ReadOptions
	if len(opts) == 1 {
		resolved = opts[0]
	}
	if resolved.MaxBytes < 0 {
		return ReadOptions{}, fmt.Errorf("read byte budget must not be negative")
	}
	switch resolved.Consistency {
	case ReadDefault, ReadAuthoritative:
		return resolved, nil
	default:
		return ReadOptions{}, fmt.Errorf("unsupported read consistency: %d", resolved.Consistency)
	}
}

// StoredDocumentBytes defines the source read budget in BSON bytes, preserving
// integer types and authoritative metadata. Missing result positions cost zero.
func StoredDocumentBytes(doc *StoredDoc) (int64, error) {
	if doc == nil {
		return 0, nil
	}
	encoded, err := bson.Marshal(doc)
	if err != nil {
		return 0, err
	}
	return int64(len(encoded)), nil
}

// LogicalDocumentID derives identity from stored metadata, which survives tombstones.
// Business data, including data.id, is not an identity authority.
func LogicalDocumentID(doc *StoredDoc) (string, error) {
	if doc == nil || doc.Database == "" {
		return "", fmt.Errorf("document identity requires a database")
	}
	if err := ValidateConcreteCollection(doc.Collection); err != nil {
		return "", err
	}
	prefix := doc.Collection + "/"
	if !strings.HasPrefix(doc.Fullpath, prefix) {
		return "", fmt.Errorf("document fullpath does not belong to its collection")
	}
	id := strings.TrimPrefix(doc.Fullpath, prefix)
	if id == "" || strings.ContainsAny(id, "/\x00") {
		return "", fmt.Errorf("document fullpath requires one logical ID segment")
	}
	return id, nil
}

func ValidateConcreteCollection(collection string) error {
	if collection == "" || strings.ContainsAny(collection, "*\x00") {
		return fmt.Errorf("source scan requires a concrete collection")
	}
	for _, segment := range strings.Split(collection, "/") {
		if segment == "" {
			return fmt.Errorf("source scan collection contains an empty path segment")
		}
	}
	return nil
}

func (request SourceScanRequest) Validate(database string) error {
	if database == "" {
		return fmt.Errorf("source scan requires a database")
	}
	if err := ValidateConcreteCollection(request.Collection); err != nil {
		return err
	}
	if strings.ContainsAny(request.AfterID, "/\x00") || request.Limit <= 0 || request.Limit > 2147483647 || request.MaxBytes < 0 {
		return fmt.Errorf("invalid source scan continuation or budget")
	}
	_, err := ResolveReadOptions([]ReadOptions{{Consistency: request.Consistency}})
	return err
}

func ResolveCollectionEnumerationOptions(database, afterCollection string, limit int, opts []CollectionEnumerationOptions) (CollectionEnumerationOptions, error) {
	if database == "" || limit <= 0 || limit > 2147483647 || len(opts) > 1 {
		return CollectionEnumerationOptions{}, fmt.Errorf("invalid collection enumeration scope, limit, or options")
	}
	if afterCollection != "" {
		if err := ValidateConcreteCollection(afterCollection); err != nil {
			return CollectionEnumerationOptions{}, err
		}
	}
	if len(opts) == 1 {
		return opts[0], nil
	}
	return CollectionEnumerationOptions{}, nil
}

// CalculateDatabase calculates the database-aware document ID
// Format: database:hash(fullpath)
func CalculateDatabase(database, fullpath string) string {
	hash := blake3.Sum256([]byte(fullpath))
	hashStr := hex.EncodeToString(hash[:16])
	return database + ":" + hashStr
}

// CalculateID calculates the document ID (hash) from the full path
// Deprecated: Use CalculateDatabase instead
func CalculateID(fullpath string) string {
	hash := blake3.Sum256([]byte(fullpath))
	return hex.EncodeToString(hash[:16])
}

// CalculateCollectionHash calculates a stable hash for a collection name.
// A prefix keeps the namespace distinct from document IDs.
func CalculateCollectionHash(collection string) string {
	hash := blake3.Sum256([]byte("collection:" + collection))
	return hex.EncodeToString(hash[:16])
}

func NewStoredDoc(database, collection, docid string, data map[string]interface{}) StoredDoc {
	// Calculate Parent from collection path
	parent := ""
	if idx := strings.LastIndex(collection, "/"); idx != -1 {
		parent = collection[:idx]
	}

	if data == nil {
		data = make(map[string]interface{})
	}

	if id, exists := data["id"]; !exists || id != docid {
		data["id"] = docid
	}

	model.StripProtectedFields(data)

	fullpath := collection + "/" + docid
	id := CalculateDatabase(database, fullpath)
	collectionHash := CalculateCollectionHash(collection)

	now := time.Now().UnixMilli()

	return StoredDoc{
		Id:             id,
		Database:       database,
		Fullpath:       fullpath,
		Collection:     collection,
		CollectionHash: collectionHash,
		Parent:         parent,
		Data:           data,
		UpdatedAt:      now,
		CreatedAt:      now,
		Version:        1,
	}
}
