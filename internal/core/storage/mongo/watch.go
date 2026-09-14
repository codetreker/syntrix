package mongo

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const watchCleanupTimeout = 5 * time.Second

var errWatchCollectionMissing = errors.New("watch collection does not exist")

type changeStream interface {
	TryNext(context.Context) bool
	Decode(any) error
	ResumeToken() bson.Raw
	ID() int64
	Err() error
	Close(context.Context) error
}

type documentWatch struct {
	native   changeStream
	binding  watchCheckpoint
	initial  types.WatchCheckpoint
	ctx      context.Context
	cancel   context.CancelFunc
	stop     func() bool
	mu       sync.Mutex
	err      error
	closed   bool
	closeErr error
}

func (m *documentStore) Watch(ctx context.Context, database, collectionName string, after types.WatchCheckpoint, opts types.WatchOptions) (types.WatchStream, error) {
	w := &documentWatch{binding: watchCheckpoint{Version: 1, Database: database, Collection: collectionName, IncludeBefore: opts.IncludeBefore}}
	if database == "" {
		return nil, w.failure(types.WatchInvalidScope, errors.New("logical database must be nonempty"))
	}
	if m.dataCollection == m.sysCollection {
		return nil, w.failure(types.WatchUnsupported, errors.New("watch requires distinct data and system source collections"))
	}
	var resumed watchCheckpoint
	if after != "" {
		var err error
		resumed, err = decodeWatchCheckpoint(after)
		if err != nil {
			return nil, w.failure(types.WatchInvalidCheckpoint, err)
		}
		if resumed.Database != database || resumed.Collection != collectionName || resumed.IncludeBefore != opts.IncludeBefore {
			return nil, w.failure(types.WatchScopeMismatch, errors.New("checkpoint scope or options differ"))
		}
	}
	collection := m.getCollection(collectionName)
	readSource := m.readSource
	if readSource == nil {
		readSource = m.watchSource
	}
	source, err := readSource(ctx, collection, after == "")
	if err != nil {
		if after != "" && errors.Is(err, errWatchCollectionMissing) {
			return nil, w.failure(types.WatchSourceMismatch, err)
		}
		return nil, w.classify(err)
	}
	if after != "" && resumed.Source != source {
		return nil, w.failure(types.WatchSourceMismatch, errors.New("checkpoint belongs to another source incarnation"))
	}
	w.binding.Source = source
	pipeline := mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "documentKey._id", Value: bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(database) + ":[0-9a-f]{32}$"}}}},
		bson.D{{Key: "operationType", Value: bson.D{{Key: "$nin", Value: bson.A{"insert", "update", "replace", "delete"}}}}},
	}}}}}}
	// A zero initial batch yields an actual server position even on an idle
	// source. Subsequent getMore calls use the server's default batch size.
	nativeOptions := options.ChangeStream().SetBatchSize(0).SetFullDocument(options.UpdateLookup).SetMaxAwaitTime(time.Second)
	if opts.IncludeBefore || collectionName != "" {
		nativeOptions.SetFullDocumentBeforeChange(options.WhenAvailable)
	}
	if after != "" {
		nativeOptions.SetResumeAfter(resumed.Token)
	}
	open := m.openStream
	if open == nil {
		open = func(ctx context.Context, collection *mongo.Collection, pipeline mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error) {
			return collection.Watch(ctx, pipeline, opts)
		}
	}
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.native, err = open(w.ctx, collection, pipeline, nativeOptions)
	if err != nil {
		w.cancel()
		return nil, w.classify(err)
	}
	currentSource, err := readSource(w.ctx, collection, false)
	if err != nil || source != currentSource {
		if err == nil || errors.Is(err, errWatchCollectionMissing) {
			err = w.failure(types.WatchSourceMismatch, errors.Join(errors.New("source changed while establishing watch"), err))
		} else {
			err = w.classify(err)
		}
		return nil, w.terminate(err)
	}
	if after != "" {
		w.initial = after
	} else {
		token := w.native.ResumeToken()
		if len(token) == 0 {
			return nil, w.terminate(w.failure(types.WatchUnsupported, errors.New("source supplied no initial resume position")))
		}
		w.initial, err = w.binding.encode(token)
		if err != nil {
			return nil, w.terminate(w.failure(types.WatchInvalidEvent, err))
		}
	}
	if err := w.ctx.Err(); err != nil {
		return nil, w.terminate(w.classify(err))
	}
	// Cancellation also releases an idle watch whose caller never reads again.
	w.mu.Lock()
	w.stop = context.AfterFunc(ctx, func() { _ = w.Close() })
	w.mu.Unlock()
	return w, nil
}

func (m *documentStore) watchSource(ctx context.Context, collection *mongo.Collection, create bool) (watchSource, error) {
	return readWatchSource(ctx, m.db, collection, create)
}

