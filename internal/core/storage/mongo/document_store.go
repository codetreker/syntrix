package mongo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/pkg/model"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

type documentStore struct {
	sourceIndexMu       sync.Mutex
	sourceIndexesReady  bool
	client              *mongo.Client
	db                  *mongo.Database
	dataCollection      string
	sysCollection       string
	softDeleteRetention time.Duration
	openStream          func(context.Context, *mongo.Collection, mongo.Pipeline, *options.ChangeStreamOptions) (changeStream, error)
	readSource          func(context.Context, *mongo.Collection, bool) (watchSource, error)
}

// NewDocumentStore initializes a new MongoDB document store
func NewDocumentStore(client *mongo.Client, db *mongo.Database, dataColl string, sysColl string, softDeleteRetention time.Duration) types.DocumentStore {
	return &documentStore{
		client:              client,
		db:                  db,
		dataCollection:      dataColl,
		sysCollection:       sysColl,
		softDeleteRetention: softDeleteRetention,
	}
}

func (m *documentStore) getCollection(nameOrPath string) *mongo.Collection {
	if nameOrPath == "sys" || strings.HasPrefix(nameOrPath, "sys/") {
		return m.db.Collection(m.sysCollection)
	}
	return m.db.Collection(m.dataCollection)
}

func (m *documentStore) Get(ctx context.Context, database string, fullpath string, opts ...types.ReadOptions) (*types.StoredDoc, error) {
	readOpts, err := types.ResolveReadOptions(opts)
	if err != nil {
		return nil, err
	}
	collection := m.getCollection(fullpath)
	if readOpts.Consistency == types.ReadAuthoritative {
		collection, err = collection.Clone(options.Collection().SetReadPreference(readpref.Primary()))
		if err != nil {
			return nil, err
		}
	}
	id := types.CalculateDatabase(database, fullpath)

	var doc types.StoredDoc
	filter := bson.M{"_id": id, "database": database}
	if !readOpts.ShowDeleted {
		filter["deleted"] = bson.M{"$ne": true}
	}
	result := collection.FindOne(ctx, filter)
	if readOpts.MaxBytes > 0 {
		raw, rawErr := result.Raw()
		if rawErr == nil && int64(len(raw)) > readOpts.MaxBytes {
			return nil, types.ErrReadBudget
		}
	}
	err = result.Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, model.ErrNotFound
		}
		return nil, err
	}
	if readOpts.MaxBytes > 0 {
		size, err := types.StoredDocumentBytes(&doc)
		if err != nil {
			return nil, err
		}
		if size > readOpts.MaxBytes {
			return nil, types.ErrReadBudget
		}
	}

	return &doc, nil
}

func (m *documentStore) GetMany(ctx context.Context, database string, paths []string, opts ...types.ReadOptions) ([]*types.StoredDoc, error) {
	readOpts, err := types.ResolveReadOptions(opts)
	if err != nil {
		return nil, err
	}
	result := make([]*types.StoredDoc, len(paths))
	groups := make(map[string][]string)
	multiplicity := make(map[string]int64, len(paths))
	ids := make([]string, len(paths))
	for i, path := range paths {
		ids[i] = types.CalculateDatabase(database, path)
		multiplicity[ids[i]]++
		name := m.getCollection(path).Name()
		groups[name] = append(groups[name], ids[i])
	}
	documents := make(map[string]*types.StoredDoc, len(paths))
	remainingBytes := readOpts.MaxBytes
	for name, groupIDs := range groups {
		collection := m.db.Collection(name)
		if readOpts.Consistency == types.ReadAuthoritative {
			collection, err = collection.Clone(options.Collection().SetReadPreference(readpref.Primary()))
			if err != nil {
				return nil, err
			}
		}
		filter := bson.M{"_id": bson.M{"$in": groupIDs}, "database": database}
		if !readOpts.ShowDeleted {
			filter["deleted"] = bson.M{"$ne": true}
		}
		cursor, err := collection.Find(ctx, filter)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer cursor.Close(ctx)
			for cursor.Next(ctx) {
				if readOpts.MaxBytes > 0 {
					id, ok := cursor.Current.Lookup("_id").StringValueOK()
					if !ok {
						return fmt.Errorf("source read document has a non-string storage key")
					}
					count := multiplicity[id]
					if count == 0 {
						return fmt.Errorf("source read returned an unrequested document")
					}
					size := int64(len(cursor.Current))
					if size > remainingBytes/count {
						return types.ErrReadBudget
					}
					remainingBytes -= size * count
				}
				var doc types.StoredDoc
				if err := cursor.Decode(&doc); err != nil {
					return err
				}
				if readOpts.MaxBytes > 0 {
					size, err := types.StoredDocumentBytes(&doc)
					if err != nil {
						return err
					}
					// Decoding typed metadata can expand its canonical encoding.
					if extra := size - int64(len(cursor.Current)); extra > 0 {
						count := multiplicity[doc.Id]
						if extra > remainingBytes/count {
							return types.ErrReadBudget
						}
						remainingBytes -= extra * count
					}
				}
				documents[doc.Id] = &doc
			}
			return cursor.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	for i, id := range ids {
		result[i] = documents[id]
	}
	return result, nil
}

const sourceScanIndexName = "source_database_collection_fullpath"

func (m *documentStore) ensureSourceIndexes(ctx context.Context) error {
	m.sourceIndexMu.Lock()
	defer m.sourceIndexMu.Unlock()
	if m.sourceIndexesReady {
		return nil
	}
	for _, name := range []string{m.dataCollection, m.sysCollection} {
		_, err := m.db.Collection(name).Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys:    bson.D{{Key: "database", Value: 1}, {Key: "collection", Value: 1}, {Key: "fullpath", Value: 1}},
			Options: options.Index().SetName(sourceScanIndexName).SetCollation(&options.Collation{Locale: "simple"}),
		})
		if err != nil {
			return fmt.Errorf("ensure source scan index: %w", err)
		}
	}
	m.sourceIndexesReady = true
	return nil
}

