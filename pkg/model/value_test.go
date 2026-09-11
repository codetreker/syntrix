package model

import (
	"encoding/json"
	"math"
	"math/big"
	"math/rand"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNumericKeyExactBoundaries(t *testing.T) {
	ordered := []any{
		-math.MaxFloat64, float64(math.MinInt64) - 2048, int64(math.MinInt64),
		int64(math.MinInt64 + 1), int64(-9007199254740993), float64(-9007199254740992),
		int64(-1), -math.SmallestNonzeroFloat64, int64(0), math.SmallestNonzeroFloat64,
		float64(1), int64(9007199254740992), int64(9007199254740993),
		int64(math.MaxInt64), float64(math.MaxInt64), math.MaxFloat64,
	}
	for i := 1; i < len(ordered); i++ {
		comparison, err := CompareValues(ordered[i-1], ordered[i])
		require.NoError(t, err)
		require.Equal(t, -1, comparison, "%#v < %#v", ordered[i-1], ordered[i])
	}
	for _, pair := range [][2]any{{int64(1), float64(1)}, {int64(math.MinInt64), float64(math.MinInt64)}, {int64(0), math.Copysign(0, -1)}, {float64(0), math.Copysign(0, -1)}} {
		left, err := ScalarKey(pair[0])
		require.NoError(t, err)
		right, err := ScalarKey(pair[1])
		require.NoError(t, err)
		require.Equal(t, left, right)
	}
}

func TestNumericKeyMatchesRationalComparison(t *testing.T) {
	rng := rand.New(rand.NewSource(41))
	next := func() any {
		if rng.Intn(2) == 0 {
			return int64(rng.Uint64())
		}
		for {
			v := math.Float64frombits(rng.Uint64())
			if !math.IsNaN(v) && !math.IsInf(v, 0) {
				return v
			}
		}
	}
	rational := func(v any) *big.Rat {
		if integer, ok := v.(int64); ok {
			return new(big.Rat).SetInt64(integer)
		}
		return new(big.Rat).SetFloat64(v.(float64))
	}
	for range 2000 {
		a, b := next(), next()
		comparison, err := CompareValues(a, b)
		require.NoError(t, err)
		require.Equal(t, rational(a).Cmp(rational(b)), comparison, "%#v compared with %#v", a, b)
	}
}

func TestScalarTotalOrder(t *testing.T) {
	values := []any{nil, false, true, -math.MaxFloat64, int64(0), math.MaxFloat64, "", "a", "a\x00", "b", "中"}
	for i := 1; i < len(values); i++ {
		comparison, err := CompareValues(values[i-1], values[i])
		require.NoError(t, err)
		require.Equal(t, -1, comparison)
	}
}

func TestTypedValueRoundTrip(t *testing.T) {
	value := Document{
		"type": "int64", "value": "business data",
		"integer": int64(math.MaxInt64), "minimum": int64(math.MinInt64),
		"floating": float64(9007199254740992), "tiny": math.SmallestNonzeroFloat64,
		"array": []any{nil, true, false, "x", int64(1), float64(1), map[string]any{"type": "null"}},
	}
	encoded, err := EncodeTypedValue(value)
	require.NoError(t, err)
	decoded, err := DecodeTypedValue(encoded)
	require.NoError(t, err)
	normalized, err := NormalizeValue(value)
	require.NoError(t, err)
	require.Equal(t, normalized, decoded)
	require.Contains(t, string(encoded), `"type":"int64","value":"9223372036854775807"`)
	require.IsType(t, float64(0), decoded.(map[string]any)["floating"])
	zero, err := EncodeTypedValue(math.Copysign(0, -1))
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"float64","value":0}`, string(zero))
}

func TestTypedValueEncodingCanonicalBytes(t *testing.T) {
	for _, test := range []struct {
		value any
		wire  string
	}{
		{nil, `{"type":"null"}`},
		{false, `{"type":"bool","value":false}`},
		{true, `{"type":"bool","value":true}`},
		{int64(0), `{"type":"int64","value":"0"}`},
		{int64(math.MaxInt64), `{"type":"int64","value":"9223372036854775807"}`},
		{int64(math.MinInt64), `{"type":"int64","value":"-9223372036854775808"}`},
		{math.Copysign(0, -1), `{"type":"float64","value":0}`},
		{math.SmallestNonzeroFloat64, `{"type":"float64","value":5e-324}`},
		{float64(1e20), `{"type":"float64","value":100000000000000000000}`},
		{float64(1e21), `{"type":"float64","value":1e+21}`},
		{"<>&\u2028\u2029", `{"type":"string","value":"\u003c\u003e\u0026\u2028\u2029"}`},
		{[]any{}, `{"type":"array","value":[]}`},
		{map[string]any{}, `{"type":"object","value":{}}`},
		{map[string]any{
			"type": "int64", "value": "001",
			"nested": []any{nil, false, int64(0), float64(0), "a\"\\\n"},
		}, `{"type":"object","value":{"nested":{"type":"array","value":[{"type":"null"},{"type":"bool","value":false},{"type":"int64","value":"0"},{"type":"float64","value":0},{"type":"string","value":"a\"\\\n"}]},"type":{"type":"string","value":"int64"},"value":{"type":"string","value":"001"}}}`},
	} {
		encoded, err := EncodeTypedValue(test.value)
		require.NoError(t, err)
		require.Equal(t, test.wire, string(encoded), "%#v", test.value)
	}
}

func TestTypedValueRejectsMalformedWire(t *testing.T) {
	for _, input := range []string{
		`null`, `[]`, `{}`, `{"type":"unknown","value":1}`, `{"type":"null","value":null}`,
		`{"type":"bool"}`, `{"type":"string","value":null}`, `{"type":"int64","value":1}`,
		`{"type":"int64","value":"9223372036854775808"}`, `{"type":"int64","value":"01"}`,
		`{"type":"float64","value":1e999}`, `{"type":"array","value":[1]}`,
		`{"type":"object","value":{"nested":true}}`, `{"type":"null","extra":true}`,
	} {
		_, err := DecodeTypedValue([]byte(input))
		require.Error(t, err, input)
	}
}

func TestValueNormalizationRejectsUnsupportedSources(t *testing.T) {
	for _, value := range []any{math.NaN(), math.Inf(1), math.Inf(-1), uint64(1), complex(1, 0), map[int]any{1: 2}, []any{math.NaN()}, map[string]any{"v": math.Inf(1)}, string([]byte{0xff})} {
		_, err := NormalizeValue(value)
		require.Error(t, err, "%#v", value)
	}
	value, err := NormalizeValue(map[string]any{"ints": []int{1, 2}, "number": json.Number("9223372036854775807")})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"ints": []any{int64(1), int64(2)}, "number": int64(math.MaxInt64)}, value)
}

func TestTypedValueNormalizesNativeNumbers(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  any
	}{
		{"nil", nil, nil},
		{"int", int(-1), int64(-1)},
		{"int8 minimum", int8(math.MinInt8), int64(math.MinInt8)},
		{"int16 maximum", int16(math.MaxInt16), int64(math.MaxInt16)},
		{"int32 minimum", int32(math.MinInt32), int64(math.MinInt32)},
		{"float32 fraction", float32(0.1), float64(float32(0.1))},
		{"float32 smallest", float32(math.SmallestNonzeroFloat32), float64(math.SmallestNonzeroFloat32)},
		{"float32 maximum", float32(math.MaxFloat32), float64(math.MaxFloat32)},
		{"float32 negative zero", float32(math.Copysign(0, -1)), float64(0)},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := EncodeTypedValue(test.value)
			require.NoError(t, err)
			decoded, err := DecodeTypedValue(encoded)
			require.NoError(t, err)
			require.Equal(t, test.want, decoded)
			if value, ok := decoded.(float64); ok && value == 0 {
				require.False(t, math.Signbit(value))
			}
		})
	}
}

func TestEncodeTypedValueRejectsInvalidNestedData(t *testing.T) {
	for _, test := range []struct {
		name, message string
		value         any
	}{
		{"float32 NaN", "non-finite", float32(math.NaN())},
		{"float32 infinity", "non-finite", float32(math.Inf(1))},
		{"unsigned number", "unsupported value type", uint64(1)},
		{"invalid decimal", "invalid number", json.Number("1.2.3")},
		{"out of range decimal", "invalid number", json.Number("1e999")},
		{"invalid object key", "object key is not valid UTF-8", map[string]any{string([]byte{0xff}): true}},
		{"nested nonfinite", `object field "values": array element 1: non-finite`, map[string]any{"values": []any{int64(1), math.Inf(-1)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := EncodeTypedValue(test.value)
			require.ErrorContains(t, err, test.message)
			require.Nil(t, encoded)
		})
	}
}

func TestDecodeJSONValuePreservesNumericTokens(t *testing.T) {
	value, err := DecodeJSONValue([]byte(`{"integer":9223372036854775807,"float":1.0,"exponent":1e2}`))
	require.NoError(t, err)
	require.Equal(t, map[string]any{"integer": int64(math.MaxInt64), "float": float64(1), "exponent": float64(100)}, value)
	for _, input := range []string{`9223372036854775808`, `-9223372036854775809`, `1e999`, `1 2`, `{"v":1} x`} {
		_, err := DecodeJSONValue([]byte(input))
		require.Error(t, err, input)
	}
}

func TestJSONDecodersRejectUnpairedSurrogates(t *testing.T) {
	for _, literal := range []string{
		`"\ud800"`, `"\uDBFF"`, `"\udc00"`, `"\uDFFF"`,
		`"\ud800\ud800"`, `"\udc00\ud800"`, `"\ud800\u0041"`,
		`"\ud800x\udc00"`, `"\ud800\\udc00"`, `"\ud800\n\udc00"`,
		`"\ud800\udc00\udfff"`, `"\ud800\udc00\ud800"`,
	} {
		t.Run(literal, func(t *testing.T) {
			for _, input := range []string{literal, `{"nested":[` + literal + `]}`, `{` + literal + `:null,"�":true}`} {
				_, err := DecodeJSONValue([]byte(input))
				require.Error(t, err, input)
				require.Contains(t, err.Error(), "surrogate")
			}
			for _, input := range []string{
				`{"type":"string","value":` + literal + `}`,
				`{"type":"object","value":{"nested":{"type":"array","value":[{"type":"string","value":` + literal + `}]}}}`,
				`{"type":"object","value":{` + literal + `:{"type":"null"},"�":{"type":"bool","value":true}}}`,
			} {
				_, err := DecodeTypedValue([]byte(input))
				require.Error(t, err, input)
				require.Contains(t, err.Error(), "surrogate")
			}
		})
	}
}

func TestJSONDecodersPreserveValidUnicodeAndLiteralEscapes(t *testing.T) {
	for _, test := range []struct{ literal, want string }{
		{`"\ud800\udc00"`, "\U00010000"},
		{`"\uDBFF\uDFFF"`, "\U0010FFFF"},
		{`"\uD83d\ude00"`, "😀"},
		{`"\ud7ff\ue000\ufffd"`, "\ud7ff\ue000�"},
		{`"\\ud800"`, `\ud800`},
		{`"\\udc00"`, `\udc00`},
		{`"\\\ud800\udc00"`, "\\\U00010000"},
		{`"\"\\ud800\""`, `"\ud800"`},
		{`"中😀�"`, "中😀�"},
	} {
		t.Run(test.literal, func(t *testing.T) {
			plain, err := DecodeJSONValue([]byte(test.literal))
			require.NoError(t, err)
			require.Equal(t, test.want, plain)
			typed, err := DecodeTypedValue([]byte(`{"type":"string","value":` + test.literal + `}`))
			require.NoError(t, err)
			require.Equal(t, test.want, typed)
			plain, err = DecodeJSONValue([]byte(`{` + test.literal + `:[` + test.literal + `]}`))
			require.NoError(t, err)
			require.Equal(t, map[string]any{test.want: []any{test.want}}, plain)
			typed, err = DecodeTypedValue([]byte(`{"type":"object","value":{` + test.literal + `:{"type":"string","value":` + test.literal + `}}}`))
			require.NoError(t, err)
			require.Equal(t, map[string]any{test.want: test.want}, typed)
		})
	}
}

func TestValidateJSONUnicodeEnvelopeAndMalformedEscapes(t *testing.T) {
	for _, input := range []string{
		`{"collection":"\ud800"}`, `{"filters":[{"field":"\udc00"}]}`,
		`{"orderBy":[{"field":"\ud800","direction":"asc"}]}`,
		`{"type":"null","\ud800":true}`,
		`"\u"`, `"\u12"`, `"\uxxxx"`, `"\ud800\uxxxx"`,
		string([]byte{'"', 0xff, '"'}),
	} {
		require.Error(t, ValidateJSONUnicode([]byte(input)), input)
	}
	for _, input := range []string{`"\`, `"\\`, `"\ud800\udc00"` + strings.Repeat(` `, 10000), `{"field":"\\ud800","value":"\ud800\udc00"}`} {
		require.NoError(t, ValidateJSONUnicode([]byte(input)), input)
	}
}
