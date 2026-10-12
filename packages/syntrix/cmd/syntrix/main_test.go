package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pullerv1 "github.com/codetreker/syntrix/api/gen/puller/v1"
	"github.com/codetreker/syntrix/internal/config"
	indexerconfig "github.com/codetreker/syntrix/internal/indexer/config"
	"github.com/codetreker/syntrix/internal/indexer/persist_store"
	"github.com/codetreker/syntrix/internal/indexer/store"
	"github.com/codetreker/syntrix/internal/server"
	"github.com/codetreker/syntrix/internal/services"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type startupPullerServer struct {
	pullerv1.UnimplementedPullerServiceServer
	entered       chan struct{}
	release       <-chan struct{}
	failure       error
	subscriptions atomic.Int32
}

func (s *startupPullerServer) ValidateBoundary(ctx context.Context, _ *pullerv1.ValidateBoundaryRequest) (*pullerv1.BoundaryResponse, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return nil, status.FromContextError(ctx.Err()).Err()
	case <-s.release:
		return nil, s.failure
	}
}

func (s *startupPullerServer) Subscribe(*pullerv1.SubscribeRequest, grpc.ServerStreamingServer[pullerv1.PullerEvent]) error {
	s.subscriptions.Add(1)
	return status.Error(codes.FailedPrecondition, "startup validation must succeed before subscribing")
}

func TestServeStartupFailureClosesListenersAndIndexerStore(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		code    codes.Code
	}{
		{name: "permanent validation", failure: status.Error(codes.FailedPrecondition, "resume boundary expired"), code: codes.FailedPrecondition},
		{name: "independent cancellation", failure: status.Error(codes.Canceled, "validation canceled by Puller"), code: codes.Canceled},
		{name: "validation timeout", code: codes.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			previousServer := server.Default()
			t.Cleanup(func() { server.SetDefault(previousServer) })
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseValidation := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseValidation)
			puller := &startupPullerServer{entered: make(chan struct{}), release: release, failure: test.failure}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			remote := grpc.NewServer()
			pullerv1.RegisterPullerServiceServer(remote, puller)
			go func() { _ = remote.Serve(listener) }()
			t.Cleanup(func() {
				remote.Stop()
				_ = listener.Close()
			})

			cfg := &config.Config{Server: server.DefaultConfig(), Indexer: indexerconfig.DefaultConfig()}
			cfg.Server.Host = "127.0.0.1"
			cfg.Server.RateLimit.Enabled = false
			cfg.Server.HTTPPort, cfg.Server.GRPCPort = unusedServerPorts(t)
			cfg.Indexer.PullerAddr = listener.Addr().String()
			cfg.Indexer.TemplatePath = ""
			cfg.Indexer.StartupTimeout = time.Second
			cfg.Indexer.StorageMode = indexerconfig.StorageModePebble
			cfg.Indexer.Store.Path = filepath.Join(t.TempDir(), "indexes")
			persisted, err := persist_store.NewPebbleStore(cfg.Indexer.Store, slog.Default())
			require.NoError(t, err)
			require.NoError(t, persisted.PublishBootstrapCatalog(store.BootstrapCatalog{
				Database: "default", Generation: "startup-generation", BootstrapProgress: "startup-boundary",
			}))
			require.NoError(t, persisted.Close())

			result := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				result <- serve(cfg, services.Options{Mode: services.ModeDistributed, RunIndexer: true})
			}()
			t.Cleanup(func() {
				releaseValidation()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("serve did not finish after startup validation failed")
				}
			})
			select {
			case <-puller.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("Indexer did not reach Puller boundary validation")
			}
			addresses := []string{
				fmt.Sprintf("127.0.0.1:%d", cfg.Server.HTTPPort),
				fmt.Sprintf("127.0.0.1:%d", cfg.Server.GRPCPort),
			}
			for _, address := range addresses {
				require.Eventually(t, func() bool {
					connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
					if err != nil {
						return false
					}
					_ = connection.Close()
					return true
				}, 500*time.Millisecond, 10*time.Millisecond)
			}
			if test.failure != nil {
				releaseValidation()
			}
			select {
			case err = <-result:
			case <-time.After(5 * time.Second):
				t.Fatal("serve kept running after startup validation failed")
			}
			require.ErrorContains(t, err, "start services: start Indexer Service:")
			require.Equal(t, test.code, status.Code(err))
			require.Zero(t, puller.subscriptions.Load())
			for _, address := range addresses {
				closedListener, err := net.Listen("tcp", address)
				require.NoError(t, err, "serve must close its listeners before returning")
				require.NoError(t, closedListener.Close())
			}
			reopened, err := persist_store.NewPebbleStore(cfg.Indexer.Store, slog.Default())
			require.NoError(t, err, "serve must release the failed Indexer's persistent store")
			require.NoError(t, reopened.Close())
		})
	}
}

func unusedServerPorts(t *testing.T) (int, int) {
	t.Helper()
	first, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer first.Close()
	second, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer second.Close()
	return first.Addr().(*net.TCPAddr).Port, second.Addr().(*net.TCPAddr).Port
}
