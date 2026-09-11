// Package encoding encodes prefix-free index tuples with exact scalar ordering.
package encoding

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/syntrixbase/syntrix/pkg/model"
)

const Version byte = 0x02

const (
	TypeMissing byte = 0x01
	TypeNull    byte = 0x10
	TypeBool    byte = 0x30
	TypeNumber  byte = 0x40
	TypeString  byte = 0x50
)

type Direction int

const (
	Asc Direction = iota
	Desc
)

type Field struct {
	Value     any
	Direction Direction
	Missing   bool
}

var (
	ErrUnsupportedType = errors.New("unsupported value type")
	ErrStringTooLong   = errors.New("string exceeds maximum length (65535 bytes)")
	ErrIDTooLong       = errors.New("document ID exceeds maximum length (65535 bytes)")
	ErrInvalidOrderKey = errors.New("invalid orderkey format")
)

// Encode appends the logical document ID as the final ascending string field.
func Encode(fields []Field, docID string) ([]byte, error) {
	if len(docID) > 65535 {
		return nil, ErrIDTooLong
	}
	if !utf8.ValidString(docID) {
		return nil, fmt.Errorf("document ID is not valid UTF-8")
	}
	prefix, err := EncodePrefix(fields)
	if err != nil {
		return nil, err
	}
	id, err := encodeField(Field{Value: docID})
	if err != nil {
		return nil, err
	}
	return append(prefix, id...), nil
}

func EncodePrefix(fields []Field) ([]byte, error) {
	buf := []byte{Version}
	for _, field := range fields {
		encoded, err := encodeField(field)
		if err != nil {
			return nil, err
		}
		buf = append(buf, encoded...)
	}
	return buf, nil
}

func encodeField(field Field) ([]byte, error) {
	if field.Direction != Asc && field.Direction != Desc {
		return nil, fmt.Errorf("invalid index direction")
	}
	raw := []byte{TypeMissing}
	if !field.Missing {
		if value, ok := field.Value.(string); ok && len(value) > 65535 {
			return nil, ErrStringTooLong
		}
		var err error
		raw, err = model.ScalarKey(field.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnsupportedType, err)
		}
	}
	buf := make([]byte, 0, len(raw)+2)
	for _, b := range raw {
		buf = append(buf, b)
		if b == 0 {
			buf = append(buf, 1)
		}
	}
	buf = append(buf, 0, 0)
	if field.Direction == Desc {
		for i := range buf {
			buf[i] = ^buf[i]
		}
	}
	return buf, nil
}

func Compare(a, b []byte) int { return bytes.Compare(a, b) }

// ExtractDocID reads the last ascending string component of a complete key.
func ExtractDocID(key []byte) (string, error) {
	if len(key) < 4 || key[0] != Version {
		return "", ErrInvalidOrderKey
	}
	var last []byte
	var descending bool
	for pos := 1; pos < len(key); {
		tag := key[pos]
		descending = tag > 0x80
		last = nil
		terminated := false
		for pos < len(key) {
			b := key[pos]
			pos++
			if descending {
				b = ^b
			}
			if b != 0 {
				last = append(last, b)
				continue
			}
			if pos == len(key) {
				return "", ErrInvalidOrderKey
			}
			next := key[pos]
			pos++
			if descending {
				next = ^next
			}
			if next == 0 {
				terminated = true
				break
			}
			if next != 1 {
				return "", ErrInvalidOrderKey
			}
			last = append(last, 0)
		}
		if !terminated || len(last) == 0 {
			return "", ErrInvalidOrderKey
		}
	}
	if descending || last[0] != TypeString || !utf8.Valid(last[1:]) {
		return "", ErrInvalidOrderKey
	}
	return string(last[1:]), nil
}

func EncodeBase64(key []byte) string        { return base64.RawURLEncoding.EncodeToString(key) }
func DecodeBase64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }
