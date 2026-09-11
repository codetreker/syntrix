package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func decodeQuery(body io.Reader) (model.Query, error) {
	var request struct {
		Collection string `json:"collection"`
		Filters    []struct {
			Field string          `json:"field"`
			Op    model.FilterOp  `json:"op"`
			Value json.RawMessage `json:"value"`
		} `json:"filters"`
		OrderBy     []model.Order `json:"orderBy"`
		Limit       int           `json:"limit"`
		StartAfter  string        `json:"startAfter"`
		ShowDeleted bool          `json:"showDeleted"`
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return model.Query{}, err
	}
	if err := model.ValidateJSONUnicode(data); err != nil {
		return model.Query{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&request); err != nil {
		return model.Query{}, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return model.Query{}, fmt.Errorf("request must contain one JSON object")
	}
	q := model.Query{Collection: request.Collection, OrderBy: request.OrderBy, Limit: request.Limit, StartAfter: request.StartAfter, ShowDeleted: request.ShowDeleted}
	for _, filter := range request.Filters {
		raw := bytes.TrimSpace(filter.Value)
		if len(raw) == 0 {
			return model.Query{}, fmt.Errorf("filter value is required")
		}
		var value any
		var err error
		if raw[0] == '{' {
			value, err = model.DecodeTypedValue(raw)
		} else {
			value, err = model.DecodeJSONValue(raw)
		}
		if err != nil {
			return model.Query{}, err
		}
		q.Filters = append(q.Filters, model.Filter{Field: filter.Field, Op: filter.Op, Value: value})
	}
	return q, nil
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	q, err := decodeQuery(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}
	if err := validateQuery(q); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid query parameters")
		return
	}
	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}
	service, ok := h.engine.(interface {
		ExecuteQueryPage(context.Context, string, model.Query) (model.QueryPage, error)
	})
	if !ok {
		writeInternalError(w, fmt.Errorf("query page service is required"), "Failed to execute query")
		return
	}
	page, err := service.ExecuteQueryPage(r.Context(), database, q)
	if err != nil {
		switch {
		case errors.Is(err, model.ErrInvalidQuery):
			writeError(w, 400, ErrCodeBadRequest, err.Error())
		case errors.Is(err, indexer.ErrNoMatchingIndex):
			writeError(w, 400, "NO_MATCHING_INDEX", "No complete index plan")
		case errors.Is(err, model.ErrStaleCursor):
			writeError(w, 409, "STALE_CURSOR", "Restart the query with the current index generation")
		case errors.Is(err, model.ErrQueryWorkLimit):
			writeError(w, 422, "QUERY_WORK_LIMIT", "Query work limit exceeded")
		case errors.Is(err, indexer.ErrIndexNotReady) || errors.Is(err, indexer.ErrIndexRebuilding):
			writeError(w, 503, "INDEX_UNAVAILABLE", "Index is unavailable")
		case errors.Is(err, context.DeadlineExceeded):
			writeError(w, 504, "DEADLINE_EXCEEDED", "Query deadline exceeded")
		default:
			writeInternalError(w, err, "Failed to execute query")
		}
		return
	}
	encoded, err := wire.EncodeJSONPage(page)
	if errors.Is(err, model.ErrQueryWorkLimit) {
		writeError(w, 422, "QUERY_WORK_LIMIT", "Query page exceeds byte limit")
		return
	}
	if err != nil {
		writeInternalError(w, err, "Failed to encode query page")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}
