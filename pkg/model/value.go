package model

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/bits"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// NormalizeValue preserves the exact int64 and finite binary64 value domains.
// Containers are copied recursively so callers can retain their input values.
func NormalizeValue(value any) (any, error) {
	switch v := value.(type) {
	case nil, bool:
		return v, nil
	case string:
		if !utf8.ValidString(v) {
			return nil, fmt.Errorf("value is not valid UTF-8")
		}
		return v, nil
	case int:
		return int64(v), nil
	case int8:
		return int64(v), nil
	case int16:
		return int64(v), nil
	case int32:
		return int64(v), nil
	case int64:
		return v, nil
	case float32:
		return NormalizeValue(float64(v))
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, fmt.Errorf("non-finite number")
		}
		if v == 0 {
			return float64(0), nil
		}
		return v, nil
	case json.Number:
		if !strings.ContainsAny(string(v), ".eE") {
			i, err := v.Int64()
			if err != nil {
				return nil, fmt.Errorf("integer is outside int64 domain: %w", err)
			}
			return i, nil
		}
		f, err := v.Float64()
		if err != nil {
			return nil, fmt.Errorf("invalid number %q: %w", v, err)
		}
		return NormalizeValue(f)
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := range out {
			item, err := NormalizeValue(rv.Index(i).Interface())
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
			out[i] = item
		}
		return out, nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			break
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			key := iter.Key().String()
			if !utf8.ValidString(key) {
				return nil, fmt.Errorf("object key is not valid UTF-8")
			}
			item, err := NormalizeValue(iter.Value().Interface())
			if err != nil {
				return nil, fmt.Errorf("object field %q: %w", key, err)
			}
			out[key] = item
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported value type %T", value)
}

// DecodeJSONValue preserves plain JSON integer tokens as int64 and decodes
// fractional/exponent tokens as finite binary64. Out-of-domain integers fail.
func DecodeJSONValue(data []byte) (any, error) {
	if err := ValidateJSONUnicode(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("unexpected trailing JSON value")
	}
	return NormalizeValue(value)
}

// ValidateJSONUnicode rejects invalid UTF-8 and unpaired UTF-16 surrogate
// escapes before encoding/json can replace them with U+FFFD. It checks all
// string tokens, including object keys; JSON syntax validation remains with
// the decoder. Validation takes linear time and constant memory.
func ValidateJSONUnicode(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("JSON value is not valid UTF-8")
	}
	inString := false
	for i := 0; i < len(data); {
		switch {
		case data[i] == '"':
			inString = !inString
			i++
		case inString && data[i] == '\\':
			if i+1 >= len(data) || data[i+1] != 'u' {
				i += 2
				continue
			}
			first, ok := jsonHexCodeUnit(data[i+2:])
			if !ok {
				return fmt.Errorf("invalid JSON Unicode escape at byte %d", i)
			}
			if first >= 0xDC00 && first <= 0xDFFF {
				return fmt.Errorf("unpaired JSON low surrogate at byte %d", i)
			}
			if first >= 0xD800 && first <= 0xDBFF {
				if i+12 > len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
					return fmt.Errorf("unpaired JSON high surrogate at byte %d", i)
				}
				second, ok := jsonHexCodeUnit(data[i+8:])
				if !ok || second < 0xDC00 || second > 0xDFFF {
					return fmt.Errorf("unpaired JSON high surrogate at byte %d", i)
				}
				i += 12
			} else {
				i += 6
			}
		default:
			i++
		}
	}
	return nil
}

