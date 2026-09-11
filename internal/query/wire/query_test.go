package wire

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

func TestQueryTypedFilterRoundTrip(t *testing.T) {
	q := model.Query{Collection: "users", Filters: model.Filters{{Field: "integer", Op: model.OpEq, Value: int64(math.MaxInt64)}, {Field: "object", Op: model.OpEq, Value: map[string]any{"type": "int64", "value": "business-data", "nested": []any{int64(math.MinInt64), false, nil, float64(1.25)}}}}, OrderBy: []model.Order{{Field: "integer", Direction: "desc"}}, Limit: 10, StartAfter: "opaque", ShowDeleted: true}
	encoded, err := EncodeQuery(q)
	require.NoError(t, err)
	serialized, err := proto.Marshal(encoded)
	require.NoError(t, err)
	var received pb.Query
	require.NoError(t, proto.Unmarshal(serialized, &received))
	decoded, err := DecodeQuery(&received)
	require.NoError(t, err)
	assert.Equal(t, q, decoded)
}

func TestDecodeQueryRejectsMalformedInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		q    *pb.Query
	}{
		{"missing query", nil},
		{"nil filter", &pb.Query{Filters: []*pb.Filter{nil}}},
		{"plain JSON filter", &pb.Query{Filters: []*pb.Filter{{Value: []byte(`1`)}}}},
		{"unknown type", &pb.Query{Filters: []*pb.Filter{{Value: []byte(`{"type":"unknown","value":1}`)}}}},
		{"nil order", &pb.Query{OrderBy: []*pb.OrderBy{nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := DecodeQuery(tc.q); require.ErrorIs(t, err, model.ErrInvalidQuery) })
	}
}

func TestPageTypedDocumentRoundTrip(t *testing.T) {
	cursor := "opaque"
	page := model.QueryPage{Documents: []model.Document{{"id": "doc", "version": int64(math.MaxInt64), "updatedAt": int64(math.MinInt64), "counter": int64(9007199254740993), "object": map[string]any{"type": "int64", "value": "business-data"}}}, NextCursor: &cursor, EffectiveOrder: []model.Order{{Field: "id", Direction: "asc"}}}
	encoded, err := EncodePage(page)
	require.NoError(t, err)
	require.EqualValues(t, 2, encoded.WireVersion)
	require.Len(t, encoded.Documents, 1)
	assert.Empty(t, encoded.Documents[0].Id)
	assert.Zero(t, encoded.Documents[0].Version)
	raw, err := proto.Marshal(encoded)
	require.NoError(t, err)
	var response pb.ExecuteQueryResponse
	require.NoError(t, proto.Unmarshal(raw, &response))
	decoded, err := DecodePage(&response)
	require.NoError(t, err)
	assert.Equal(t, page, decoded)
	jsonPage, err := EncodeJSONPage(page)
	require.NoError(t, err)
	var envelope struct {
		Documents      []json.RawMessage `json:"documents"`
		NextCursor     *string           `json:"nextCursor"`
		EffectiveOrder []model.Order     `json:"effectiveOrder"`
	}
	require.NoError(t, json.Unmarshal(jsonPage, &envelope))
	require.Len(t, envelope.Documents, 1)
	doc, err := model.DecodeTypedValue(envelope.Documents[0])
	require.NoError(t, err)
	assert.Equal(t, map[string]any(page.Documents[0]), doc)
	assert.Equal(t, page.NextCursor, envelope.NextCursor)
	assert.Equal(t, page.EffectiveOrder, envelope.EffectiveOrder)
}

func TestDecodePageRejectsMalformedInput(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *pb.ExecuteQueryResponse
	}{
		{"missing response", nil},
		{"legacy version", &pb.ExecuteQueryResponse{}},
		{"future version", &pb.ExecuteQueryResponse{WireVersion: 3}},
		{"missing cursor", &pb.ExecuteQueryResponse{WireVersion: Version, HasMore: true}},
		{"terminal cursor", &pb.ExecuteQueryResponse{WireVersion: Version, NextCursor: "unexpected"}},
		{"nil document", &pb.ExecuteQueryResponse{WireVersion: Version, Documents: []*pb.Document{nil}}},
		{"legacy document", &pb.ExecuteQueryResponse{WireVersion: Version, Documents: []*pb.Document{{Id: "doc", Data: []byte(`{"value":1}`)}}}},
		{"scalar document", &pb.ExecuteQueryResponse{WireVersion: Version, Documents: []*pb.Document{{Data: []byte(`{"type":"int64","value":"1"}`)}}}},
		{"unknown node", &pb.ExecuteQueryResponse{WireVersion: Version, Documents: []*pb.Document{{Data: []byte(`{"type":"future","value":null}`)}}}},
		{"nil order", &pb.ExecuteQueryResponse{WireVersion: Version, EffectiveOrder: []*pb.OrderBy{nil}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := DecodePage(tc.response)
			require.Error(t, err)
			assert.Empty(t, page.Documents)
		})
	}
}

