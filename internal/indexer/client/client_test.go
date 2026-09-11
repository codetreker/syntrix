package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	indexerv1 "github.com/syntrixbase/syntrix/api/gen/indexer/v1"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// mockServer implements IndexerServiceServer for testing.
type mockServer struct {
	indexerv1.UnimplementedIndexerServiceServer
	candidatesFn      func(*indexerv1.CandidateRequest, indexerv1.IndexerService_OpenCandidatesServer) error
	statsFn           func(context.Context, *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error)
	searchFn          func(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error)
	healthFn          func(ctx context.Context, req *indexerv1.HealthRequest) (*indexerv1.HealthResponse, error)
	getStateFn        func(ctx context.Context, req *indexerv1.GetStateRequest) (*indexerv1.IndexerState, error)
	reloadFn          func(ctx context.Context, req *indexerv1.ReloadRequest) (*indexerv1.ReloadResponse, error)
	invalidateIndexFn func(ctx context.Context, req *indexerv1.InvalidateIndexRequest) (*indexerv1.InvalidateIndexResponse, error)
}

func (m *mockServer) OpenCandidates(req *indexerv1.CandidateRequest, stream indexerv1.IndexerService_OpenCandidatesServer) error {
	if m.candidatesFn != nil {
		return m.candidatesFn(req, stream)
	}
	return m.UnimplementedIndexerServiceServer.OpenCandidates(req, stream)
}

func (m *mockServer) Stats(ctx context.Context, req *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error) {
	if m.statsFn != nil {
		return m.statsFn(ctx, req)
	}
	return m.UnimplementedIndexerServiceServer.Stats(ctx, req)
}

func (m *mockServer) Search(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error) {
	if m.searchFn != nil {
		return m.searchFn(ctx, req)
	}
	return &indexerv1.SearchResponse{}, nil
}

func (m *mockServer) Health(ctx context.Context, req *indexerv1.HealthRequest) (*indexerv1.HealthResponse, error) {
	if m.healthFn != nil {
		return m.healthFn(ctx, req)
	}
	return &indexerv1.HealthResponse{Status: "ok"}, nil
}

func (m *mockServer) GetState(ctx context.Context, req *indexerv1.GetStateRequest) (*indexerv1.IndexerState, error) {
	if m.getStateFn != nil {
		return m.getStateFn(ctx, req)
	}
	return &indexerv1.IndexerState{}, nil
}

func (m *mockServer) Reload(ctx context.Context, req *indexerv1.ReloadRequest) (*indexerv1.ReloadResponse, error) {
	if m.reloadFn != nil {
		return m.reloadFn(ctx, req)
	}
	return &indexerv1.ReloadResponse{}, nil
}

func (m *mockServer) InvalidateIndex(ctx context.Context, req *indexerv1.InvalidateIndexRequest) (*indexerv1.InvalidateIndexResponse, error) {
	if m.invalidateIndexFn != nil {
		return m.invalidateIndexFn(ctx, req)
	}
	return &indexerv1.InvalidateIndexResponse{}, nil
}

// setupTestServer creates a test gRPC server and returns a client connected to it.
func setupTestServer(t *testing.T, mock *mockServer) (*Client, func()) {
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)

	server := grpc.NewServer()
	indexerv1.RegisterIndexerServiceServer(server, mock)

	go func() {
		_ = server.Serve(lis)
	}()

	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	client := &Client{
		conn:   conn,
		client: indexerv1.NewIndexerServiceClient(conn),
		logger: slog.Default(),
	}

	cleanup := func() {
		client.Close()
		server.Stop()
		lis.Close()
	}

	return client, cleanup
}

