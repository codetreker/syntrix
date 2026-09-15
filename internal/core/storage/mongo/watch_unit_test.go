package mongo

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type stubChangeStream struct {
	token      bson.Raw
	events     []bson.Raw
	current    bson.Raw
	err        error
	decodeErr  error
	onDecode   func()
	closeErr   error
	exhausted  bool
	started    chan struct{}
	closeCalls atomic.Int32
	closeBound atomic.Bool
}

func (s *stubChangeStream) TryNext(ctx context.Context) bool {
	if s.started != nil {
		close(s.started)
		<-ctx.Done()
		s.err = ctx.Err()
		return false
	}
	if len(s.events) == 0 {
		return false
	}
	s.current, s.events = s.events[0], s.events[1:]
	return true
}

func (s *stubChangeStream) Decode(v any) error {
	if s.decodeErr != nil {
		return s.decodeErr
	}
	err := bson.Unmarshal(s.current, v)
	if s.onDecode != nil {
		s.onDecode()
	}
	return err
}

func (s *stubChangeStream) ResumeToken() bson.Raw { return s.token }
func (s *stubChangeStream) ID() int64 {
	if s.exhausted {
		return 0
	}
	return 1
}
func (s *stubChangeStream) Err() error { return s.err }
func (s *stubChangeStream) Close(ctx context.Context) error {
	s.closeCalls.Add(1)
	deadline, ok := ctx.Deadline()
	s.closeBound.Store(ok && time.Until(deadline) <= watchCleanupTimeout && ctx.Err() == nil)
	return s.closeErr
}

func watchToken(t *testing.T, position string) bson.Raw {
	t.Helper()
	data, err := bson.Marshal(bson.D{{Key: "_data", Value: position}, {Key: "_typeBits", Value: primitive.Binary{Data: []byte{1, 2, 3}}}})
	require.NoError(t, err)
	return data
}

func watchBinding() watchCheckpoint {
	return watchCheckpoint{Version: 2, Source: watchSource{Database: "physical", Collection: "docs", UUID: strings.Repeat("01", 16)}, Database: "tenant", Collection: "users"}
}

func watchChange(t *testing.T, operation string) changeStreamEvent {
	t.Helper()
	doc := types.NewStoredDoc("tenant", "users", "alice", map[string]interface{}{"v": 1})
	change := changeStreamEvent{ID: watchToken(t, "01"), OperationType: operation, FullDocument: &doc, ClusterTime: primitive.Timestamp{T: 100, I: 1}}
	change.DocumentKey.ID = doc.Id
	change.UpdateDescription = &struct {
		UpdatedFields bson.M   `bson:"updatedFields"`
		RemovedFields []string `bson:"removedFields"`
	}{UpdatedFields: bson.M{"data.v": 2}}
	return change
}

func nativeWatch(t *testing.T, native *stubChangeStream) *documentStore {
	t.Helper()
	client, err := mongo.NewClient()
	require.NoError(t, err)
	return &documentStore{
		db: client.Database("physical"), dataCollection: "docs", sysCollection: "sys",
		readSource:      func(context.Context, *mongo.Collection, bool) (watchSource, error) { return watchBinding().Source, nil },
		readWatchTarget: func(context.Context, *mongo.Collection, watchCheckpoint) (bson.Raw, error) { return native.token, nil },
		openStream: func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
			return native, nil
		},
	}
}