func jsonHexCodeUnit(data []byte) (uint16, bool) {
	if len(data) < 4 {
		return 0, false
	}
	var value uint16
	for _, c := range data[:4] {
		value <<= 4
		switch {
		case c >= '0' && c <= '9':
			value |= uint16(c - '0')
		case c >= 'a' && c <= 'f':
			value |= uint16(c-'a') + 10
		case c >= 'A' && c <= 'F':
			value |= uint16(c-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

// ScalarKey sorts null, false, true, numbers, and strings in that order.
// Equal integers and floats share a key, including both signs of zero.
func ScalarKey(value any) ([]byte, error) {
	v, err := NormalizeValue(value)
	if err != nil {
		return nil, err
	}
	switch v := v.(type) {
	case nil:
		return []byte{0x10}, nil
	case bool:
		if v {
			return []byte{0x30}, nil
		}
		return []byte{0x20}, nil
	case string:
		return append([]byte{0x50}, []byte(v)...), nil
	case int64:
		return numberKey(v, 0, true), nil
	case float64:
		return numberKey(0, v, false), nil
	default:
		return nil, fmt.Errorf("expected scalar or null, got %T", value)
	}
}

func numberKey(integer int64, floating float64, isInteger bool) []byte {
	key := make([]byte, 12)
	key[0] = 0x40
	negative := integer < 0
	var significand uint64
	var exponent int
	if isInteger {
		magnitude := uint64(integer)
		if negative {
			magnitude = uint64(-(integer + 1)) + 1
		}
		if magnitude != 0 {
			length := bits.Len64(magnitude)
			exponent = length - 1
			significand = magnitude << (64 - length)
		}
	} else {
		negative = floating < 0
		raw := math.Float64bits(math.Abs(floating))
		fraction := raw & ((1 << 52) - 1)
		encodedExponent := int((raw >> 52) & 0x7ff)
		if encodedExponent == 0 {
			if fraction != 0 {
				length := bits.Len64(fraction)
				exponent = -1074 + length - 1
				significand = fraction << (64 - length)
			}
		} else {
			exponent = encodedExponent - 1023
			significand = ((1 << 52) | fraction) << 11
		}
	}
	if significand == 0 {
		key[1] = 1
		return key
	}
	key[1] = 2
	binary.BigEndian.PutUint16(key[2:4], uint16(exponent+1074))
	binary.BigEndian.PutUint64(key[4:], significand)
	if negative {
		key[1] = 0
		for i := 2; i < len(key); i++ {
			key[i] = ^key[i]
		}
	}
	return key
}

func CompareValues(a, b any) (int, error) {
	left, err := ScalarKey(a)
	if err != nil {
		return 0, err
	}
	right, err := ScalarKey(b)
	if err != nil {
		return 0, err
	}
	return bytes.Compare(left, right), nil
}

// EqualValues compares admitted scalar values without cross-family coercion.
// Structured values cannot equal a scalar query operand.
func EqualValues(a, b any) bool {
	comparison, err := CompareValues(a, b)
	return err == nil && comparison == 0
}

type typedValue struct {
	Type  string `json:"type"`
	Value any    `json:"value,omitempty"`
}

// EncodeTypedValue tags every node, including objects, to avoid collisions with
// business properties that happen to have the same names as codec fields.
func EncodeTypedValue(value any) ([]byte, error) {
	normalized, err := NormalizeValue(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(typedValueTree(normalized))
}

func typedValueTree(value any) typedValue {
	switch v := value.(type) {
	case nil:
		return typedValue{Type: "null"}
	case bool:
		return typedValue{Type: "bool", Value: v}
	case string:
		return typedValue{Type: "string", Value: v}
	case int64:
		return typedValue{Type: "int64", Value: strconv.FormatInt(v, 10)}
	case float64:
		return typedValue{Type: "float64", Value: v}
	case []any:
		items := make([]typedValue, len(v))
		for i, item := range v {
			items[i] = typedValueTree(item)
		}
		return typedValue{Type: "array", Value: items}
	case map[string]any:
		items := make(map[string]typedValue, len(v))
		for key, item := range v {
			items[key] = typedValueTree(item)
		}
		return typedValue{Type: "object", Value: items}
	default:
		panic(fmt.Sprintf("unexpected normalized value type %T", value))
	}
}

func DecodeTypedValue(data []byte) (any, error) {
	if err := ValidateJSONUnicode(data); err != nil {
		return nil, err
	}
	return decodeTypedValue(data)
}

func decodeTypedValue(data []byte) (any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("typed value must be an object")
	}
	for key := range fields {
		if key != "type" && key != "value" {
			return nil, fmt.Errorf("unknown typed value field %q", key)
		}
	}
	var kind string
	if err := json.Unmarshal(fields["type"], &kind); err != nil {
		return nil, fmt.Errorf("typed value type: %w", err)
	}
	raw, present := fields["value"]
	if kind == "null" {
		if present {
			return nil, fmt.Errorf("null typed value must omit value")
		}
		return nil, nil
	}
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fmt.Errorf("%s typed value requires a non-null value", kind)
	}
	switch kind {
	case "bool":
		var v bool
		err := json.Unmarshal(raw, &v)
		return v, err
	case "string":
		var v string
		err := json.Unmarshal(raw, &v)
		return v, err
	case "int64":
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		i, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, err
		}
		if strconv.FormatInt(i, 10) != v {
			return nil, fmt.Errorf("noncanonical int64 %q", v)
		}
		return i, nil
	case "float64":
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return NormalizeValue(v)
	case "array":
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		out := make([]any, len(items))
		for i, item := range items {
			v, err := decodeTypedValue(item)
			if err != nil {
				return nil, fmt.Errorf("array element %d: %w", i, err)
			}
			out[i] = v
		}
		return out, nil
	case "object":
		var items map[string]json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		out := make(map[string]any, len(items))
		for key, item := range items {
			v, err := decodeTypedValue(item)
			if err != nil {
				return nil, fmt.Errorf("object field %q: %w", key, err)
			}
			out[key] = v
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown typed value type %q", kind)
	}
}