func TestClient_Search(t *testing.T) {
	ctx := context.Background()

	t.Run("successful search", func(t *testing.T) {
		mock := &mockServer{
			searchFn: func(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error) {
				assert.Equal(t, "testdb", req.Database)
				assert.Equal(t, "users/alice/chats", req.Collection)
				assert.Equal(t, int32(10), req.Limit)

				return &indexerv1.SearchResponse{
					Docs: []*indexerv1.DocRef{
						{Id: "doc1", OrderKey: base64.StdEncoding.EncodeToString([]byte{0x01, 0x02})},
						{Id: "doc2", OrderKey: base64.StdEncoding.EncodeToString([]byte{0x03, 0x04})},
					},
				}, nil
			},
		}

		client, cleanup := setupTestServer(t, mock)
		defer cleanup()

		docs, err := client.Search(ctx, "testdb", manager.Plan{
			Collection: "users/alice/chats",
			Limit:      10,
		})

		require.NoError(t, err)
		assert.Len(t, docs, 2)
		assert.Equal(t, "doc1", docs[0].ID)
		assert.Equal(t, []byte{0x01, 0x02}, docs[0].OrderKey)
	})

	t.Run("search with filters", func(t *testing.T) {
		mock := &mockServer{
			searchFn: func(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error) {
				require.Len(t, req.Filters, 1)
				assert.Equal(t, "status", req.Filters[0].Field)
				assert.Equal(t, "eq", req.Filters[0].Op)

				var value string
				err := json.Unmarshal(req.Filters[0].Value, &value)
				require.NoError(t, err)
				assert.Equal(t, "active", value)

				return &indexerv1.SearchResponse{}, nil
			},
		}

		client, cleanup := setupTestServer(t, mock)
		defer cleanup()

		_, err := client.Search(ctx, "testdb", manager.Plan{
			Collection: "users/alice/chats",
			Filters: []manager.Filter{
				{Field: "status", Op: manager.FilterEq, Value: "active"},
			},
		})

		require.NoError(t, err)
	})

	t.Run("search with order by", func(t *testing.T) {
		mock := &mockServer{
			searchFn: func(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error) {
				require.Len(t, req.OrderBy, 1)
				assert.Equal(t, "timestamp", req.OrderBy[0].Field)
				assert.Equal(t, "desc", req.OrderBy[0].Direction)
				return &indexerv1.SearchResponse{}, nil
			},
		}

		client, cleanup := setupTestServer(t, mock)
		defer cleanup()

		_, err := client.Search(ctx, "testdb", manager.Plan{
			Collection: "users/alice/chats",
			OrderBy: []manager.OrderField{
				{Field: "timestamp", Direction: encoding.Desc},
			},
		})

		require.NoError(t, err)
	})
}