func TestWatchCheckpointCodec(t *testing.T) {
	binding := watchBinding()
	token := watchToken(t, "0102")
	encoded, err := binding.encode(token)
	require.NoError(t, err)
	decoded, err := decodeWatchCheckpoint(encoded)
	require.NoError(t, err)
	require.Equal(t, token, decoded.Token)
	require.Equal(t, binding.Source, decoded.Source)
	data, err := base64.RawURLEncoding.DecodeString(string(encoded))
	require.NoError(t, err)
	for name, corrupt := range map[string]types.WatchCheckpoint{
		"empty": "", "oversized": types.WatchCheckpoint(strings.Repeat("a", maxWatchCheckpointSize+1)), "base64": "!invalid",
		"invalid JSON":    types.WatchCheckpoint(base64.RawURLEncoding.EncodeToString([]byte("{"))),
		"unknown field":   checkpointJSON(strings.Replace(string(data), `"version":2`, `"unknown":0,"version":2`, 1)),
		"duplicate field": checkpointJSON(strings.Replace(string(data), `"version":2`, `"version":2,"version":2`, 1)),
		"missing field":   checkpointJSON(strings.Replace(string(data), `,"includeBefore":false`, "", 1)),
		"trailing JSON":   checkpointJSON(string(data) + "{}"),
		"version":         checkpointJSON(strings.Replace(string(data), `"version":2`, `"version":1`, 1)),
		"empty scope":     checkpointJSON(strings.Replace(string(data), `"database":"tenant"`, `"database":""`, 1)),
		"empty source":    checkpointJSON(strings.Replace(string(data), `"database":"physical"`, `"database":""`, 1)),
		"UUID":            checkpointJSON(strings.Replace(string(data), binding.Source.UUID, "bad", 1)),
		"missing token":   checkpointJSON(strings.Replace(string(data), base64.StdEncoding.EncodeToString(token), "", 1)),
	} {
		t.Run(name, func(t *testing.T) { _, err := decodeWatchCheckpoint(corrupt); require.Error(t, err) })
	}
	for name, token := range map[string]bson.Raw{
		"empty": nil, "malformed BSON": []byte{1, 2, 3}, "oversized": make([]byte, maxWatchCheckpointSize+1),
		"trailing BSON":      append(watchToken(t, "01"), 0),
		"invalid terminator": {5, 0, 0, 0, 1},
		"empty document":     mustBSON(t, bson.D{}),
		"duplicate data":     mustBSON(t, bson.D{{Key: "_data", Value: "01"}, {Key: "_data", Value: "02"}}),
	} {
		t.Run(name, func(t *testing.T) { _, err := binding.encode(token); require.Error(t, err) })
	}
	large := mustBSON(t, bson.D{{Key: "_data", Value: strings.Repeat("01", 20000)}})
	_, err = binding.encode(large)
	require.Error(t, err)
	_, err = decodeWatchCheckpoint(checkpointJSON(strings.Replace(string(data), base64.StdEncoding.EncodeToString(token), base64.StdEncoding.EncodeToString([]byte{1, 2, 3}), 1)))
	require.Error(t, err)
	other := binding.Source
	other.UUID = strings.Repeat("02", 16)
	require.NotEqual(t, binding.Source.changeID(token), other.changeID(token))
}

func checkpointJSON(data string) types.WatchCheckpoint {
	return types.WatchCheckpoint(base64.RawURLEncoding.EncodeToString([]byte(data)))
}

func mustBSON(t *testing.T, value any) bson.Raw {
	t.Helper()
	data, err := bson.Marshal(value)
	require.NoError(t, err)
	return data
}

func TestWatchRejectsAliasedSourceCollections(t *testing.T) {
	for _, collection := range []string{"", "users", "sys/config"} {
		t.Run(collection, func(t *testing.T) {
			binding := watchBinding()
			binding.Collection = collection
			checkpoint, err := binding.encode(watchToken(t, "01"))
			require.NoError(t, err)
			for _, after := range []types.WatchCheckpoint{"", checkpoint} {
				native := &stubChangeStream{token: watchToken(t, "01")}
				store := nativeWatch(t, native)
				store.sysCollection = store.dataCollection
				sourceReads, nativeOpens := 0, 0
				store.readSource = func(context.Context, *mongo.Collection, bool) (watchSource, error) {
					sourceReads++
					return binding.Source, nil
				}
				store.openStream = func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
					nativeOpens++
					return native, nil
				}
				stream, err := store.Watch(context.Background(), "tenant", collection, after, types.WatchOptions{})
				requireWatchCode(t, err, types.WatchUnsupported)
				require.Nil(t, stream)
				require.Zero(t, sourceReads, "source discovery and provisioning must not run")
				require.Zero(t, nativeOpens)
			}
		})
	}
}

