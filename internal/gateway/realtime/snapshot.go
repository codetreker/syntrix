package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type encodedSnapshot struct {
	SubID     string            `json:"subId"`
	Documents []json.RawMessage `json:"documents"`
}

func (c *Client) sendSnapshot(ctx context.Context, subID, collection string) {
	payload, err := c.collectSnapshot(ctx, subID, collection)
	message := BaseMessage{ID: subID, Type: TypeSnapshot, Payload: payload}
	if err != nil {
		slog.Error("WS: Snapshot failed", "error", err)
		failure := ErrorPayload{Code: "snapshot_failed", Message: "Snapshot could not be completed"}
		if errors.Is(err, model.ErrQueryWorkLimit) {
			failure = ErrorPayload{Code: "snapshot_limit", Message: "Snapshot exceeds 1000 documents or 16 MiB; use paginated Pull"}
		}
		message.Type, message.Payload = TypeError, mustMarshal(failure)
	}

	// A fetch deadline must still allow its error response to reach the client.
	deliveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeWait)
	defer cancel()
	c.deliverSnapshot(deliveryCtx, message)
}

func (c *Client) deliverSnapshot(ctx context.Context, message BaseMessage) {
	stopped := c.outboundDone()
	c.sendMu.RLock()
	defer c.sendMu.RUnlock()
	select {
	case <-stopped:
		return
	default:
	}
	select {
	case c.send <- message:
	case <-ctx.Done():
		slog.Warn("WS: Snapshot response delivery timed out", "error", ctx.Err())
	case <-stopped:
	case <-c.hub.Done():
	}
}

func (c *Client) collectSnapshot(ctx context.Context, subID, collection string) (json.RawMessage, error) {
	payload := encodedSnapshot{SubID: subID, Documents: []json.RawMessage{}}
	envelopeBytes := len(mustMarshal(payload))
	documents := make(map[string]json.RawMessage)
	documentBytes := 0
	request := storage.ReplicationPullRequest{Collection: collection, Limit: wire.MaxPullLimit}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := c.queryService.Pull(ctx, c.database, request)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page == nil || page.Checkpoint == "" || (!page.CaughtUp && page.Checkpoint == request.Checkpoint) {
			return nil, errors.New("snapshot Pull did not supply a progressing checkpoint")
		}
		for _, doc := range page.Documents {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			id := doc.GetID()
			if id == "" {
				return nil, errors.New("snapshot document has no logical ID")
			}
			documentBytes -= len(documents[id])
			delete(documents, id)
			if doc.IsDeleted() {
				continue
			}
			encoded, err := json.Marshal(doc)
			if err != nil {
				return nil, fmt.Errorf("encode snapshot document: %w", err)
			}
			documents[id] = encoded
			documentBytes += len(encoded)
			if len(documents) > wire.MaxPullLimit || envelopeBytes+documentBytes+max(0, len(documents)-1) > wire.MaxPageBytes {
				return nil, model.ErrQueryWorkLimit
			}
		}
		if page.CaughtUp {
			break
		}
		request.Checkpoint = page.Checkpoint
	}
	ids := make([]string, 0, len(documents))
	for id := range documents {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		payload.Documents = append(payload.Documents, documents[id])
	}
	encoded, err := json.Marshal(payload)
	if contextErr := ctx.Err(); contextErr != nil {
		return nil, contextErr
	}
	return encoded, err
}
