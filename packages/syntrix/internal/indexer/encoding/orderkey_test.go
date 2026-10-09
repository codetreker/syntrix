package encoding

import (
	"bytes"
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncode_Version(t *testing.T) {
	key, err := Encode(nil, "doc1")
	require.NoError(t, err)
	assert.Equal(t, Version, key[0], "first byte should be version")
}

func TestEncode_DocID(t *testing.T) {
	key, err := Encode(nil, "hello")
	require.NoError(t, err)
	id, err := ExtractDocID(key)
	require.NoError(t, err)
	assert.Equal(t, "hello", id)
}

func TestEncode_TypeTags(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		wantTag byte
	}{
		{"null", nil, TypeNull},
		{"bool", true, TypeBool},
		{"number", 42.0, TypeNumber},
		{"string", "test", TypeString},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := Encode([]Field{{Value: tt.value, Direction: Asc}}, "id")
			require.NoError(t, err)
			assert.Equal(t, tt.wantTag, key[1], "type tag mismatch")
		})
	}
}

func TestEncode_TypeOrdering(t *testing.T) {
	// Type ordering should be: null < bool < number < string
	nullKey, _ := Encode([]Field{{Value: nil, Direction: Asc}}, "a")
	boolKey, _ := Encode([]Field{{Value: false, Direction: Asc}}, "a")
	numKey, _ := Encode([]Field{{Value: 0.0, Direction: Asc}}, "a")
	strKey, _ := Encode([]Field{{Value: "", Direction: Asc}}, "a")

	assert.Equal(t, -1, Compare(nullKey, boolKey), "null < bool")
	assert.Equal(t, -1, Compare(boolKey, numKey), "bool < number")
	assert.Equal(t, -1, Compare(numKey, strKey), "number < string")
}

func TestEncode_BoolOrdering(t *testing.T) {
	falseKey, _ := Encode([]Field{{Value: false, Direction: Asc}}, "a")
	trueKey, _ := Encode([]Field{{Value: true, Direction: Asc}}, "a")

	assert.Equal(t, -1, Compare(falseKey, trueKey), "false < true in asc")
}

func TestEncode_NumberOrdering(t *testing.T) {
	tests := []struct {
		a, b float64
		want int
	}{
		{-100, -50, -1},
		{-1, 0, -1},
		{0, 1, -1},
		{1, 100, -1},
		{-0.5, 0.5, -1},
	}

	for _, tt := range tests {
		keyA, _ := Encode([]Field{{Value: tt.a, Direction: Asc}}, "x")
		keyB, _ := Encode([]Field{{Value: tt.b, Direction: Asc}}, "x")
		assert.Equal(t, tt.want, Compare(keyA, keyB), "%v < %v", tt.a, tt.b)
	}
}

func TestEncode_StringOrdering(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "a", -1},
		{"a", "b", -1},
		{"a", "aa", -1},
		{"ab", "b", -1},
		{"abc", "abd", -1},
	}

	for _, tt := range tests {
		keyA, _ := Encode([]Field{{Value: tt.a, Direction: Asc}}, "x")
		keyB, _ := Encode([]Field{{Value: tt.b, Direction: Asc}}, "x")
		assert.Equal(t, tt.want, Compare(keyA, keyB), "%q < %q", tt.a, tt.b)
	}
}

func TestEncode_DescInvertsOrder(t *testing.T) {
	// In desc mode, larger values should come first (smaller key)
	keySmall, _ := Encode([]Field{{Value: 10.0, Direction: Desc}}, "a")
	keyLarge, _ := Encode([]Field{{Value: 100.0, Direction: Desc}}, "a")

	// 100 should sort before 10 in desc (so 100's key < 10's key)
	assert.Equal(t, -1, Compare(keyLarge, keySmall), "100 should sort before 10 in desc")
}

func TestEncode_DescString(t *testing.T) {
	keyA, _ := Encode([]Field{{Value: "aaa", Direction: Desc}}, "x")
	keyB, _ := Encode([]Field{{Value: "bbb", Direction: Desc}}, "x")

	// "bbb" should sort before "aaa" in desc
	assert.Equal(t, -1, Compare(keyB, keyA), "bbb should sort before aaa in desc")
}

func TestEncode_DescStringLength(t *testing.T) {
	// This tests the fixed-width length encoding for desc
	// "abc" (len=3) vs "ab" (len=2) in desc should have "abc" first
	keyABC, _ := Encode([]Field{{Value: "abc", Direction: Desc}}, "x")
	keyAB, _ := Encode([]Field{{Value: "ab", Direction: Desc}}, "x")

	// In desc, "abc" > "ab" so "abc" should come first (smaller key)
	assert.Equal(t, -1, Compare(keyABC, keyAB), "abc should sort before ab in desc")
}

