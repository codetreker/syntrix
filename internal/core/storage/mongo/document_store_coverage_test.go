package mongo

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/event"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func TestDocumentStore_Delete_Coverage(t *testing.T) {
	env := setupTestEnv(t)
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	ctx := context.Background()
	database := "default"

	// Ensure indexes are created if the method exists
	if ds, ok := store.(interface{ EnsureIndexes(context.Context) error }); ok {
		err := ds.EnsureIndexes(ctx)
		require.NoError(t, err)
	}

	t.Run("Delete Non-Existent Document", func(t *testing.T) {
		err := store.Delete(ctx, database, "non/existent", nil)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})

	t.Run("Delete Already Soft-Deleted Document", func(t *testing.T) {
		collection := "users"
		docID := "deleted_user"
		path := collection + "/" + docID
		doc := types.NewStoredDoc(database, collection, docID, map[string]interface{}{
			"name": "To Be Deleted",
		})

		// Create
		err := store.Create(ctx, database, doc)
		require.NoError(t, err)

		// First Delete (Soft Delete)
		err = store.Delete(ctx, database, path, nil)
		require.NoError(t, err)

		// Verify it is soft deleted (Get should fail with ErrNotFound)
		_, err = store.Get(ctx, database, path)
		assert.ErrorIs(t, err, model.ErrNotFound)

		// Second Delete (Should fail with ErrNotFound because it's already deleted)
		err = store.Delete(ctx, database, path, nil)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestDocumentStore_Coverage_Extended(t *testing.T) {
	env := setupTestEnv(t)
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	ctx := context.Background()
	database := "default"

	// Ensure indexes
	if ds, ok := store.(interface{ EnsureIndexes(context.Context) error }); ok {
		require.NoError(t, ds.EnsureIndexes(ctx))
	}

	t.Run("Get Non-Existent Document", func(t *testing.T) {
		_, err := store.Get(ctx, database, "non/existent/get")
		assert.ErrorIs(t, err, model.ErrNotFound)
	})

	t.Run("Create with Empty CollectionHash", func(t *testing.T) {
		doc := types.NewStoredDoc(database, "users", "empty_hash", map[string]interface{}{"a": 1})
		doc.CollectionHash = "" // Explicitly empty
		err := store.Create(ctx, database, doc)
		require.NoError(t, err)
		// Fetch the doc from the database to verify CollectionHash was populated
		fetched, err := store.Get(ctx, database, "users/empty_hash")
		require.NoError(t, err)
		assert.NotEmpty(t, fetched.CollectionHash)
		assert.Equal(t, types.CalculateCollectionHash("users"), fetched.CollectionHash)
	})

	t.Run("Update Non-Existent Document", func(t *testing.T) {
		err := store.Update(ctx, database, "non/existent/update", map[string]interface{}{"a": 2}, nil)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})

	t.Run("Patch Non-Existent Document", func(t *testing.T) {
		err := store.Patch(ctx, database, "non/existent/patch", map[string]interface{}{"a": 2}, nil)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestDocumentStore_GetMany_Coverage(t *testing.T) {
	env := setupTestEnv(t)
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	ctx := context.Background()
	database := "default"

	// Ensure indexes
	if ds, ok := store.(interface{ EnsureIndexes(context.Context) error }); ok {
		require.NoError(t, ds.EnsureIndexes(ctx))
	}

	t.Run("GetMany Empty Paths", func(t *testing.T) {
		docs, err := store.GetMany(ctx, database, []string{})
		require.NoError(t, err)
		assert.Empty(t, docs)
	})

	t.Run("GetMany Non-Existent Documents", func(t *testing.T) {
		docs, err := store.GetMany(ctx, database, []string{"nonexistent/doc1", "nonexistent/doc2"})
		require.NoError(t, err)
		assert.Len(t, docs, 2)
		assert.Nil(t, docs[0])
		assert.Nil(t, docs[1])
	})

	t.Run("GetMany Mixed Existing and Non-Existing", func(t *testing.T) {
		// Create a doc
		doc := types.NewStoredDoc(database, "getmany", "existing1", map[string]interface{}{"name": "test"})
		require.NoError(t, store.Create(ctx, database, doc))

		paths := []string{"getmany/existing1", "getmany/nonexistent"}
		docs, err := store.GetMany(ctx, database, paths)
		require.NoError(t, err)
		assert.Len(t, docs, 2)
		assert.NotNil(t, docs[0])
		assert.Equal(t, "getmany/existing1", docs[0].Fullpath)
		assert.Nil(t, docs[1])
	})

	t.Run("GetMany All Existing", func(t *testing.T) {
		// Create docs
		doc1 := types.NewStoredDoc(database, "getmany", "all1", map[string]interface{}{"name": "one"})
		doc2 := types.NewStoredDoc(database, "getmany", "all2", map[string]interface{}{"name": "two"})
		require.NoError(t, store.Create(ctx, database, doc1))
		require.NoError(t, store.Create(ctx, database, doc2))

		paths := []string{"getmany/all1", "getmany/all2"}
		docs, err := store.GetMany(ctx, database, paths)
		require.NoError(t, err)
		assert.Len(t, docs, 2)
		assert.NotNil(t, docs[0])
		assert.NotNil(t, docs[1])
		assert.Equal(t, "getmany/all1", docs[0].Fullpath)
		assert.Equal(t, "getmany/all2", docs[1].Fullpath)
	})

	t.Run("GetMany Excludes Deleted Docs", func(t *testing.T) {
		// Create and delete a doc
		doc := types.NewStoredDoc(database, "getmany", "deleted1", map[string]interface{}{"name": "deleted"})
		require.NoError(t, store.Create(ctx, database, doc))
		require.NoError(t, store.Delete(ctx, database, "getmany/deleted1", nil))

		paths := []string{"getmany/deleted1"}
		docs, err := store.GetMany(ctx, database, paths)
		require.NoError(t, err)
		assert.Len(t, docs, 1)
		assert.Nil(t, docs[0]) // Deleted doc should not be returned
	})
}

func TestDocumentStore_DeleteByDatabase(t *testing.T) {
	env := setupTestEnv(t)
	store := NewDocumentStore(env.Client, env.DB, "docs", "sys", 0)
	ctx := context.Background()

	// Create documents in two different databases
	for i := 0; i < 5; i++ {
		doc := types.NewStoredDoc("db1", "users", "user"+string(rune('a'+i)), map[string]interface{}{
			"name": "User " + string(rune('A'+i)),
		})
		err := store.Create(ctx, "db1", doc)
		require.NoError(t, err)
	}

	for i := 0; i < 3; i++ {
		doc := types.NewStoredDoc("db2", "posts", "post"+string(rune('a'+i)), map[string]interface{}{
			"title": "Post " + string(rune('A'+i)),
		})
		err := store.Create(ctx, "db2", doc)
		require.NoError(t, err)
	}

	t.Run("DeleteByDatabase with no limit", func(t *testing.T) {
		deleted, err := store.DeleteByDatabase(ctx, "db1", 0)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, deleted, 5) // At least 5 docs deleted
	})

	t.Run("DeleteByDatabase with limit", func(t *testing.T) {
		// First add more documents
		for i := 0; i < 5; i++ {
			doc := types.NewStoredDoc("db3", "items", "item"+string(rune('a'+i)), map[string]interface{}{
				"name": "Item " + string(rune('A'+i)),
			})
			err := store.Create(ctx, "db3", doc)
			require.NoError(t, err)
		}

		// Delete with limit
		deleted, err := store.DeleteByDatabase(ctx, "db3", 2)
		require.NoError(t, err)
		assert.Equal(t, 2, deleted)

		// Delete remaining
		deleted, err = store.DeleteByDatabase(ctx, "db3", 0)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, deleted, 3)
	})

	t.Run("DeleteByDatabase non-existent database", func(t *testing.T) {
		deleted, err := store.DeleteByDatabase(ctx, "nonexistent", 0)
		require.NoError(t, err)
		assert.Equal(t, 0, deleted)
	})

	t.Run("DeleteByDatabase with canceled context", func(t *testing.T) {
		// Add documents to test error path
		for i := 0; i < 3; i++ {
			doc := types.NewStoredDoc("db_cancel", "items", "item"+string(rune('a'+i)), map[string]interface{}{
				"name": "Item " + string(rune('A'+i)),
			})
			err := store.Create(ctx, "db_cancel", doc)
			require.NoError(t, err)
		}

		// Cancel context before delete
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()

		// Delete with canceled context should return error
		_, err := store.DeleteByDatabase(cancelCtx, "db_cancel", 0)
		assert.Error(t, err)
	})

	t.Run("DeleteByDatabase with limit and canceled context", func(t *testing.T) {
		// Add documents
		for i := 0; i < 3; i++ {
			doc := types.NewStoredDoc("db_cancel2", "items", "item"+string(rune('a'+i)), map[string]interface{}{
				"name": "Item " + string(rune('A'+i)),
			})
			err := store.Create(ctx, "db_cancel2", doc)
			require.NoError(t, err)
		}

		// Cancel context before delete with limit
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancel()

		// Delete with canceled context and limit should return error
		_, err := store.DeleteByDatabase(cancelCtx, "db_cancel2", 2)
		assert.Error(t, err)
	})

	t.Run("DeleteByDatabase deletes from both data and sys collections", func(t *testing.T) {
		// Add documents to data collection
		for i := 0; i < 2; i++ {
			doc := types.NewStoredDoc("db_both", "users", "user"+string(rune('a'+i)), map[string]interface{}{
				"name": "User " + string(rune('A'+i)),
			})
			err := store.Create(ctx, "db_both", doc)
			require.NoError(t, err)
		}

		// Add documents to sys collection
		for i := 0; i < 2; i++ {
			doc := types.NewStoredDoc("db_both", "sys", "config"+string(rune('a'+i)), map[string]interface{}{
				"value": i,
			})
			err := store.Create(ctx, "db_both", doc)
			require.NoError(t, err)
		}

		// Delete all - should delete from both collections
		deleted, err := store.DeleteByDatabase(ctx, "db_both", 0)
		require.NoError(t, err)
		assert.Equal(t, 4, deleted)
	})

	t.Run("DeleteByDatabase with limit hits data collection only", func(t *testing.T) {
		// Add documents to data collection
		for i := 0; i < 5; i++ {
			doc := types.NewStoredDoc("db_limit_test", "users", "user"+string(rune('a'+i)), map[string]interface{}{
				"name": "User " + string(rune('A'+i)),
			})
			err := store.Create(ctx, "db_limit_test", doc)
			require.NoError(t, err)
		}

		// Add documents to sys collection
		for i := 0; i < 3; i++ {
			doc := types.NewStoredDoc("db_limit_test", "sys", "config"+string(rune('a'+i)), map[string]interface{}{
				"value": i,
			})
			err := store.Create(ctx, "db_limit_test", doc)
			require.NoError(t, err)
		}

		// Delete with limit 5 - should only delete from data collection
		deleted, err := store.DeleteByDatabase(ctx, "db_limit_test", 5)
		require.NoError(t, err)
		assert.Equal(t, 5, deleted)

		// Delete remaining - should be 3 from sys collection
		deleted, err = store.DeleteByDatabase(ctx, "db_limit_test", 0)
		require.NoError(t, err)
		assert.Equal(t, 3, deleted)
	})

	t.Run("DeleteByDatabase with limit spans both collections", func(t *testing.T) {
		// Add documents to data collection
		for i := 0; i < 2; i++ {
			doc := types.NewStoredDoc("db_span", "users", "user"+string(rune('a'+i)), map[string]interface{}{
				"name": "User " + string(rune('A'+i)),
			})
			err := store.Create(ctx, "db_span", doc)
			require.NoError(t, err)
		}

		// Add documents to sys collection
		for i := 0; i < 3; i++ {
			doc := types.NewStoredDoc("db_span", "sys", "config"+string(rune('a'+i)), map[string]interface{}{
				"value": i,
			})
			err := store.Create(ctx, "db_span", doc)
			require.NoError(t, err)
		}

		// Delete with limit 4 - should delete 2 from data and 2 from sys
		deleted, err := store.DeleteByDatabase(ctx, "db_span", 4)
		require.NoError(t, err)
		assert.Equal(t, 4, deleted)

		// Delete remaining - should be 1 from sys collection
		deleted, err = store.DeleteByDatabase(ctx, "db_span", 0)
		require.NoError(t, err)
		assert.Equal(t, 1, deleted)
	})
}

func TestDocumentStoreGetRejectsInvalidOptions(t *testing.T) {
	store := &documentStore{}
	for _, opts := range [][]types.ReadOptions{
		{{Consistency: 255}},
		{{}, {}},
		{{Consistency: types.ReadAuthoritative}, {Consistency: types.ReadAuthoritative}},
	} {
		doc, err := store.Get(context.Background(), "app", "users/user1", opts...)
		assert.Nil(t, doc)
		assert.Error(t, err)
	}
}

func TestDocumentStoreGetReadPreference(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var hello struct {
		SetName string `bson:"setName"`
	}
	require.NoError(t, env.Client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello))
	require.NotEmpty(t, hello.SetName, "read preference verification requires a replica set")

	var mu sync.Mutex
	var commands []bson.Raw
	monitor := &event.CommandMonitor{Started: func(_ context.Context, e *event.CommandStartedEvent) {
		if e.CommandName == "find" && e.DatabaseName == env.DBName {
			mu.Lock()
			commands = append(commands, append(bson.Raw(nil), e.Command...))
			mu.Unlock()
		}
	}}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(testMongoURI).SetReplicaSet(hello.SetName).SetDirect(false).SetMonitor(monitor))
	require.NoError(t, err)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		assert.NoError(t, client.Disconnect(cleanupCtx))
	})
	require.NoError(t, client.Ping(ctx, readpref.Primary()))
	// Unmatched secondary tags keep fixture reads on the primary while retaining a visible non-primary preference.
	preference := readpref.SecondaryPreferred(readpref.WithTags("syntrix-test", env.DBName))
	db := client.Database(env.DBName, options.Database().SetReadPreference(preference))
	store := NewDocumentStore(client, db, "docs", "sys", 0).(*documentStore)
	authoritative := types.ReadOptions{Consistency: types.ReadAuthoritative}

	for _, collection := range []string{"users", "sys/databases"} {
		t.Run(collection, func(t *testing.T) {
			expected := types.NewStoredDoc("app", collection, "item", map[string]interface{}{"value": "current"})
			expected.Version = 7
			require.NoError(t, store.Create(ctx, "app", expected))
			other := types.NewStoredDoc("other", collection, "item", map[string]interface{}{"value": "other"})
			require.NoError(t, store.Create(ctx, "other", other))
			mu.Lock()
			commands = nil
			mu.Unlock()
			for _, opts := range [][]types.ReadOptions{nil, {authoritative}, {{}}} {
				actual, err := store.Get(ctx, "app", expected.Fullpath, opts...)
				require.NoError(t, err)
				assert.Equal(t, expected.Id, actual.Id)
				assert.Equal(t, int64(7), actual.Version)
				assert.Equal(t, "current", actual.Data["value"])
			}
			mu.Lock()
			observed := append([]bson.Raw(nil), commands...)
			mu.Unlock()
			require.Len(t, observed, 3)
			for i, command := range observed {
				expectedCollection := "docs"
				if collection == "sys/databases" {
					expectedCollection = "sys"
				}
				assert.Equal(t, expectedCollection, command.Lookup("find").StringValue())
				filter := command.Lookup("filter").Document()
				assert.Equal(t, expected.Id, filter.Lookup("_id").StringValue())
				assert.Equal(t, "app", filter.Lookup("database").StringValue())
				assert.True(t, filter.Lookup("deleted", "$ne").Boolean())
				if i == 1 {
					_, err := command.LookupErr("$readPreference")
					assert.Error(t, err, "primary replica-set reads omit the wire read preference")
				} else {
					assert.Equal(t, "secondaryPreferred", command.Lookup("$readPreference", "mode").StringValue())
				}
			}
			assert.Same(t, preference, db.ReadPreference())

			actual, err := store.Get(ctx, "other", expected.Fullpath, authoritative)
			require.NoError(t, err)
			assert.Equal(t, "other", actual.Data["value"])
			_, err = store.Get(ctx, "missing", expected.Fullpath, authoritative)
			assert.ErrorIs(t, err, model.ErrNotFound)
			require.NoError(t, store.Delete(ctx, "app", expected.Fullpath, nil))
			_, err = store.Get(ctx, "app", expected.Fullpath, authoritative)
			assert.ErrorIs(t, err, model.ErrNotFound)
		})
	}

	t.Run("canceled context", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		doc, err := store.Get(canceled, "app", "users/item", authoritative)
		assert.Nil(t, doc)
		assert.ErrorIs(t, err, context.Canceled)
	})
	t.Run("expired context", func(t *testing.T) {
		expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer cancel()
		doc, err := store.Get(expired, "app", "users/item", authoritative)
		assert.Nil(t, doc)
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("database filter rejects inconsistent stored metadata", func(t *testing.T) {
		malformed := types.NewStoredDoc("app", "users", "malformed", nil)
		malformed.Database = "other"
		_, err := db.Collection("docs").InsertOne(ctx, malformed)
		require.NoError(t, err)
		doc, err := store.Get(ctx, "app", malformed.Fullpath, authoritative)
		assert.Nil(t, doc)
		assert.ErrorIs(t, err, model.ErrNotFound)
	})
}

func TestDocumentStoreSourceScan(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Collection defaults must not alter source identity or seek order.
	require.NoError(t, env.DB.CreateCollection(ctx, "scan_data", options.CreateCollection().SetCollation(&options.Collation{Locale: "en", Strength: 2})))
	require.NoError(t, env.DB.CreateCollection(ctx, "scan_sys", options.CreateCollection().SetCollation(&options.Collation{Locale: "en", Strength: 2})))
	store := NewDocumentStore(env.Client, env.DB, "scan_data", "scan_sys", time.Hour).(*documentStore)
	const exactInteger int64 = 9007199254740993
	for _, scope := range []struct{ database, collection string }{{"app", "users"}, {"APP", "users"}, {"app", "Users"}, {"app", "users/a/children"}, {"app", "sys/settings"}} {
		ids := []string{"z", "a", "A"}
		if scope.database == "APP" {
			ids = []string{"foreign"}
		}
		for _, id := range ids {
			doc := types.NewStoredDoc(scope.database, scope.collection, id, map[string]interface{}{"large": exactInteger})
			doc.Data["id"] = "untrusted"
			require.NoError(t, store.Create(ctx, scope.database, doc))
		}
	}
	// Use the storage deletion path to prove that a tombstone's empty data retains identity.
	require.NoError(t, store.Delete(ctx, "app", "users/a", nil))
	for _, collection := range []string{"users", "sys/settings"} {
		request := types.SourceScanRequest{Collection: collection, Limit: 2, Consistency: types.ReadAuthoritative}
		page, err := store.ScanDocuments(ctx, "app", request)
		require.NoError(t, err)
		require.Len(t, page.Documents, 2)
		assert.Equal(t, collection+"/A", page.Documents[0].Fullpath)
		assert.Equal(t, collection+"/a", page.Documents[1].Fullpath)
		assert.Equal(t, "a", page.NextAfter)
		assert.False(t, page.Exhausted)
		assert.Positive(t, page.Bytes)
		assert.Equal(t, exactInteger, page.Documents[0].Data["large"])
		if collection == "users" {
			assert.True(t, page.Documents[1].Deleted)
			assert.Empty(t, page.Documents[1].Data)
		}
		request.AfterID = page.NextAfter
		page, err = store.ScanDocuments(ctx, "app", request)
		require.NoError(t, err)
		require.Len(t, page.Documents, 1)
		assert.Equal(t, collection+"/z", page.Documents[0].Fullpath)
		assert.True(t, page.Exhausted)
		assert.Equal(t, "z", page.NextAfter)
		request.AfterID = "z"
		page, err = store.ScanDocuments(ctx, "app", request)
		require.NoError(t, err)
		assert.Empty(t, page.Documents)
		assert.True(t, page.Exhausted)
		assert.Equal(t, "z", page.NextAfter)
	}
	limited := types.SourceScanRequest{Collection: "users", Limit: 1, MaxBytes: 1}
	page, err := store.ScanDocuments(ctx, "app", limited)
	assert.ErrorIs(t, err, types.ErrSourceScanBudget)
	assert.Empty(t, page.Documents)
	// Tombstones spend the entire raw-candidate page budget.
	page, err = store.ScanDocuments(ctx, "app", types.SourceScanRequest{Collection: "users", AfterID: "A", Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Documents, 1)
	assert.True(t, page.Documents[0].Deleted)
	assert.False(t, page.Exhausted)
	assert.Equal(t, "a", page.NextAfter)
}

func TestDocumentStoreSourceScanExplain(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "scan_data", "scan_sys", time.Hour).(*documentStore)
	require.NoError(t, store.EnsureIndexes(ctx))
	for _, collection := range []string{"users", "sys/settings"} {
		for _, id := range []string{"a", "b", "c"} {
			require.NoError(t, store.Create(ctx, "app", types.NewStoredDoc("app", collection, id, nil)))
		}
		physical := store.getCollection(collection).Name()
		var explain bson.M
		err := env.DB.RunCommand(ctx, bson.D{{Key: "explain", Value: bson.D{
			{Key: "find", Value: physical},
			{Key: "filter", Value: bson.D{{Key: "database", Value: "app"}, {Key: "collection", Value: collection}, {Key: "fullpath", Value: bson.M{"$gt": collection + "/a"}}}},
			{Key: "sort", Value: bson.D{{Key: "fullpath", Value: 1}}},
			{Key: "hint", Value: sourceScanIndexName},
			{Key: "collation", Value: bson.M{"locale": "simple"}},
			{Key: "limit", Value: 1},
		}}, {Key: "verbosity", Value: "executionStats"}}).Decode(&explain)
		require.NoError(t, err)
		encoded, err := bson.MarshalExtJSON(explain, false, false)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), `"stage":"SORT"`)
		assert.NotContains(t, string(encoded), `"stage":"COLLSCAN"`)
		assert.Contains(t, string(encoded), sourceScanIndexName)
		stats := explain["executionStats"].(bson.M)
		assert.EqualValues(t, 1, stats["nReturned"])
		assert.LessOrEqual(t, stats["totalDocsExamined"].(int32), int32(1))
	}
}