func readWatchSource(ctx context.Context, db *mongo.Database, collection *mongo.Collection, create bool) (watchSource, error) {
	specs, err := db.ListCollectionSpecifications(ctx, bson.D{{Key: "name", Value: collection.Name()}})
	if err != nil {
		return watchSource{}, err
	}
	if len(specs) == 0 && create {
		err := db.CreateCollection(ctx, collection.Name())
		var command mongo.CommandError
		if err != nil && !(errors.As(err, &command) && command.Code == 48) {
			return watchSource{}, err
		}
		return readWatchSource(ctx, db, collection, false)
	}
	if len(specs) == 0 {
		return watchSource{}, errWatchCollectionMissing
	}
	spec := specs[0]
	if spec.Type != "collection" || spec.UUID == nil || spec.UUID.Subtype != 4 || len(spec.UUID.Data) != 16 {
		return watchSource{}, &types.WatchError{Code: types.WatchUnsupported, Cause: errors.New("source has no stable collection UUID")}
	}
	return watchSource{Database: db.Name(), Collection: collection.Name(), UUID: hex.EncodeToString(spec.UUID.Data)}, nil
}

func (w *documentWatch) InitialCheckpoint() types.WatchCheckpoint { return w.initial }

func (w *documentWatch) Next(ctx context.Context) (types.WatchFrame, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return types.WatchFrame{}, w.err
	}
	stop := context.AfterFunc(ctx, w.cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		w.cancel()
		return types.WatchFrame{}, w.terminate(w.classify(err))
	}
	if err := w.ctx.Err(); err != nil {
		return types.WatchFrame{}, w.terminate(w.classify(err))
	}
	hasEvent := w.native.TryNext(w.ctx)
	if err := ctx.Err(); err != nil {
		return types.WatchFrame{}, w.terminate(w.classify(err))
	}
	if err := w.ctx.Err(); err != nil {
		return types.WatchFrame{}, w.terminate(w.classify(err))
	}
	var event *types.Event
	var token bson.Raw
	if hasEvent {
		var change changeStreamEvent
		if err := w.native.Decode(&change); err != nil {
			return types.WatchFrame{}, w.terminate(w.failure(types.WatchInvalidEvent, err))
		}
		// ResumeToken may already be the batch PBRT when this is its last event.
		// Checkpoint the raw event itself until every preceding event is delivered.
		token = change.ID
		if err := validateWatchToken(token); err != nil {
			return types.WatchFrame{}, w.terminate(w.failure(types.WatchInvalidEvent, err))
		}
		var err error
		event, err = w.convertChangeEvent(change)
		if err != nil {
			return types.WatchFrame{}, w.terminate(err)
		}
	} else {
		if err := w.native.Err(); err != nil {
			return types.WatchFrame{}, w.terminate(w.classify(err))
		}
		if w.native.ID() == 0 {
			return types.WatchFrame{}, w.terminate(w.failure(types.WatchSourceUnavailable, errors.New("source cursor exhausted")))
		}
		token = w.native.ResumeToken()
	}
	checkpoint, err := w.binding.encode(token)
	if err != nil {
		return types.WatchFrame{}, w.terminate(w.failure(types.WatchInvalidEvent, err))
	}
	if err := ctx.Err(); err != nil {
		return types.WatchFrame{}, w.terminate(w.classify(err))
	}
	if err := w.ctx.Err(); err != nil {
		return types.WatchFrame{}, w.terminate(w.classify(err))
	}
	return types.WatchFrame{Event: event, Checkpoint: checkpoint}, nil
}

func (w *documentWatch) Close() error {
	w.cancel()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop != nil {
		w.stop()
	}
	if w.err == nil {
		w.terminate(w.classify(w.ctx.Err()))
	}
	return w.closeErr
}

// terminate runs while Next/Close owns mu, or before Watch publishes the stream.
func (w *documentWatch) terminate(err error) error {
	w.cancel()
	if !w.closed {
		w.closed = true
		ctx, cancel := context.WithTimeout(context.Background(), watchCleanupTimeout)
		defer cancel()
		if closeErr := w.native.Close(ctx); closeErr != nil {
			w.closeErr = w.classify(closeErr)
		}
	}
	w.err = errors.Join(err, w.closeErr)
	return w.err
}

func (w *documentWatch) failure(code types.WatchErrorCode, cause error) error {
	return &types.WatchError{Code: code, Database: w.binding.Database, Collection: w.binding.Collection, Cause: cause}
}

