package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func snapshotClient(t *testing.T, pages ...*storage.ReplicationPullResponse) (*Client, *MockQueryService) {
	t.Helper()
	service := &MockQueryService{}
	checkpoint := ""
	for _, page := range pages {
		service.On("Pull", mock.Anything, "db", storage.ReplicationPullRequest{
			Collection: "users", Checkpoint: checkpoint, Limit: 1000,
		}).Return(page, nil).Once()
		if page != nil {
			checkpoint = page.Checkpoint
		}
	}
	client := &Client{
		hub: NewTestHub(), queryService: service, database: "db", authenticated: true,
		send: make(chan BaseMessage, 3), subscriptions: make(map[string]Subscription), streamerSubIDs: make(map[string]string),
	}
	t.Cleanup(func() { service.AssertExpectations(t) })
	return client, service
}

func requestSnapshot(t *testing.T, client *Client) BaseMessage {
	t.Helper()
	client.handleMessage(BaseMessage{Type: TypeSubscribe, ID: "sub", Payload: mustMarshal(SubscribePayload{
		Query: model.Query{Collection: "users"}, SendSnapshot: true,
	})})
	require.Equal(t, TypeSubscribeAck, (<-client.send).Type)
	response := <-client.send
	require.Equal(t, "sub", response.ID)
	require.Empty(t, client.send)
	return response
}

func TestSnapshotConsumesPagesAndAppliesReplay(t *testing.T) {
	client, _ := snapshotClient(t,
		&storage.ReplicationPullResponse{Checkpoint: "scan-2", Documents: []model.Document{
			{"id": "alice", "name": "old"}, {"id": "bob"}, {"id": "carol"},
		}},
		&storage.ReplicationPullResponse{Checkpoint: "replay"},
		&storage.ReplicationPullResponse{Checkpoint: "replay-2", Documents: []model.Document{
			{"id": "alice", "name": "new"}, {"id": "bob", "deleted": true},
			{"id": "carol", "deleted": true}, {"id": "absent", "deleted": true},
		}},
		&storage.ReplicationPullResponse{Checkpoint: "head", CaughtUp: true, Documents: []model.Document{
			{"id": "carol", "name": "recreated"}, {"id": "alice", "name": "new"},
		}},
	)
	response := requestSnapshot(t, client)
	require.Equal(t, TypeSnapshot, response.Type)
	require.JSONEq(t, `{"subId":"sub","documents":[{"id":"alice","name":"new"},{"id":"carol","name":"recreated"}]}`, string(response.Payload))
}

func TestSnapshotCountsRetainedDocumentsNotReplayEvents(t *testing.T) {
	docs := make([]model.Document, 1000)
	for i := range docs {
		docs[i] = model.Document{"id": "alice", "value": i}
	}
	client, _ := snapshotClient(t,
		&storage.ReplicationPullResponse{Checkpoint: "one", Documents: docs},
		&storage.ReplicationPullResponse{Checkpoint: "head", CaughtUp: true, Documents: docs},
	)
	response := requestSnapshot(t, client)
	require.Equal(t, TypeSnapshot, response.Type)
	require.JSONEq(t, `{"subId":"sub","documents":[{"id":"alice","value":999}]}`, string(response.Payload))
}

func TestSnapshotEmptyAndDeletedCollections(t *testing.T) {
	for _, docs := range [][]model.Document{nil, {{"id": "alice", "deleted": true}}} {
		client, _ := snapshotClient(t, &storage.ReplicationPullResponse{Checkpoint: "head", CaughtUp: true, Documents: docs})
		response := requestSnapshot(t, client)
		require.Equal(t, TypeSnapshot, response.Type)
		require.JSONEq(t, `{"subId":"sub","documents":[]}`, string(response.Payload))
	}
}

func TestSnapshotOversizedAggregateDoesNotEmitPrefix(t *testing.T) {
	pages := make([]*storage.ReplicationPullResponse, 2)
	for page := range pages {
		docs := make([]model.Document, 400)
		for i := range docs {
			docs[i] = model.Document{"id": fmt.Sprint(page*400 + i), "collection": "users", "data": strings.Repeat("x", 40<<10)}
		}
		pages[page] = &storage.ReplicationPullResponse{Documents: docs, Checkpoint: fmt.Sprint(page + 1), CaughtUp: page == 1}
		encoded, err := wire.EncodeJSONPullPage(pages[page])
		require.NoError(t, err, "each Pull page is valid independently")
		require.Less(t, len(encoded), wire.MaxPageBytes)
	}
	client, _ := snapshotClient(t, pages...)
	response := requestSnapshot(t, client)
	require.Equal(t, TypeError, response.Type)
	var failure ErrorPayload
	require.NoError(t, json.Unmarshal(response.Payload, &failure))
	require.Equal(t, "snapshot_limit", failure.Code)
}

func TestSnapshotDocumentCapacityDoesNotEmitPrefix(t *testing.T) {
	docs := make([]model.Document, 1000)
	for i := range docs {
		docs[i] = model.Document{"id": fmt.Sprint(i)}
	}
	client, _ := snapshotClient(t,
		&storage.ReplicationPullResponse{Checkpoint: "next", Documents: docs},
		&storage.ReplicationPullResponse{Checkpoint: "head", CaughtUp: true, Documents: []model.Document{{"id": "1000"}}},
	)
	response := requestSnapshot(t, client)
	require.Equal(t, TypeError, response.Type)
	require.Contains(t, string(response.Payload), `"code":"snapshot_limit"`)
}

