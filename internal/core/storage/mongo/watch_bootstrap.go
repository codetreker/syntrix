package mongo

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func watchReadSession(ctx context.Context, client *mongo.Client, cp *watchCheckpoint) (mongo.SessionContext, mongo.Session, error) {
	session, err := client.StartSession(options.Session().SetCausalConsistency(true))
	if err != nil {
		return nil, nil, err
	}
	if cp != nil {
		if err = session.AdvanceClusterTime(cp.ClusterTime); err == nil {
			err = session.AdvanceOperationTime(cp.Start)
		}
		if err != nil {
			session.EndSession(ctx)
			return nil, nil, err
		}
	}
	return mongo.NewSessionContext(ctx, session), session, nil
}

func (m *documentStore) watchTarget(ctx context.Context, collection *mongo.Collection, cp watchCheckpoint) (target bson.Raw, err error) {
	// A fresh committed target is independent of an earlier bootstrap scan's
	// lower bound. Replaying from that bound must also cover writes during scan.
	cp.Start, cp.ClusterTime, cp.Token, cp.Target = nil, nil, nil, nil
	sctx, session, err := captureWatchBoundary(ctx, collection, &cp)
	if err != nil {
		return nil, err
	}
	defer session.EndSession(ctx)
	var hello struct {
		Msg string `bson:"msg"`
	}
	if err := collection.Database().RunCommand(sctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return nil, err
	}
	start := *cp.Start
	if hello.Msg == "isdbgrid" {
		start, err = nextWatchTimestamp(start)
		if err != nil {
			return nil, err
		}
	}
	opts := options.ChangeStream().SetBatchSize(0).SetStartAtOperationTime(&start)
	var stream changeStream
	if m.openStream != nil {
		stream, err = m.openStream(sctx, collection, mongo.Pipeline{}, opts)
	} else {
		stream, err = collection.Watch(sctx, mongo.Pipeline{}, opts)
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), watchCleanupTimeout)
		defer cancel()
		err = errors.Join(err, stream.Close(closeCtx))
	}()
	target = append(bson.Raw(nil), stream.ResumeToken()...)
	if err := validateWatchToken(target); err != nil {
		return nil, err
	}
	if _, err := watchTokenOrderKey(target); err != nil {
		return nil, err
	}
	return target, nil
}

func nextWatchTimestamp(ts primitive.Timestamp) (primitive.Timestamp, error) {
	if ts.I < ^uint32(0) {
		ts.I++
	} else if ts.T < ^uint32(0) {
		ts.T++
		ts.I = 0
	} else {
		return primitive.Timestamp{}, errors.New("source time cannot advance")
	}
	return ts, nil
}

func withWatchReadBoundary(ctx context.Context, client *mongo.Client, cp watchCheckpoint, read func(context.Context) error) error {
	if err := validateWatchBootstrap(cp); err != nil {
		return err
	}
	sctx, session, err := watchReadSession(ctx, client, &cp)
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	return read(sctx)
}

func captureWatchBoundary(ctx context.Context, collection *mongo.Collection, cp *watchCheckpoint) (mongo.SessionContext, mongo.Session, error) {
	committed, err := collection.Clone(options.Collection().SetReadPreference(readpref.Primary()).SetReadConcern(readconcern.Majority()))
	if err != nil {
		return nil, nil, err
	}
	sctx, session, err := watchReadSession(ctx, collection.Database().Client(), nil)
	if err != nil {
		return nil, nil, err
	}
	// Majority operationTime identifies a committed read boundary. clusterTime
	// alone may include uncommitted writes. Replay includes this boundary so a
	// scan covering it and the stream overlap even across concurrent commits.
	// https://github.com/mongodb/mongo/blob/r8.0.0/src/mongo/db/service_entry_point_common.cpp#L325-L354
	err = committed.FindOne(sctx, bson.D{{Key: "_id", Value: ""}}, options.FindOne().SetProjection(bson.D{{Key: "_id", Value: 1}})).Err()
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		session.EndSession(ctx)
		return nil, nil, err
	}
	op := session.OperationTime()
	if op == nil || op.T == 0 {
		session.EndSession(ctx)
		return nil, nil, errors.New("source supplied no committed operation time")
	}
	t := *op
	cp.Start = &t
	cp.ClusterTime = append(bson.Raw(nil), session.ClusterTime()...)
	if err = validateWatchBootstrap(*cp); err != nil {
		session.EndSession(ctx)
		return nil, nil, err
	}
	return sctx, session, nil
}
