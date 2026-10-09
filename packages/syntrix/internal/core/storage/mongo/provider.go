package mongo

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Provider implements the generic Provider interface for MongoDB
type Provider struct {
	client *mongo.Client
	dbName string
}

// NewProvider initializes a new MongoDB provider (connection)
func NewProvider(ctx context.Context, uri string, dbName string) (*Provider, error) {
	clientOpts := options.Client().ApplyURI(uri)

	// Set some reasonable defaults if not provided in URI
	if clientOpts.ConnectTimeout == nil {
		timeout := 10 * time.Second
		clientOpts.SetConnectTimeout(timeout)
	}

	client, err := mongo.Connect(ctx, clientOpts)
	if err != nil {
		return nil, err
	}

	// Ping the database to verify connection
	if err := client.Ping(ctx, nil); err != nil {
		client.Disconnect(ctx)
		return nil, err
	}

	return &Provider{
		client: client,
		dbName: dbName,
	}, nil
}

// Client returns the underlying MongoDB client
func (p *Provider) Client() *mongo.Client {
	return p.client
}

// DatabaseName returns the default database name for this provider
func (p *Provider) DatabaseName() string {
	return p.dbName
}

// Close closes the MongoDB connection
func (p *Provider) Close(ctx context.Context) error {
	return p.client.Disconnect(ctx)
}
