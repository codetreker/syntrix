package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/indexer/config"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"github.com/syntrixbase/syntrix/internal/indexer/mem_store"
	"google.golang.org/grpc"
)

// mockLocalService implements LocalService for testing.
type mockLocalService struct {
	searchFn func(ctx context.Context, database string, plan Plan) ([]DocRef, error)
	healthFn func(ctx context.Context) (Health, error)
	mgr      *manager.Manager
}

func (m *mockLocalService) Start(ctx context.Context) error { return nil }
func (m *mockLocalService) Stop(ctx context.Context) error  { return nil }
func (m *mockLocalService) ApplyEvent(ctx context.Context, evt *ChangeEvent, progress string) error {
	return nil
}
func (m *mockLocalService) Stats(ctx context.Context) (Stats, error) {
	return Stats{}, nil
}

func (m *mockLocalService) Search(ctx context.Context, database string, plan Plan) ([]DocRef, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, database, plan)
	}
	return nil, nil
}

func (m *mockLocalService) Health(ctx context.Context) (Health, error) {
	if m.healthFn != nil {
		return m.healthFn(ctx)
	}
	return Health{Status: HealthOK}, nil
}

func (m *mockLocalService) Manager() *manager.Manager {
	return m.mgr
}

func (m *mockLocalService) InvalidateDatabase(ctx context.Context, database string) error {
	return nil
}

func TestNewGRPCServer(t *testing.T) {
	mock := &mockLocalService{
		mgr: manager.New(mem_store.New()),
	}

	server := NewGRPCServer(mock)
	require.NotNil(t, server)
}

func TestGrpcServiceAdapter_Search(t *testing.T) {
	ctx := context.Background()

	mock := &mockLocalService{
		searchFn: func(ctx context.Context, database string, plan Plan) ([]DocRef, error) {
			assert.Equal(t, "testdb", database)
			assert.Equal(t, "users/alice/chats", plan.Collection)
			return []DocRef{
				{ID: "doc1", OrderKey: []byte{0x01, 0x02}},
			}, nil
		},
		mgr: manager.New(mem_store.New()),
	}

	docs, err := mock.Search(ctx, "testdb", Plan{
		Collection: "users/alice/chats",
	})

	require.NoError(t, err)
	assert.Len(t, docs, 1)
	assert.Equal(t, "doc1", docs[0].ID)
}

func TestGrpcServiceAdapter_Health(t *testing.T) {
	ctx := context.Background()

	mock := &mockLocalService{
		healthFn: func(ctx context.Context) (Health, error) {
			return Health{
				Status: HealthDegraded,
				Indexes: map[string]manager.IndexHealth{
					"db1|users/*/chats|ts:desc": {State: "rebuilding", DocCount: 0},
				},
			}, nil
		},
		mgr: manager.New(mem_store.New()),
	}

	health, err := mock.Health(ctx)

	require.NoError(t, err)
	assert.Equal(t, HealthDegraded, health.Status)
	assert.Equal(t, "rebuilding", health.Indexes["db1|users/*/chats|ts:desc"].State)
}

func TestGrpcServiceAdapter_HealthError(t *testing.T) {
	ctx := context.Background()

	mock := &mockLocalService{
		healthFn: func(ctx context.Context) (Health, error) {
			return Health{}, assert.AnError
		},
		mgr: manager.New(mem_store.New()),
	}

	_, err := mock.Health(ctx)
	require.Error(t, err)
}

func TestGrpcServiceAdapter_Manager(t *testing.T) {
	mgr := manager.New(mem_store.New())
	mock := &mockLocalService{mgr: mgr}

	assert.Same(t, mgr, mock.Manager())
}

func TestNewGRPCServer_Integration(t *testing.T) {
	mock := &mockLocalService{
		searchFn: func(ctx context.Context, database string, plan Plan) ([]DocRef, error) {
			return []DocRef{
				{ID: "doc1", OrderKey: []byte{0x01}},
			}, nil
		},
		healthFn: func(ctx context.Context) (Health, error) {
			return Health{Status: HealthOK}, nil
		},
		mgr: manager.New(mem_store.New()),
	}

	server := NewGRPCServer(mock)

	// The server should be ready for use - just verify it was created
	require.NotNil(t, server)
}

func TestNewClient(t *testing.T) {
	// Create a client - this exercises the NewClient function in grpc.go
	// The client.New function is already tested in client_test.go, but
	// NewClient in grpc.go wraps it
	client, err := NewClient("localhost:0", slog.Default())
	// grpc.NewClient doesn't actually connect, so this should succeed
	require.NoError(t, err)
	require.NotNil(t, client)
	client.Close()
}

