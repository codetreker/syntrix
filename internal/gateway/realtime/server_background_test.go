package realtime

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	api_config "github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/streamer"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/stretchr/testify/assert"
)

// mockStreamerError implements streamer.Service but fails on Stream()
type mockStreamerError struct{}

func (m *mockStreamerError) Stream(ctx context.Context) (streamer.Stream, error) {
	return nil, errors.New("stream error")
}

// mockStreamerStream implements streamer.Service and streamer.Stream
type mockStreamerStream struct {
	stream chan *streamer.EventDelivery
}

func (m *mockStreamerStream) Stream(ctx context.Context) (streamer.Stream, error) {
	return m, nil
}

func (m *mockStreamerStream) Subscribe(ctx context.Context, database, collection string, filters []model.Filter) (streamer.Registration, error) {
	if err := ctx.Err(); err != nil {
		return streamer.Registration{}, err
	}
	return streamer.Registration{ID: "sub-id", Generation: 1}, nil
}

func (m *mockStreamerStream) Status() streamer.StreamStatus {
	return streamer.StreamStatus{State: streamer.StateConnected, Generation: 1}
}

func (m *mockStreamerStream) Unsubscribe(subscriptionID string) error {
	return nil
}

func (m *mockStreamerStream) Recv() (*streamer.EventDelivery, error) {
	select {
	case evt, ok := <-m.stream:
		if !ok {
			return nil, io.EOF
		}
		return evt, nil
	case <-time.After(1 * time.Second):
		// Timeout to avoid blocking tests forever if channel empty
		return nil, nil // Or wait? Ideally we block or return EOF if closed.
		// For Broadcast test, we might not read from here unless we are mocking the other side.
		// Use a blocking receive for now.
	}
	return nil, nil
}

func (m *mockStreamerStream) Close() error {
	return nil
}

type stubQuery struct{}

func (m *stubQuery) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	return nil, nil
}
func (m *stubQuery) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	return nil
}
func (m *stubQuery) ReplaceDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	return nil, nil
}
func (m *stubQuery) PatchDocument(ctx context.Context, database string, data model.Document, pred model.Filters) (model.Document, error) {
	return nil, nil
}
func (m *stubQuery) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	return nil
}
func (m *stubQuery) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	return nil, nil
}
func (m *stubQuery) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	return nil, nil
}
func (m *stubQuery) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	return nil, nil
}

func TestServer_StartBackgroundTasks_WatchError(t *testing.T) {
	srv := NewServer(&stubQuery{}, &mockStreamerError{}, "", nil, api_config.RealtimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := srv.StartBackgroundTasks(ctx)
	assert.Error(t, err)
}

func TestServer_StartBackgroundTasks_Broadcast(t *testing.T) {
	stream := make(chan *streamer.EventDelivery, 1)
	ms := &mockStreamerStream{stream: stream}
	qs := &stubQuery{}
	srv := NewServer(qs, ms, "", nil, api_config.RealtimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// StartBackgroundTasks starts a goroutine that calls Stream() and then reads Recv()
	// and broadcasts to hubs.
	// Since ms.Recv() blocks on channel, we can push to channel and it should be processed.
	// However, we just want to ensure it starts up without error.

	err := srv.StartBackgroundTasks(ctx)
	assert.NoError(t, err)
}

type mockStreamerRecvError struct {
	mockStreamerStream
}

func (m *mockStreamerRecvError) Stream(ctx context.Context) (streamer.Stream, error) {
	return m, nil
}

func (m *mockStreamerRecvError) Recv() (*streamer.EventDelivery, error) {
	return nil, errors.New("unexpected error")
}

func TestStartBackgroundTasks_RecvError(t *testing.T) {
	ms := &mockStreamerRecvError{}
	qs := &stubQuery{}
	srv := NewServer(qs, ms, "", nil, api_config.RealtimeConfig{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := srv.StartBackgroundTasks(ctx)
	assert.NoError(t, err)

	// Sleep briefly to let the goroutine Recv() and hit error path
	time.Sleep(50 * time.Millisecond)
}