func TestSnapshotExactByteBudgetAndReplacementAccounting(t *testing.T) {
	for _, excess := range []int{0, 1} {
		t.Run(fmt.Sprint(excess), func(t *testing.T) {
			doc := model.Document{"id": "alice", "data": ""}
			empty := mustMarshal(SnapshotPayload{SubID: "escaped\"sub", Documents: []map[string]interface{}{doc}})
			doc["data"] = strings.Repeat("x", wire.MaxPageBytes-len(empty)+excess)
			client, _ := snapshotClient(t,
				&storage.ReplicationPullResponse{Checkpoint: "next", Documents: []model.Document{{"id": "alice", "data": "old"}}},
				&storage.ReplicationPullResponse{Checkpoint: "head", CaughtUp: true, Documents: []model.Document{doc}},
			)
			payload, err := client.collectSnapshot(context.Background(), "escaped\"sub", "users")
			if excess == 1 {
				require.ErrorIs(t, err, model.ErrQueryWorkLimit)
				require.Nil(t, payload)
			} else {
				require.NoError(t, err)
				require.Len(t, payload, wire.MaxPageBytes)
			}
		})
	}
}

func TestSnapshotFailuresDoNotEmitSuccess(t *testing.T) {
	tests := []struct {
		name  string
		pages []*storage.ReplicationPullResponse
	}{
		{"nil response", []*storage.ReplicationPullResponse{nil}},
		{"missing checkpoint", []*storage.ReplicationPullResponse{{CaughtUp: true}}},
		{"stalled checkpoint", []*storage.ReplicationPullResponse{{Checkpoint: "next"}, {Checkpoint: "next"}}},
		{"missing logical ID", []*storage.ReplicationPullResponse{{Checkpoint: "head", CaughtUp: true, Documents: []model.Document{{"value": 1}}}}},
		{"invalid JSON value", []*storage.ReplicationPullResponse{{Checkpoint: "head", CaughtUp: true, Documents: []model.Document{{"id": "alice", "value": make(chan int)}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := snapshotClient(t, test.pages...)
			response := requestSnapshot(t, client)
			require.Equal(t, TypeError, response.Type)
			require.Contains(t, string(response.Payload), `"code":"snapshot_failed"`)
		})
	}
}

func TestSnapshotPullFailureAfterProgress(t *testing.T) {
	client, service := snapshotClient(t, &storage.ReplicationPullResponse{Checkpoint: "next", Documents: []model.Document{{"id": "alice"}}})
	service.On("Pull", mock.Anything, "db", storage.ReplicationPullRequest{Collection: "users", Checkpoint: "next", Limit: 1000}).Return(nil, context.DeadlineExceeded).Once()
	response := requestSnapshot(t, client)
	require.Equal(t, TypeError, response.Type)
	require.Contains(t, string(response.Payload), `"code":"snapshot_failed"`)
}

func TestSnapshotContextCancellation(t *testing.T) {
	t.Run("before Pull", func(t *testing.T) {
		client, service := snapshotClient(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client.sendSnapshot(ctx, "sub", "users")
		require.Equal(t, TypeError, (<-client.send).Type)
		service.AssertNotCalled(t, "Pull", mock.Anything, mock.Anything, mock.Anything)
	})
	t.Run("during Pull", func(t *testing.T) {
		client, service := snapshotClient(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		service.On("Pull", mock.Anything, "db", mock.Anything).Run(func(mock.Arguments) { cancel() }).Return(&storage.ReplicationPullResponse{Checkpoint: "head", CaughtUp: true}, nil).Once()
		client.sendSnapshot(ctx, "sub", "users")
		require.Equal(t, TypeError, (<-client.send).Type)
	})
}

type snapshotCancelMarshaler struct {
	cancel context.CancelFunc
}

func (value snapshotCancelMarshaler) MarshalJSON() ([]byte, error) {
	value.cancel()
	return []byte(`"encoded"`), nil
}

func TestSnapshotCancellationDuringEncoding(t *testing.T) {
	for _, documentCount := range []int{1, 2} {
		t.Run(fmt.Sprint(documentCount), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			secondEncoded := false
			documents := []model.Document{{"id": "alice", "value": snapshotCancelMarshaler{cancel: cancel}}}
			if documentCount == 2 {
				documents = append(documents, model.Document{"id": "bob", "value": snapshotCancelMarshaler{cancel: func() { secondEncoded = true }}})
			}
			client, _ := snapshotClient(t, &storage.ReplicationPullResponse{Checkpoint: "head", CaughtUp: true, Documents: documents})
			client.sendSnapshot(ctx, "sub", "users")
			response := <-client.send
			require.Equal(t, TypeError, response.Type)
			require.Contains(t, string(response.Payload), `"code":"snapshot_failed"`)
			require.Empty(t, client.send)
			require.False(t, secondEncoded)
		})
	}
}

func TestSnapshotResponseDeliveryIsBounded(t *testing.T) {
	for _, stopHub := range []bool{false, true} {
		client, _ := snapshotClient(t)
		client.send = make(chan BaseMessage)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if stopHub {
			client.hub.setRunCtx(ctx)
			cancel()
			ctx = context.Background()
		} else {
			cancel()
		}
		done := make(chan struct{})
		go func() {
			client.deliverSnapshot(ctx, BaseMessage{Type: TypeSnapshot})
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("snapshot response blocked on a full outbound queue")
		}
	}
}
