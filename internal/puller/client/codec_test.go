package client

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/buffer"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	pullergrpc "github.com/syntrixbase/syntrix/internal/puller/grpc"
	"github.com/syntrixbase/syntrix/internal/puller/normalizer"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type codecEventSource struct {
	handler func(context.Context, string, *events.StoreChangeEvent) error
}

func (s *codecEventSource) SetEventHandler(handler func(context.Context, string, *events.StoreChangeEvent) error) {
	s.handler = handler
}

func (s *codecEventSource) Replay(context.Context, map[string]string, bool) (events.Iterator, error) {
	return nil, fmt.Errorf("unexpected replay")
}

func TestGRPCDocumentCodecRoundTrip(t *testing.T) {
	t.Parallel()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	source := &codecEventSource{}
	service := pullergrpc.NewServer(config.GRPCConfig{}, source, nil)
	pullerv1.RegisterPullerServiceServer(server, service)
	service.Init()
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		service.Shutdown()
		server.Stop()
		require.NoError(t, listener.Close())
		require.NoError(t, <-serveDone)
	})
	conn, err := grpc.NewClient("passthrough:///puller", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	require.NoError(t, err)
	c := &Client{conn: conn, client: pullerv1.NewPullerServiceClient(conn), logger: slog.Default(), cfg: DefaultClientConfig()}
	t.Cleanup(func() { require.NoError(t, c.Close()) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream := c.Subscribe(ctx, "codec", "")
	require.Eventually(t, func() bool { return service.SubscriberCount() == 1 }, time.Second, time.Millisecond)

	for i, kind := range []string{"live", "tombstone", "expanded"} {
		deleted := kind == "tombstone"
		doc := storage.NewStoredDoc("db", "parents/p/children", "logical-id", nil)
		doc.Deleted = deleted
		doc.Data = map[string]any{"id": "business-id", "before": int64(9007199254740991), "at": int64(9007199254740992), "after": int64(9007199254740993), "float": float64(9007199254740992)}
		if deleted {
			doc.Data = map[string]any{}
		}
		if kind == "expanded" {
			values := make([]any, 200000)
			for j := range values {
				values[j] = int64(0)
			}
			doc.Data["values"] = values
			encoded, err := events.MarshalDocument(&doc)
			require.NoError(t, err)
			require.Greater(t, len(encoded), 4<<20)
		}
		evt := &events.StoreChangeEvent{EventID: fmt.Sprintf("source-%d", i), Database: "db", FullDocument: &doc, OpType: events.StoreOperationUpdate, ClusterTime: events.ClusterTime{T: uint32(i + 1)}, UpdateDesc: &events.UpdateDescription{UpdatedFields: map[string]any{"data.n": int64(9007199254740993)}}}
		if deleted {
			encoded, err := bson.Marshal(doc)
			require.NoError(t, err)
			var fullDocument bson.M
			require.NoError(t, bson.Unmarshal(encoded, &fullDocument))
			raw := &normalizer.RawEvent{
				OperationType: "update", ClusterTime: primitive.Timestamp{T: 2},
				DocumentKey: bson.M{"_id": doc.Id}, FullDocument: fullDocument,
				UpdateDescription: bson.M{"updatedFields": bson.M{
					"deleted": true, "data": bson.M{}, "updated_at": doc.UpdatedAt,
					"version": doc.Version, "sys_expires_at": primitive.DateTime(1789102091123),
				}},
			}
			evt, err = normalizer.New().Normalize(raw)
			require.NoError(t, err)
		}
		if deleted || kind == "expanded" {
			dir := t.TempDir()
			persisted, err := buffer.New(buffer.Options{Path: dir})
			require.NoError(t, err)
			require.NoError(t, persisted.Write(ctx, evt, bson.Raw{5, 0, 0, 0, 0}))
			require.NoError(t, persisted.Close())
			persisted, err = buffer.New(buffer.Options{Path: dir})
			require.NoError(t, err)
			evt, err = persisted.Read(evt.BufferKey())
			require.NoError(t, err)
			require.NoError(t, persisted.Close())
			if deleted {
				require.Equal(t, int64(1789102091123), evt.UpdateDesc.UpdatedFields["sys_expires_at"])
			}
		}
		require.NoError(t, source.handler(ctx, "backend", evt))
		select {
		case received := <-stream:
			require.NotNil(t, received)
			require.Equal(t, &doc, received.Change.FullDocument)
			require.Equal(t, evt.UpdateDesc, received.Change.UpdateDesc)
			require.Equal(t, evt.EventID, received.Change.EventID)
			require.Equal(t, evt.BufferKey(), received.Change.BufferKey())
			require.NotEmpty(t, received.Progress)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	cancel()
	for range stream {
	}
}

func TestClientInvalidPayloadTerminatesWithoutLaterEvents(t *testing.T) {
	t.Parallel()
	stateErrors := make(chan error, 1)
	cfg := DefaultClientConfig()
	cfg.OnStateChange = func(state ConnectionState, err error) {
		if state == StateDisconnected {
			stateErrors <- err
		}
	}
	c := &Client{logger: slog.Default(), cfg: cfg, client: &mockPullerServiceClient{subscribeFunc: func(context.Context, *pullerv1.SubscribeRequest, ...grpc.CallOption) (pullerv1.PullerService_SubscribeClient, error) {
		return &mockSubscribeClient{failAfter: -1, events: []*pullerv1.PullerEvent{
			{ChangeEvent: &pullerv1.ChangeEvent{EventId: "bad", FullDoc: []byte(`{"data":{}}`)}, Progress: "bad-progress"},
			{ChangeEvent: &pullerv1.ChangeEvent{EventId: "later"}, Progress: "later-progress"},
		}}, nil
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream := c.Subscribe(ctx, "invalid", "before")
	select {
	case evt, ok := <-stream:
		require.False(t, ok)
		require.Nil(t, evt)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-stateErrors:
		require.ErrorContains(t, err, "unsupported document wire version")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