func TestDocumentStoreGetManyReadOptions(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "batch_data", "batch_sys", time.Hour)
	for _, scope := range []struct{ db, collection, id string }{{"app", "users", "a"}, {"app", "users", "b"}, {"app", "sys/settings", "a"}, {"other", "users", "missing"}} {
		require.NoError(t, store.Create(ctx, scope.db, types.NewStoredDoc(scope.db, scope.collection, scope.id, nil)))
	}
	require.NoError(t, store.Delete(ctx, "app", "users/b", nil))
	paths := []string{"users/a", "sys/settings/a", "users/missing", "users/b", "users/a"}
	for _, showDeleted := range []bool{false, true} {
		docs, err := store.GetMany(ctx, "app", paths, types.ReadOptions{Consistency: types.ReadAuthoritative, ShowDeleted: showDeleted})
		require.NoError(t, err)
		require.Len(t, docs, len(paths))
		assert.Equal(t, paths[0], docs[0].Fullpath)
		assert.Equal(t, paths[1], docs[1].Fullpath)
		assert.Nil(t, docs[2])
		if showDeleted {
			require.NotNil(t, docs[3])
			assert.True(t, docs[3].Deleted)
		} else {
			assert.Nil(t, docs[3])
		}
		assert.Same(t, docs[0], docs[4])
	}
	_, err := store.Get(ctx, "app", "users/b")
	assert.ErrorIs(t, err, model.ErrNotFound)
	doc, err := store.Get(ctx, "app", "users/b", types.ReadOptions{ShowDeleted: true})
	require.NoError(t, err)
	assert.True(t, doc.Deleted)
	_, err = store.GetMany(ctx, "app", nil, types.ReadOptions{Consistency: -1})
	assert.Error(t, err)
}

