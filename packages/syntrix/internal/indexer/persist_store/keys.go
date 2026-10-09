package persist_store

import (
	"encoding/hex"
	"net/url"

	"github.com/cespare/xxhash/v2"
)

// Key prefixes for different data types in PebbleDB.
const (
	prefixIdx   = "idx/"          // Index data: idx/{db}/{hash}/{orderKey} → {docID}
	prefixRev   = "rev/"          // Reverse index: rev/{db}/{hash}/{docID} → {orderKey}
	prefixMap   = "map/"          // Hash mapping: map/{db}/{hash} → {pattern}|{tmplID}
	prefixMeta  = "meta/"         // Metadata prefix
	prefixState = "meta/state/"   // Index state: meta/state/{db}/{hash} → state
	keyProgress = "meta/progress" // Global checkpoint
)

// indexHash computes the hash for a (pattern, tmplID) pair.
// Returns a 16-char hex string (full xxHash64, no truncation).
func indexHash(pattern, tmplID string) string {
	h := xxhash.New()
	h.WriteString(pattern)
	h.WriteString("|")
	h.WriteString(tmplID)
	sum := h.Sum64()
	return hex.EncodeToString([]byte{
		byte(sum >> 56), byte(sum >> 48), byte(sum >> 40), byte(sum >> 32),
		byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum),
	})
}

// encodePathComponent URL-encodes a path component to avoid '/' conflicts.
func encodePathComponent(s string) string {
	return url.PathEscape(s)
}

// decodePathComponent URL-decodes a path component.
func decodePathComponent(s string) (string, error) {
	return url.PathUnescape(s)
}

// indexKey builds the key for index data.
// Format: idx/{db}/{hash}/{orderKey}
func indexKey(db, pattern, tmplID string, orderKey []byte) []byte {
	hash := indexHash(pattern, tmplID)
	// Estimate capacity: prefix(4) + db + / + hash(16) + / + orderKey
	key := make([]byte, 0, 4+len(db)+1+16+1+len(orderKey))
	key = append(key, prefixIdx...)
	key = append(key, encodePathComponent(db)...)
	key = append(key, '/')
	key = append(key, hash...)
	key = append(key, '/')
	key = append(key, orderKey...)
	return key
}

// indexKeyPrefix builds the prefix for scanning index data.
// Format: idx/{db}/{hash}/
func indexKeyPrefix(db, pattern, tmplID string) []byte {
	hash := indexHash(pattern, tmplID)
	prefix := make([]byte, 0, 4+len(db)+1+16+1)
	prefix = append(prefix, prefixIdx...)
	prefix = append(prefix, encodePathComponent(db)...)
	prefix = append(prefix, '/')
	prefix = append(prefix, hash...)
	prefix = append(prefix, '/')
	return prefix
}

// reverseKey builds the key for reverse index.
// Format: rev/{db}/{hash}/{docID}
func reverseKey(db, pattern, tmplID, docID string) []byte {
	hash := indexHash(pattern, tmplID)
	encodedDocID := encodePathComponent(docID)
	key := make([]byte, 0, 4+len(db)+1+16+1+len(encodedDocID))
	key = append(key, prefixRev...)
	key = append(key, encodePathComponent(db)...)
	key = append(key, '/')
	key = append(key, hash...)
	key = append(key, '/')
	key = append(key, encodedDocID...)
	return key
}

// reverseKeyPrefix builds the prefix for scanning reverse index.
// Format: rev/{db}/{hash}/
func reverseKeyPrefix(db, pattern, tmplID string) []byte {
	hash := indexHash(pattern, tmplID)
	prefix := make([]byte, 0, 4+len(db)+1+16+1)
	prefix = append(prefix, prefixRev...)
	prefix = append(prefix, encodePathComponent(db)...)
	prefix = append(prefix, '/')
	prefix = append(prefix, hash...)
	prefix = append(prefix, '/')
	return prefix
}

// mapKey builds the key for hash mapping.
// Format: map/{db}/{hash}
func mapKey(db, pattern, tmplID string) []byte {
	hash := indexHash(pattern, tmplID)
	key := make([]byte, 0, 4+len(db)+1+16)
	key = append(key, prefixMap...)
	key = append(key, encodePathComponent(db)...)
	key = append(key, '/')
	key = append(key, hash...)
	return key
}

// stateKey builds the key for index state.
// Format: meta/state/{db}/{hash}
func stateKey(db, pattern, tmplID string) []byte {
	hash := indexHash(pattern, tmplID)
	key := make([]byte, 0, 11+len(db)+1+16)
	key = append(key, prefixState...)
	key = append(key, encodePathComponent(db)...)
	key = append(key, '/')
	key = append(key, hash...)
	return key
}

// parseIndexKey extracts orderKey from an index key.
// The key format is: idx/{db}/{hash}/{orderKey}
// Returns the orderKey portion.
func parseIndexKey(key, prefix []byte) []byte {
	if len(key) <= len(prefix) {
		return nil
	}
	return key[len(prefix):]
}