func TestEncode_MultipleFields(t *testing.T) {
	// name:asc, age:desc
	key1, _ := Encode([]Field{
		{Value: "alice", Direction: Asc},
		{Value: 30.0, Direction: Desc},
	}, "doc1")

	key2, _ := Encode([]Field{
		{Value: "alice", Direction: Asc},
		{Value: 25.0, Direction: Desc},
	}, "doc2")

	key3, _ := Encode([]Field{
		{Value: "bob", Direction: Asc},
		{Value: 20.0, Direction: Desc},
	}, "doc3")

	// alice < bob (asc), so key1, key2 < key3
	assert.Equal(t, -1, Compare(key1, key3), "alice < bob")
	assert.Equal(t, -1, Compare(key2, key3), "alice < bob")

	// For alice, age 30 > 25, so in desc 30 comes first
	assert.Equal(t, -1, Compare(key1, key2), "alice@30 < alice@25 in (name:asc, age:desc)")
}

func TestEncode_DocIDTiebreaker(t *testing.T) {
	// Same fields, different doc IDs
	key1, _ := Encode([]Field{{Value: "test", Direction: Asc}}, "aaa")
	key2, _ := Encode([]Field{{Value: "test", Direction: Asc}}, "bbb")

	assert.Equal(t, -1, Compare(key1, key2), "aaa < bbb as tiebreaker")
}

func TestEncode_Errors(t *testing.T) {
	t.Run("unsupported type", func(t *testing.T) {
		_, err := Encode([]Field{{Value: []int{1, 2, 3}, Direction: Asc}}, "id")
		assert.ErrorIs(t, err, ErrUnsupportedType)
	})

	t.Run("string too long", func(t *testing.T) {
		longStr := string(make([]byte, 65536))
		_, err := Encode([]Field{{Value: longStr, Direction: Asc}}, "id")
		assert.ErrorIs(t, err, ErrStringTooLong)
	})

	t.Run("doc ID too long", func(t *testing.T) {
		longID := string(make([]byte, 65536))
		_, err := Encode(nil, longID)
		assert.ErrorIs(t, err, ErrIDTooLong)
	})
}

func TestEncode_IntTypes(t *testing.T) {
	// int64 and int should work and produce same result as float64
	key1, _ := Encode([]Field{{Value: int64(42), Direction: Asc}}, "x")
	key2, _ := Encode([]Field{{Value: int(42), Direction: Asc}}, "x")
	key3, _ := Encode([]Field{{Value: 42.0, Direction: Asc}}, "x")

	assert.Equal(t, 0, Compare(key1, key2), "int64(42) == int(42)")
	assert.Equal(t, 0, Compare(key1, key3), "int64(42) == float64(42)")
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b []byte
		want int
	}{
		{[]byte{1, 2, 3}, []byte{1, 2, 3}, 0},
		{[]byte{1, 2, 3}, []byte{1, 2, 4}, -1},
		{[]byte{1, 2, 4}, []byte{1, 2, 3}, 1},
		{[]byte{1, 2}, []byte{1, 2, 3}, -1},
		{[]byte{1, 2, 3}, []byte{1, 2}, 1},
		{[]byte{}, []byte{}, 0},
		{[]byte{}, []byte{1}, -1},
	}

	for _, tt := range tests {
		got := Compare(tt.a, tt.b)
		assert.Equal(t, tt.want, got, "Compare(%v, %v)", tt.a, tt.b)
	}
}

func TestEncode_SortCorrectness(t *testing.T) {
	// Create a bunch of keys and verify they sort correctly
	type doc struct {
		name string
		age  float64
		id   string
	}

	docs := []doc{
		{"alice", 30, "d1"},
		{"alice", 25, "d2"},
		{"alice", 25, "d3"},
		{"bob", 20, "d4"},
		{"bob", 35, "d5"},
		{"charlie", 28, "d6"},
	}

	// Encode with name:asc, age:desc
	var keys [][]byte
	for _, d := range docs {
		key, err := Encode([]Field{
			{Value: d.name, Direction: Asc},
			{Value: d.age, Direction: Desc},
		}, d.id)
		require.NoError(t, err)
		keys = append(keys, key)
	}

	// Sort using bytes.Compare
	sorted := make([]int, len(keys))
	for i := range sorted {
		sorted[i] = i
	}
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(keys[sorted[i]], keys[sorted[j]]) < 0
	})

	// Expected order: alice@30, alice@25(d2), alice@25(d3), bob@35, bob@20, charlie@28
	expected := []string{"d1", "d2", "d3", "d5", "d4", "d6"}
	for i, idx := range sorted {
		assert.Equal(t, expected[i], docs[idx].id, "position %d", i)
	}
}

