package persist_store

import (
	"testing"
)

func TestIndexHash(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		tmplID  string
	}{
		{
			name:    "simple pattern",
			pattern: "users/*",
			tmplID:  "created_at",
		},
		{
			name:    "complex pattern",
			pattern: "organizations/*/projects/*/tasks/*",
			tmplID:  "priority_due_date",
		},
		{
			name:    "empty pattern",
			pattern: "",
			tmplID:  "default",
		},
		{
			name:    "unicode pattern",
			pattern: "用户/*",
			tmplID:  "创建时间",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := indexHash(tt.pattern, tt.tmplID)

			// Should be 16 hex chars (64 bits / 4 bits per hex char)
			if len(hash) != 16 {
				t.Errorf("hash length = %d, want 16", len(hash))
			}

			// Should be valid hex
			for _, c := range hash {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
					t.Errorf("invalid hex char: %c", c)
				}
			}

			// Same inputs should produce same hash
			hash2 := indexHash(tt.pattern, tt.tmplID)
			if hash != hash2 {
				t.Errorf("hash not deterministic: %s != %s", hash, hash2)
			}
		})
	}
}

func TestIndexHashUniqueness(t *testing.T) {
	// Different inputs should produce different hashes (with high probability)
	pairs := [][2]string{
		{"users/*", "created_at"},
		{"users/*", "updated_at"},
		{"posts/*", "created_at"},
		{"users/*/posts/*", "created_at"},
	}

	hashes := make(map[string]bool)
	for _, pair := range pairs {
		hash := indexHash(pair[0], pair[1])
		if hashes[hash] {
			t.Errorf("hash collision for pattern=%s, tmplID=%s", pair[0], pair[1])
		}
		hashes[hash] = true
	}
}

func TestEncodeDecodePathComponent(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"simple", "mydb"},
		{"with slash", "my/db"},
		{"with spaces", "my db"},
		{"with special chars", "my%db&test=1"},
		{"unicode", "数据库"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded := encodePathComponent(tt.input)
			decoded, err := decodePathComponent(encoded)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if decoded != tt.input {
				t.Errorf("roundtrip failed: got %q, want %q", decoded, tt.input)
			}
		})
	}
}

func TestIndexKey(t *testing.T) {
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	orderKey := []byte{0x00, 0x01, 0x02, 0x03}

	key := indexKey(db, pattern, tmplID, orderKey)

	// Should start with idx/ prefix
	if string(key[:4]) != "idx/" {
		t.Errorf("key should start with 'idx/', got: %s", string(key[:10]))
	}

	// Should contain the db name
	if string(key[4:4+len(db)]) != db {
		t.Errorf("key should contain db name")
	}

	// Should end with orderKey
	keyLen := len(key)
	if string(key[keyLen-len(orderKey):]) != string(orderKey) {
		t.Errorf("key should end with orderKey")
	}
}

func TestIndexKeyPrefix(t *testing.T) {
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	prefix := indexKeyPrefix(db, pattern, tmplID)

	// Should start with idx/ prefix
	if string(prefix[:4]) != "idx/" {
		t.Errorf("prefix should start with 'idx/'")
	}

	// Should end with /
	if prefix[len(prefix)-1] != '/' {
		t.Errorf("prefix should end with '/'")
	}

	// Full key should have same prefix
	orderKey := []byte{0x00, 0x01, 0x02}
	fullKey := indexKey(db, pattern, tmplID, orderKey)
	if string(fullKey[:len(prefix)]) != string(prefix) {
		t.Errorf("full key should start with prefix")
	}
}

func TestReverseKey(t *testing.T) {
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "user123"

	key := reverseKey(db, pattern, tmplID, docID)

	// Should start with rev/ prefix
	if string(key[:4]) != "rev/" {
		t.Errorf("key should start with 'rev/'")
	}

	// Same inputs should produce same key
	key2 := reverseKey(db, pattern, tmplID, docID)
	if string(key) != string(key2) {
		t.Errorf("key not deterministic")
	}
}

func TestReverseKeyPrefix(t *testing.T) {
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	prefix := reverseKeyPrefix(db, pattern, tmplID)

	// Should start with rev/ prefix
	if string(prefix[:4]) != "rev/" {
		t.Errorf("prefix should start with 'rev/'")
	}

	// Should end with /
	if prefix[len(prefix)-1] != '/' {
		t.Errorf("prefix should end with '/'")
	}
}

func TestMapKey(t *testing.T) {
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	key := mapKey(db, pattern, tmplID)

	// Should start with map/ prefix
	if string(key[:4]) != "map/" {
		t.Errorf("key should start with 'map/'")
	}
}

func TestStateKey(t *testing.T) {
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"

	key := stateKey(db, pattern, tmplID)

	// Should start with meta/state/ prefix
	if string(key[:11]) != "meta/state/" {
		t.Errorf("key should start with 'meta/state/'")
	}
}

func TestParseIndexKey(t *testing.T) {
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	orderKey := []byte{0x00, 0x01, 0x02, 0x03, 0x04}

	prefix := indexKeyPrefix(db, pattern, tmplID)
	fullKey := indexKey(db, pattern, tmplID, orderKey)

	extracted := parseIndexKey(fullKey, prefix)
	if string(extracted) != string(orderKey) {
		t.Errorf("extracted orderKey = %v, want %v", extracted, orderKey)
	}
}

func TestParseIndexKeyEdgeCases(t *testing.T) {
	prefix := []byte("idx/testdb/abcd1234abcd1234/")

	// Key shorter than prefix
	shortKey := []byte("idx/test")
	if result := parseIndexKey(shortKey, prefix); result != nil {
		t.Errorf("short key should return nil, got %v", result)
	}

	// Key equal to prefix length
	equalKey := prefix
	if result := parseIndexKey(equalKey, prefix); result != nil {
		t.Errorf("equal length key should return nil, got %v", result)
	}
}

func TestKeyConsistency(t *testing.T) {
	// Same pattern+tmplID should produce same hash in all key functions
	db := "testdb"
	pattern := "users/*"
	tmplID := "created_at"
	docID := "doc1"
	orderKey := []byte{0x01, 0x02}

	idxKey := indexKey(db, pattern, tmplID, orderKey)
	revKey := reverseKey(db, pattern, tmplID, docID)
	mKey := mapKey(db, pattern, tmplID)
	sKey := stateKey(db, pattern, tmplID)

	// Extract hash from each key (after db/ component)
	// idx/{db}/{hash}/... -> hash starts at position 4+len(db)+1
	hashStart := 4 + len(db) + 1
	hashLen := 16

	idxHash := string(idxKey[hashStart : hashStart+hashLen])
	revHash := string(revKey[hashStart : hashStart+hashLen])
	mapHash := string(mKey[hashStart : hashStart+hashLen])
	// state key has longer prefix: meta/state/{db}/{hash}
	stateHashStart := 11 + len(db) + 1
	stateHash := string(sKey[stateHashStart : stateHashStart+hashLen])

	if idxHash != revHash || idxHash != mapHash || idxHash != stateHash {
		t.Errorf("hashes inconsistent: idx=%s, rev=%s, map=%s, state=%s",
			idxHash, revHash, mapHash, stateHash)
	}
}