func statsRemoteClient(t *testing.T, svc LocalService) *Client {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	indexerv1.RegisterIndexerServiceServer(server, NewGRPCServer(svc))
	stopped := make(chan struct{})
	go func() { defer close(stopped); _ = server.Serve(listener) }()
	client, err := NewClient(listener.Addr().String(), testLogger())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Close())
		server.Stop()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			t.Error("gRPC server did not stop")
		}
	})
	return client
}

const statsTemplates = `
templates:
  - name: messages_by_timestamp
    collectionPattern: messages
    fields:
      - { field: timestamp, order: desc }
  - name: messages_by_priority
    collectionPattern: messages
    fields:
      - { field: priority, order: desc }
`

func statsEvent(database string, id int) *ChangeEvent {
	docID := fmt.Sprintf("doc-%d", id)
	doc := storage.NewStoredDoc(database, "messages", docID, map[string]any{"timestamp": id, "priority": id})
	return &ChangeEvent{Database: database, FullDocument: &doc}
}

func TestRemoteStats_ServiceLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc := newTestService(config.Config{}, &bootstrapPuller{marker: "stats-bootstrap"}, testLogger())
	t.Cleanup(func() { require.NoError(t, svc.Stop(context.Background())) })
	remote := statsRemoteClient(t, svc)
	empty, err := remote.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, Stats{}, empty)
	require.NoError(t, svc.Manager().LoadTemplatesFromBytes([]byte(statsTemplates)))
	source := &bootstrapDocuments{}
	require.NoError(t, svc.(BootstrapService).Bootstrap(ctx, BootstrapRequest{Databases: []string{"alpha", "beta"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	loaded, err := remote.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, Stats{TemplateCount: 2}, loaded)
	before := time.Now().Unix()
	for i, db := range []string{"alpha", "beta"} {
		require.NoError(t, svc.ApplyEvent(ctx, statsEvent(db, i), ""))
	}
	local, err := svc.Stats(ctx)
	require.NoError(t, err)
	got, err := remote.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, local, got)
	assert.Equal(t, int64(2), got.EventsApplied)
	assert.GreaterOrEqual(t, got.LastEventTime, before)
	assert.LessOrEqual(t, got.LastEventTime, time.Now().Unix())
	require.NoError(t, svc.Manager().LoadTemplatesFromBytes([]byte(`templates:
  - name: messages_by_timestamp
    collectionPattern: messages
    fields:
      - { field: timestamp, order: desc }
`)))
	reloaded, err := remote.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, reloaded.TemplateCount)
	assert.Equal(t, got.EventsApplied, reloaded.EventsApplied)
	assert.Equal(t, got.LastEventTime, reloaded.LastEventTime)
	replacement := newTestService(config.Config{}, nil, testLogger())
	require.NoError(t, replacement.Manager().LoadTemplatesFromBytes([]byte(statsTemplates)))
	restarted, err := statsRemoteClient(t, replacement).Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, Stats{TemplateCount: 2}, restarted)
}

func TestRemoteStats_ConcurrentApplyEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	svc := newTestService(config.Config{}, &bootstrapPuller{marker: "stats-bootstrap"}, testLogger())
	t.Cleanup(func() { require.NoError(t, svc.Stop(context.Background())) })
	require.NoError(t, svc.Manager().LoadTemplatesFromBytes([]byte(statsTemplates)))
	source := &bootstrapDocuments{}
	require.NoError(t, svc.(BootstrapService).Bootstrap(ctx, BootstrapRequest{Databases: []string{"alpha", "beta"}, Scanner: source, Enumerator: source, WritesQuiesced: true}))
	remote := statsRemoteClient(t, svc)
	const count = 200
	start := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		<-start
		for i := range count {
			if err := svc.ApplyEvent(ctx, statsEvent("alpha", i), ""); err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()
	close(start)
	for range 20 {
		sampled, err := remote.Stats(ctx)
		require.NoError(t, err)
		assert.Equal(t, 2, sampled.TemplateCount)
		assert.GreaterOrEqual(t, sampled.EventsApplied, int64(0))
		assert.LessOrEqual(t, sampled.EventsApplied, int64(count))
	}
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("event application did not finish")
	}
	local, err := svc.Stats(ctx)
	require.NoError(t, err)
	final, err := remote.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(count), final.EventsApplied)
	assert.Equal(t, local, final)
}