func TestClient_Health(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		healthFn: func(ctx context.Context, req *indexerv1.HealthRequest) (*indexerv1.HealthResponse, error) {
			return &indexerv1.HealthResponse{
				Status: "ok",
				Indexes: map[string]*indexerv1.IndexHealth{
					"db1|users/*/chats|ts:desc": {
						State:    "healthy",
						DocCount: 100,
					},
				},
			}, nil
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	health, err := client.Health(ctx)

	require.NoError(t, err)
	assert.Equal(t, manager.HealthStatus("ok"), health.Status)
	assert.Len(t, health.Indexes, 1)
	assert.Equal(t, "healthy", health.Indexes["db1|users/*/chats|ts:desc"].State)
	assert.Equal(t, int64(100), health.Indexes["db1|users/*/chats|ts:desc"].DocCount)
}

func TestClient_GetState(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		getStateFn: func(ctx context.Context, req *indexerv1.GetStateRequest) (*indexerv1.IndexerState, error) {
			return &indexerv1.IndexerState{
				Desired: []*indexerv1.IndexSpec{
					{
						Pattern:    "users/*/chats",
						TemplateId: "ts:desc",
						Fields: []*indexerv1.IndexField{
							{Field: "timestamp", Direction: "desc"},
						},
					},
				},
				Actual: []*indexerv1.IndexInfo{
					{
						Database:   "db1",
						Pattern:    "users/*/chats",
						TemplateId: "ts:desc",
						State:      "healthy",
						DocCount:   50,
					},
				},
				PendingOps: []*indexerv1.PendingOperation{
					{
						OpType:     "rebuild",
						Database:   "db1",
						Pattern:    "rooms/*/messages",
						TemplateId: "ts:desc",
						Status:     "running",
						Progress:   45,
					},
				},
			}, nil
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	state, err := client.GetState(ctx, "", "")

	require.NoError(t, err)
	assert.Len(t, state.Desired, 1)
	assert.Equal(t, "users/*/chats", state.Desired[0].Pattern)
	assert.Len(t, state.Actual, 1)
	assert.Equal(t, "db1", state.Actual[0].Database)
	assert.Len(t, state.PendingOps, 1)
	assert.Equal(t, "rebuild", state.PendingOps[0].OpType)
	assert.Equal(t, int32(45), state.PendingOps[0].Progress)
}

func TestClient_Reload(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		reloadFn: func(ctx context.Context, req *indexerv1.ReloadRequest) (*indexerv1.ReloadResponse, error) {
			return &indexerv1.ReloadResponse{
				TemplatesLoaded: 5,
				Errors:          []string{"warning: deprecated field"},
			}, nil
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	result, err := client.Reload(ctx)

	require.NoError(t, err)
	assert.Equal(t, int32(5), result.TemplatesLoaded)
	assert.Len(t, result.Errors, 1)
}

func TestClient_InvalidateIndex(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		invalidateIndexFn: func(ctx context.Context, req *indexerv1.InvalidateIndexRequest) (*indexerv1.InvalidateIndexResponse, error) {
			assert.Equal(t, "db1", req.Database)
			assert.Equal(t, "users/*/chats", req.Pattern)
			assert.Equal(t, "ts:desc", req.TemplateId)
			return &indexerv1.InvalidateIndexResponse{
				IndexesInvalidated: 3,
			}, nil
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	count, err := client.InvalidateIndex(ctx, "db1", "users/*/chats", "ts:desc")

	require.NoError(t, err)
	assert.Equal(t, int32(3), count)
}

func TestClient_Close(t *testing.T) {
	mock := &mockServer{}
	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	err := client.Close()
	assert.NoError(t, err)
}

func TestClient_Close_Nil(t *testing.T) {
	client := &Client{conn: nil}
	err := client.Close()
	assert.NoError(t, err)
}

func TestClient_New(t *testing.T) {
	// Start a test server
	lis, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	defer lis.Close()

	server := grpc.NewServer()
	indexerv1.RegisterIndexerServiceServer(server, &mockServer{})
	go func() {
		_ = server.Serve(lis)
	}()
	defer server.Stop()

	// Create client using New function
	client, err := New(lis.Addr().String(), slog.Default())
	require.NoError(t, err)
	require.NotNil(t, client)

	// Verify the client can make calls
	ctx := context.Background()
	health, err := client.Health(ctx)
	require.NoError(t, err)
	assert.Equal(t, manager.HealthStatus("ok"), health.Status)

	// Close the client
	err = client.Close()
	assert.NoError(t, err)
}

func TestClient_New_InvalidAddress(t *testing.T) {
	// grpc.NewClient doesn't actually connect, it just validates the address
	// We need to test actual connection failure during a call
	client, err := New("invalid-address-without-port", slog.Default())
	// NewClient may succeed but calls will fail
	if err == nil && client != nil {
		defer client.Close()
	}
}

func TestClient_Search_Error(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		searchFn: func(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error) {
			return nil, assert.AnError
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	_, err := client.Search(ctx, "testdb", manager.Plan{
		Collection: "users/alice/chats",
	})

	require.Error(t, err)
}

func TestClient_Health_Error(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		healthFn: func(ctx context.Context, req *indexerv1.HealthRequest) (*indexerv1.HealthResponse, error) {
			return nil, assert.AnError
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	_, err := client.Health(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "health check failed")
}

func TestClient_GetState_Error(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		getStateFn: func(ctx context.Context, req *indexerv1.GetStateRequest) (*indexerv1.IndexerState, error) {
			return nil, assert.AnError
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	_, err := client.GetState(ctx, "", "")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "get state failed")
}

func TestClient_Reload_Error(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		reloadFn: func(ctx context.Context, req *indexerv1.ReloadRequest) (*indexerv1.ReloadResponse, error) {
			return nil, assert.AnError
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	_, err := client.Reload(ctx)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reload failed")
}

func TestClient_InvalidateIndex_Error(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		invalidateIndexFn: func(ctx context.Context, req *indexerv1.InvalidateIndexRequest) (*indexerv1.InvalidateIndexResponse, error) {
			return nil, assert.AnError
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	_, err := client.InvalidateIndex(ctx, "db1", "pattern", "template")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalidate index failed")
}

func TestClient_Search_InvalidOrderKey(t *testing.T) {
	ctx := context.Background()

	mock := &mockServer{
		searchFn: func(ctx context.Context, req *indexerv1.SearchRequest) (*indexerv1.SearchResponse, error) {
			return &indexerv1.SearchResponse{
				Docs: []*indexerv1.DocRef{
					{Id: "doc1", OrderKey: "not-valid-base64!!!"}, // Invalid base64
				},
			}, nil
		},
	}

	client, cleanup := setupTestServer(t, mock)
	defer cleanup()

	_, err := client.Search(ctx, "testdb", manager.Plan{
		Collection: "users/alice/chats",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode order key")
}

func TestClient_New_DialError(t *testing.T) {
	// Save original dialer and restore after test
	origDialer := grpcDialer
	defer func() { grpcDialer = origDialer }()

	// Override dialer to return an error
	grpcDialer = func(target string, opts ...grpc.DialOption) (*grpc.ClientConn, error) {
		return nil, assert.AnError
	}

	client, err := New("localhost:0", slog.Default())
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "failed to connect to indexer")
}
func TestClient_TranslateError(t *testing.T) {
	client := &Client{}

	t.Run("NotFound returns ErrNoMatchingIndex", func(t *testing.T) {
		err := status.Error(codes.NotFound, "index not found")
		result := client.translateError(err)
		assert.ErrorIs(t, result, manager.ErrNoMatchingIndex)
	})

	t.Run("Unavailable with rebuilding message", func(t *testing.T) {
		err := status.Error(codes.Unavailable, "index is rebuilding")
		result := client.translateError(err)
		assert.ErrorIs(t, result, manager.ErrIndexRebuilding)
	})

	t.Run("Unavailable without rebuilding message", func(t *testing.T) {
		err := status.Error(codes.Unavailable, "some other message")
		result := client.translateError(err)
		assert.ErrorIs(t, result, manager.ErrIndexNotReady)
	})

	t.Run("InvalidArgument returns ErrInvalidPlan", func(t *testing.T) {
		err := status.Error(codes.InvalidArgument, "bad plan")
		result := client.translateError(err)
		assert.ErrorIs(t, result, manager.ErrInvalidPlan)
	})

	t.Run("Unknown code returns original error", func(t *testing.T) {
		err := status.Error(codes.Internal, "internal error")
		result := client.translateError(err)
		assert.Equal(t, err, result)
	})

	t.Run("Non-status error returns original", func(t *testing.T) {
		err := assert.AnError
		result := client.translateError(err)
		assert.Equal(t, err, result)
	})
}

func TestClient_Stats(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response *indexerv1.StatsResponse
	}{
		{"empty", &indexerv1.StatsResponse{}},
		{"exact int64 values", &indexerv1.StatsResponse{TemplateCount: int64(^uint(0) >> 1), EventsApplied: 1<<53 + 1, LastEventTime: math.MaxInt64}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := make(chan struct{}, 1)
			client, cleanup := setupTestServer(t, &mockServer{statsFn: func(context.Context, *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error) {
				called <- struct{}{}
				return tc.response, nil
			}})
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			got, err := client.Stats(ctx)
			require.NoError(t, err)
			assert.Equal(t, manager.Stats{TemplateCount: int(tc.response.TemplateCount), EventsApplied: tc.response.EventsApplied, LastEventTime: tc.response.LastEventTime}, got)
			select {
			case <-called:
			default:
				t.Fatal("Stats did not reach the server")
			}
		})
	}
	t.Run("rejects invalid template count", func(t *testing.T) {
		counts := []int64{-1}
		if strconv.IntSize == 32 {
			counts = append(counts, int64(math.MaxInt32)+1)
		}
		for _, count := range counts {
			client, cleanup := setupTestServer(t, &mockServer{statsFn: func(context.Context, *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error) {
				return &indexerv1.StatsResponse{TemplateCount: count}, nil
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := client.Stats(ctx)
			require.Error(t, err)
			cancel()
			cleanup()
		}
	})
	for _, code := range []codes.Code{codes.Unavailable, codes.Internal, codes.ResourceExhausted} {
		t.Run(code.String(), func(t *testing.T) {
			client, cleanup := setupTestServer(t, &mockServer{statsFn: func(context.Context, *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error) {
				return nil, status.Error(code, "statistics failed")
			}})
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := client.Stats(ctx)
			require.Error(t, err)
			assert.Equal(t, code, status.Code(err))
			assert.Contains(t, err.Error(), client.conn.Target())
		})
	}
	t.Run("old server", func(t *testing.T) {
		client, cleanup := setupTestServer(t, &mockServer{})
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Stats(ctx)
		assert.Equal(t, codes.Unimplemented, status.Code(err))
	})
	t.Run("canceled in flight", func(t *testing.T) {
		entered := make(chan struct{})
		client, cleanup := setupTestServer(t, &mockServer{statsFn: func(ctx context.Context, _ *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := client.Stats(ctx); result <- err }()
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("request never reached server")
		}
		cancel()
		select {
		case err := <-result:
			assert.Equal(t, codes.Canceled, status.Code(err))
		case <-time.After(5 * time.Second):
			t.Fatal("canceled request did not return")
		}
	})
	t.Run("deadline", func(t *testing.T) {
		client, cleanup := setupTestServer(t, &mockServer{statsFn: func(ctx context.Context, _ *indexerv1.StatsRequest) (*indexerv1.StatsResponse, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		defer cleanup()
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		_, err := client.Stats(ctx)
		assert.Equal(t, codes.DeadlineExceeded, status.Code(err))
	})
	t.Run("connection closed", func(t *testing.T) {
		client, cleanup := setupTestServer(t, &mockServer{})
		defer cleanup()
		require.NoError(t, client.Close())
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := client.Stats(ctx)
		assert.Equal(t, codes.Canceled, status.Code(err))
	})
}
