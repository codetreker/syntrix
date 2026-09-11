package types

import (
	"context"
	"errors"
	"time"

	"github.com/syntrixbase/syntrix/pkg/model"
)

var (
	ErrUserNotFound = errors.New("user not found")
	ErrUserExists   = errors.New("user already exists")
)

// User represents a user in the system
type User struct {
	ID            string                 `json:"id"`
	Username      string                 `json:"username"`
	PasswordHash  string                 `json:"password_hash"`
	PasswordAlgo  string                 `json:"password_algo"` // "argon2id" or "bcrypt"
	CreatedAt     time.Time              `json:"createdAt"`
	UpdatedAt     time.Time              `json:"updatedAt"`
	Disabled      bool                   `json:"disabled"`
	Roles         []string               `json:"roles"`
	DBAdmin       []string               `json:"db_admin"` // Databases with admin access
	Profile       map[string]interface{} `json:"profile"`
	LastLoginAt   time.Time              `json:"last_login_at"`
	LoginAttempts int                    `json:"login_attempts"`
	LockoutUntil  time.Time              `json:"lockout_until"`
}

// RevokedToken represents a revoked JWT
type RevokedToken struct {
	JTI       string    `bson:"_id"`
	ExpiresAt time.Time `bson:"expires_at"`
	RevokedAt time.Time `bson:"revoked_at"`
}

// StoredDoc represents a stored document in the database
type StoredDoc struct {
	// Id is the unique identifier for the document, database:hash(fullpath)
	Id string `json:"id" bson:"_id"`

	// Database is the database identifier
	Database string `json:"database" bson:"database"`

	// Fullpath is the Full Pathname of document
	Fullpath string `json:"-" bson:"fullpath"`

	// Collection is the parent collection name
	Collection string `json:"collection" bson:"collection"`

	// CollectionHash is a compact hash of collection for shorter indexes
	CollectionHash string `json:"collectionHash" bson:"collection_hash"`

	// Parent is the parent of collection
	Parent string `json:"-" bson:"parent"`

	// UpdatedAt is the timestamp of the last update (Unix millionseconds)
	UpdatedAt int64 `json:"updatedAt" bson:"updated_at"`

	// CreatedAt is the timestamp of the creation (Unix millionseconds)
	CreatedAt int64 `json:"createdAt" bson:"created_at"`

	// Version is the optimistic concurrency control version
	Version int64 `json:"version" bson:"version"`

	// Data is the actual content of the document
	Data map[string]interface{} `json:"data" bson:"data"`

	// Deleted indicates if the document is soft-deleted
	Deleted bool `json:"deleted,omitempty" bson:"deleted,omitempty"`
}

type ReadConsistency int

const (
	ReadDefault ReadConsistency = iota
	// ReadAuthoritative reads from the database's write source. It does not lock
	// the document or establish a transaction or linearizable read guarantee.
	ReadAuthoritative
)

type ReadOptions struct {
	Consistency ReadConsistency
	ShowDeleted bool
	// MaxBytes caps one read's populated result positions, including duplicate paths,
	// using StoredDocumentBytes. Backend record overhead may be charged conservatively.
	// Zero disables the cap. Exceeding a positive cap returns ErrReadBudget without
	// partial results; implementations must enforce the cap before accumulating rows.
	MaxBytes int64
}

var ErrReadBudget = errors.New("source read byte budget exceeded")

