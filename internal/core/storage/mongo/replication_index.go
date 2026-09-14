package mongo

import (
	"context"
	"errors"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type replicationIndexSpec struct {
	Name      string   `bson:"name"`
	Keys      bson.Raw `bson:"key"`
	Collation bson.Raw `bson:"collation"`
	Sparse    bool     `bson:"sparse"`
	Hidden    bool     `bson:"hidden"`
	Partial   bson.Raw `bson:"partialFilterExpression"`
}

func (s replicationIndexSpec) usable() bool {
	keys, err := s.Keys.Elements()
	if err != nil || len(keys) != 3 || s.Sparse || s.Hidden || len(s.Partial) != 0 {
		return false
	}
	for i, name := range []string{"database", "collection", "fullpath"} {
		direction, ok := keys[i].Value().AsInt64OK()
		if keys[i].Key() != name || !ok || direction != 1 {
			return false
		}
	}
	if len(s.Collation) != 0 {
		locale, ok := s.Collation.Lookup("locale").StringValueOK()
		if !ok || locale != "simple" {
			return false
		}
	}
	return true
}

func ensureReplicationIndex(ctx context.Context, collection *mongo.Collection, cp replicationCheckpoint) (err error) {
	// Verify the current incarnation through reads. Even an otherwise idempotent
	// createIndexes command can wait for unrelated uncommitted writes under
	// majority write concern, preventing a committed bootstrap during that wait.
	cursor, err := collection.Indexes().List(ctx, options.ListIndexes().SetBatchSize(1))
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), watchCleanupTimeout)
		defer cancel()
		err = errors.Join(err, cursor.Close(closeCtx))
	}()
	for cursor.Next(ctx) {
		var spec replicationIndexSpec
		if err = cursor.Decode(&spec); err != nil {
			return err
		}
		if spec.Name != sourceScanIndexName {
			continue
		}
		if !spec.usable() {
			return replicationFailure(cp.Database, cp.Collection, types.ReplicationUnsupported, errors.New("source scan index is incompatible"))
		}
		return nil
	}
	if err = cursor.Err(); err != nil {
		return err
	}
	_, err = collection.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "fullpath", Value: 1}},
		Options: options.Index().SetName(sourceScanIndexName).SetCollation(&options.Collation{Locale: "simple"}),
	})
	return err
}
