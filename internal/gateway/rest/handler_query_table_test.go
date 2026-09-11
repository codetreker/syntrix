package rest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/query"
	queryclient "github.com/syntrixbase/syntrix/internal/query/client"
	querygrpc "github.com/syntrixbase/syntrix/internal/query/grpc"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

func TestQueryHandler_TableDriven(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		setupMock      func(*MockQueryService)
		expectedStatus int
		expectedLen    int
	}{
		{
			name: "Success",
			body: `{"collection": "rooms/room-1/messages", "filters": [{"field": "name", "op": "==", "value": "Alice"}]}`,
			setupMock: func(m *MockQueryService) {
				docs := []model.Document{
					{"id": "msg-1", "collection": "rooms/room-1/messages", "name": "Alice", "version": int64(1)},
					{"id": "msg-2", "collection": "rooms/room-1/messages", "name": "Bob", "version": int64(1)},
				}
				m.On("ExecuteQueryPage", mock.Anything, "default", mock.AnythingOfType("model.Query")).Return(model.QueryPage{Documents: docs, EffectiveOrder: []model.Order{{Field: "id", Direction: "asc"}}}, nil)
			},
			expectedStatus: http.StatusOK,
			expectedLen:    2,
		},
		{
			name:           "BadJSON",
			body:           "{bad",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "ValidateError",
			body:           `{}`, // missing collection
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "EngineError",
			body: `{"collection": "rooms"}`,
			setupMock: func(m *MockQueryService) {
				q := model.Query{Collection: "rooms"}
				m.On("ExecuteQueryPage", mock.Anything, "default", q).Return(nil, assert.AnError)
			},
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name: "Canceled",
			body: `{"collection": "rooms"}`,
			setupMock: func(m *MockQueryService) {
				m.On("ExecuteQueryPage", mock.Anything, "default", model.Query{Collection: "rooms"}).Return(nil, context.Canceled)
			},
			expectedStatus: 499,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockService := new(MockQueryService)
			if tt.setupMock != nil {
				tt.setupMock(mockService)
			}

			server := createTestServer(mockService, nil, nil)

			req := httptest.NewRequest("POST", "/api/v1/databases/default/query", bytes.NewReader([]byte(tt.body)))
			rr := httptest.NewRecorder()

			server.ServeHTTP(rr, req)

			assert.Equal(t, tt.expectedStatus, rr.Code)

			if tt.expectedStatus == http.StatusOK {
				var resp struct {
					Documents      []json.RawMessage `json:"documents"`
					NextCursor     *string           `json:"nextCursor"`
					EffectiveOrder []model.Order     `json:"effectiveOrder"`
				}
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
				assert.Len(t, resp.Documents, tt.expectedLen)
				assert.Nil(t, resp.NextCursor)
				assert.Equal(t, []model.Order{{Field: "id", Direction: "asc"}}, resp.EffectiveOrder)
				for _, raw := range resp.Documents {
					_, err := model.DecodeTypedValue(raw)
					require.NoError(t, err)
				}
			}

			mockService.AssertExpectations(t)
		})
	}
}

func TestQueryHandler_InvalidIndexedFilterTransportParity(t *testing.T) {
	for _, transport := range []string{"local", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			service := new(MockQueryService)
			var engine query.Service = service
			if transport == "grpc" {
				listener := bufconn.Listen(1024 * 1024)
				server := grpc.NewServer()
				pb.RegisterQueryServiceServer(server, querygrpc.NewServer(service))
				serveDone := make(chan error, 1)
				go func() { serveDone <- server.Serve(listener) }()
				t.Cleanup(func() {
					server.Stop()
					_ = listener.Close()
					select {
					case err := <-serveDone:
						assert.NoError(t, err)
					case <-time.After(5 * time.Second):
						t.Error("query gRPC server did not stop")
					}
				})
				client, err := queryclient.NewWithOptions("passthrough:///query-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
					return listener.DialContext(ctx)
				}))
				require.NoError(t, err)
				t.Cleanup(func() { assert.NoError(t, client.Close()) })
				engine = client
			}
			handler := createTestServer(engine, nil, nil)
			for _, op := range []model.FilterOp{model.OpNe, model.OpIn, model.OpContains} {
				t.Run(string(op), func(t *testing.T) {
					var value interface{} = "private-filter-value"
					if op == model.OpIn {
						value = []interface{}{"private-filter-value"}
					}
					q := model.Query{Collection: "users", Filters: model.Filters{
						{Field: "status", Op: model.OpEq, Value: "active"},
						{Field: "role", Op: op, Value: value},
					}}
					err := fmt.Errorf("%w: operator %q is not supported by indexed queries", model.ErrInvalidQuery, op)
					service.On("ExecuteQueryPage", mock.Anything, "default", mock.MatchedBy(func(actual model.Query) bool {
						return actual.Collection == q.Collection && assert.ObjectsAreEqual(actual.Filters, q.Filters) &&
							len(actual.OrderBy) == 0 && actual.Limit == 0 && actual.StartAfter == "" && !actual.ShowDeleted
					})).Return(nil, err).Once()
					body, marshalErr := json.Marshal(q)
					require.NoError(t, marshalErr)
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/databases/default/query", bytes.NewReader(body))
					response := httptest.NewRecorder()
					handler.ServeHTTP(response, req)
					require.Equal(t, http.StatusBadRequest, response.Code)
					var actual APIError
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &actual))
					assert.Equal(t, APIError{Code: ErrCodeBadRequest, Message: err.Error()}, actual)
					assert.NotContains(t, response.Body.String(), "private-filter-value")
				})
			}
			service.AssertExpectations(t)
		})
	}
}