func TestWatchOpenFailures(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		code  types.WatchErrorCode
		cause error
	}{
		{"permission", types.WatchPermissionDenied, &mongo.CommandError{Code: 13}},
		{"authentication", types.WatchPermissionDenied, &mongo.CommandError{Code: 18}},
		{"unsupported", types.WatchUnsupported, &mongo.CommandError{Code: 40573}},
		{"history", types.WatchHistoryUnavailable, &mongo.CommandError{Code: 286}},
		{"invalid native checkpoint", types.WatchInvalidCheckpoint, &mongo.CommandError{Code: 260}},
		{"source", types.WatchSourceUnavailable, errors.New("source disconnected")},
		{"cancellation", types.WatchSourceUnavailable, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := nativeWatch(t, &stubChangeStream{})
			store.openStream = func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error) {
				return nil, tc.cause
			}
			stream, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
			require.Nil(t, stream)
			requireWatchCode(t, err, tc.code)
			require.ErrorIs(t, err, tc.cause)
		})
	}
	t.Run("source access", func(t *testing.T) {
		store := nativeWatch(t, &stubChangeStream{})
		cause := &mongo.CommandError{Code: 13}
		store.readSource = func(context.Context, *mongo.Collection, bool) (watchSource, error) { return watchSource{}, cause }
		_, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
		requireWatchCode(t, err, types.WatchPermissionDenied)
		require.ErrorIs(t, err, cause)
	})
	for _, tc := range []struct {
		name  string
		token bson.Raw
		code  types.WatchErrorCode
	}{
		{"no initial token", nil, types.WatchUnsupported}, {"invalid initial token", bson.Raw{1, 2, 3}, types.WatchInvalidEvent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			native := &stubChangeStream{token: tc.token}
			_, err := nativeWatch(t, native).Watch(ctx, "tenant", "users", "", types.WatchOptions{})
			requireWatchCode(t, err, tc.code)
			require.EqualValues(t, 1, native.closeCalls.Load())
			require.True(t, native.closeBound.Load())
		})
	}
	for _, scenario := range []string{"UUID change", "missing source", "metadata permission", "canceled after open"} {
		t.Run(scenario, func(t *testing.T) {
			native := &stubChangeStream{token: watchToken(t, "01")}
			store := nativeWatch(t, native)
			calls := 0
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			store.readSource = func(context.Context, *mongo.Collection, bool) (watchSource, error) {
				calls++
				source := watchBinding().Source
				if calls == 2 {
					switch scenario {
					case "UUID change":
						source.UUID = strings.Repeat("02", 16)
					case "missing source":
						return watchSource{}, errWatchCollectionMissing
					case "metadata permission":
						return watchSource{}, &mongo.CommandError{Code: 13}
					case "canceled after open":
						cancel()
					}
				}
				return source, nil
			}
			_, err := store.Watch(ctx, "tenant", "users", "", types.WatchOptions{})
			switch scenario {
			case "metadata permission":
				requireWatchCode(t, err, types.WatchPermissionDenied)
			case "canceled after open":
				require.ErrorIs(t, err, context.Canceled)
			default:
				requireWatchCode(t, err, types.WatchSourceMismatch)
			}
			if scenario == "missing source" {
				require.ErrorIs(t, err, errWatchCollectionMissing)
			}
			require.EqualValues(t, 1, native.closeCalls.Load())
		})
	}
	store := nativeWatch(t, &stubChangeStream{})
	_, err := store.Watch(ctx, "tenant", "users", "bad", types.WatchOptions{})
	requireWatchCode(t, err, types.WatchInvalidCheckpoint)
}

func TestWatchFrameOrderingAndOwnership(t *testing.T) {
	change := watchChange(t, "insert")
	native := &stubChangeStream{token: watchToken(t, "ff"), events: []bson.Raw{mustBSON(t, change), mustBSON(t, change)}}
	store := nativeWatch(t, native)
	stream, err := store.Watch(context.Background(), "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	defer stream.Close()
	first, err := stream.Next(context.Background())
	require.NoError(t, err)
	decoded, err := decodeWatchCheckpoint(first.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, change.ID, decoded.Token)
	first.Event.Document.Data["v"] = "owned"
	second, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 1, second.Event.Document.Data["v"])
	require.Equal(t, "owned", first.Event.Document.Data["v"])
	progress, err := stream.Next(context.Background())
	require.NoError(t, err)
	require.Nil(t, progress.Event)
	decoded, err = decodeWatchCheckpoint(progress.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, native.token, decoded.Token)
}

