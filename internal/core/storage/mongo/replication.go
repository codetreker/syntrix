package mongo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

var _ types.ReplicationSource = (*documentStore)(nil)

func replicationFailure(database, collection string, code types.ReplicationErrorCode, cause error) error {
	return &types.ReplicationError{Code: code, Database: database, Collection: collection, Cause: cause}
}

func replicationClassify(database, collection string, err error) error {
	if err == nil {
		return nil
	}
	var own *types.ReplicationError
	if errors.As(err, &own) {
		return err
	}
	code := types.ReplicationUnavailable
	var server mongo.ServerError
	var watch *types.WatchError
	switch {
	case errors.Is(err, errWatchCollectionMissing):
		code = types.ReplicationSourceMismatch
	case errors.As(err, &watch):
		switch watch.Code {
		case types.WatchUnsupported:
			code = types.ReplicationUnsupported
		case types.WatchSourceMismatch:
			code = types.ReplicationSourceMismatch
		}
	case errors.As(err, &server):
		switch {
		case server.HasErrorCode(13), server.HasErrorCode(18):
			code = types.ReplicationPermissionDenied
		case server.HasErrorCode(136), server.HasErrorCode(280), server.HasErrorCode(286):
			code = types.ReplicationHistoryUnavailable
		case server.HasErrorCode(260), server.HasErrorCode(40647), server.HasErrorCode(40648):
			code = types.ReplicationInvalidCursor
		case server.HasErrorCode(40573), server.HasErrorCode(115), server.HasErrorCode(303), server.HasErrorCode(20):
			code = types.ReplicationUnsupported
		}
	}
	return replicationFailure(database, collection, code, err)
}

func replicationStreamClassify(database, collection string, err error) error {
	var server mongo.ServerError
	// FailedToParse is also used by ordinary queries. Only the native replay
	// operation establishes that this error describes a supplied resume token.
	if errors.As(err, &server) && server.HasErrorCode(9) {
		return replicationFailure(database, collection, types.ReplicationInvalidCursor, err)
	}
	return replicationClassify(database, collection, err)
}

func (m *documentStore) replicationCollection(collection string) (*mongo.Collection, error) {
	return m.getCollection(collection).Clone(options.Collection().SetReadConcern(readconcern.Majority()).SetReadPreference(readpref.Primary()))
}

func (m *documentStore) checkReplicationSource(ctx context.Context, collection *mongo.Collection, cp replicationCheckpoint) error {
	source, err := m.replicationSourceIdentity(ctx, collection, false)
	if err != nil {
		return err
	}
	if source != cp.Source {
		return replicationFailure(cp.Database, cp.Collection, types.ReplicationSourceMismatch, errors.New("source incarnation changed"))
	}
	return nil
}

func (m *documentStore) replicationSourceIdentity(ctx context.Context, collection *mongo.Collection, create bool) (watchSource, error) {
	db := m.client.Database(m.db.Name(), options.Database().SetReadPreference(readpref.Primary()))
	return readWatchSource(ctx, db, collection, create)
}

func (m *documentStore) replicationSession(ctx context.Context, cp *replicationCheckpoint) (mongo.SessionContext, mongo.Session, error) {
	session, err := m.client.StartSession(options.Session().SetCausalConsistency(true))
	if err != nil {
		return nil, nil, err
	}
	if cp != nil {
		if err = session.AdvanceClusterTime(cp.ClusterTime); err == nil {
			err = session.AdvanceOperationTime(&cp.OperationTime)
		}
		if err != nil {
			session.EndSession(ctx)
			return nil, nil, err
		}
	}
	return mongo.NewSessionContext(ctx, session), session, nil
}

func replicationRequest(ctx context.Context, database, collection string, budget types.ReplicationBudget) (context.Context, context.CancelFunc, types.ReplicationBudget, error) {
	if err := types.ValidateReplicationScope(database, collection); err != nil {
		return nil, nil, budget, replicationFailure(database, collection, types.ReplicationScopeMismatch, err)
	}
	resolved, err := types.ResolveReplicationBudget(budget)
	if err != nil {
		return nil, nil, budget, err
	}
	ctx, cancel := context.WithDeadline(ctx, resolved.HardDeadline)
	return ctx, cancel, resolved, nil
}

