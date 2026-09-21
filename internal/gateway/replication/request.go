package replication

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	storagetypes "github.com/syntrixbase/syntrix/internal/core/storage/types"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// DecodePullRequest decodes the common HTTP and WebSocket source request.
// Transports bound their outer envelopes; this decoder separately bounds the
// embedded request before materializing its JSON or typed values.
func DecodePullRequest(body io.Reader) (request storage.ReplicationPullRequest, failure error) {
	defer func() {
		var watch *storagetypes.WatchError
		var tooLarge *http.MaxBytesError
		if failure != nil && !errors.As(failure, &watch) && !errors.As(failure, &tooLarge) && !errors.Is(failure, storagetypes.ErrInvalidReplicationSource) {
			failure = &Failure{Status: http.StatusBadRequest, Code: "BAD_REQUEST", Message: "Invalid pull request body", Cause: failure}
		}
	}()
	data, err := io.ReadAll(io.LimitReader(body, querycore.MaxPullRequestBytes+1))
	if err != nil {
		return storage.ReplicationPullRequest{}, err
	}
	if len(data) > querycore.MaxPullRequestBytes {
		return request, &http.MaxBytesError{Limit: querycore.MaxPullRequestBytes}
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	_, queryMode := fields["source"]
	if queryMode {
		if err := wire.ValidatePullSourceJSON(data); err != nil {
			return request, err
		}
		defer func() {
			var watchError *storagetypes.WatchError
			var tooLarge *http.MaxBytesError
			if failure != nil && !errors.As(failure, &watchError) && !errors.As(failure, &tooLarge) {
				failure = storagetypes.ErrInvalidReplicationSource
			}
		}()
	}
	if err := model.ValidateJSONUnicode(data); err != nil {
		return storage.ReplicationPullRequest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return storage.ReplicationPullRequest{}, errors.New("pull request must be an object")
	}
	var req storage.ReplicationPullRequest
	var semanticErr error
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return req, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return req, errors.New("duplicate pull request field")
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return req, err
		}
		switch key {
		case "source":
			req.Source, err = wire.DecodeJSONPullSource(raw)
			if err != nil {
				return req, err
			}
		case "requestId":
			var id string
			if bytes.Equal(raw, []byte("null")) || json.Unmarshal(raw, &id) != nil || id == "" || len(id) > querycore.MaxPullRequestBytes {
				return req, storagetypes.ErrInvalidReplicationSource
			}
			req.RequestID = &id
		case "collection":
			if bytes.Equal(raw, []byte("null")) {
				return req, errors.New("collection must be a string")
			}
			if err := json.Unmarshal(raw, &req.Collection); err != nil {
				return req, err
			}
		case "checkpoint":
			if bytes.Equal(raw, []byte("null")) {
				continue
			}
			if len(raw) > 0 && (raw[0] == '-' || (raw[0] >= '0' && raw[0] <= '9')) {
				semanticErr = &storagetypes.WatchError{Code: storagetypes.WatchHistoryUnavailable}
				continue
			}
			if err := json.Unmarshal(raw, &req.Checkpoint); err != nil {
				return req, err
			}
			if len(req.Checkpoint) > querycore.MaxPullCursorBytes {
				semanticErr = &http.MaxBytesError{Limit: querycore.MaxPullCursorBytes}
			}
		case "limit":
			if bytes.Equal(raw, []byte("null")) {
				return req, errors.New("limit must be an integer")
			}
			if err := json.Unmarshal(raw, &req.Limit); err != nil {
				return req, err
			}
		default:
			return req, errors.New("unknown pull request field")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return req, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return req, errors.New("pull request must contain one object")
	}
	if req.Source != nil {
		if req.Source.Limit != nil {
			if req.RequestID == nil || seen["checkpoint"] || seen["limit"] {
				return req, storagetypes.ErrInvalidReplicationSource
			}
		} else if req.RequestID != nil {
			return req, storagetypes.ErrInvalidReplicationSource
		}
		if req.Limit < 0 || req.Limit > wire.MaxPullLimit {
			return req, storagetypes.ErrInvalidReplicationSource
		}
	} else if req.RequestID != nil {
		return req, storagetypes.ErrInvalidReplicationSource
	}
	return req, semanticErr
}
