package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/helper"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/pkg/model"
)

var (
	ErrIndexerRequired = fmt.Errorf("%w: indexer is required", indexer.ErrIndexNotReady)
)

// Engine handles all business logic and coordinates with the storage backend.
type Engine struct {
	storage storage.DocumentStore
	indexer indexer.Service
}

// New creates a new Query Engine instance.
func New(storage storage.DocumentStore, indexer indexer.Service) *Engine {
	return &Engine{
		storage: storage,
		indexer: indexer,
	}
}

// GetDocument retrieves a document by path.
func (e *Engine) GetDocument(ctx context.Context, database string, path string) (model.Document, error) {
	stored, err := e.storage.Get(ctx, database, path)
	if err != nil {
		return nil, err
	}
	return helper.FlattenStorageDocument(stored), nil
}

// CreateDocument creates a new document.
func (e *Engine) CreateDocument(ctx context.Context, database string, doc model.Document) error {
	if doc == nil {
		return errors.New("document cannot be nil")
	}

	doc.GenerateIDIfEmpty()
	collection := doc.GetCollection()
	if collection == "" {
		return errors.New("collection is required")
	}
	doc.StripProtectedFields()

	return e.storage.Create(ctx, database, types.NewStoredDoc(database, collection, doc.GetID(), doc))
}

// ReplaceDocument replaces a document or creates it if it doesn't exist (Upsert).
func (e *Engine) ReplaceDocument(ctx context.Context, database string, doc model.Document, pred model.Filters) (model.Document, error) {
	if doc == nil {
		return nil, errors.New("document cannot be nil")
	}

	collection := doc.GetCollection()
	if collection == "" {
		return nil, errors.New("collection is required")
	}

	id := doc.GetID()
	if id == "" {
		return nil, errors.New("document ID is required")
	}
	doc.StripProtectedFields()

	fullpath := collection + "/" + id

	// Try Get first
	_, err := e.storage.Get(ctx, database, fullpath)
	if err != nil {
		if err == model.ErrNotFound {
			// Create
			storedDoc := storage.NewStoredDoc(database, collection, id, doc)
			if err := e.storage.Create(ctx, database, storedDoc); err != nil {
				return nil, err
			}
			return helper.FlattenStorageDocument(&storedDoc), nil
		}
		return nil, err
	}

	// Update (Replace data)
	if err := e.storage.Update(ctx, database, fullpath, map[string]interface{}(doc), pred); err != nil {
		return nil, err
	}

	// Return updated doc
	updatedDoc, err := e.storage.Get(ctx, database, fullpath)
	if err != nil {
		return nil, err
	}

	return helper.FlattenStorageDocument(updatedDoc), nil
}

// PatchDocument updates specific fields of a document (Merge + CAS).
func (e *Engine) PatchDocument(ctx context.Context, database string, doc model.Document, pred model.Filters) (model.Document, error) {
	if doc == nil {
		return nil, errors.New("document cannot be nil")
	}

	collection := doc.GetCollection()
	if collection == "" {
		return nil, errors.New("collection is required")
	}

	id := doc.GetID()
	if id == "" {
		return nil, errors.New("document ID is required")
	}

	fullpath := collection + "/" + id
	doc.StripProtectedFields()
	delete(doc, "id")

	if err := e.storage.Patch(ctx, database, fullpath, map[string]interface{}(doc), pred); err != nil {
		return nil, err
	}

	updatedDoc, err := e.storage.Get(ctx, database, fullpath)
	if err != nil {
		return nil, err
	}

	return helper.FlattenStorageDocument(updatedDoc), nil
}

// DeleteDocument deletes a document.
func (e *Engine) DeleteDocument(ctx context.Context, database string, path string, pred model.Filters) error {
	return e.storage.Delete(ctx, database, path, pred)
}

// ExecuteQuery returns the documents from one page.
func (e *Engine) ExecuteQuery(ctx context.Context, database string, q model.Query) ([]model.Document, error) {
	page, err := e.ExecuteQueryPage(ctx, database, q)
	return page.Documents, err
}

func (e *Engine) isIDOnlyQuery(q model.Query) bool {
	if len(q.OrderBy) > 0 || len(q.Filters) == 0 {
		return false
	}
	for _, f := range q.Filters {
		if f.Field != "id" || (f.Op != model.OpEq && f.Op != model.OpIn) {
			return false
		}
	}
	return true
}