func TestEncode_StringWithNullBytes(t *testing.T) {
	// Test that strings with embedded null bytes are properly escaped
	// and maintain correct sort order

	t.Run("escape null bytes", func(t *testing.T) {
		// String with null byte should be encoded properly
		strWithNull := "a\x00b"
		key, err := Encode([]Field{{Value: strWithNull, Direction: Asc}}, "id")
		require.NoError(t, err)

		// Verify the key is valid (no error)
		assert.NotNil(t, key)
	})

	t.Run("null byte ordering", func(t *testing.T) {
		// "a\x00b" should sort between "a" and "a\x01"
		keyA, _ := Encode([]Field{{Value: "a", Direction: Asc}}, "x")
		keyA0B, _ := Encode([]Field{{Value: "a\x00b", Direction: Asc}}, "x")
		keyA1, _ := Encode([]Field{{Value: "a\x01", Direction: Asc}}, "x")

		assert.Equal(t, -1, Compare(keyA, keyA0B), "a < a\\x00b")
		assert.Equal(t, -1, Compare(keyA0B, keyA1), "a\\x00b < a\\x01")
	})

	t.Run("multiple null bytes", func(t *testing.T) {
		strMultiNull := "\x00\x00\x00"
		key, err := Encode([]Field{{Value: strMultiNull, Direction: Asc}}, "id")
		require.NoError(t, err)
		assert.NotNil(t, key)
	})

	t.Run("desc with null bytes", func(t *testing.T) {
		keyA, _ := Encode([]Field{{Value: "a\x00b", Direction: Desc}}, "x")
		keyB, _ := Encode([]Field{{Value: "b\x00a", Direction: Desc}}, "x")

		// In desc, "b\x00a" should sort before "a\x00b"
		assert.Equal(t, -1, Compare(keyB, keyA), "b\\x00a should sort before a\\x00b in desc")
	})
}

func TestExtractDocID(t *testing.T) {
	t.Run("key too short", func(t *testing.T) {
		_, err := ExtractDocID([]byte{0x01, 0x02})
		assert.Error(t, err)
	})

	t.Run("mixed directions and escaped fields", func(t *testing.T) {
		key, err := Encode([]Field{{Value: "test\x00", Direction: Desc}, {Value: int64(9007199254740993)}}, "doc123")
		require.NoError(t, err)
		id, err := ExtractDocID(key)
		require.NoError(t, err)
		assert.Equal(t, "doc123", id)
	})
}

func TestEncode_DescNull(t *testing.T) {
	// Test desc ordering for null values
	keyNullAsc, _ := Encode([]Field{{Value: nil, Direction: Asc}}, "a")
	keyNullDesc, _ := Encode([]Field{{Value: nil, Direction: Desc}}, "a")

	// Asc and desc should produce different keys (inverted)
	assert.NotEqual(t, keyNullAsc[1], keyNullDesc[1], "null asc and desc should differ")
}

func TestEncode_DescBool(t *testing.T) {
	// In desc mode, true should sort before false
	keyFalse, _ := Encode([]Field{{Value: false, Direction: Desc}}, "a")
	keyTrue, _ := Encode([]Field{{Value: true, Direction: Desc}}, "a")

	assert.Equal(t, -1, Compare(keyTrue, keyFalse), "true should sort before false in desc")
}

func TestEncode_NegativeNumbers(t *testing.T) {
	// Test edge cases for negative numbers
	tests := []struct {
		a, b float64
		want int
	}{
		{-0.0, 0.0, 0}, // -0 and +0 should be equal
		{math.SmallestNonzeroFloat64, 0, 1},
		{-math.SmallestNonzeroFloat64, 0, -1},
	}

	for _, tt := range tests {
		keyA, _ := Encode([]Field{{Value: tt.a, Direction: Asc}}, "x")
		keyB, _ := Encode([]Field{{Value: tt.b, Direction: Asc}}, "x")
		assert.Equal(t, tt.want, Compare(keyA, keyB), "%v vs %v", tt.a, tt.b)
	}
}

func TestBase64Encoding(t *testing.T) {
	t.Run("encode and decode roundtrip", func(t *testing.T) {
		key, err := Encode([]Field{
			{Value: "hello", Direction: Asc},
			{Value: float64(42), Direction: Desc},
		}, "doc123")
		require.NoError(t, err)

		encoded := EncodeBase64(key)
		assert.NotEmpty(t, encoded)

		decoded, err := DecodeBase64(encoded)
		require.NoError(t, err)
		assert.Equal(t, key, decoded)
	})

	t.Run("decode invalid base64", func(t *testing.T) {
		_, err := DecodeBase64("!!!invalid!!!")
		assert.Error(t, err)
	})

	t.Run("empty key", func(t *testing.T) {
		encoded := EncodeBase64(nil)
		assert.Equal(t, "", encoded)

		decoded, err := DecodeBase64("")
		require.NoError(t, err)
		assert.Empty(t, decoded)
	})
}