func TestDocumentStoreSourceScanRejectsCorruptIdentity(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "scan_data", "scan_sys", time.Hour).(*documentStore)
	doc := types.NewStoredDoc("app", "users", "a", nil)
	doc.Fullpath = "other/a"
	_, err := env.DB.Collection("scan_data").InsertOne(ctx, doc)
	require.NoError(t, err)
	_, err = store.ScanDocuments(ctx, "app", types.SourceScanRequest{Collection: "users", Limit: 10})
	assert.ErrorContains(t, err, "fullpath does not belong")
}

func TestDocumentStoreSourceScanRequiresSourceIndex(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "scan_data", "scan_sys", time.Hour).(*documentStore)
	require.NoError(t, store.EnsureIndexes(ctx))
	require.NoError(t, store.Create(ctx, "app", types.NewStoredDoc("app", "users", "a", nil)))
	_, err := env.DB.Collection("scan_data").Indexes().DropOne(ctx, sourceScanIndexName)
	require.NoError(t, err)

	// Removing a ready source index invalidates the bounded-scan guarantee.
	page, err := store.ScanDocuments(ctx, "app", types.SourceScanRequest{Collection: "users", Limit: 1})
	require.ErrorContains(t, err, "hint")
	assert.Empty(t, page.Documents)
	assert.Empty(t, page.NextAfter)
	assert.False(t, page.Exhausted)
}

