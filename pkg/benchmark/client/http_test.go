package client

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/pkg/benchmark/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func TestNewHTTPClient(t *testing.T) {
	tests := []struct {
		name        string
		baseURL     string
		database    string
		token       string
		expectError bool
	}{
		{"valid URL", "http://localhost:8080", "default", "token123", false},
		{"valid URL with trailing slash", "http://localhost:8080/", "testdb", "token123", false},
		{"empty URL", "", "default", "token123", true},
		{"empty database defaults to default", "http://localhost:8080", "", "token123", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewHTTPClient(tt.baseURL, tt.database, tt.token)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
				assert.False(t, strings.HasSuffix(client.baseURL, "/"), "baseURL should not have trailing slash")
				if tt.database == "" {
					assert.Equal(t, "default", client.database)
				} else {
					assert.Equal(t, tt.database, client.database)
				}
			}
		})
	}
}

func TestHTTPClient_CreateDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/databases/default/documents/test-collection", r.URL.Path)
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))

		// Parse request body
		var body map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&body)
		assert.NoError(t, err)

		doc := body["doc"].(map[string]interface{})
		assert.Equal(t, "test-value", doc["test-field"])

		// Return response
		response := map[string]interface{}{
			"id":   "doc-123",
			"data": doc,
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)

	doc := map[string]interface{}{
		"test-field": "test-value",
	}

	result, err := client.CreateDocument(context.Background(), "test-collection", doc)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "doc-123", result.ID)
}

func TestHTTPClient_GetDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "GET", r.Method)
		assert.Equal(t, "/api/v1/databases/default/documents/test-collection/doc-123", r.URL.Path)

		response := map[string]interface{}{
			"id": "doc-123",
			"data": map[string]interface{}{
				"field": "value",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)

	result, err := client.GetDocument(context.Background(), "test-collection", "doc-123")
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "doc-123", result.ID)
}

func TestHTTPClient_UpdateDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "PATCH", r.Method)
		assert.Equal(t, "/api/v1/databases/default/documents/test-collection/doc-123", r.URL.Path)

		response := map[string]interface{}{
			"id": "doc-123",
			"data": map[string]interface{}{
				"updated": "value",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)

	doc := map[string]interface{}{
		"updated": "value",
	}

	result, err := client.UpdateDocument(context.Background(), "test-collection", "doc-123", doc)
	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "doc-123", result.ID)
}

func TestHTTPClient_DeleteDocument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "DELETE", r.Method)
		assert.Equal(t, "/api/v1/databases/default/documents/test-collection/doc-123", r.URL.Path)

		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)

	err = client.DeleteDocument(context.Background(), "test-collection", "doc-123")
	assert.NoError(t, err)
}

func TestHTTPClient_Query(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "/api/v1/databases/default/query", r.URL.Path)
		var body json.RawMessage
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		assert.JSONEq(t, `{
			"collection":"test-collection",
			"filters":[{"field":"count","op":">=","value":{"type":"int64","value":"9007199254740993"}}],
			"orderBy":[{"field":"count","direction":"desc"}],
			"limit":3
		}`, string(body))
		_, err := w.Write([]byte(`{
			"documents":[{"type":"object","value":{
				"id":{"type":"string","value":"doc-1"},
				"collection":{"type":"string","value":"test-collection"},
				"version":{"type":"int64","value":"9007199254740993"},
				"createdAt":{"type":"int64","value":"1720000000000"},
				"updatedAt":{"type":"int64","value":"1720000000001"},
				"name":{"type":"string","value":"test1"},
				"count":{"type":"int64","value":"9223372036854775807"},
				"nested":{"type":"array","value":[{"type":"int64","value":"-9223372036854775808"},{"type":"float64","value":1.5}]},
				"data":{"type":"object","value":{"type":{"type":"string","value":"business"}}}
			}}],
			"nextCursor":"continue-this-page",
			"effectiveOrder":[{"field":"count","direction":"desc"},{"field":"id","direction":"asc"}]
		}`))
		assert.NoError(t, err)
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)

	query := types.Query{
		Collection: "test-collection",
		Filters:    []model.Filter{{Field: "count", Op: model.OpGte, Value: int64(9007199254740993)}},
		OrderBy:    []model.Order{{Field: "count", Direction: "desc"}},
		Limit:      3,
	}

	results, err := client.Query(context.Background(), query)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "doc-1", results[0].ID)
	assert.Equal(t, "test-collection", results[0].Collection)
	assert.Equal(t, int64(9007199254740993), results[0].Version)
	assert.Equal(t, int64(1720000000000), results[0].CreatedAt.UnixMilli())
	assert.Equal(t, int64(1720000000001), results[0].UpdatedAt.UnixMilli())
	assert.Equal(t, map[string]any{
		"name": "test1", "count": int64(math.MaxInt64),
		"nested": []any{int64(math.MinInt64), float64(1.5)},
		"data":   map[string]any{"type": "business"},
	}, results[0].Data)
	assert.Equal(t, int32(1), requests.Load())
}

func TestHTTPClient_QueryRejectsOffsetBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)
	for _, offset := range []int{-1, 1, 100} {
		results, err := client.Query(context.Background(), types.Query{Collection: "test-collection", Offset: offset})
		require.ErrorContains(t, err, "offsets are not supported")
		assert.Nil(t, results)
	}
	assert.Zero(t, requests.Load())
}