func queryTransport(t *testing.T, service *MockQueryService, transport string) query.Service {
	t.Helper()
	if transport == "local" {
		return service
	}
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.MaxSendMsgSize(wire.MaxGRPCBytes))
	pb.RegisterQueryServiceServer(server, querygrpc.NewServer(service))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, listener.Close())
		select {
		case err := <-serveDone:
			assert.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Error("query gRPC server did not stop")
		}
	})
	client, err := queryclient.NewWithOptions("passthrough:///query-page-test", grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	return client
}

func TestQueryHandler_PageTransportParity(t *testing.T) {
	cursor := "next-page-token"
	expected := model.QueryPage{
		NextCursor:     &cursor,
		EffectiveOrder: []model.Order{{Field: "counter", Direction: "desc"}, {Field: "id", Direction: "asc"}},
	}
	for i := range 6 {
		doc := model.Document{
			"id": fmt.Sprintf("large-%d", i), "collection": "users", "deleted": false,
			"version": int64(math.MaxInt64), "createdAt": int64(9007199254740993), "updatedAt": int64(9007199254740995),
			"counter": int64(math.MaxInt64), "minimum": int64(math.MinInt64), "payload": strings.Repeat("x", 900<<10),
			"nested": map[string]any{"type": "int64", "value": "business-data", "values": []any{int64(math.MaxInt64), nil, true}},
		}
		raw, err := json.Marshal(doc)
		require.NoError(t, err)
		require.Less(t, len(raw), 1<<20)
		expected.Documents = append(expected.Documents, doc)
	}
	for _, transport := range []string{"local", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			service := new(MockQueryService)
			service.On("ExecuteQueryPage", mock.MatchedBy(func(ctx context.Context) bool { return ctxkeys.RequestID(ctx) == "request-page-parity" }), "default", mock.MatchedBy(func(q model.Query) bool {
				return q.Collection == "users" && len(q.Filters) == 1 && q.Filters[0].Value == int64(math.MaxInt64) && q.StartAfter == "previous-page-token" && q.Limit == 6
			})).Return(expected, nil).Once()
			engine := queryTransport(t, service, transport)
			value, err := model.EncodeTypedValue(int64(math.MaxInt64))
			require.NoError(t, err)
			body := fmt.Sprintf(`{"collection":"users","filters":[{"field":"counter","op":"==","value":%s}],"limit":6,"startAfter":"previous-page-token"}`, value)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, ctxkeys.KeyRequestID, "request-page-parity")
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/databases/default/query", strings.NewReader(body))
			response := httptest.NewRecorder()
			createTestServer(engine, nil, nil).ServeHTTP(response, req)
			require.Equal(t, http.StatusOK, response.Code)
			require.Greater(t, response.Body.Len(), 4<<20)
			var actual struct {
				Documents      []json.RawMessage `json:"documents"`
				NextCursor     *string           `json:"nextCursor"`
				EffectiveOrder []model.Order     `json:"effectiveOrder"`
			}
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &actual))
			require.Len(t, actual.Documents, len(expected.Documents))
			for i, raw := range actual.Documents {
				doc, err := model.DecodeTypedValue(raw)
				require.NoError(t, err)
				assert.Equal(t, map[string]any(expected.Documents[i]), doc)
			}
			assert.Equal(t, expected.NextCursor, actual.NextCursor)
			assert.Equal(t, expected.EffectiveOrder, actual.EffectiveOrder)
			service.AssertExpectations(t)
		})
	}
}