func (m *documentStore) ScanDocuments(ctx context.Context, database string, request types.SourceScanRequest) (types.SourceScanPage, error) {
	if err := request.Validate(database); err != nil {
		return types.SourceScanPage{}, err
	}
	if err := m.ensureSourceIndexes(ctx); err != nil {
		return types.SourceScanPage{}, err
	}
	collection := m.getCollection(request.Collection)
	var err error
	if request.Consistency == types.ReadAuthoritative {
		collection, err = collection.Clone(options.Collection().SetReadPreference(readpref.Primary()))
		if err != nil {
			return types.SourceScanPage{}, err
		}
	}
	filter := bson.D{{Key: "database", Value: database}, {Key: "collection", Value: request.Collection}}
	if request.AfterID != "" {
		filter = append(filter, bson.E{Key: "fullpath", Value: bson.M{"$gt": request.Collection + "/" + request.AfterID}})
	}
	// A required index hint and simple collation prevent hidden blocking sorts.
	findOptions := options.Find().SetSort(bson.D{{Key: "fullpath", Value: 1}}).
		SetHint(sourceScanIndexName).SetCollation(&options.Collation{Locale: "simple"}).
		SetLimit(int64(request.Limit)).SetBatchSize(int32(request.Limit)).SetAllowDiskUse(false)
	cursor, err := collection.Find(ctx, filter, findOptions)
	if err != nil {
		return types.SourceScanPage{}, err
	}
	defer cursor.Close(ctx)
	page := types.SourceScanPage{Documents: make([]*types.StoredDoc, 0), NextAfter: request.AfterID}
	for cursor.Next(ctx) {
		page.Bytes += int64(len(cursor.Current))
		if request.MaxBytes > 0 && page.Bytes > request.MaxBytes {
			return types.SourceScanPage{}, types.ErrSourceScanBudget
		}
		var doc types.StoredDoc
		if err := cursor.Decode(&doc); err != nil {
			return types.SourceScanPage{}, err
		}
		if doc.Database != database || doc.Collection != request.Collection {
			return types.SourceScanPage{}, fmt.Errorf("source candidate escaped requested scope")
		}
		id, err := types.LogicalDocumentID(&doc)
		if err != nil {
			return types.SourceScanPage{}, err
		}
		if id <= page.NextAfter {
			return types.SourceScanPage{}, fmt.Errorf("source candidates are not strictly ordered")
		}
		page.Documents = append(page.Documents, &doc)
		page.NextAfter = id
	}
	if err := cursor.Err(); err != nil {
		return types.SourceScanPage{}, err
	}
	page.Exhausted = len(page.Documents) < request.Limit
	return page, nil
}