func TestWatchReadFailuresAreSticky(t *testing.T) {
	decodeFailure := errors.New("BSON decode failed")
	cleanupFailure := errors.New("cursor cleanup failed")
	for _, scenario := range []string{"decode", "native error", "exhausted", "invalid event token", "invalid progress token", "invalid event", "cleanup"} {
		t.Run(scenario, func(t *testing.T) {
			native := &stubChangeStream{token: watchToken(t, "01")}
			change := watchChange(t, "insert")
			store := nativeWatch(t, native)
			stream, err := store.Watch(context.Background(), "tenant", "users", "", types.WatchOptions{})
			require.NoError(t, err)
			switch scenario {
			case "decode":
				native.events = []bson.Raw{mustBSON(t, change)}
				native.decodeErr = decodeFailure
			case "native error":
				native.err = &mongo.CommandError{Code: 286}
			case "exhausted":
				native.exhausted = true
			case "invalid event token":
				change.ID = mustBSON(t, bson.D{})
				native.events = []bson.Raw{mustBSON(t, change)}
			case "invalid progress token":
				native.token = nil
			case "invalid event":
				change.OperationType = "invalidate"
				native.events = []bson.Raw{mustBSON(t, change)}
			case "cleanup":
				native.err = decodeFailure
				native.closeErr = cleanupFailure
			}
			frame, firstErr := stream.Next(context.Background())
			require.Error(t, firstErr)
			require.Empty(t, frame.Checkpoint)
			_, secondErr := stream.Next(context.Background())
			require.Same(t, firstErr, secondErr)
			if scenario == "decode" || scenario == "cleanup" {
				require.ErrorIs(t, firstErr, decodeFailure)
			}
			if scenario == "cleanup" {
				require.ErrorIs(t, firstErr, cleanupFailure)
				require.ErrorIs(t, stream.Close(), cleanupFailure)
			} else {
				require.NoError(t, stream.Close())
			}
			require.EqualValues(t, 1, native.closeCalls.Load())
			require.True(t, native.closeBound.Load())
		})
	}
}

func TestWatchBlockedReadInterruption(t *testing.T) {
	for _, scenario := range []string{"close", "read cancel", "parent cancel", "read deadline", "idle parent cancel"} {
		t.Run(scenario, func(t *testing.T) {
			native := &stubChangeStream{token: watchToken(t, "01"), started: make(chan struct{})}
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			stream, err := nativeWatch(t, native).Watch(parent, "tenant", "users", "", types.WatchOptions{})
			require.NoError(t, err)
			if scenario == "idle parent cancel" {
				cancelParent()
				require.Eventually(t, func() bool { return native.closeCalls.Load() == 1 }, time.Second, time.Millisecond)
				_, err := stream.Next(context.Background())
				require.ErrorIs(t, err, context.Canceled)
				return
			}
			read, cancelRead := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancelRead()
			done := make(chan error, 1)
			go func() { _, err := stream.Next(read); done <- err }()
			select {
			case <-native.started:
			case <-time.After(time.Second):
				t.Fatal("read did not reach source")
			}
			switch scenario {
			case "close":
				require.NoError(t, stream.Close())
			case "read cancel":
				cancelRead()
			case "parent cancel":
				cancelParent()
			}
			select {
			case err := <-done:
				if scenario == "read deadline" {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.ErrorIs(t, err, context.Canceled)
				}
			case <-time.After(time.Second):
				t.Fatal("read did not stop")
			}
			require.NoError(t, stream.Close())
			require.NoError(t, stream.Close())
			require.EqualValues(t, 1, native.closeCalls.Load())
		})
	}
}

func TestWatchCancellationDuringDecode(t *testing.T) {
	for _, parentCanceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "read context", true: "watch context"}[parentCanceled], func(t *testing.T) {
			parent, cancelParent := context.WithCancel(context.Background())
			defer cancelParent()
			read, cancelRead := context.WithCancel(context.Background())
			defer cancelRead()
			native := &stubChangeStream{token: watchToken(t, "01"), events: []bson.Raw{mustBSON(t, watchChange(t, "insert"))}, onDecode: cancelRead}
			if parentCanceled {
				native.onDecode = cancelParent
			}
			stream, err := nativeWatch(t, native).Watch(parent, "tenant", "users", "", types.WatchOptions{})
			require.NoError(t, err)
			frame, err := stream.Next(read)
			require.ErrorIs(t, err, context.Canceled)
			require.Nil(t, frame.Event)
			require.Empty(t, frame.Checkpoint)
			_, sticky := stream.Next(context.Background())
			require.Same(t, err, sticky)
			require.NoError(t, stream.Close())
			require.EqualValues(t, 1, native.closeCalls.Load())
		})
	}
}

func TestWatchExplicitCloseFailure(t *testing.T) {
	cause := errors.New("killCursors failed")
	native := &stubChangeStream{token: watchToken(t, "01"), closeErr: cause}
	stream, err := nativeWatch(t, native).Watch(context.Background(), "tenant", "users", "", types.WatchOptions{})
	require.NoError(t, err)
	require.ErrorIs(t, stream.Close(), cause)
	require.ErrorIs(t, stream.Close(), cause)
	_, err = stream.Next(context.Background())
	require.ErrorIs(t, err, cause)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 1, native.closeCalls.Load())
}

