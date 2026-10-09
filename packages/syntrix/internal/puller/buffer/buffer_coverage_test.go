package buffer

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/codetreker/syntrix/internal/core/storage"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuffer_New_Error(t *testing.T) {
	t.Parallel()
	// Test empty path
	_, err := New(Options{Path: ""})
	if err == nil {
		t.Error("Expected error for empty path")
	}

	// Test mkdir error (permission denied)
	// Note: This might be hard to test reliably across OSs, but we can try using a file as path
	tmpFile, _ := os.CreateTemp("", "buffer-test-file")
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	_, err = New(Options{Path: tmpFile.Name()})
	if err == nil {
		t.Error("Expected error when path is a file")
	}
}

// TestBuffer_Closed_Errors REMOVED - consolidated into TestBuffer_ClosedScenarios in buffer_test.go

func TestBuffer_Write_MarshalError(t *testing.T) {
	t.Parallel()
	dir, _ := os.MkdirTemp("", "buffer-test-*")
	defer os.RemoveAll(dir)
	buf, _ := New(Options{Path: dir})
	defer buf.Close()

	// Create event with unserializable field
	evt := &events.StoreChangeEvent{
		EventID: "1",
		FullDocument: &storage.StoredDoc{
			Data: map[string]any{
				"bad": make(chan int),
			},
		},
	}

	if err := buf.Write(context.Background(), evt, testToken); err == nil {
		t.Error("Expected error from Write with unserializable event")
	}
}

func TestBuffer_Read_UnmarshalError(t *testing.T) {
	t.Parallel()
	dir, _ := os.MkdirTemp("", "buffer-test-*")
	defer os.RemoveAll(dir)
	buf, _ := New(Options{Path: dir})
	defer buf.Close()

	// Manually write garbage to DB
	// We need to access the private db field. Since we are in the same package, we can.
	key := []byte("evt-1")
	buf.db.Set(key, []byte("garbage"), pebble.Sync)

	// Read should fail
	_, err := buf.Read("evt-1")
	if err == nil {
		t.Error("Expected error from Read with corrupted data")
	}
}

func TestBuffer_Iterator_UnmarshalError(t *testing.T) {
	t.Parallel()
	dir, _ := os.MkdirTemp("", "buffer-test-*")
	defer os.RemoveAll(dir)
	buf, _ := New(Options{Path: dir})
	defer buf.Close()

	// Manually write garbage to DB
	key := []byte("evt-1")
	buf.db.Set(key, []byte("garbage"), pebble.Sync)

	iter, _ := buf.ScanFrom("")
	defer iter.Close()

	if iter.Next() {
		t.Error("Expected Next() to return false on error")
	}
	if iter.Err() == nil {
		t.Error("Expected error from iterator")
	}
}

func TestBuffer_ScanFrom_AfterKey(t *testing.T) {
	t.Parallel()
	dir, _ := os.MkdirTemp("", "buffer-test-*")
	defer os.RemoveAll(dir)
	buf, _ := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	defer buf.Close()

	// Write 3 events
	evts := []*events.StoreChangeEvent{
		{EventID: "1", Timestamp: 100},
		{EventID: "2", Timestamp: 200},
		{EventID: "3", Timestamp: 300},
	}
	for _, e := range evts {
		buf.Write(context.Background(), e, testToken)
	}

	// Wait for batch flush
	time.Sleep(20 * time.Millisecond)

	// Scan from event 1 (should get 2 and 3)
	// Note: BufferKey uses timestamp-eventID
	key1 := evts[0].BufferKey()

	iter, err := buf.ScanFrom(key1)
	if err != nil {
		t.Fatalf("ScanFrom failed: %v", err)
	}
	defer iter.Close()

	count := 0
	for iter.Next() {
		count++
		if iter.Event().EventID == "1" {
			t.Error("Should not see event 1")
		}
	}
	if count != 2 {
		t.Errorf("Expected 2 events, got %d", count)
	}
}