func (m *documentStore) EnumerateCollections(ctx context.Context, database, afterCollection string, limit int, opts ...types.CollectionEnumerationOptions) ([]string, error) {
	resolved, err := types.ResolveCollectionEnumerationOptions(database, afterCollection, limit, opts)
	if err != nil {
		return nil, err
	}
	if err := m.ensureSourceIndexes(ctx); err != nil {
		return nil, err
	}
	names := []string{m.dataCollection}
	if resolved.IncludeSystem && m.sysCollection != m.dataCollection {
		names = append(names, m.sysCollection)
	}
	collections := make([]*mongo.Collection, len(names))
	heads := make([]string, len(names))
	next := func(i int, after string) error {
		filter := bson.D{{Key: "database", Value: database}}
		if after != "" {
			filter = append(filter, bson.E{Key: "collection", Value: bson.M{"$gt": after}})
		}
		var row struct {
			Collection string `bson:"collection"`
		}
		err := collections[i].FindOne(ctx, filter, options.FindOne().
			SetSort(bson.D{{Key: "collection", Value: 1}, {Key: "fullpath", Value: 1}}).
			SetHint(sourceScanIndexName).SetCollation(&options.Collation{Locale: "simple"}).
			SetProjection(bson.D{{Key: "_id", Value: 0}, {Key: "collection", Value: 1}})).Decode(&row)
		if errors.Is(err, mongo.ErrNoDocuments) {
			heads[i] = ""
			return nil
		}
		if err != nil {
			return err
		}
		if err := types.ValidateConcreteCollection(row.Collection); err != nil {
			return err
		}
		if row.Collection <= after {
			return fmt.Errorf("source collections are not strictly ordered")
		}
		heads[i] = row.Collection
		return nil
	}
	for i, name := range names {
		collections[i], err = m.db.Collection(name).Clone(options.Collection().SetReadPreference(readpref.Primary()))
		if err != nil {
			return nil, err
		}
		if err := next(i, afterCollection); err != nil {
			return nil, err
		}
	}
	result := make([]string, 0)
	for len(result) < limit {
		smallest := ""
		for _, head := range heads {
			if head != "" && (smallest == "" || head < smallest) {
				smallest = head
			}
		}
		if smallest == "" {
			break
		}
		result = append(result, smallest)
		if len(result) == limit {
			break
		}
		for i, head := range heads {
			if head == smallest {
				if err := next(i, smallest); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}

func (m *documentStore) Create(ctx context.Context, database string, doc types.StoredDoc) error {
	collection := m.getCollection(doc.Collection)

	// Ensure derived fields are populated
	if doc.CollectionHash == "" {
		doc.CollectionHash = types.CalculateCollectionHash(doc.Collection)
	}
	doc.Database = database

	// Ensure soft-delete fields are reset
	doc.Deleted = false

	_, err := collection.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		// Check if the document exists but is soft-deleted
		id := types.CalculateDatabase(database, doc.Fullpath)
		var existingDoc types.StoredDoc
		if findErr := collection.FindOne(ctx, bson.M{"_id": id, "database": database}).Decode(&existingDoc); findErr == nil {
			if existingDoc.Deleted {
				// Overwrite the soft-deleted document
				_, replaceErr := collection.ReplaceOne(ctx, bson.M{"_id": id, "database": database}, doc)
				return replaceErr
			}
		}
		return model.ErrExists
	}
	return err
}

func (m *documentStore) Update(ctx context.Context, database string, path string, data map[string]interface{}, precond model.Filters) error {
	collection := m.getCollection(path)
	id := types.CalculateDatabase(database, path)

	filter := makeFilterBSON(precond)
	filter["_id"] = id
	filter["database"] = database
	filter["deleted"] = bson.M{"$ne": true}

	update := bson.M{
		"$set": bson.M{
			"data":       data,
			"updated_at": time.Now().UnixMilli(),
		},
		"$inc": bson.M{
			"version": 1,
		},
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}

	if result.MatchedCount == 0 {
		count, _ := collection.CountDocuments(ctx, bson.M{"_id": id, "database": database})
		if count == 0 {
			return model.ErrNotFound
		}
		return model.ErrPreconditionFailed
	}

	return nil
}

func (m *documentStore) Patch(ctx context.Context, database string, path string, data map[string]interface{}, precond model.Filters) error {
	collection := m.getCollection(path)
	id := types.CalculateDatabase(database, path)

	filter := makeFilterBSON(precond)
	filter["_id"] = id
	filter["database"] = database
	filter["deleted"] = bson.M{"$ne": true}

	updates := bson.M{
		"updated_at": time.Now().UnixMilli(),
	}
	for k, v := range data {
		updates["data."+k] = v
	}

	update := bson.M{
		"$set": updates,
		"$inc": bson.M{
			"version": 1,
		},
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}

	if result.MatchedCount == 0 {
		count, _ := collection.CountDocuments(ctx, bson.M{"_id": id, "database": database})
		if count == 0 {
			return model.ErrNotFound
		}
		return model.ErrPreconditionFailed
	}

	return nil
}

func (m *documentStore) Delete(ctx context.Context, database string, path string, precond model.Filters) error {
	collection := m.getCollection(path)
	id := types.CalculateDatabase(database, path)

	filter := makeFilterBSON(precond)
	filter["_id"] = id
	filter["database"] = database
	filter["deleted"] = bson.M{"$ne": true}

	update := bson.M{
		"$set": bson.M{
			"deleted":        true,
			"data":           bson.M{},
			"updated_at":     time.Now().UnixMilli(),
			"sys_expires_at": time.Now().Add(m.softDeleteRetention),
		},
		"$inc": bson.M{
			"version": 1,
		},
	}

	result, err := collection.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}

	if result.MatchedCount == 0 {
		count, _ := collection.CountDocuments(ctx, bson.M{"_id": id, "database": database})
		if count == 0 {
			return model.ErrNotFound
		}
		// If document exists but matched count is 0, it means version conflict or already deleted
		// We can check if it is already deleted
		var doc types.StoredDoc
		if err := collection.FindOne(ctx, bson.M{"_id": id, "database": database}).Decode(&doc); err == nil {
			if doc.Deleted {
				return model.ErrNotFound // Already deleted
			}
		}
		return model.ErrPreconditionFailed
	}

	return nil
}

func (m *documentStore) DeleteByDatabase(ctx context.Context, database string, limit int) (int, error) {
	// Delete from data collection
	dataCollection := m.db.Collection(m.dataCollection)
	dataDeleted, err := m.deleteByDatabaseFromCollection(ctx, dataCollection, database, limit)
	if err != nil {
		return dataDeleted, err
	}

	// If we hit the limit in data collection, return early
	if limit > 0 && dataDeleted >= limit {
		return dataDeleted, nil
	}

	// Calculate remaining limit for sys collection
	remainingLimit := 0
	if limit > 0 {
		remainingLimit = limit - dataDeleted
	}

	// Delete from sys collection
	sysCollection := m.db.Collection(m.sysCollection)
	sysDeleted, err := m.deleteByDatabaseFromCollection(ctx, sysCollection, database, remainingLimit)
	if err != nil {
		return dataDeleted + sysDeleted, err
	}

	return dataDeleted + sysDeleted, nil
}

func (m *documentStore) deleteByDatabaseFromCollection(ctx context.Context, collection *mongo.Collection, database string, limit int) (int, error) {
	filter := bson.M{"database": database}

	if limit > 0 {
		// When limiting, we need to find documents first, then delete by IDs
		findOpts := options.Find().SetLimit(int64(limit)).SetProjection(bson.M{"_id": 1})
		cursor, err := collection.Find(ctx, filter, findOpts)
		if err != nil {
			return 0, err
		}
		defer cursor.Close(ctx)

		var ids []string
		for cursor.Next(ctx) {
			var doc struct {
				ID string `bson:"_id"`
			}
			if err := cursor.Decode(&doc); err != nil {
				return 0, err
			}
			ids = append(ids, doc.ID)
		}

		if err := cursor.Err(); err != nil {
			return 0, err
		}

		if len(ids) == 0 {
			return 0, nil
		}

		// Hard delete the documents by IDs
		result, err := collection.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
		if err != nil {
			return 0, err
		}
		return int(result.DeletedCount), nil
	}

	// No limit - delete all documents for this database
	result, err := collection.DeleteMany(ctx, filter)
	if err != nil {
		return 0, err
	}
	return int(result.DeletedCount), nil
}

func (m *documentStore) Query(ctx context.Context, database string, q model.Query) ([]*types.StoredDoc, error) {
	collection := m.getCollection(q.Collection)

	filter := makeFilterBSON(q.Filters)
	filter["database"] = database
	filter["collection_hash"] = types.CalculateCollectionHash(q.Collection)
	if !q.ShowDeleted {
		filter["deleted"] = bson.M{"$ne": true}
	}

	findOptions := options.Find()
	if q.Limit > 0 {
		findOptions.SetLimit(int64(q.Limit))
	}

	if len(q.OrderBy) > 0 {
		sort := bson.D{}
		for _, o := range q.OrderBy {
			dir := 1
			if o.Direction == "desc" {
				dir = -1
			}
			sort = append(sort, bson.E{Key: mapField(o.Field), Value: dir})
		}
		findOptions.SetSort(sort)
	}

	// TODO: Implement StartAfter (Cursor)

	cursor, err := collection.Find(ctx, filter, findOptions)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var docs []*types.StoredDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}

	for _, d := range docs {
		d.Collection = q.Collection // Ensure collection is set
	}

	return docs, nil
}

// EnsureIndexes creates necessary indexes
func (s *documentStore) EnsureIndexes(ctx context.Context) error {
	if err := s.ensureSourceIndexes(ctx); err != nil {
		return err
	}
	coll := s.getCollection("")

	// (database, collection_hash)
	_, err := coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "database", Value: 1}, {Key: "collection_hash", Value: 1}},
		Options: options.Index().SetUnique(false),
	})
	if err != nil {
		return err
	}

	// Revocation TTL index (wait, revocation is separate now. But soft delete uses sys_expires_at)
	// "sys_expires_at" is used for soft delete retention.
	_, err = coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "sys_expires_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	})
	return err
}

func (m *documentStore) Close(ctx context.Context) error {
	if m.client != nil {
		return m.client.Disconnect(ctx)
	}
	return nil
}
