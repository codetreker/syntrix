package mongo

import (
	"context"
	"errors"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readconcern"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

func (m *documentStore) scanAtWatchBoundary(ctx context.Context, database string, request types.SourceScanRequest) (page types.SourceScanPage, err error) {
	w := &documentWatch{binding: watchCheckpoint{Database: database, Collection: request.Collection}}
	defer func() {
		if err != nil {
			page = types.SourceScanPage{}
			if !errors.Is(err, types.ErrSourceScanBudget) {
				err = w.classify(err)
			}
		}
	}()
	cp, err := decodeWatchCheckpoint(request.AtLeast)
	if err != nil {
		return page, w.failure(types.WatchInvalidCheckpoint, err)
	}
	if err = validateWatchBootstrap(cp); err != nil {
		return page, w.failure(types.WatchInvalidCheckpoint, err)
	}
	if cp.Database != database || cp.Collection != request.Collection {
		return page, w.failure(types.WatchScopeMismatch, errors.New("scan boundary belongs to another logical scope"))
	}
	collection, err := m.getCollection(request.Collection).Clone(options.Collection().SetReadPreference(readpref.Primary()).SetReadConcern(readconcern.Majority()))
	if err != nil {
		return page, err
	}
	readSource := m.readSource
	if readSource == nil {
		readSource = m.watchSource
	}
	checkSource := func() error {
		source, sourceErr := readSource(ctx, collection, false)
		if errors.Is(sourceErr, errWatchCollectionMissing) || sourceErr == nil && source != cp.Source {
			return w.failure(types.WatchSourceMismatch, errors.Join(errors.New("scan source incarnation changed"), sourceErr))
		}
		return sourceErr
	}
	if err = checkSource(); err != nil {
		return page, err
	}
	if err = ensureWatchScanIndex(ctx, collection); err != nil {
		return page, err
	}
	// Catalog reads and index installation must not advance the causal session:
	// their operationTime can include writes beyond the fixed committed boundary.
	err = withWatchReadBoundary(ctx, m.client, cp, func(readCtx context.Context) error {
		var readErr error
		page, readErr = scanDocumentPage(readCtx, collection, database, request)
		return readErr
	})
	if err == nil {
		err = checkSource()
	}
	return page, err
}

type watchScanIndexSpec struct {
	Name      string   `bson:"name"`
	Keys      bson.Raw `bson:"key"`
	Collation bson.Raw `bson:"collation"`
	Sparse    bool     `bson:"sparse"`
	Hidden    bool     `bson:"hidden"`
	Partial   bson.Raw `bson:"partialFilterExpression"`
}

func (s watchScanIndexSpec) usable() bool {
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

func ensureWatchScanIndex(ctx context.Context, collection *mongo.Collection) (err error) {
	// Check the current incarnation rather than trusting the ordinary scan cache.
	// Avoid redundant DDL, which may wait behind unrelated uncommitted writes.
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
		var spec watchScanIndexSpec
		if err = cursor.Decode(&spec); err != nil {
			return err
		}
		if spec.Name == sourceScanIndexName {
			if !spec.usable() {
				return &types.WatchError{Code: types.WatchUnsupported, Cause: errors.New("source scan index is incompatible")}
			}
			return nil
		}
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