func TestHTTPClient_QueryEmptyPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := w.Write([]byte(`{"documents":[],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`))
		assert.NoError(t, err)
	}))
	defer server.Close()
	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)
	results, err := client.Query(context.Background(), types.Query{Collection: "test-collection"})
	require.NoError(t, err)
	assert.NotNil(t, results)
	assert.Empty(t, results)
}

func TestHTTPClient_QueryRejectsInvalidPage(t *testing.T) {
	for _, response := range []string{
		`[]`, `null`, `{}`, `{"documents":[],"nextCursor":null}`,
		`{"documents":null,"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`,
		`{"documents":{},"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`,
		`{"documents":[],"nextCursor":12,"effectiveOrder":[{"field":"id","direction":"asc"}]}`,
		`{"documents":[],"nextCursor":"","effectiveOrder":[{"field":"id","direction":"asc"}]}`,
		`{"documents":[],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"sideways"}]}`,
		`{"documents":[],"nextCursor":null,"effectiveOrder":[{"field":"\ud800","direction":"asc"}]}`,
		`{"documents":[],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}],"data":[]}`,
		`{"documents":[{"type":"null"}],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`,
		`{"documents":[{"id":"plain"}],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`,
		`{"documents":[{"type":"object","value":{"id":{"type":"string","value":"missing-metadata"}}}],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`,
		`{"documents":[{"type":"object","value":{"id":{"type":"string","value":"\ud800"}}}],"nextCursor":null,"effectiveOrder":[{"field":"id","direction":"asc"}]}`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := w.Write([]byte(response))
				assert.NoError(t, err)
			}))
			defer server.Close()
			client, err := NewHTTPClient(server.URL, "default", "test-token")
			require.NoError(t, err)
			results, err := client.Query(context.Background(), types.Query{Collection: "test-collection"})
			require.Error(t, err)
			assert.Nil(t, results)
		})
	}
}

func TestHTTPClient_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error": "not found"}`))
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)

	_, err = client.GetDocument(context.Background(), "test-collection", "nonexistent")
	assert.Error(t, err)

	httpErr, ok := GetHTTPError(err)
	assert.True(t, ok)
	assert.Equal(t, http.StatusNotFound, httpErr.StatusCode)
	assert.Contains(t, httpErr.Body, "not found")
}

func TestHTTPClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewHTTPClient(server.URL, "default", "test-token")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = client.GetDocument(ctx, "test-collection", "doc-123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestHTTPClient_SetToken(t *testing.T) {
	client, err := NewHTTPClient("http://localhost:8080", "default", "old-token")
	require.NoError(t, err)

	assert.Equal(t, "old-token", client.token)

	client.SetToken("new-token")
	assert.Equal(t, "new-token", client.token)
}

func TestHTTPClient_GetBaseURL(t *testing.T) {
	client, err := NewHTTPClient("http://localhost:8080", "default", "token")
	require.NoError(t, err)

	assert.Equal(t, "http://localhost:8080", client.GetBaseURL())
}

func TestHTTPClient_GetDatabase(t *testing.T) {
	client, err := NewHTTPClient("http://localhost:8080", "mydb", "token")
	require.NoError(t, err)

	assert.Equal(t, "mydb", client.GetDatabase())
}

func TestHTTPClient_Close(t *testing.T) {
	client, err := NewHTTPClient("http://localhost:8080", "default", "token")
	require.NoError(t, err)

	err = client.Close()
	assert.NoError(t, err)
}

func TestParseURL(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		expectError bool
		errorMsg    string
	}{
		{"valid http", "http://localhost:8080", false, ""},
		{"valid https", "https://api.example.com", false, ""},
		{"invalid scheme", "ftp://example.com", true, "invalid URL scheme"},
		{"no scheme", "localhost:8080", false, ""}, // url.Parse doesn't fail on this
		{"invalid URL", "ht!tp://invalid", true, "invalid URL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseURL(tt.url)
			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				if err == nil {
					assert.Equal(t, tt.url, result)
				}
			}
		})
	}
}

func TestHTTPClient_Subscribe_NotImplemented(t *testing.T) {
	client, err := NewHTTPClient("http://localhost:8080", "default", "token")
	require.NoError(t, err)

	query := types.Query{Collection: "test"}
	_, err = client.Subscribe(context.Background(), query)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestHTTPClient_Unsubscribe_NotImplemented(t *testing.T) {
	client, err := NewHTTPClient("http://localhost:8080", "default", "token")
	require.NoError(t, err)

	err = client.Unsubscribe(context.Background(), "sub-123")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestIsHTTPError(t *testing.T) {
	httpErr := &HTTPError{StatusCode: 404}
	assert.True(t, IsHTTPError(httpErr))

	otherErr := assert.AnError
	assert.False(t, IsHTTPError(otherErr))
}

func TestHTTPError_Error(t *testing.T) {
	err := &HTTPError{
		StatusCode: 404,
		Status:     "Not Found",
		Body:       `{"error": "resource not found"}`,
	}

	errMsg := err.Error()
	assert.Contains(t, errMsg, "404")
	assert.Contains(t, errMsg, "Not Found")
	assert.Contains(t, errMsg, "resource not found")
}