// DocumentStore defines the interface for document storage operations
type DocumentStore interface {
	// Get retrieves a document by path, excluding tombstones unless ShowDeleted is set.
	// At most one options value is accepted;
	// omission or ReadDefault preserves configured read routing. Unsupported options
	// and authoritative-source failures must be returned without replica fallback.
	Get(ctx context.Context, database string, path string, opts ...ReadOptions) (*StoredDoc, error)

	// GetMany retrieves multiple documents by their paths within a collection.
	// Returns documents in the same order as the provided paths.
	// Documents that are not found are returned as nil in the result slice.
	// Read options have the same routing and tombstone semantics as Get.
	GetMany(ctx context.Context, database string, paths []string, opts ...ReadOptions) ([]*StoredDoc, error)

	// Create inserts a new document. Fails if it already exists.
	Create(ctx context.Context, database string, doc StoredDoc) error

	// Update updates an existing document.
	// If pred is provided, it performs a CAS (Compare-And-Swap) operation.
	Update(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error

	// Patch updates specific fields of an existing document.
	// If pred is provided, it performs a CAS (Compare-And-Swap) operation.
	Patch(ctx context.Context, database string, path string, data map[string]interface{}, pred model.Filters) error

	// Delete removes a document by its path
	Delete(ctx context.Context, database string, path string, pred model.Filters) error

	// DeleteByDatabase deletes all documents belonging to a database.
	// Returns the number of documents deleted and any error.
	// If limit > 0, only deletes up to that many documents (for batching).
	DeleteByDatabase(ctx context.Context, database string, limit int) (int, error)

	// Query executes a complex query
	Query(ctx context.Context, database string, q model.Query) ([]*StoredDoc, error)

	// Watch observes one logical database. An empty collection selects its ordinary
	// data collections; a system collection must be selected explicitly. Empty after
	// starts at a source-established current boundary. A checkpoint resumes after
	// its completed prefix and must belong to the same source, scope, and options.
	Watch(ctx context.Context, database string, collection string, after WatchCheckpoint, opts WatchOptions) (WatchStream, error)

	// Close closes the connection to the backend
	Close(ctx context.Context) error
}

// DocumentScanner reads bounded raw source candidates in logical document ID order.
// Every returned row, including a tombstone, consumes the candidate and byte budgets.
// Pages are independent reads and do not establish a snapshot across requests.
type DocumentScanner interface {
	ScanDocuments(ctx context.Context, database string, request SourceScanRequest) (SourceScanPage, error)
}

// DocumentCollectionEnumerator lists concrete source scopes from the authoritative
// source, including scopes containing only tombstones. Results use binary order;
// a short page proves exhaustion. Enumeration does not establish a snapshot.
type DocumentCollectionEnumerator interface {
	EnumerateCollections(ctx context.Context, database, afterCollection string, limit int, opts ...CollectionEnumerationOptions) ([]string, error)
}

type CollectionEnumerationOptions struct {
	IncludeSystem bool
}

type SourceScanRequest struct {
	Collection string
	AfterID    string
	Limit      int
	// MaxBytes limits the BSON bytes of the whole page; zero disables this limit.
	MaxBytes    int64
	Consistency ReadConsistency
}

type SourceScanPage struct {
	Documents []*StoredDoc
	// NextAfter is the last candidate ID, including deleted candidates.
	NextAfter string
	// Exhausted is conservative: a full page requires another read to prove exhaustion.
	Exhausted bool
	Bytes     int64
}

var ErrSourceScanBudget = errors.New("source candidate byte budget exceeded")

// UserStore defines the interface for user storage operations
type UserStore interface {
	CreateUser(ctx context.Context, user *User) error
	GetUserByUsername(ctx context.Context, username string) (*User, error)
	GetUserByID(ctx context.Context, id string) (*User, error)
	ListUsers(ctx context.Context, limit int, offset int) ([]*User, error)
	UpdateUser(ctx context.Context, user *User) error
	UpdateUserLoginStats(ctx context.Context, id string, lastLogin time.Time, attempts int, lockoutUntil time.Time) error
	EnsureIndexes(ctx context.Context) error
	Close(ctx context.Context) error
}

// ErrTokenAlreadyRevoked is returned when attempting to revoke an already-revoked token.
var ErrTokenAlreadyRevoked = errors.New("token already revoked")

// TokenRevocationStore defines the interface for token revocation storage operations
type TokenRevocationStore interface {
	RevokeToken(ctx context.Context, jti string, expiresAt time.Time) error
	RevokeTokenImmediate(ctx context.Context, jti string, expiresAt time.Time) error
	// RevokeTokenIfNotRevoked atomically checks if token is revoked and revokes it.
	// Returns ErrTokenAlreadyRevoked if the token was already revoked (within grace period).
	// This prevents race conditions in concurrent token refresh attempts.
	RevokeTokenIfNotRevoked(ctx context.Context, jti string, expiresAt time.Time, gracePeriod time.Duration) error
	IsRevoked(ctx context.Context, jti string, gracePeriod time.Duration) (bool, error)
	EnsureIndexes(ctx context.Context) error
	Close(ctx context.Context) error
}

// DocumentProvider provides access to DocumentStore
type DocumentProvider interface {
	Document() DocumentStore
	Close(ctx context.Context) error
}

// AuthProvider provides access to UserStore and TokenRevocationStore
type AuthProvider interface {
	Users() UserStore
	Revocations() TokenRevocationStore
	Close(ctx context.Context) error
}

// OpKind represents the type of operation for routing
type OpKind int

const (
	OpRead OpKind = iota
	OpWrite
	OpMigrate
	// OpWatch requires the authoritative source used for writes, rather than an
	// independently configured ordinary read replica.
	OpWatch
)

// Router defines the interface for selecting stores based on operation
// Deprecated: Use specific routers instead
type Router interface {
	SelectDocument(op OpKind) DocumentStore
	SelectUser(op OpKind) UserStore
	SelectRevocation(op OpKind) TokenRevocationStore
}

// DocumentRouter routes document operations
type DocumentRouter interface {
	Select(database string, op OpKind) (DocumentStore, error)
}

// UserRouter routes user operations
type UserRouter interface {
	Select(database string, op OpKind) (UserStore, error)
}

// RevocationRouter routes revocation operations
type RevocationRouter interface {
	Select(database string, op OpKind) (TokenRevocationStore, error)
}

// EventType represents the type of change
type EventType string

const (
	EventCreate EventType = "create"
	EventUpdate EventType = "update"
	EventDelete EventType = "delete"
)

// Event represents a database change event
type Event struct {
	// ChangeID identifies one source change across watches and retries. Id remains
	// the affected document's storage key; neither field is an ordered checkpoint.
	ChangeID string     `json:"changeId,omitempty"`
	Id       string     `json:"id"`
	Database string     `json:"database"`
	Type     EventType  `json:"type"`
	Document *StoredDoc `json:"document,omitempty"` // Nil for delete
	Before   *StoredDoc `json:"before,omitempty"`   // Previous state, if available
	// Timestamp is Unix nanoseconds at the source's available precision. It is
	// event metadata, not an ordering key or a checkpoint.
	Timestamp int64 `json:"timestamp"`
}

// ReplicationPullRequest represents a request to pull changes
type ReplicationPullRequest struct {
	Collection string `json:"collection"`
	Checkpoint int64  `json:"checkpoint"`
	Limit      int    `json:"limit"`
}

// ReplicationPullResponse represents the response for a pull request
type ReplicationPullResponse struct {
	Documents  []*StoredDoc `json:"documents"`
	Checkpoint int64        `json:"checkpoint"`
}

// ReplicationPushChange represents a single change in a push request
type ReplicationPushChange struct {
	Doc         *StoredDoc `json:"doc"`
	BaseVersion *int64     `json:"baseVersion"` // Version known to the client
}

// ReplicationPushRequest represents a request to push changes
type ReplicationPushRequest struct {
	Collection string                  `json:"collection"`
	Changes    []ReplicationPushChange `json:"changes"`
}

// ReplicationPushResponse represents the response for a push request
type ReplicationPushResponse struct {
	Conflicts []*StoredDoc `json:"conflicts"`
}
