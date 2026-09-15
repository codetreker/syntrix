package mongo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func TestWatchBoundaryScanCommittedPages(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", time.Hour).(*documentStore)
	require.NoError(t, store.EnsureIndexes(ctx))
	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", id, map[string]interface{}{"n": int64(9007199254740993)})))
	}
	require.NoError(t, store.Delete(ctx, "tenant", "users/b", nil))
	transaction, err := env.Client.StartSession()
	require.NoError(t, err)
	defer transaction.EndSession(ctx)
	require.NoError(t, transaction.StartTransaction())
	defer transaction.AbortTransaction(ctx)
	_, err = env.DB.Collection("docs").InsertOne(mongo.NewSessionContext(ctx, transaction), types.NewStoredDoc("tenant", "users", "uncommitted", nil))
	require.NoError(t, err)

	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan})
	require.NoError(t, err)
	boundary := stream.InitialCheckpoint()
	require.NoError(t, stream.Close())
	commands := make(chan bson.Raw, 10)
	monitor := &event.CommandMonitor{Started: func(_ context.Context, command *event.CommandStartedEvent) {
		if command.CommandName == "find" && command.DatabaseName == env.DBName {
			commands <- append(bson.Raw(nil), command.Command...)
		}
	}}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI).SetReadPreference(readpref.SecondaryPreferred()).SetMonitor(monitor))
	require.NoError(t, err)
	defer client.Disconnect(ctx)
	fresh := NewDocumentStore(client, client.Database(env.DBName), "docs", "sys", time.Hour).(*documentStore)
	request := types.SourceScanRequest{Collection: "users", Limit: 2, AtLeast: boundary}
	page, err := fresh.ScanDocuments(ctx, "tenant", request)
	require.NoError(t, err)
	command := <-commands
	concern := command.Lookup("readConcern").Document()
	assert.Equal(t, "majority", concern.Lookup("level").StringValue())
	cp, err := decodeWatchCheckpoint(boundary)
	require.NoError(t, err)
	seconds, increment := concern.Lookup("afterClusterTime").Timestamp()
	assert.Equal(t, cp.Start.T, seconds)
	assert.Equal(t, cp.Start.I, increment)
	if preference, ok := command.Lookup("$readPreference").DocumentOK(); ok {
		assert.Equal(t, "primary", preference.Lookup("mode").StringValue())
	}
	require.Len(t, page.Documents, 2)
	assert.Equal(t, "users/a", page.Documents[0].Fullpath)
	assert.Equal(t, int64(9007199254740993), page.Documents[0].Data["n"])
	assert.Equal(t, "users/b", page.Documents[1].Fullpath)
	assert.True(t, page.Documents[1].Deleted)
	assert.Empty(t, page.Documents[1].Data)
	assert.Equal(t, "b", page.NextAfter)
	assert.False(t, page.Exhausted)
	assert.Positive(t, page.Bytes)
	request.AfterID = page.NextAfter
	page, err = fresh.ScanDocuments(ctx, "tenant", request)
	require.NoError(t, err)
	require.Len(t, page.Documents, 1)
	assert.Equal(t, "users/c", page.Documents[0].Fullpath)
	assert.True(t, page.Exhausted)
	request.MaxBytes = 1
	page, err = fresh.ScanDocuments(ctx, "tenant", request)
	require.ErrorIs(t, err, types.ErrSourceScanBudget)
	assert.Equal(t, types.SourceScanPage{}, page)
}

func TestWatchBoundaryScanRejectsInvalidBinding(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", time.Hour).(*documentStore)
	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan})
	require.NoError(t, err)
	boundary := stream.InitialCheckpoint()
	require.NoError(t, stream.Close())
	native, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	nativeCP := native.InitialCheckpoint()
	require.NoError(t, native.Close())
	for _, test := range []struct {
		name       string
		database   string
		collection string
		checkpoint types.WatchCheckpoint
		code       types.WatchErrorCode
	}{
		{"malformed", "tenant", "users", "invalid", types.WatchInvalidCheckpoint},
		{"native position", "tenant", "users", nativeCP, types.WatchInvalidCheckpoint},
		{"database", "other", "users", boundary, types.WatchScopeMismatch},
		{"collection", "tenant", "other", boundary, types.WatchScopeMismatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			store.readSource = func(context.Context, *mongo.Collection, bool) (watchSource, error) {
				t.Fatal("invalid boundary reached source reads")
				return watchSource{}, nil
			}
			page, err := store.ScanDocuments(ctx, test.database, types.SourceScanRequest{Collection: test.collection, AtLeast: test.checkpoint, Limit: 1})
			requireWatchCode(t, err, test.code)
			assert.Equal(t, types.SourceScanPage{}, page)
		})
	}
	store.readSource = nil
	// A missing source must fail before index installation can recreate it.
	require.NoError(t, env.DB.Collection("docs").Drop(ctx))
	page, err := store.ScanDocuments(ctx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 1})
	requireWatchCode(t, err, types.WatchSourceMismatch)
	assert.Equal(t, types.SourceScanPage{}, page)
	names, err := env.DB.ListCollectionNames(ctx, bson.D{{Key: "name", Value: "docs"}})
	require.NoError(t, err)
	assert.Empty(t, names)
	require.NoError(t, env.DB.CreateCollection(ctx, "docs"))
	_, err = store.ScanDocuments(ctx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 1})
	requireWatchCode(t, err, types.WatchSourceMismatch)
}

