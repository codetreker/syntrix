package wire

import (
	"encoding/json"
	"fmt"

	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

const Version = 2
const MaxPageBytes = 16 << 20
const MaxGRPCBytes = 20 << 20

func EncodeQuery(q model.Query) (*pb.Query, error) {
	out := &pb.Query{Collection: q.Collection, Limit: int32(q.Limit), StartAfter: q.StartAfter, ShowDeleted: q.ShowDeleted}
	if q.Limit < 0 || q.Limit > 1000 {
		return nil, fmt.Errorf("%w: limit must be between 0 and 1000", model.ErrInvalidQuery)
	}
	for _, f := range q.Filters {
		value, err := model.EncodeTypedValue(f.Value)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", model.ErrInvalidQuery, err)
		}
		out.Filters = append(out.Filters, &pb.Filter{Field: f.Field, Op: string(f.Op), Value: value})
	}
	for _, order := range q.OrderBy {
		out.OrderBy = append(out.OrderBy, &pb.OrderBy{Field: order.Field, Direction: order.Direction})
	}
	return out, nil
}

func DecodeQuery(q *pb.Query) (model.Query, error) {
	if q == nil {
		return model.Query{}, fmt.Errorf("%w: query is required", model.ErrInvalidQuery)
	}
	out := model.Query{Collection: q.Collection, Limit: int(q.Limit), StartAfter: q.StartAfter, ShowDeleted: q.ShowDeleted}
	for _, f := range q.Filters {
		if f == nil {
			return model.Query{}, fmt.Errorf("%w: filter is required", model.ErrInvalidQuery)
		}
		value, err := model.DecodeTypedValue(f.Value)
		if err != nil {
			return model.Query{}, fmt.Errorf("%w: %w", model.ErrInvalidQuery, err)
		}
		out.Filters = append(out.Filters, model.Filter{Field: f.Field, Op: model.FilterOp(f.Op), Value: value})
	}
	for _, order := range q.OrderBy {
		if order == nil {
			return model.Query{}, fmt.Errorf("%w: order is required", model.ErrInvalidQuery)
		}
		out.OrderBy = append(out.OrderBy, model.Order{Field: order.Field, Direction: order.Direction})
	}
	return out, nil
}

func EncodePage(page model.QueryPage) (*pb.ExecuteQueryResponse, error) {
	out := &pb.ExecuteQueryResponse{WireVersion: Version, Documents: make([]*pb.Document, 0, len(page.Documents))}
	for _, doc := range page.Documents {
		data, err := model.EncodeTypedValue(map[string]any(doc))
		if err != nil {
			return nil, err
		}
		out.Documents = append(out.Documents, &pb.Document{Data: data})
		if proto.Size(out) > MaxGRPCBytes {
			return nil, model.ErrQueryWorkLimit
		}
	}
	if page.NextCursor != nil {
		out.NextCursor = *page.NextCursor
		out.HasMore = true
	}
	for _, order := range page.EffectiveOrder {
		out.EffectiveOrder = append(out.EffectiveOrder, &pb.OrderBy{Field: order.Field, Direction: order.Direction})
	}
	if proto.Size(out) > MaxGRPCBytes {
		return nil, model.ErrQueryWorkLimit
	}
	return out, nil
}

func DecodePage(in *pb.ExecuteQueryResponse) (model.QueryPage, error) {
	if in == nil || in.WireVersion != Version {
		return model.QueryPage{}, fmt.Errorf("unsupported query response version")
	}
	out := model.QueryPage{Documents: make([]model.Document, 0, len(in.Documents)), EffectiveOrder: make([]model.Order, 0, len(in.EffectiveOrder))}
	if in.HasMore {
		if in.NextCursor == "" {
			return model.QueryPage{}, fmt.Errorf("missing query continuation")
		}
		out.NextCursor = &in.NextCursor
	} else if in.NextCursor != "" {
		return model.QueryPage{}, fmt.Errorf("unexpected terminal continuation")
	}
	for _, doc := range in.Documents {
		if doc == nil {
			return model.QueryPage{}, fmt.Errorf("missing query document")
		}
		value, err := model.DecodeTypedValue(doc.Data)
		if err != nil {
			return model.QueryPage{}, err
		}
		data, ok := value.(map[string]any)
		if !ok {
			return model.QueryPage{}, fmt.Errorf("query document is not an object")
		}
		out.Documents = append(out.Documents, model.Document(data))
	}
	for _, order := range in.EffectiveOrder {
		if order == nil {
			return model.QueryPage{}, fmt.Errorf("missing query order")
		}
		out.EffectiveOrder = append(out.EffectiveOrder, model.Order{Field: order.Field, Direction: order.Direction})
	}
	return out, nil
}

type jsonPageEnvelope struct {
	Documents      []json.RawMessage `json:"documents"`
	NextCursor     *string           `json:"nextCursor"`
	EffectiveOrder []model.Order     `json:"effectiveOrder"`
}

// CheckJSONPageSize accounts for documents already encoded during materialization.
// Only the envelope is serialized again; RawMessage nodes contribute their exact
// compact encoding lengths and one separator between adjacent documents.
func CheckJSONPageSize(page model.QueryPage, documentBytes int) error {
	envelope, err := json.Marshal(jsonPageEnvelope{Documents: []json.RawMessage{}, NextCursor: page.NextCursor, EffectiveOrder: page.EffectiveOrder})
	if err != nil {
		return err
	}
	size := len(envelope) + documentBytes + max(0, len(page.Documents)-1)
	if size > MaxPageBytes {
		return model.ErrQueryWorkLimit
	}
	return nil
}

func EncodeJSONPage(page model.QueryPage) ([]byte, error) {
	response := jsonPageEnvelope{Documents: make([]json.RawMessage, 0, len(page.Documents)), NextCursor: page.NextCursor, EffectiveOrder: page.EffectiveOrder}
	size := 0
	for _, doc := range page.Documents {
		data, err := model.EncodeTypedValue(map[string]any(doc))
		if err != nil {
			return nil, err
		}
		size += len(data)
		if size > MaxPageBytes {
			return nil, model.ErrQueryWorkLimit
		}
		response.Documents = append(response.Documents, data)
	}
	data, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxPageBytes {
		return nil, model.ErrQueryWorkLimit
	}
	return data, nil
}