func TestQueryHandler_ErrorTransportParity(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"stale cursor", model.ErrStaleCursor, 409, "STALE_CURSOR"},
		{"no index", indexer.ErrNoMatchingIndex, 400, "NO_MATCHING_INDEX"},
		{"work limit", model.ErrQueryWorkLimit, 422, "QUERY_WORK_LIMIT"},
		{"unavailable", indexer.ErrIndexNotReady, 503, "INDEX_UNAVAILABLE"},
		{"rebuilding", indexer.ErrIndexRebuilding, 503, "INDEX_UNAVAILABLE"},
		{"deadline", context.DeadlineExceeded, 504, "DEADLINE_EXCEEDED"},
	}
	for _, transport := range []string{"local", "grpc"} {
		t.Run(transport, func(t *testing.T) {
			service := new(MockQueryService)
			engine := queryTransport(t, service, transport)
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					service.On("ExecuteQueryPage", mock.Anything, "default", model.Query{Collection: "users"}).Return(nil, fmt.Errorf("execution failed: %w", tc.err)).Once()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/databases/default/query", strings.NewReader(`{"collection":"users"}`))
					response := httptest.NewRecorder()
					createTestServer(engine, nil, nil).ServeHTTP(response, req)
					require.Equal(t, tc.status, response.Code)
					var actual APIError
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &actual))
					assert.Equal(t, tc.code, actual.Code)
				})
			}
			service.AssertExpectations(t)
		})
	}
}

func TestQueryHandler_RejectsMalformedTypedFilters(t *testing.T) {
	for _, value := range []string{`{"type":"unknown","value":"1"}`, `{"type":"int64","value":"9223372036854775808"}`, `{"plain":"object"}`} {
		t.Run(value, func(t *testing.T) {
			service := new(MockQueryService)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/databases/default/query", strings.NewReader(fmt.Sprintf(`{"collection":"users","filters":[{"field":"value","op":"==","value":%s}]}`, value)))
			response := httptest.NewRecorder()
			createTestServer(service, nil, nil).ServeHTTP(response, req)
			assert.Equal(t, http.StatusBadRequest, response.Code)
			service.AssertNotCalled(t, "ExecuteQueryPage", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

type documentOnlyQueryService struct{ query.Service }

func TestQueryHandler_RequiresPageService(t *testing.T) {
	service := new(MockQueryService)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/databases/default/query", strings.NewReader(`{"collection":"users"}`))
	response := httptest.NewRecorder()
	createTestServer(documentOnlyQueryService{Service: service}, nil, nil).ServeHTTP(response, req)
	assert.Equal(t, http.StatusInternalServerError, response.Code)
	service.AssertNotCalled(t, "ExecuteQuery", mock.Anything, mock.Anything, mock.Anything)
}

func TestQueryHandlerRejectsInvalidUnicode(t *testing.T) {
	for _, body := range []string{
		`{"collection":"users\ud800"}`,
		`{"collection":"users","filters":[{"field":"\ud800","op":"==","value":null}]}`,
		`{"collection":"users","orderBy":[{"field":"\udfff","direction":"asc"}]}`,
		`{"collection":"users","filters":[{"field":"name","op":"==","value":"\ud800"}]}`,
		`{"collection":"users","filters":[{"field":"name","op":"==","value":{"type":"string","value":"\udfff"}}]}`,
		`{"collection":"users","\ud800":null}`,
	} {
		service := new(MockQueryService)
		request := httptest.NewRequest(http.MethodPost, "/api/v1/databases/default/query", strings.NewReader(body))
		response := httptest.NewRecorder()
		createTestServer(service, nil, nil).ServeHTTP(response, request)
		require.Equal(t, http.StatusBadRequest, response.Code, body)
		service.AssertNotCalled(t, "ExecuteQueryPage", mock.Anything, mock.Anything, mock.Anything)
	}
	q, err := decodeQuery(strings.NewReader(`{"collection":"users","filters":[{"field":"name","op":"==","value":"\ud83d\ude00"}]}`))
	require.NoError(t, err)
	require.Equal(t, "😀", q.Filters[0].Value)
}