func (m *documentStore) BeginBootstrap(ctx context.Context, database, collectionName string, budget types.ReplicationBudget) (position types.ReplicationPosition, err error) {
	ctx, cancel, _, err := replicationRequest(ctx, database, collectionName, budget)
	if err != nil {
		return position, err
	}
	defer cancel()
	defer func() { err = replicationClassify(database, collectionName, err) }()
	if m.dataCollection == m.sysCollection {
		return position, replicationFailure(database, collectionName, types.ReplicationUnsupported, errors.New("replication requires distinct data and system sources"))
	}
	collection, err := m.replicationCollection(collectionName)
	if err != nil {
		return position, err
	}
	source, err := m.replicationSourceIdentity(ctx, collection, true)
	if err != nil {
		return position, err
	}
	cp := replicationCheckpoint{Version: 1, Source: source, Database: database, Collection: collectionName, Phase: types.ReplicationScan}
	if err = ensureReplicationIndex(ctx, collection, cp); err != nil {
		return position, err
	}
	sctx, session, err := m.replicationSession(ctx, nil)
	if err != nil {
		return position, err
	}
	defer session.EndSession(ctx)
	// A majority read's operationTime is the committed read boundary. An empty
	// result still executes the read; clusterTime alone is not such a boundary.
	err = collection.FindOne(sctx, bson.D{{Key: "_id", Value: ""}}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return position, err
	}
	if err = cp.capture(session); err != nil {
		return position, err
	}
	cp.Start = cp.OperationTime
	if err = m.checkReplicationSource(ctx, collection, cp); err != nil {
		return position, err
	}
	position, err = cp.position()
	return position, err
}

func (m *documentStore) resumeReplication(ctx context.Context, database, collectionName string, position types.ReplicationPosition, phase types.ReplicationPhase) (replicationCheckpoint, *mongo.Collection, error) {
	cp, err := decodeReplicationCheckpoint(position)
	if err != nil || cp.Phase != phase {
		if err == nil {
			err = errors.New("unexpected replication phase")
		}
		return cp, nil, replicationFailure(database, collectionName, types.ReplicationInvalidCursor, err)
	}
	if cp.Database != database || cp.Collection != collectionName {
		return cp, nil, replicationFailure(database, collectionName, types.ReplicationScopeMismatch, errors.New("cursor belongs to another logical scope"))
	}
	collection, err := m.replicationCollection(collectionName)
	if err != nil {
		return cp, nil, err
	}
	if err = m.checkReplicationSource(ctx, collection, cp); err != nil {
		return cp, nil, err
	}
	return cp, collection, nil
}

func (m *documentStore) openReplicationStream(ctx context.Context, collection *mongo.Collection, cp replicationCheckpoint) (stream changeStream, err error) {
	defer func() { err = replicationStreamClassify(cp.Database, cp.Collection, err) }()
	pipeline := mongo.Pipeline{bson.D{{Key: "$match", Value: bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "documentKey._id", Value: bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(cp.Database) + ":[0-9a-f]{32}$"}}}},
		bson.D{{Key: "operationType", Value: bson.D{{Key: "$nin", Value: bson.A{"insert", "update", "replace", "delete"}}}}},
	}}}}}}
	// A single event batch bounds native buffering and executes history checks
	// even before getMore. FullDocument is only used to recover logical identity.
	opts := options.ChangeStream().SetBatchSize(1).SetFullDocument(options.UpdateLookup).SetFullDocumentBeforeChange(options.WhenAvailable).SetMaxAwaitTime(100 * time.Millisecond)
	if len(cp.Token) == 0 {
		opts.SetStartAtOperationTime(&cp.Start)
	} else {
		opts.SetResumeAfter(cp.Token)
	}
	if m.openStream != nil {
		return m.openStream(ctx, collection, pipeline, opts)
	}
	return collection.Watch(ctx, pipeline, opts)
}