func TestWatchConversion(t *testing.T) {
	for _, tc := range []struct {
		name, operation string
		modify          func(*changeStreamEvent)
		eventType       types.EventType
		code            types.WatchErrorCode
		skip            bool
	}{
		{"insert", "insert", nil, types.EventCreate, "", false},
		{"replace", "replace", nil, types.EventCreate, "", false},
		{"deleted replace", "replace", func(c *changeStreamEvent) { c.FullDocument.Deleted = true }, types.EventDelete, "", false},
		{"update", "update", nil, types.EventUpdate, "", false},
		{"lookup reflects later delete", "update", func(c *changeStreamEvent) { c.FullDocument.Deleted = true }, types.EventUpdate, "", false},
		{"soft delete", "update", func(c *changeStreamEvent) { c.UpdateDescription.UpdatedFields["deleted"] = true }, types.EventDelete, "", false},
		{"undelete", "update", func(c *changeStreamEvent) { c.UpdateDescription.UpdatedFields["deleted"] = false }, types.EventCreate, "", false},
		{"delete", "delete", func(c *changeStreamEvent) { c.FullDocumentBeforeChange = c.FullDocument; c.FullDocument = nil }, "", "", true},
		{"unknown", "noop", nil, "", types.WatchInvalidEvent, false},
		{"drop", "drop", nil, "", types.WatchSourceMismatch, false},
		{"bad key", "insert", func(c *changeStreamEvent) { c.DocumentKey.ID = "invalid" }, "", types.WatchInvalidEvent, false},
		{"bad hash", "insert", func(c *changeStreamEvent) { c.DocumentKey.ID = "tenant:not-a-hash" }, "", types.WatchInvalidEvent, false},
		{"foreign database", "insert", func(c *changeStreamEvent) { c.DocumentKey.ID = types.CalculateDatabase("other", "users/alice") }, "", "", true},
		{"wrong document id", "insert", func(c *changeStreamEvent) { c.FullDocument.Id = "wrong" }, "", types.WatchInvalidEvent, false},
		{"wrong document database", "insert", func(c *changeStreamEvent) { c.FullDocument.Database = "other" }, "", types.WatchInvalidEvent, false},
		{"wrong document path", "insert", func(c *changeStreamEvent) { c.FullDocument.Fullpath = "users/bob" }, "", types.WatchInvalidEvent, false},
		{"wrong collection metadata", "insert", func(c *changeStreamEvent) { c.FullDocument.Collection = "other" }, "", types.WatchInvalidEvent, false},
		{"missing metadata", "delete", func(c *changeStreamEvent) { c.FullDocument = nil }, "", "", true},
		{"missing lookup", "update", func(c *changeStreamEvent) { c.FullDocumentBeforeChange = c.FullDocument; c.FullDocument = nil }, "", types.WatchPayloadUnavailable, false},
		{"wrong collection", "insert", func(c *changeStreamEvent) {
			doc := types.NewStoredDoc("tenant", "other", "bob", nil)
			c.FullDocument = &doc
			c.DocumentKey.ID = doc.Id
		}, "", "", true},
		{"missing source time", "insert", func(c *changeStreamEvent) { c.ClusterTime.T = 0 }, "", types.WatchInvalidEvent, false},
		{"missing update description", "update", func(c *changeStreamEvent) { c.UpdateDescription = nil }, "", types.WatchInvalidEvent, false},
		{"invalid deleted field", "update", func(c *changeStreamEvent) { c.UpdateDescription.UpdatedFields["deleted"] = "true" }, "", types.WatchInvalidEvent, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &documentWatch{binding: watchBinding()}
			c := watchChange(t, tc.operation)
			if tc.modify != nil {
				tc.modify(&c)
			}
			event, err := w.convertChangeEvent(c)
			if tc.code != "" {
				requireWatchCode(t, err, tc.code)
				require.Nil(t, event)
				return
			}
			require.NoError(t, err)
			if tc.skip {
				require.Nil(t, event)
				return
			}
			require.Equal(t, tc.eventType, event.Type)
			require.NotEmpty(t, event.ChangeID)
			require.Equal(t, c.DocumentKey.ID, event.Id)
			assert.Nil(t, event.Before)
		})
	}
}
