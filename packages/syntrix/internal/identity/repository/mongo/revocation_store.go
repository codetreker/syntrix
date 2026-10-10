package mongo

import (
	"context"
	"errors"
	"time"

	"github.com/codetreker/syntrix/internal/identity/repository"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type revocationStore struct {
	coll *mongo.Collection
}

func NewRevocationStore(db *mongo.Database, collectionName string) repository.TokenRevocationStore {
	if collectionName == "" {
		collectionName = "auth_revocations"
	}
	return &revocationStore{
		coll: db.Collection(collectionName),
	}
}

func (s *revocationStore) RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error {
	doc := repository.RevokedToken{
		JTI:       jti,
		ExpiresAt: expiresAt,
		RevokedAt: time.Now(),
	}
	_, err := s.coll.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return nil // Already revoked
	}
	return err
}

func (s *revocationStore) RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error {
	// Set RevokedAt to the past to bypass grace period
	doc := repository.RevokedToken{
		JTI:       jti,
		ExpiresAt: expiresAt,
		RevokedAt: time.Now().Add(-24 * time.Hour),
	}
	_, err := s.coll.InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return nil // Already revoked
	}
	return err
}

// RevokeTokenIfNotRevoked atomically checks if token is revoked and revokes it.
// Uses MongoDB's unique index on _id to ensure atomicity.
// Returns ErrTokenAlreadyRevoked if the token was already revoked (within grace period).
func (s *revocationStore) RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error {
	// First, try to insert the revocation record
	doc := repository.RevokedToken{
		JTI:       jti,
		ExpiresAt: expiresAt,
		RevokedAt: time.Now(),
	}
	_, err := s.coll.InsertOne(ctx, doc)
	if err == nil {
		// Successfully inserted - token was not revoked
		return nil
	}

	// If it's a duplicate key error, check if within grace period
	if mongo.IsDuplicateKeyError(err) {
		// Check if the existing revocation is past the grace period
		filter := bson.M{"_id": jti}
		var existingDoc repository.RevokedToken
		findErr := s.coll.FindOne(ctx, filter).Decode(&existingDoc)
		if findErr != nil {
			// This shouldn't happen since we just got a duplicate key error
			return findErr
		}

		// If within grace period, token is effectively not revoked yet (overlap window)
		if gracePeriod > 0 && time.Since(existingDoc.RevokedAt) < gracePeriod {
			// Token is in grace period, but we need to deny concurrent refresh
			// to prevent race condition
			return repository.ErrTokenAlreadyRevoked
		}

		// Token is already revoked (past grace period)
		return repository.ErrTokenAlreadyRevoked
	}

	return err
}

func (s *revocationStore) IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error) {
	filter := bson.M{"_id": jti}
	var doc repository.RevokedToken
	err := s.coll.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return false, nil // Not revoked
		}
		return false, err
	}

	// If grace period is 0, it's revoked immediately
	if gracePeriod == 0 {
		return true, nil
	}

	// Check if within grace period
	if time.Since(doc.RevokedAt) < gracePeriod {
		return false, nil // Treated as not revoked yet (for overlap)
	}

	return true, nil
}

func (s *revocationStore) EnsureIndexes(ctx context.Context) error {
	// Revocation TTL index (Global cleanup)
	_, err := s.coll.Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "expires_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	})
	return err
}