func closeReplicationStream(stream changeStream) error {
	ctx, cancel := context.WithTimeout(context.Background(), watchCleanupTimeout)
	defer cancel()
	return stream.Close(ctx)
}

func (m *documentStore) probeReplicationHistory(ctx context.Context, collection *mongo.Collection, cp replicationCheckpoint, maxBytes int64) (size int64, err error) {
	stream, err := m.openReplicationStream(ctx, collection, cp)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, closeReplicationStream(stream)) }()
	if stream.TryNext(ctx) {
		var raw bson.Raw
		if err = stream.Decode(&raw); err != nil {
			return 0, err
		}
		size = int64(len(raw))
		if size > maxBytes {
			return size, replicationFailure(cp.Database, cp.Collection, types.ReplicationBudgetExceeded, errors.New("history probe exceeds source budget"))
		}
		var lifecycle struct {
			Operation string `bson:"operationType"`
		}
		if err = bson.Unmarshal(raw, &lifecycle); err != nil {
			return size, err
		}
		if lifecycle.Operation == "drop" || lifecycle.Operation == "rename" || lifecycle.Operation == "dropDatabase" || lifecycle.Operation == "invalidate" {
			return size, replicationFailure(cp.Database, cp.Collection, types.ReplicationSourceMismatch, errors.New("source lifecycle event in history probe"))
		}
	} else {
		if err = stream.Err(); err != nil {
			return 0, replicationStreamClassify(cp.Database, cp.Collection, err)
		}
		if stream.ID() == 0 {
			return 0, errors.New("history probe source cursor exhausted")
		}
	}
	return size, ctx.Err()
}

func replicationSoftStopped(budget types.ReplicationBudget) bool {
	return !time.Now().Before(budget.SoftDeadline)
}

func replicationState(database, collection string, doc *types.StoredDoc) (*types.ReplicationState, error) {
	id, err := types.LogicalDocumentID(doc)
	if err != nil {
		return nil, replicationFailure(database, collection, types.ReplicationIdentityUnavailable, err)
	}
	if doc.Database != database || doc.Collection != collection || doc.Id != types.CalculateDatabase(database, doc.Fullpath) {
		return nil, replicationFailure(database, collection, types.ReplicationInvalidState, errors.New("source document identity differs from requested scope"))
	}
	if doc.Deleted {
		doc.Data = nil
	}
	return &types.ReplicationState{ID: id, Collection: collection, Deleted: doc.Deleted, Document: doc}, nil
}

func replicationFrameBytes(frame types.ReplicationFrame) (int64, error) {
	// BSON counts the stored payload without losing integer types. The fixed
	// allowance covers frame/state headers and the duplicated logical identity.
	n := int64(len(frame.After.Opaque) + len(frame.After.Phase) + 128)
	if frame.State != nil {
		n += int64(len(frame.State.ID) + len(frame.State.Collection))
		size, err := types.StoredDocumentBytes(frame.State.Document)
		if err != nil {
			return 0, err
		}
		n += size
	}
	return n, nil
}

func appendReplicationFrame(page *types.ReplicationPage, frame types.ReplicationFrame, sourceBytes int64, budget types.ReplicationBudget) (bool, error) {
	retained, err := replicationFrameBytes(frame)
	if err != nil {
		return false, err
	}
	if sourceBytes > budget.MaxSourceBytes-page.Usage.SourceBytes || retained > budget.MaxPageBytes-page.Usage.PageBytes {
		if len(page.Frames) == 0 {
			return false, fmt.Errorf("%w", &types.ReplicationError{Code: types.ReplicationBudgetExceeded, Cause: errors.New("source unit exceeds empty page budget")})
		}
		page.EndReason = types.ReplicationEndBytes
		return false, nil
	}
	page.Frames = append(page.Frames, frame)
	page.End = frame.After
	page.Usage.SourceBytes += sourceBytes
	page.Usage.PageBytes += retained
	return true, nil
}