func TestEncodePrefix(t *testing.T) {
	t.Run("empty fields", func(t *testing.T) {
		prefix, err := EncodePrefix(nil)
		require.NoError(t, err)
		// Should only have version byte
		assert.Len(t, prefix, 1)
		assert.Equal(t, Version, prefix[0])
	})

	t.Run("single string field ascending", func(t *testing.T) {
		prefix, err := EncodePrefix([]Field{
			{Value: "electronics", Direction: Asc},
		})
		require.NoError(t, err)

		// Should have version + type tag + string content + terminator
		assert.True(t, len(prefix) > 1)
		assert.Equal(t, Version, prefix[0])
		assert.Equal(t, TypeString, prefix[1])
	})

	t.Run("single number field descending", func(t *testing.T) {
		prefix, err := EncodePrefix([]Field{
			{Value: float64(100), Direction: Desc},
		})
		require.NoError(t, err)

		// Should have version + type tag + number encoding
		// For descending order, all bytes are XOR'd with 0xFF
		assert.True(t, len(prefix) > 1)
		assert.Equal(t, Version, prefix[0])
		// The type family is reversed with the rest of the field.
		assert.Equal(t, ^TypeNumber, prefix[1])
	})

	t.Run("multiple fields", func(t *testing.T) {
		prefix, err := EncodePrefix([]Field{
			{Value: "category", Direction: Asc},
			{Value: float64(200), Direction: Asc},
		})
		require.NoError(t, err)

		// Should have version + field1 + field2
		assert.True(t, len(prefix) > 2)
		assert.Equal(t, Version, prefix[0])
	})

	t.Run("prefix is prefix of full key", func(t *testing.T) {
		fields := []Field{
			{Value: "electronics", Direction: Asc},
			{Value: float64(100), Direction: Asc},
		}

		prefix, err := EncodePrefix(fields)
		require.NoError(t, err)

		fullKey, err := Encode(fields, "doc123")
		require.NoError(t, err)

		// Prefix should be a prefix of the full key
		assert.True(t, bytes.HasPrefix(fullKey, prefix))
		// Prefix should be shorter (no doc ID suffix)
		assert.Less(t, len(prefix), len(fullKey))
	})

	t.Run("unsupported type returns error", func(t *testing.T) {
		_, err := EncodePrefix([]Field{
			{Value: []int{1, 2, 3}, Direction: Asc}, // Slice is unsupported
		})
		assert.Error(t, err)
	})

	t.Run("boolean field", func(t *testing.T) {
		prefix, err := EncodePrefix([]Field{
			{Value: true, Direction: Asc},
		})
		require.NoError(t, err)
		assert.Equal(t, TypeBool, prefix[1])
	})

	t.Run("null field", func(t *testing.T) {
		prefix, err := EncodePrefix([]Field{
			{Value: nil, Direction: Asc},
		})
		require.NoError(t, err)
		assert.Equal(t, TypeNull, prefix[1])
	})

	t.Run("int64 field", func(t *testing.T) {
		prefix, err := EncodePrefix([]Field{
			{Value: int64(42), Direction: Asc},
		})
		require.NoError(t, err)
		assert.Equal(t, TypeNumber, prefix[1])
	})

	t.Run("int field", func(t *testing.T) {
		prefix, err := EncodePrefix([]Field{
			{Value: int(42), Direction: Asc},
		})
		require.NoError(t, err)
		assert.Equal(t, TypeNumber, prefix[1])
	})
}

func TestExactScalarAndIdentityOrder(t *testing.T) {
	values := []any{nil, false, true, int64(-9007199254740993), float64(-9007199254740992), int64(0), math.SmallestNonzeroFloat64, int64(9007199254740992), int64(9007199254740993), "", "a", "a\x00", "aa"}
	previous, err := Encode([]Field{{Missing: true}}, "same")
	require.NoError(t, err)
	for _, value := range values {
		current, err := Encode([]Field{{Value: value}}, "same")
		require.NoError(t, err)
		assert.Negative(t, Compare(previous, current), "%#v", value)
		previous = current
	}
	for _, pair := range [][2]string{{"aa", "b"}, {"a", "aa"}, {"a\x00", "a\x01"}} {
		first, err := Encode(nil, pair[0])
		require.NoError(t, err)
		second, err := Encode(nil, pair[1])
		require.NoError(t, err)
		assert.Negative(t, Compare(first, second))
	}
	for _, value := range []any{math.Inf(1), math.Inf(-1), math.NaN(), uint64(1)} {
		_, err := Encode([]Field{{Value: value}}, "id")
		require.Error(t, err)
	}
	positive, err := Encode([]Field{{Value: float64(0)}}, "id")
	require.NoError(t, err)
	negative, err := Encode([]Field{{Value: math.Copysign(0, -1)}}, "id")
	require.NoError(t, err)
	assert.Equal(t, positive, negative)
}
