// Package store defines the shared Store interface and types for index storage backends.
// This package exists to break circular dependencies between manager and storage implementations.
package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
)

// Store defines the interface for index storage backends.
// Implementations: mem_store.Store (memory), persist_store.PebbleStore (PebbleDB)
type Store interface {
	ApplyDocumentProjection(replacements []Projection, progress string) error
	ReadView(ctx context.Context, ref QueryIndexRef, budget ReadBudget) (ReadView, error)
	ReadGeneration(db, collection, fingerprint string) (Generation, bool, error)
	PublishGeneration(ref QueryIndexRef, progress string) error
	SetFailure(ref QueryIndexRef, failure string) error
	PublishBootstrapCatalog(catalog BootstrapCatalog) error
	PublishBootstrapCatalogs(catalogs []BootstrapCatalog, progress string) error
	ReadBootstrapCatalog(database string) (BootstrapCatalog, bool, error)
	ListBootstrapCatalogs() ([]BootstrapCatalog, error)
	ListQueryIndexes() ([]QueryIndexRef, error)
	DeleteQueryIndex(ref QueryIndexRef) error
	// Index operations
	// progress is the event progress marker to save atomically with the operation.
	// Pass empty string if no progress update is needed.
	Upsert(db, pattern, tmplID, docID string, orderKey []byte, progress string) error
	Delete(db, pattern, tmplID, docID string, progress string) error
	Get(db, pattern, tmplID, docID string) (orderKey []byte, found bool)
	Search(db, pattern, tmplID string, opts SearchOptions) ([]DocRef, error)

	// Index management
	DeleteIndex(db, pattern, tmplID string) error
	DeleteDatabase(db string) error // Delete all indexes for a database
	IsDatabaseRetired(db string) (bool, error)
	SetState(db, pattern, tmplID string, state IndexState) error
	GetState(db, pattern, tmplID string) (IndexState, error)

	// Index enumeration (for reconciliation)
	ListDatabases() ([]string, error)
	ListIndexes(db string) ([]IndexInfo, error)

	// Checkpoint for event progress
	// LoadProgress loads the last saved progress marker.
	LoadProgress() (string, error)

	// Lifecycle
	Flush() error
	Close() error
}

// QueryIndexRef identifies one concrete, immutable index definition and build.
type QueryIndexRef struct {
	Database            string
	Collection          string
	TemplateFingerprint string
	Generation          string
}

// Projection replaces every posting for a document in one index partition.
// An empty PostingKeys removes the document's complete previous projection.
type Projection struct {
	Index       QueryIndexRef
	DocumentID  string
	PostingKeys [][]byte
}

type ReadBudget struct {
	MaxOverlayReplacements int
	MaxOverlayBytes        int
	MaxExamined            int64
}

func NormalizeReadBudget(budget ReadBudget) (ReadBudget, error) {
	if budget.MaxOverlayReplacements < 0 || budget.MaxOverlayBytes < 0 || budget.MaxExamined < 0 {
		return ReadBudget{}, fmt.Errorf("index read budget must not be negative")
	}
	if budget.MaxOverlayReplacements == 0 {
		budget.MaxOverlayReplacements = 10_000
	}
	if budget.MaxOverlayBytes == 0 {
		budget.MaxOverlayBytes = 16 << 20
	}
	if budget.MaxExamined == 0 {
		budget.MaxExamined = 100_000
	}
	return budget, nil
}

var ErrWorkLimit = errors.New("index query work limit exceeded")

// ReadView pins one partition across all branches of a candidate stream.
// Close every iterator before closing its owning view.
type ReadView interface {
	Scan(ctx context.Context, opts SearchOptions) (Iterator, error)
	Close() error
}

type Iterator interface {
	Next() (DocRef, bool, error)
	// Examined includes superseded rows suppressed while advancing the iterator.
	Examined() int64
	Close() error
}

// Generation is published only after its postings and bootstrap progress commit.
type Generation struct {
	ID                string
	Ready             bool
	BootstrapProgress string
	Failure           string
}

// BootstrapCatalog certifies a complete database scan for these definitions.
// Its generation also covers empty and subsequently created collections.
type BootstrapCatalog struct {
	Database             string
	Generation           string
	TemplateFingerprints []string
	BootstrapProgress    string
}

