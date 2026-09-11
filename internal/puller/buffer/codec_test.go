package buffer

import (
	"context"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestBufferCodecReopenPreservesDocumentsAndCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	buf, err := New(Options{Path: dir, BatchInterval: time.Hour})
	require.NoError(t, err)
	doc := storage.NewStoredDoc("db", "parents/p/children", "real-id", nil)
	doc.Data = map[string]any{"id": "business-id", "before": int64(9007199254740991), "at": int64(9007199254740992), "after": int64(9007199254740993)}
	live := &events.StoreChangeEvent{EventID: "live", Database: "db", FullDocument: &doc, ClusterTime: events.ClusterTime{T: 1}}
	tombstone := doc
	tombstone.Data, tombstone.Deleted = map[string]any{}, true
	deleted := &events.StoreChangeEvent{EventID: "deleted", Database: "db", FullDocument: &tombstone, ClusterTime: events.ClusterTime{T: 2}}
	for _, evt := range []*events.StoreChangeEvent{live, deleted} {
		require.NoError(t, buf.Write(context.Background(), evt, testToken))
	}
	require.NoError(t, buf.Close())
	buf, err = New(Options{Path: dir})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, buf.Close()) })
	checkpoint, err := buf.LoadCheckpoint()
	require.NoError(t, err)
	require.Equal(t, testToken, checkpoint)
	iter, err := buf.ScanFrom("")
	require.NoError(t, err)
	defer iter.Close()
	for _, evt := range []*events.StoreChangeEvent{live, deleted} {
		read, err := buf.Read(evt.BufferKey())
		require.NoError(t, err)
		require.Equal(t, evt, read)
		require.True(t, iter.Next())
		require.Equal(t, evt, iter.Event())
	}
	require.False(t, iter.Next())
	require.NoError(t, iter.Err())
	require.Equal(t, 2, func() int { count, err := buf.Count(); require.NoError(t, err); return count }())
}

func TestBufferRejectsUnversionedDataWithoutDeletingIt(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"0000000001-0000000000-legacy", checkpointKey, formatKey} {
		t.Run(key, func(t *testing.T) {
			dir := t.TempDir()
			db, err := pebble.Open(dir, nil)
			require.NoError(t, err)
			require.NoError(t, db.Set([]byte(key), []byte("old-value"), pebble.Sync))
			require.NoError(t, db.Close())
			buf, err := New(Options{Path: dir})
			require.Nil(t, buf)
			require.ErrorContains(t, err, "offline buffer rebuild required")
			db, err = pebble.Open(dir, nil)
			require.NoError(t, err)
			value, closer, err := db.Get([]byte(key))
			require.NoError(t, err)
			require.Equal(t, "old-value", string(value))
			require.NoError(t, closer.Close())
			require.NoError(t, db.Close())
		})
	}
}

func TestBufferInvalidDurableEventStopsBeforePendingEvent(t *testing.T) {
	t.Parallel()
	buf, err := New(Options{Path: t.TempDir(), BatchInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, buf.Close()) })
	key := events.FormatBufferKey(events.ClusterTime{T: 1}, "legacy")
	require.NoError(t, buf.db.Set([]byte(key), []byte(`{"eventId":"legacy"}`), pebble.Sync))
	require.NoError(t, buf.Write(context.Background(), &events.StoreChangeEvent{EventID: "later", ClusterTime: events.ClusterTime{T: 2}}, testToken))
	_, err = buf.Read(key)
	require.ErrorContains(t, err, "unsupported event wire version")
	iter, err := buf.ScanFrom("")
	require.NoError(t, err)
	defer iter.Close()
	require.False(t, iter.Next())
	require.ErrorContains(t, iter.Err(), "unsupported event wire version")
	require.False(t, iter.Next())
}