func TestBuffer_Write_Atomicity(t *testing.T) {
	t.Parallel()
	dir, err := os.MkdirTemp("", "buffer-test-atomicity-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	// 1. Open buffer and write event + token
	buf, err := New(Options{
		Path:          dir,
		BatchInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	evt := &events.StoreChangeEvent{
		EventID:  "evt-atomicity",
		Database: "database-1",
		MgoColl:  "testcoll",
		MgoDocID: "doc-1",
		OpType:   events.StoreOperationInsert,
		ClusterTime: events.ClusterTime{
			T: 1234567890,
			I: 1,
		},
		Timestamp: time.Now().UnixMilli(),
	}
	token := []byte("token-atomicity")

	if err := buf.Write(context.Background(), evt, token); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	// Normal shutdown must commit the pending event and token together.
	require.NoError(t, buf.Close())

	// 2. Re-open buffer (simulate restart)
	buf2, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() reopen error = %v", err)
	}
	defer buf2.Close()

	// 3. Verify Token
	lastToken, err := buf2.LoadCheckpoint()
	if err != nil {
		t.Fatalf("LoadCheckpoint() error = %v", err)
	}
	if string(lastToken) != string(token) {
		t.Errorf("LoadCheckpoint() = %s, want %s", string(lastToken), string(token))
	}

	// 4. Verify Event
	key := evt.BufferKey()
	readEvt, err := buf2.Read(key)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if readEvt == nil {
		t.Fatal("Read() returned nil")
	}
	if readEvt.EventID != evt.EventID {
		t.Errorf("EventID = %s, want %s", readEvt.EventID, evt.EventID)
	}
}

func TestBuffer_ScanFrom_Bounds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer buf.Close()

	// Write 3 events: A, B, C
	evts := make([]*events.StoreChangeEvent, 3)
	for i := 0; i < 3; i++ {
		evts[i] = &events.StoreChangeEvent{
			EventID:  string(rune('A' + i)), // A, B, C
			Database: "database-1",
			MgoColl:  "testcoll",
			MgoDocID: "doc-1",
			OpType:   events.StoreOperationInsert,
			ClusterTime: events.ClusterTime{
				T: uint32(1000 + i),
				I: 1,
			},
			Timestamp: time.Now().UnixMilli(),
		}
		if err := buf.Write(context.Background(), evts[i], testToken); err != nil {
			t.Fatalf("Write(%d) error = %v", i, err)
		}
	}

	time.Sleep(50 * time.Millisecond)

	// Scan from Event A's key (Exclusive)
	startKey := evts[0].BufferKey()
	iter, err := buf.ScanFrom(startKey)
	if err != nil {
		t.Fatalf("ScanFrom() error = %v", err)
	}
	defer iter.Close()

	// Expect B
	if !iter.Next() {
		t.Fatal("Expected event B, got none")
	}
	if iter.Event().EventID != "B" {
		t.Errorf("Expected event B, got %s", iter.Event().EventID)
	}

	// Expect C
	if !iter.Next() {
		t.Fatal("Expected event C, got none")
	}
	if iter.Event().EventID != "C" {
		t.Errorf("Expected event C, got %s", iter.Event().EventID)
	}

	// Expect End
	if iter.Next() {
		t.Errorf("Expected end of stream, got %s", iter.Event().EventID)
	}
}

func TestBuffer_DeleteBefore_NoMatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	defer buf.Close()

	// Write an event with a "high" key
	evt := &events.StoreChangeEvent{
		EventID:     "evt-high",
		ClusterTime: events.ClusterTime{T: 2000, I: 1},
	}
	require.NoError(t, buf.Write(context.Background(), evt, testToken))

	// Wait for flush
	require.Eventually(t, func() bool {
		c, _ := buf.Count()
		return c == 1
	}, 1*time.Second, 10*time.Millisecond)

	// Delete before a "low" key
	// T=1000
	lowKey := "0000000000001000-00000001-hash"

	deleted, err := buf.DeleteBefore(lowKey)
	require.NoError(t, err)
	assert.Equal(t, 0, deleted)
}

func TestBuffer_First_SkipsCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	defer buf.Close()

	// Save checkpoint only
	require.NoError(t, buf.SaveCheckpoint(testToken))

	// First should return empty/error, not checkpoint key
	key, err := buf.First()
	require.NoError(t, err)
	assert.Equal(t, "", key)
}

func TestBuffer_Head_SkipsCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	defer buf.Close()

	// Save checkpoint only
	require.NoError(t, buf.SaveCheckpoint(testToken))

	// Head should return empty/error, not checkpoint key
	key, err := buf.Head()
	require.NoError(t, err)
	assert.Equal(t, "", key)
}

func TestBuffer_Count_SkipsCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	defer buf.Close()

	// Save checkpoint only
	require.NoError(t, buf.SaveCheckpoint(testToken))

	count, err := buf.Count()
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestBuffer_CountAfter_SkipsCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	defer buf.Close()

	// Save checkpoint only
	require.NoError(t, buf.SaveCheckpoint(testToken))

	count, err := buf.CountAfter("")
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestBuffer_ScanFrom_SkipsCheckpoint(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	buf, err := New(Options{
		Path:          dir,
		BatchInterval: 5 * time.Millisecond,
	})
	require.NoError(t, err)
	defer buf.Close()

	// Save checkpoint only
	require.NoError(t, buf.SaveCheckpoint(testToken))

	iter, err := buf.ScanFrom("")
	require.NoError(t, err)
	defer iter.Close()

	// Should be empty
	assert.False(t, iter.Next())
}
