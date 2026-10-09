package model

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
)

var (
	idRegex = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]{1,64}$`)
)

func CheckDocumentID(id string) bool {
	return idRegex.MatchString(id)
}

func StripProtectedFields(doc Document) {
	delete(doc, "version")
	delete(doc, "updatedAt")
	delete(doc, "createdAt")
	delete(doc, "collection")
	delete(doc, "deleted")
}

// User facing document type, represents a JSON object.
//
//	"id" field is reserved for document ID.
//	"version" field is reserved for document version.
//	"updatedAt" field is reserved for last updated timestamp.
//	"createdAt" field is reserved for creation timestamp.
//	"collection" field is reserved for collection name.
type Document map[string]interface{}

func (doc Document) GetID() string {
	if id, ok := doc["id"].(string); ok {
		return id
	}
	return ""
}

func (doc Document) SetID(newID string) {
	doc["id"] = newID
}

func (doc Document) GenerateIDIfEmpty() {
	if _, ok := doc["id"]; !ok {
		doc["id"] = uuid.New().String()
	}
}

func (doc Document) GetCollection() string {
	if collection, ok := doc["collection"].(string); ok {
		return collection
	}
	return ""
}

func (doc Document) SetCollection(collection string) {
	doc["collection"] = collection
}

func (doc Document) HasVersion() bool {
	_, exists := doc["version"]
	return exists
}

func (doc Document) GetVersion() int64 {
	if v, ok := doc["version"].(float64); ok {
		return int64(v)
	}

	return -1
}

func (doc Document) HasKey(key string) bool {
	_, exists := doc[key]
	return exists
}

func (doc Document) StripProtectedFields() {
	StripProtectedFields(doc)
}

func (doc Document) IsEmpty() bool {
	return len(doc) == 1 && doc.HasKey("id")
}

func (doc Document) ValidateDocument() error {
	if doc == nil {
		return errors.New("data cannot be nil")
	}

	if idVal, ok := doc["id"]; ok {
		switch idValue := idVal.(type) {
		case string:
			if idValue == "" {
				return errors.New("data field 'id' cannot be empty")
			}

			if !idRegex.MatchString(idVal.(string)) {
				return errors.New("invalid 'id' field: must be 1-64 characters of a-z, A-Z, 0-9, _, ., -")
			}
		case int, int32, int64:
			doc["id"] = fmt.Sprintf("%d", idValue)
		default:
			return errors.New("data field 'id' must be a string or integer")
		}
	}

	return nil
}

func (doc Document) IsDeleted() bool {
	if deleted, ok := doc["deleted"].(bool); ok && deleted {
		return true
	}
	return false
}
