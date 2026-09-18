package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/helper"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

// ValidatePushRequest checks the complete batch before any storage operation.
func ValidatePushRequest(database string, req storage.ReplicationPushRequest) error {
	if database == "" || !utf8.ValidString(database) || strings.ContainsRune(database, '\x00') || helper.CheckCollectionPath(req.Collection) != nil {
		return fmt.Errorf("%w: push requires a concrete database and collection", model.ErrInvalidQuery)
	}
	if len(req.Changes) == 0 {
		return fmt.Errorf("%w: push requires at least one change", model.ErrInvalidQuery)
	}
	for i, change := range req.Changes {
		if change.Action != storage.PushCreate && change.Action != storage.PushUpdate && change.Action != storage.PushDelete {
			return fmt.Errorf("%w: change %d has invalid action", model.ErrInvalidQuery, i)
		}
		if change.BaseVersion != nil && *change.BaseVersion < 0 {
			return fmt.Errorf("%w: change %d has negative version", model.ErrInvalidQuery, i)
		}
		doc := change.Doc
		if doc == nil || (doc.Database != "" && doc.Database != database) || (doc.Collection != "" && doc.Collection != req.Collection) || (doc.Deleted && change.Action != storage.PushDelete) {
			return fmt.Errorf("%w: change %d has invalid document scope or action", model.ErrInvalidQuery, i)
		}
		path := pushDocumentPath(req.Collection, doc)
		if helper.CheckDocumentPath(path) != nil || !strings.HasPrefix(path, req.Collection+"/") || strings.Contains(strings.TrimPrefix(path, req.Collection+"/"), "/") {
			return fmt.Errorf("%w: change %d has invalid document path", model.ErrInvalidQuery, i)
		}
		if id, ok := doc.Data["id"]; ok && id != strings.TrimPrefix(path, req.Collection+"/") {
			return fmt.Errorf("%w: change %d has inconsistent document ID", model.ErrInvalidQuery, i)
		}
		if _, err := json.Marshal(doc.Data); err != nil {
			return fmt.Errorf("%w: change %d has invalid document data: %w", model.ErrInvalidQuery, i, err)
		}
		if _, err := model.NormalizeValue(doc.Data); err != nil {
			return fmt.Errorf("%w: change %d has invalid document data: %w", model.ErrInvalidQuery, i, err)
		}
	}
	_, err := wire.EncodePushRequest(database, req)
	return err
}

func pushDocumentPath(collection string, doc *storage.StoredDoc) string {
	if doc.Fullpath != "" {
		return doc.Fullpath
	}
	id, _ := doc.Data["id"].(string)
	return collection + "/" + id
}

func (e *Engine) Push(ctx context.Context, database string, req storage.ReplicationPushRequest) (*storage.ReplicationPushResponse, error) {
	if err := ValidatePushRequest(database, req); err != nil {
		return nil, err
	}
	var conflicts []storage.ReplicationPushConflict
	for index, change := range req.Changes {
		path := pushDocumentPath(req.Collection, change.Doc)
		current, err := e.pushCurrent(ctx, database, path)
		if err != nil {
			return nil, err
		}
		missing := current == nil || current.Deleted
		if change.Action == storage.PushCreate && !missing {
			conflicts = append(conflicts, pushConflict(index, path, change, current, true))
			continue
		}
		if change.Action != storage.PushCreate && change.BaseVersion != nil && missing {
			conflicts = append(conflicts, pushConflict(index, path, change, current, false))
			continue
		}
		if change.Action == storage.PushDelete && change.BaseVersion == nil && missing {
			continue
		}
		if !missing && change.BaseVersion != nil && current.Version != *change.BaseVersion {
			conflicts = append(conflicts, pushConflict(index, path, change, current, false))
			continue
		}
		creating := missing && change.Action != storage.PushDelete
		if err := e.writePushChange(ctx, database, req.Collection, path, change, creating); err != nil {
			if !errors.Is(err, model.ErrPreconditionFailed) && !errors.Is(err, model.ErrNotFound) && !(creating && errors.Is(err, model.ErrExists)) {
				return nil, err
			}
			latest, readErr := e.pushCurrent(ctx, database, path)
			if readErr != nil {
				return nil, readErr
			}
			if change.Action == storage.PushDelete && change.BaseVersion == nil && (latest == nil || latest.Deleted) {
				continue
			}
			conflicts = append(conflicts, pushConflict(index, path, change, latest, creating))
		}
	}
	response := &storage.ReplicationPushResponse{Conflicts: conflicts}
	if _, err := wire.EncodePushResponse(database, req, response); err != nil {
		return nil, err
	}
	return response, nil
}

func (e *Engine) pushCurrent(ctx context.Context, database, path string) (*storage.StoredDoc, error) {
	doc, err := e.storage.Get(ctx, database, path, storage.ReadOptions{Consistency: storage.ReadAuthoritative, ShowDeleted: true})
	if errors.Is(err, model.ErrNotFound) {
		return nil, nil
	}
	return doc, err
}

func (e *Engine) writePushChange(ctx context.Context, database, collection, path string, change storage.ReplicationPushChange, creating bool) error {
	normalized, err := model.NormalizeValue(change.Doc.Data)
	if err != nil {
		return err
	}
	data := normalized.(map[string]interface{})
	if creating {
		doc := storage.NewStoredDoc(database, collection, strings.TrimPrefix(path, collection+"/"), data)
		return e.storage.Create(ctx, database, doc)
	}
	filters := model.Filters{}
	if change.BaseVersion != nil {
		filters = append(filters, model.Filter{Field: "version", Op: model.OpEq, Value: *change.BaseVersion})
	}
	if change.Action == storage.PushDelete {
		return e.storage.Delete(ctx, database, path, filters)
	}
	model.StripProtectedFields(data)
	return e.storage.Update(ctx, database, path, data, filters)
}

func pushConflict(index int, path string, change storage.ReplicationPushChange, current *storage.StoredDoc, creating bool) storage.ReplicationPushConflict {
	reason := storage.PushPreconditionFailed
	switch {
	case current == nil:
		reason = storage.PushMissing
	case current.Deleted:
		reason = storage.PushTombstoned
	case creating:
		reason = storage.PushAlreadyExists
	case change.BaseVersion != nil && current.Version != *change.BaseVersion:
		reason = storage.PushVersionMismatch
	}
	return storage.ReplicationPushConflict{ChangeIndex: index, ID: path[strings.LastIndex(path, "/")+1:], Reason: reason, Current: current}
}