func (e *Engine) queryToPlan(q model.Query) (indexer.Plan, error) {
	plan := indexer.Plan{Collection: q.Collection, Limit: q.Limit, ShowDeleted: q.ShowDeleted}
	operators := map[model.FilterOp]indexer.FilterOp{model.OpEq: indexer.FilterEq, model.OpNe: indexer.FilterNe, model.OpGt: indexer.FilterGt, model.OpGte: indexer.FilterGte, model.OpLt: indexer.FilterLt, model.OpLte: indexer.FilterLte, model.OpIn: indexer.FilterIn, model.OpContains: indexer.FilterContains}
	for _, f := range q.Filters {
		op, ok := operators[f.Op]
		if !ok {
			return indexer.Plan{}, fmt.Errorf("%w: invalid operator", model.ErrInvalidQuery)
		}
		plan.Filters = append(plan.Filters, indexer.Filter{Field: f.Field, Op: op, Value: f.Value})
	}
	for _, order := range q.OrderBy {
		dir := indexer.Asc
		if order.Direction == "desc" {
			dir = indexer.Desc
		}
		plan.OrderBy = append(plan.OrderBy, indexer.OrderField{Field: order.Field, Direction: dir})
	}
	return plan, nil
}

// Pull handles replication pull requests.
func (e *Engine) Pull(ctx context.Context, database string, req storage.ReplicationPullRequest) (*storage.ReplicationPullResponse, error) {
	q := model.Query{
		Collection: req.Collection,
		Filters: []model.Filter{
			{
				Field: "updatedAt",
				Op:    ">=",
				Value: req.Checkpoint,
			},
		},
		OrderBy: []model.Order{
			{
				Field:     "updatedAt",
				Direction: "asc",
			},
			{
				Field:     "id",
				Direction: "asc",
			},
		},
		Limit:       req.Limit,
		ShowDeleted: true,
	}

	docs, err := e.storage.Query(ctx, database, q)
	if err != nil {
		return nil, err
	}

	if docs == nil {
		docs = make([]*storage.StoredDoc, 0)
	}

	newCheckpoint := req.Checkpoint
	if len(docs) > 0 {
		newCheckpoint = docs[len(docs)-1].UpdatedAt
	}

	return &storage.ReplicationPullResponse{
		Documents:  docs,
		Checkpoint: newCheckpoint,
	}, nil
}

// Push handles replication push requests.
func (e *Engine) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	var conflicts []*storage.StoredDoc

	for _, change := range req.Changes {
		doc := change.Doc
		doc.Collection = req.Collection

		if doc.Fullpath == "" {
			var id string
			if v, ok := doc.Data["id"].(string); ok {
				id = v
			}

			if id != "" {
				doc.Fullpath = doc.Collection + "/" + id
			}
		}

		existing, err := e.storage.Get(ctx, database, doc.Fullpath, storage.ReadOptions{Consistency: storage.ReadAuthoritative})
		if err != nil {
			if err == model.ErrNotFound {
				if err := e.storage.Create(ctx, database, *doc); err != nil {
					conflicts = append(conflicts, doc)
				}
				continue
			}
			return nil, err
		}

		if change.BaseVersion != nil && existing.Version != *change.BaseVersion {
			conflicts = append(conflicts, existing)
			continue
		}

		filters := model.Filters{}
		if change.BaseVersion != nil {
			filters = append(filters, model.Filter{
				Field: "version",
				Op:    "==",
				Value: *change.BaseVersion,
			})
		}

		// Handle Delete
		if doc.Deleted {
			if err := e.storage.Delete(ctx, database, doc.Fullpath, filters); err != nil {
				if err == model.ErrPreconditionFailed {
					latest, getErr := e.storage.Get(ctx, database, doc.Fullpath, storage.ReadOptions{Consistency: storage.ReadAuthoritative})
					if getErr != nil && !errors.Is(getErr, model.ErrNotFound) {
						return nil, getErr
					}
					if latest != nil {
						conflicts = append(conflicts, latest)
					}
				} else if err == model.ErrNotFound {
					latest, getErr := e.storage.Get(ctx, database, doc.Fullpath, storage.ReadOptions{Consistency: storage.ReadAuthoritative})
					if getErr != nil && !errors.Is(getErr, model.ErrNotFound) {
						return nil, getErr
					}
					if getErr == nil && latest != nil {
						conflicts = append(conflicts, latest)
					}
				} else {
					return nil, err
				}
			}
			continue
		}

		// Update
		if err := e.storage.Update(ctx, database, doc.Fullpath, doc.Data, filters); err != nil {
			if err == model.ErrPreconditionFailed {
				latest, getErr := e.storage.Get(ctx, database, doc.Fullpath, storage.ReadOptions{Consistency: storage.ReadAuthoritative})
				if getErr != nil && !errors.Is(getErr, model.ErrNotFound) {
					return nil, getErr
				}
				if latest != nil {
					conflicts = append(conflicts, latest)
				}
			} else {
				return nil, err
			}
		}
	}

	return &storage.ReplicationPushResponse{
		Conflicts: conflicts,
	}, nil
}
