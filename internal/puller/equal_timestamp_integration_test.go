package puller

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	pullerclient "github.com/syntrixbase/syntrix/internal/puller/client"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/core"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/puller/normalizer"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestPuller_EqualTimestampTransactionLiveReplayAndReopen(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	mongoURI := os.Getenv("MONGO_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI).SetServerSelectionTimeout(3*time.Second))
	require.NoError(t, err)
	dbName := fmt.Sprintf("equal_timestamp_%d", time.Now().UnixNano())
	db := mongoClient.Database(dbName)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		require.NoError(t, db.Drop(cleanupCtx))
		require.NoError(t, mongoClient.Disconnect(cleanupCtx))
	})
	require.NoError(t, mongoClient.Ping(ctx, nil))
	const collection = "documents"
	require.NoError(t, db.CreateCollection(ctx, collection))
	coll := db.Collection(collection)
	cfg := config.Config{
		Buffer: config.BufferConfig{
			Path: filepath.Join(t.TempDir(), "buffer"), BatchSize: 1,
			BatchInterval: time.Millisecond, QueueSize: 100, MaxSize: "10MB",
		},
		Cleaner:   config.CleanerConfig{Retention: time.Hour, Interval: time.Minute},
		Bootstrap: config.BootstrapConfig{Mode: "from_now"},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := func() (*core.Puller, *pullerclient.Client, func()) {
		p := core.New(cfg, logger)
		require.NoError(t, p.AddBackend("backend1", mongoClient, dbName, config.PullerBackendConfig{
			Name: "backend1", Collections: []string{collection},
		}))
		require.NoError(t, p.Start(ctx))
		server := newTestGRPCServer(t, p, logger)
		remote, err := pullerclient.New(fmt.Sprintf("localhost:%d", server.port), logger)
		require.NoError(t, err)
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			require.NoError(t, remote.Close())
			server.Stop()
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer stopCancel()
			require.NoError(t, p.Stop(stopCtx))
		}
		t.Cleanup(stop)
		return p, remote, stop
	}
	p, remote, stop := start()
	boundary, err := p.BootstrapBoundary(ctx)
	require.NoError(t, err)
	original, err := cursor.DecodeProgressMarker(boundary)
	require.NoError(t, err)
	require.NotEmpty(t, original.Lineages["backend1"])
	require.Empty(t, original.Positions["backend1"])

	liveCtx, liveCancel := context.WithCancel(ctx)
	defer liveCancel()
	live := []struct {
		name   string
		stream <-chan *events.PullerEvent
	}{
		{"local", p.SubscribeReady(liveCtx, "local-live", boundary, nil)},
		{"remote", remote.SubscribeReady(liveCtx, "remote-live", boundary, nil)},
	}
	for _, subscription := range live {
		require.True(t, receiveEqualTimestampEvent(t, ctx, subscription.stream).Ready, subscription.name)
	}

	// Transaction order deliberately opposes buffer key order so a timestamp-only
	// watermark or a lexicographic EventID watermark loses sibling documents.
	docs := make([]storage.StoredDoc, 4)
	eventIDs := make(map[string]string, len(docs))
	for i := range docs {
		docs[i] = storage.NewStoredDoc(dbName, collection, fmt.Sprintf("doc-%d", i), map[string]any{"value": i})
		raw := normalizer.RawEvent{
			OperationType: "insert", ClusterTime: primitive.Timestamp{T: 1, I: 1},
			DocumentKey: bson.M{"_id": docs[i].Id},
		}
		raw.Namespace.DB, raw.Namespace.Coll = dbName, collection
		event, err := normalizer.New().Normalize(&raw)
		require.NoError(t, err)
		eventIDs[docs[i].Id] = event.EventID
	}
	slices.SortFunc(docs, func(a, b storage.StoredDoc) int {
		return strings.Compare(eventIDs[b.Id], eventIDs[a.Id])
	})
	insertions := make([]any, len(docs))
	expectedDocumentIDs := make([]string, len(docs))
	for i, doc := range docs {
		insertions[i] = doc
		expectedDocumentIDs[i] = doc.Id
	}
	session, err := mongoClient.StartSession()
	require.NoError(t, err)
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx mongo.SessionContext) (any, error) {
		return coll.InsertMany(txCtx, insertions, options.InsertMany().SetOrdered(true))
	})
	require.NoError(t, err)

	insideProgress := make(map[string]string, len(live))
	var expectedEvents []string
	var transactionTime events.ClusterTime
	for _, subscription := range live {
		var deliveredIDs, deliveredEvents []string
		for i := range docs {
			event := receiveEqualTimestampEvent(t, ctx, subscription.stream)
			require.False(t, event.Ready, subscription.name)
			require.NotNil(t, event.Change, subscription.name)
			require.NotNil(t, event.Change.TxnNumber, "must originate from an actual MongoDB transaction")
			if transactionTime.IsZero() {
				transactionTime = event.Change.ClusterTime
			}
			require.Equal(t, transactionTime, event.Change.ClusterTime, "transaction documents share ClusterTime")
			deliveredIDs = append(deliveredIDs, event.Change.MgoDocID)
			deliveredEvents = append(deliveredEvents, event.Change.EventID)
			if i == 1 {
				insideProgress[subscription.name] = event.Progress
			}
		}
		require.Equal(t, expectedDocumentIDs, deliveredIDs, subscription.name)
		require.True(t, slices.IsSortedFunc(deliveredEvents, func(a, b string) int {
			return strings.Compare(b, a)
		}), "source order must oppose EventID hash order")
		if expectedEvents == nil {
			expectedEvents = deliveredEvents
		} else {
			require.Equal(t, expectedEvents, deliveredEvents)
		}
	}
	liveCancel()
	for _, subscription := range live {
		waitEqualTimestampStreamClosed(t, ctx, subscription.stream)
	}

	verifyReplay := func(stage string, local, remote BoundaryService) {
		for _, transport := range []struct {
			name    string
			service BoundaryService
		}{{"local", local}, {"remote", remote}} {
			t.Run(stage+"/"+transport.name, func(t *testing.T) {
				progress := insideProgress[transport.name]
				require.NoError(t, transport.service.ValidateBoundary(ctx, progress))
				replayCtx, replayCancel := context.WithCancel(ctx)
				defer replayCancel()
				stream := transport.service.SubscribeReady(replayCtx, stage+"-"+transport.name, progress, nil)
				seen := make(map[string]bool)
				var lastChangeProgress string
				for {
					event := receiveEqualTimestampEvent(t, ctx, stream)
					if event.Ready {
						require.Equal(t, lastChangeProgress, event.Progress)
						readyProgress, err := cursor.DecodeProgressMarker(event.Progress)
						require.NoError(t, err)
						require.Equal(t, original.Lineages, readyProgress.Lineages)
						break
					}
					require.NotNil(t, event.Change)
					require.Equal(t, transactionTime, event.Change.ClusterTime)
					require.Contains(t, expectedEvents, event.Change.EventID)
					require.False(t, seen[event.Change.EventID], "each timestamp sibling is delivered once per subscription")
					seen[event.Change.EventID] = true
					lastChangeProgress = event.Progress
				}
				require.Len(t, seen, len(docs), "every timestamp sibling must replay, including the acknowledged prefix")
				replayCancel()
				waitEqualTimestampStreamClosed(t, ctx, stream)
			})
		}
	}
	verifyReplay("reconnect", p, remote)
	stop()
	p, remote, _ = start()
	require.NoError(t, p.ValidateBoundary(ctx, boundary))
	verifyReplay("reopen", p, remote)
}

func receiveEqualTimestampEvent(t *testing.T, ctx context.Context, stream <-chan *events.PullerEvent) *events.PullerEvent {
	t.Helper()
	select {
	case event, ok := <-stream:
		require.True(t, ok, "subscription closed before its expected event")
		require.NotNil(t, event)
		require.NoError(t, event.Error)
		return event
	case <-ctx.Done():
		t.Fatalf("waiting for transaction event: %v", ctx.Err())
		return nil
	}
}

func waitEqualTimestampStreamClosed(t *testing.T, ctx context.Context, stream <-chan *events.PullerEvent) {
	t.Helper()
	for {
		select {
		case _, ok := <-stream:
			if !ok {
				return
			}
		case <-ctx.Done():
			t.Fatalf("waiting for canceled subscription: %v", ctx.Err())
		}
	}
}