func TestWatchBoundaryScanRevalidatesSource(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", time.Hour).(*documentStore)
	require.NoError(t, store.Create(ctx, "tenant", types.NewStoredDoc("tenant", "users", "a", nil)))
	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan})
	require.NoError(t, err)
	boundary := stream.InitialCheckpoint()
	require.NoError(t, stream.Close())
	calls := 0
	store.readSource = func(ctx context.Context, collection *mongo.Collection, create bool) (watchSource, error) {
		assert.False(t, create)
		calls++
		if calls == 2 {
			require.NoError(t, collection.Drop(ctx))
			require.NoError(t, env.DB.CreateCollection(ctx, collection.Name()))
		}
		return store.watchSource(ctx, collection, false)
	}
	page, err := store.ScanDocuments(ctx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 1})
	requireWatchCode(t, err, types.WatchSourceMismatch)
	assert.Equal(t, types.SourceScanPage{}, page)
	assert.Equal(t, 2, calls)
	cause := errors.New("catalog unavailable")
	store.readSource = func(context.Context, *mongo.Collection, bool) (watchSource, error) { return watchSource{}, cause }
	_, err = store.ScanDocuments(ctx, "tenant", types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 1})
	requireWatchCode(t, err, types.WatchSourceUnavailable)
	assert.ErrorIs(t, err, cause)
}

func TestWatchScanIndexShape(t *testing.T) {
	encode := func(value any) bson.Raw { data, err := bson.Marshal(value); require.NoError(t, err); return data }
	keys := bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "fullpath", Value: 1}}
	valid := watchScanIndexSpec{Keys: encode(keys)}
	assert.True(t, valid.usable())
	valid.Collation = encode(bson.M{"locale": "simple"})
	assert.True(t, valid.usable())
	for _, spec := range []watchScanIndexSpec{
		{Keys: bson.Raw{1}}, {Keys: encode(bson.D{})},
		{Keys: encode(keys), Sparse: true}, {Keys: encode(keys), Hidden: true},
		{Keys: encode(keys), Partial: encode(bson.M{"deleted": false})},
		{Keys: encode(keys), Collation: encode(bson.M{"locale": "en"})},
		{Keys: encode(keys), Collation: encode(bson.M{"locale": 1})},
		{Keys: encode(bson.D{{Key: "collection", Value: 1}, {Key: "database", Value: 1}, {Key: "fullpath", Value: 1}})},
		{Keys: encode(bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "fullpath", Value: -1}})},
		{Keys: encode(bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "fullpath", Value: "hashed"}})},
	} {
		assert.False(t, spec.usable())
	}
}

func TestWatchBoundaryScanCurrentIndex(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", time.Hour).(*documentStore)
	require.NoError(t, store.EnsureIndexes(ctx))
	// The ordinary cache still says ready after the physical collection changes.
	require.NoError(t, env.DB.Collection("docs").Drop(ctx))
	stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{StartMode: types.WatchStartForScan})
	require.NoError(t, err)
	boundary := stream.InitialCheckpoint()
	require.NoError(t, stream.Close())
	request := types.SourceScanRequest{Collection: "users", AtLeast: boundary, Limit: 1}
	page, err := store.ScanDocuments(ctx, "tenant", request)
	require.NoError(t, err)
	assert.True(t, page.Exhausted)
	_, err = env.DB.Collection("docs").Indexes().DropOne(ctx, sourceScanIndexName)
	require.NoError(t, err)
	_, err = env.DB.Collection("docs").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "fullpath", Value: 1}}, Options: options.Index().SetName(sourceScanIndexName)})
	require.NoError(t, err)
	page, err = store.ScanDocuments(ctx, "tenant", request)
	requireWatchCode(t, err, types.WatchUnsupported)
	assert.Equal(t, types.SourceScanPage{}, page)
	assert.NotContains(t, err.Error(), strings.Trim(string(boundary), "="))
}