func TestDocumentStoreEnumerateCollections(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "enumeration_data", "enumeration_sys", time.Hour).(*documentStore)
	for _, collection := range []string{"z", "a", "nested/a/items", "sys/settings", "sys/users"} {
		for _, id := range []string{"a", "b", "c"} {
			require.NoError(t, store.Create(ctx, "app", types.NewStoredDoc("app", collection, id, nil)))
		}
	}
	for _, id := range []string{"a", "b", "c"} {
		require.NoError(t, store.Delete(ctx, "app", "a/"+id, nil))
	}
	require.NoError(t, store.Create(ctx, "other", types.NewStoredDoc("other", "foreign", "a", nil)))
	first, err := store.EnumerateCollections(ctx, "app", "", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "nested/a/items"}, first)
	second, err := store.EnumerateCollections(ctx, "app", first[1], 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"z"}, second)
	all, err := store.EnumerateCollections(ctx, "app", "", 10, types.CollectionEnumerationOptions{IncludeSystem: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "nested/a/items", "sys/settings", "sys/users", "z"}, all)
	for _, physical := range []string{"enumeration_data", "enumeration_sys"} {
		var explain bson.M
		err := env.DB.RunCommand(ctx, bson.D{{Key: "explain", Value: bson.D{
			{Key: "find", Value: physical},
			{Key: "filter", Value: bson.D{{Key: "database", Value: "app"}, {Key: "collection", Value: bson.M{"$gt": "a"}}}},
			{Key: "sort", Value: bson.D{{Key: "collection", Value: 1}, {Key: "fullpath", Value: 1}}},
			{Key: "hint", Value: sourceScanIndexName}, {Key: "collation", Value: bson.M{"locale": "simple"}},
			{Key: "projection", Value: bson.M{"_id": 0, "collection": 1}}, {Key: "limit", Value: 1},
		}}, {Key: "verbosity", Value: "executionStats"}}).Decode(&explain)
		require.NoError(t, err)
		encoded, err := bson.MarshalExtJSON(explain, false, false)
		require.NoError(t, err)
		assert.NotContains(t, string(encoded), `"stage":"SORT"`)
		assert.NotContains(t, string(encoded), `"stage":"COLLSCAN"`)
		stats := explain["executionStats"].(bson.M)
		assert.EqualValues(t, 1, stats["nReturned"])
		assert.LessOrEqual(t, stats["totalKeysExamined"].(int32), int32(1))
	}
}