func TestPageEncodingBudgets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limit  int
		encode func(model.QueryPage) (int, error)
	}{
		{"JSON", 16 << 20, func(page model.QueryPage) (int, error) {
			encoded, err := EncodeJSONPage(page)
			return len(encoded), err
		}},
		{"protobuf", 20 << 20, func(page model.QueryPage) (int, error) {
			encoded, err := EncodePage(page)
			return proto.Size(encoded), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cursor := "continuation-token"
			page := model.QueryPage{
				NextCursor:     &cursor,
				EffectiveOrder: []model.Order{{Field: "counter", Direction: "desc"}, {Field: "id", Direction: "asc"}},
			}
			payload := strings.Repeat("x", tc.limit/24-1024)
			for i := range 24 {
				page.Documents = append(page.Documents, model.Document{"id": fmt.Sprintf("doc-%02d", i), "payload": payload})
			}
			size, err := tc.encode(page)
			require.NoError(t, err)
			require.Less(t, size, tc.limit)
			last := page.Documents[len(page.Documents)-1]
			boundaryPayload := payload + strings.Repeat("x", tc.limit-size)
			last["payload"] = boundaryPayload
			for _, doc := range page.Documents {
				raw, err := json.Marshal(doc)
				require.NoError(t, err)
				require.Less(t, len(raw), 1<<20)
			}
			size, err = tc.encode(page)
			require.NoError(t, err)
			require.Equal(t, tc.limit, size)

			t.Run("document adds one byte", func(t *testing.T) {
				last["payload"] = boundaryPayload + "x"
				defer func() { last["payload"] = boundaryPayload }()
				_, err := tc.encode(page)
				require.ErrorIs(t, err, model.ErrQueryWorkLimit)
			})
			t.Run("cursor adds one byte", func(t *testing.T) {
				extended := cursor + "x"
				oversized := page
				oversized.NextCursor = &extended
				_, err := tc.encode(oversized)
				require.ErrorIs(t, err, model.ErrQueryWorkLimit)
			})
			t.Run("order adds one byte", func(t *testing.T) {
				oversized := page
				oversized.EffectiveOrder = []model.Order{{Field: "counter", Direction: "desc"}, {Field: "idx", Direction: "asc"}}
				_, err := tc.encode(oversized)
				require.ErrorIs(t, err, model.ErrQueryWorkLimit)
			})
		})
	}
}

func TestTerminalJSONPage(t *testing.T) {
	raw, err := EncodeJSONPage(model.QueryPage{EffectiveOrder: []model.Order{{Field: "id", Direction: "asc"}}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"documents":[],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`, string(raw))
}

func TestPageSizeCheckMatchesEncodedEnvelope(t *testing.T) {
	cursor := "escaped<cursor>\""
	for _, docs := range [][]model.Document{nil, {{"escaped": "<>&\"", "number": int64(math.MaxInt64)}}, {{"nested": []any{map[string]any{"x": "a"}, true, nil}}, {"id": "second"}}} {
		page := model.QueryPage{Documents: docs, NextCursor: &cursor, EffectiveOrder: []model.Order{{Field: "field<>", Direction: "asc"}}}
		documentBytes := 0
		for _, doc := range docs {
			encoded, err := model.EncodeTypedValue(map[string]any(doc))
			require.NoError(t, err)
			documentBytes += len(encoded)
		}
		encoded, err := EncodeJSONPage(page)
		require.NoError(t, err)
		require.NoError(t, CheckJSONPageSize(page, documentBytes+MaxPageBytes-len(encoded)))
		require.ErrorIs(t, CheckJSONPageSize(page, documentBytes+MaxPageBytes-len(encoded)+1), model.ErrQueryWorkLimit)
	}
}
