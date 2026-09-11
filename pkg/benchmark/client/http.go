// Package client provides HTTP client for communicating with Syntrix API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/syntrixbase/syntrix/pkg/benchmark/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// HTTPClient implements the Client interface for HTTP communication.
type HTTPClient struct {
	baseURL    string
	database   string
	httpClient *http.Client
	token      string
}

// NewHTTPClient creates a new HTTP client.
func NewHTTPClient(baseURL string, database string, token string) (*HTTPClient, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("baseURL is required")
	}

	if database == "" {
		database = "default"
	}

	// Ensure baseURL doesn't have trailing slash
	baseURL = strings.TrimSuffix(baseURL, "/")

	return &HTTPClient{
		baseURL:  baseURL,
		database: database,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 100,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		token: token,
	}, nil
}

// CreateDocument creates a new document in the specified collection.
func (c *HTTPClient) CreateDocument(ctx context.Context, collection string, doc map[string]interface{}) (*types.Document, error) {
	url := fmt.Sprintf("%s/api/v1/databases/%s/documents/%s", c.baseURL, c.database, collection)

	body := map[string]interface{}{
		"doc": doc,
	}

	var result types.Document
	if err := c.doRequest(ctx, "POST", url, body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// GetDocument retrieves a document by ID.
func (c *HTTPClient) GetDocument(ctx context.Context, collection, id string) (*types.Document, error) {
	url := fmt.Sprintf("%s/api/v1/databases/%s/documents/%s/%s", c.baseURL, c.database, collection, id)

	var result types.Document
	if err := c.doRequest(ctx, "GET", url, nil, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// UpdateDocument updates an existing document.
func (c *HTTPClient) UpdateDocument(ctx context.Context, collection, id string, doc map[string]interface{}) (*types.Document, error) {
	url := fmt.Sprintf("%s/api/v1/databases/%s/documents/%s/%s", c.baseURL, c.database, collection, id)

	body := map[string]interface{}{
		"doc": doc,
	}

	var result types.Document
	if err := c.doRequest(ctx, "PATCH", url, body, &result); err != nil {
		return nil, err
	}

	return &result, nil
}

// DeleteDocument deletes a document by ID.
func (c *HTTPClient) DeleteDocument(ctx context.Context, collection, id string) error {
	url := fmt.Sprintf("%s/api/v1/databases/%s/documents/%s/%s", c.baseURL, c.database, collection, id)

	return c.doRequest(ctx, "DELETE", url, nil, nil)
}

// Query returns the first response page so one benchmark operation makes one
// query request, regardless of whether the server provides a continuation.
func (c *HTTPClient) Query(ctx context.Context, query types.Query) ([]*types.Document, error) {
	if query.Offset != 0 {
		return nil, fmt.Errorf("query offsets are not supported")
	}
	url := fmt.Sprintf("%s/api/v1/databases/%s/query", c.baseURL, c.database)
	type queryFilter struct {
		Field string          `json:"field"`
		Op    model.FilterOp  `json:"op"`
		Value json.RawMessage `json:"value"`
	}
	request := struct {
		Collection string        `json:"collection"`
		Filters    []queryFilter `json:"filters,omitempty"`
		OrderBy    []model.Order `json:"orderBy,omitempty"`
		Limit      int           `json:"limit,omitempty"`
	}{Collection: query.Collection, OrderBy: query.OrderBy, Limit: query.Limit}
	filters, err := model.NormalizeFilters(model.Filters(query.Filters))
	if err != nil {
		return nil, fmt.Errorf("invalid query filters: %w", err)
	}
	for _, filter := range filters {
		value, err := model.EncodeTypedValue(filter.Value)
		if err != nil {
			return nil, fmt.Errorf("encode query filter %q: %w", filter.Field, err)
		}
		request.Filters = append(request.Filters, queryFilter{Field: filter.Field, Op: filter.Op, Value: value})
	}

	var response json.RawMessage
	if err := c.doRequest(ctx, "POST", url, request, &response); err != nil {
		return nil, err
	}
	if err := model.ValidateJSONUnicode(response); err != nil {
		return nil, fmt.Errorf("invalid query page: %w", err)
	}
	var page struct {
		Documents      []json.RawMessage `json:"documents"`
		NextCursor     json.RawMessage   `json:"nextCursor"`
		EffectiveOrder []model.Order     `json:"effectiveOrder"`
	}
	decoder := json.NewDecoder(bytes.NewReader(response))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		return nil, fmt.Errorf("invalid query page: %w", err)
	}
	if page.Documents == nil || len(page.NextCursor) == 0 || len(page.EffectiveOrder) == 0 {
		return nil, fmt.Errorf("query page requires documents, nextCursor, and effectiveOrder")
	}
	if !bytes.Equal(bytes.TrimSpace(page.NextCursor), []byte("null")) {
		var cursor string
		if err := json.Unmarshal(page.NextCursor, &cursor); err != nil {
			return nil, fmt.Errorf("invalid query page nextCursor: %w", err)
		}
		if cursor == "" {
			return nil, fmt.Errorf("query page nextCursor must be nonempty or null")
		}
	}
	for _, order := range page.EffectiveOrder {
		if err := model.ValidateQueryField(order.Field); err != nil {
			return nil, fmt.Errorf("invalid query page effectiveOrder: %w", err)
		}
		if order.Direction != "asc" && order.Direction != "desc" {
			return nil, fmt.Errorf("invalid query page order direction %q", order.Direction)
		}
	}
	results := make([]*types.Document, 0, len(page.Documents))
	for i, raw := range page.Documents {
		value, err := model.DecodeTypedValue(raw)
		if err != nil {
			return nil, fmt.Errorf("query document %d: %w", i, err)
		}
		data, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("query document %d must be an object", i)
		}
		id, validID := data["id"].(string)
		collection, validCollection := data["collection"].(string)
		version, validVersion := data["version"].(int64)
		createdAt, validCreatedAt := data["createdAt"].(int64)
		updatedAt, validUpdatedAt := data["updatedAt"].(int64)
		if !validID || id == "" || !validCollection || collection == "" || !validVersion || !validCreatedAt || !validUpdatedAt {
			return nil, fmt.Errorf("query document %d has invalid or missing metadata", i)
		}
		for _, field := range []string{"id", "collection", "version", "createdAt", "updatedAt"} {
			delete(data, field)
		}
		results = append(results, &types.Document{
			ID: id, Collection: collection, Version: version,
			CreatedAt: types.UnixMilliTime{Time: time.UnixMilli(createdAt)},
			UpdatedAt: types.UnixMilliTime{Time: time.UnixMilli(updatedAt)},
			Data:      data,
		})
	}

	return results, nil
}

// Subscribe creates a real-time subscription (stub implementation for now).
func (c *HTTPClient) Subscribe(ctx context.Context, query types.Query) (*types.Subscription, error) {
	// WebSocket implementation would go here
	// For now, return an error indicating it's not implemented
	return nil, fmt.Errorf("realtime subscriptions not yet implemented in HTTP client")
}

// Unsubscribe removes a subscription (stub implementation for now).
func (c *HTTPClient) Unsubscribe(ctx context.Context, subID string) error {
	// WebSocket implementation would go here
	return fmt.Errorf("realtime subscriptions not yet implemented in HTTP client")
}

// Close closes the HTTP client and releases resources.
func (c *HTTPClient) Close() error {
	// Close idle connections
	c.httpClient.CloseIdleConnections()
	return nil
}

// doRequest performs an HTTP request with the given method, URL, and body.
func (c *HTTPClient) doRequest(ctx context.Context, method, urlStr string, body interface{}, result interface{}) error {
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.token))
	}

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	// Check status code
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{
			StatusCode: resp.StatusCode,
			Status:     resp.Status,
			Body:       string(respBody),
		}
	}

	// Parse response if result is provided
	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			slog.Error("failed to unmarshal response",
				"error", err,
				"method", method,
				"url", urlStr,
				"status", resp.StatusCode,
				"body", string(respBody),
			)
			return fmt.Errorf("failed to unmarshal response: %w", err)
		}
	}

	return nil
}

// HTTPError represents an HTTP error response.
type HTTPError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("HTTP %d %s: %s", e.StatusCode, e.Status, e.Body)
}

// IsHTTPError checks if an error is an HTTPError.
func IsHTTPError(err error) bool {
	_, ok := err.(*HTTPError)
	return ok
}

// GetHTTPError returns the HTTPError from an error if it exists.
func GetHTTPError(err error) (*HTTPError, bool) {
	httpErr, ok := err.(*HTTPError)
	return httpErr, ok
}

// SetToken updates the authentication token.
func (c *HTTPClient) SetToken(token string) {
	c.token = token
}

// GetBaseURL returns the base URL.
func (c *HTTPClient) GetBaseURL() string {
	return c.baseURL
}

// GetDatabase returns the database name.
func (c *HTTPClient) GetDatabase() string {
	return c.database
}

// ParseURL parses and validates a URL.
func ParseURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("invalid URL scheme: %s (must be http or https)", u.Scheme)
	}

	return rawURL, nil
}