func OwnBootstrapCatalog(catalog BootstrapCatalog) (BootstrapCatalog, error) {
	if catalog.Database == "" || catalog.Generation == "" {
		return BootstrapCatalog{}, fmt.Errorf("incomplete bootstrap catalog identity")
	}
	catalog.TemplateFingerprints = slices.Clone(catalog.TemplateFingerprints)
	for _, fingerprint := range catalog.TemplateFingerprints {
		if fingerprint == "" {
			return BootstrapCatalog{}, fmt.Errorf("empty bootstrap template fingerprint")
		}
	}
	slices.Sort(catalog.TemplateFingerprints)
	catalog.TemplateFingerprints = slices.Compact(catalog.TemplateFingerprints)
	return catalog, nil
}

func OwnBootstrapCatalogs(catalogs []BootstrapCatalog, progress string) ([]BootstrapCatalog, error) {
	if len(catalogs) == 0 {
		return nil, fmt.Errorf("bootstrap catalog inventory is empty")
	}
	owned := make([]BootstrapCatalog, len(catalogs))
	databases := make(map[string]struct{}, len(catalogs))
	hasTemplate := false
	for i, catalog := range catalogs {
		var err error
		owned[i], err = OwnBootstrapCatalog(catalog)
		if err != nil {
			return nil, err
		}
		if catalog.Generation != catalogs[0].Generation || catalog.BootstrapProgress != progress {
			return nil, fmt.Errorf("bootstrap catalogs must share one generation and progress boundary")
		}
		if _, exists := databases[catalog.Database]; exists {
			return nil, fmt.Errorf("duplicate bootstrap database %q", catalog.Database)
		}
		databases[catalog.Database] = struct{}{}
		hasTemplate = hasTemplate || len(catalog.TemplateFingerprints) != 0
	}
	if !hasTemplate {
		return nil, fmt.Errorf("bootstrap catalog inventory has no index templates")
	}
	return owned, nil
}

func ResolveGeneration(catalog BootstrapCatalog, catalogExists bool, fingerprint string, local Generation, localExists bool) (Generation, bool) {
	if !catalogExists {
		return local, localExists
	}
	if !slices.Contains(catalog.TemplateFingerprints, fingerprint) {
		return Generation{}, false
	}
	generation := Generation{ID: catalog.Generation, Ready: true, BootstrapProgress: catalog.BootstrapProgress}
	if localExists && local.ID == catalog.Generation && local.Failure != "" {
		generation.Ready = false
		generation.Failure = local.Failure
	}
	return generation, true
}

// OwnProjections validates event identity and copies bytes before publication.
// The returned replacement sets must remain immutable while readers hold them.
func OwnProjections(replacements []Projection) ([]Projection, error) {
	owned := make([]Projection, len(replacements))
	type identity struct {
		index QueryIndexRef
		docID string
	}
	seen := make(map[identity]struct{}, len(replacements))
	for i, projection := range replacements {
		ref := projection.Index
		if ref.Database == "" || ref.Collection == "" || ref.TemplateFingerprint == "" || ref.Generation == "" || projection.DocumentID == "" {
			return nil, fmt.Errorf("incomplete index projection identity")
		}
		id := identity{ref, projection.DocumentID}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate index projection for document %q", projection.DocumentID)
		}
		seen[id] = struct{}{}
		owned[i] = Projection{Index: ref, DocumentID: projection.DocumentID, PostingKeys: make([][]byte, 0, len(projection.PostingKeys))}
		keys := make(map[string]struct{}, len(projection.PostingKeys))
		for _, key := range projection.PostingKeys {
			if len(key) == 0 {
				return nil, fmt.Errorf("empty posting key for document %q", projection.DocumentID)
			}
			if _, exists := keys[string(key)]; exists {
				continue
			}
			keys[string(key)] = struct{}{}
			owned[i].PostingKeys = append(owned[i].PostingKeys, bytes.Clone(key))
		}
	}
	return owned, nil
}

// IndexInfo provides metadata about an index.
type IndexInfo struct {
	Pattern    string
	TemplateID string
	RawPattern string
	State      IndexState
	DocCount   int
}

// SearchOptions configures a search operation.
type SearchOptions struct {
	Lower      []byte // Lower bound (inclusive)
	Upper      []byte // Upper bound (exclusive)
	StartAfter []byte // Cursor for pagination (exclusive)
	Limit      int    // Max results to return
}

// DocRef represents a document reference returned by Store.Search.
type DocRef struct {
	ID       string
	OrderKey []byte
}

// IndexState represents the current state of an index.
type IndexState string

const (
	IndexStateHealthy    IndexState = "healthy"
	IndexStateRebuilding IndexState = "rebuilding"
	IndexStateFailed     IndexState = "failed"
)
