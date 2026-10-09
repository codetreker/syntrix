package mongo

import (
	"context"
	"time"

	"github.com/codetreker/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type documentProvider struct {
	store types.DocumentStore
}

// NewDocumentProvider initializes a new MongoDB document provider
func NewDocumentProvider(ctx context.Context, uri string, dbName string, dataColl string, sysColl string, softDeleteRetention time.Duration) (types.DocumentProvider, error) {
	clientOpts := options.Client().ApplyURI(uri)
	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, err
	}

	// Ping the database to verify connection
	if err := client.Ping(ctx, nil); err != nil {
		return nil, err
	}

	db := client.Database(dbName)

	// We use the concrete type to call EnsureIndexes
	dStore := &documentStore{
		client:              client,
		db:                  db,
		dataCollection:      dataColl,
		sysCollection:       sysColl,
		softDeleteRetention: softDeleteRetention,
	}

	if err := dStore.EnsureIndexes(ctx); err != nil {
		client.Disconnect(ctx)
		return nil, err
	}

	return &documentProvider{store: dStore}, nil
}

func (p *documentProvider) Document() types.DocumentStore {
	return p.store
}

func (p *documentProvider) Close(ctx context.Context) error {
	return p.store.Close(ctx)
}