func (w *documentWatch) classify(err error) error {
	code := types.WatchSourceUnavailable
	var watchErr *types.WatchError
	var server mongo.ServerError
	switch {
	case errors.As(err, &watchErr):
		code = watchErr.Code
	case errors.As(err, &server):
		switch {
		case server.HasErrorCode(13), server.HasErrorCode(18):
			code = types.WatchPermissionDenied
		case server.HasErrorCode(136), server.HasErrorCode(286):
			code = types.WatchHistoryUnavailable
		case server.HasErrorCode(260):
			code = types.WatchInvalidCheckpoint
		case server.HasErrorCode(40573), server.HasErrorCode(115), server.HasErrorCode(303), server.HasErrorCode(20):
			code = types.WatchUnsupported
		}
	}
	return w.failure(code, err)
}

type changeStreamEvent struct {
	ID                       bson.Raw         `bson:"_id"`
	OperationType            string           `bson:"operationType"`
	FullDocument             *types.StoredDoc `bson:"fullDocument"`
	FullDocumentBeforeChange *types.StoredDoc `bson:"fullDocumentBeforeChange"`
	DocumentKey              struct {
		ID string `bson:"_id"`
	} `bson:"documentKey"`
	ClusterTime       primitive.Timestamp `bson:"clusterTime"`
	UpdateDescription *struct {
		UpdatedFields bson.M   `bson:"updatedFields"`
		RemovedFields []string `bson:"removedFields"`
	} `bson:"updateDescription"`
}

func (w *documentWatch) convertChangeEvent(change changeStreamEvent) (*types.Event, error) {
	switch change.OperationType {
	case "drop", "rename", "dropDatabase", "invalidate":
		return nil, w.failure(types.WatchSourceMismatch, fmt.Errorf("source lifecycle event: %s", change.OperationType))
	case "insert", "update", "replace", "delete":
	default:
		return nil, w.failure(types.WatchInvalidEvent, fmt.Errorf("unsupported source event: %s", change.OperationType))
	}
	id := change.DocumentKey.ID
	separator := strings.LastIndexByte(id, ':')
	if separator <= 0 {
		return nil, w.failure(types.WatchInvalidEvent, errors.New("document key lacks database prefix"))
	}
	hash, err := hex.DecodeString(id[separator+1:])
	if err != nil || len(hash) != 16 {
		return nil, w.failure(types.WatchInvalidEvent, errors.New("document key has invalid path hash"))
	}
	if id[:separator] != w.binding.Database {
		return nil, nil
	}
	for _, doc := range []*types.StoredDoc{change.FullDocument, change.FullDocumentBeforeChange} {
		if doc == nil {
			continue
		}
		if doc.Id != id || doc.Database != w.binding.Database || doc.Collection == "" || !strings.HasPrefix(doc.Fullpath, doc.Collection+"/") || types.CalculateDatabase(doc.Database, doc.Fullpath) != id {
			return nil, w.failure(types.WatchInvalidEvent, errors.New("document metadata disagrees with source key"))
		}
	}
	metadata := change.FullDocumentBeforeChange
	if metadata == nil {
		metadata = change.FullDocument
	}
	if w.binding.Collection != "" {
		if metadata == nil {
			return nil, w.failure(types.WatchPayloadUnavailable, errors.New("collection routing requires document metadata"))
		}
		if metadata.Collection != w.binding.Collection {
			return nil, nil
		}
	}
	if change.OperationType != "delete" && change.FullDocument == nil {
		return nil, w.failure(types.WatchPayloadUnavailable, errors.New("source document enrichment is unavailable"))
	}
	if change.ClusterTime.T == 0 {
		return nil, w.failure(types.WatchInvalidEvent, errors.New("source event has no cluster time"))
	}
	event := &types.Event{Id: id, ChangeID: w.binding.Source.changeID(change.ID), Database: w.binding.Database, Timestamp: int64(change.ClusterTime.T) * int64(time.Second)}
	if w.binding.IncludeBefore {
		event.Before = change.FullDocumentBeforeChange
	}
	switch change.OperationType {
	case "delete":
		event.Type = types.EventDelete
	case "insert", "replace":
		event.Type = types.EventCreate
		event.Document = change.FullDocument
		if change.FullDocument.Deleted {
			event.Type = types.EventDelete
			event.Document = nil
		}
	case "update":
		event.Type = types.EventUpdate
		event.Document = change.FullDocument
		if change.UpdateDescription == nil {
			return nil, w.failure(types.WatchInvalidEvent, errors.New("update has no immutable update description"))
		}
		if deleted, changed := change.UpdateDescription.UpdatedFields["deleted"]; changed {
			deleted, ok := deleted.(bool)
			if !ok {
				return nil, w.failure(types.WatchInvalidEvent, errors.New("deleted update is not boolean"))
			}
			if deleted {
				event.Type = types.EventDelete
				event.Document = nil
			} else {
				event.Type = types.EventCreate
			}
		}
	}
	return event, nil
}