func TestDocumentStoreReadByteBudget(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "bounded_data", "bounded_sys", time.Hour)
	a := types.NewStoredDoc("app", "items", "a", map[string]interface{}{"body": strings.Repeat("a", 2048)})
	b := types.NewStoredDoc("app", "sys/settings", "b", map[string]interface{}{"body": "system"})
	require.NoError(t, store.Create(ctx, "app", a))
	require.NoError(t, store.Create(ctx, "app", b))
	aBytes, err := types.StoredDocumentBytes(&a)
	require.NoError(t, err)
	bBytes, err := types.StoredDocumentBytes(&b)
	require.NoError(t, err)
	for _, limit := range []int64{aBytes, aBytes - 1} {
		doc, err := store.Get(ctx, "app", a.Fullpath, types.ReadOptions{Consistency: types.ReadAuthoritative, MaxBytes: limit})
		if limit == aBytes {
			require.NoError(t, err)
			assert.Equal(t, a.Data, doc.Data)
		} else {
			assert.ErrorIs(t, err, types.ErrReadBudget)
			assert.Nil(t, doc)
		}
	}
	paths := []string{a.Fullpath, "items/missing", b.Fullpath, a.Fullpath}
	total := 2*aBytes + bBytes
	for _, limit := range []int64{total, total - 1} {
		docs, err := store.GetMany(ctx, "app", paths, types.ReadOptions{Consistency: types.ReadAuthoritative, MaxBytes: limit})
		if limit == total {
			require.NoError(t, err)
			require.Len(t, docs, 4)
			assert.Equal(t, a.Fullpath, docs[0].Fullpath)
			assert.Nil(t, docs[1])
			assert.Equal(t, b.Fullpath, docs[2].Fullpath)
			assert.Same(t, docs[0], docs[3])
		} else {
			assert.ErrorIs(t, err, types.ErrReadBudget)
			assert.Nil(t, docs)
		}
	}
	docs, err := store.GetMany(ctx, "app", []string{"items/missing"}, types.ReadOptions{MaxBytes: 1})
	require.NoError(t, err)
	assert.Equal(t, []*types.StoredDoc{nil}, docs)
	require.NoError(t, store.Delete(ctx, "app", a.Fullpath, nil))
	docs, err = store.GetMany(ctx, "app", []string{a.Fullpath}, types.ReadOptions{MaxBytes: 1})
	require.NoError(t, err)
	assert.Equal(t, []*types.StoredDoc{nil}, docs)
	docs, err = store.GetMany(ctx, "app", []string{a.Fullpath}, types.ReadOptions{ShowDeleted: true, MaxBytes: 1})
	assert.ErrorIs(t, err, types.ErrReadBudget)
	assert.Nil(t, docs)
	_, err = store.Get(ctx, "app", "items/missing", types.ReadOptions{MaxBytes: 1})
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestDocumentStoreReadBudgetChargesCanonicalMetadata(t *testing.T) {
	env := setupTestEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := NewDocumentStore(env.Client, env.DB, "bounded_data", "bounded_sys", time.Hour)
	path := "items/a"
	raw := bson.M{"_id": types.CalculateDatabase("app", path), "database": "app", "collection": "items", "fullpath": path, "version": int32(1)}
	_, err := env.DB.Collection("bounded_data").InsertOne(ctx, raw)
	require.NoError(t, err)
	encoded, err := bson.Marshal(raw)
	require.NoError(t, err)
	opts := types.ReadOptions{MaxBytes: int64(len(encoded))}
	doc, err := store.Get(ctx, "app", path, opts)
	assert.ErrorIs(t, err, types.ErrReadBudget)
	assert.Nil(t, doc)
	docs, err := store.GetMany(ctx, "app", []string{path}, opts)
	assert.ErrorIs(t, err, types.ErrReadBudget)
	assert.Nil(t, docs)
}
