package rest

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/codetreker/syntrix/internal/helper"
	"github.com/codetreker/syntrix/pkg/model"
)

type TriggerGetRequest struct {
	Paths []string `json:"paths"`
}

type TriggerGetResponse struct {
	Documents []map[string]interface{} `json:"documents"`
}

type TriggerWriteOp struct {
	Type    string                 `json:"type"` // create, update, delete
	Path    string                 `json:"path"`
	Data    map[string]interface{} `json:"data,omitempty"`
	IfMatch model.Filters          `json:"ifMatch,omitempty"`
}

type TriggerWriteRequest struct {
	Writes []TriggerWriteOp `json:"writes"`
}

func (h *Handler) handleTriggerGet(w http.ResponseWriter, r *http.Request) {
	var req TriggerGetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	if len(req.Paths) == 0 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "paths cannot be empty")
		return
	}

	// Validate all paths before processing
	for _, path := range req.Paths {
		if err := helper.CheckDocumentPath(path); err != nil {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid path: "+path)
			return
		}
	}

	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	docs := make([]map[string]interface{}, 0, len(req.Paths))
	for _, path := range req.Paths {
		doc, err := h.engine.GetDocument(r.Context(), database, path)
		if err != nil {
			if err == model.ErrNotFound {
				continue // Skip not found documents
			}
			writeInternalError(w, err, "Failed to retrieve document")
			return
		}
		docs = append(docs, doc)
	}

	resp := TriggerGetResponse{Documents: docs}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleTriggerWrite(w http.ResponseWriter, r *http.Request) {
	var req TriggerWriteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	if len(req.Writes) == 0 {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "writes cannot be empty")
		return
	}

	// Validate all paths before processing
	for _, op := range req.Writes {
		if err := helper.CheckDocumentPath(op.Path); err != nil {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid path: "+op.Path)
			return
		}
	}

	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	for _, op := range req.Writes {
		collection, id, err := splitPath(op.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid path")
			return
		}

		switch op.Type {
		case "create":
			doc := model.Document(op.Data)
			if doc == nil {
				doc = make(model.Document)
			}
			doc.SetCollection(collection)
			doc.SetID(id)
			err = h.engine.CreateDocument(r.Context(), database, doc)
		case "update":
			patchDoc := model.Document(op.Data)
			if patchDoc == nil {
				patchDoc = make(model.Document)
			}
			patchDoc.SetCollection(collection)
			patchDoc.SetID(id)
			_, err = h.engine.PatchDocument(r.Context(), database, patchDoc, op.IfMatch)
		case "replace":
			replaceDoc := model.Document(op.Data)
			if replaceDoc == nil {
				replaceDoc = make(model.Document)
			}
			replaceDoc.SetCollection(collection)
			replaceDoc.SetID(id)
			_, err = h.engine.ReplaceDocument(r.Context(), database, replaceDoc, op.IfMatch)
		case "delete":
			err = h.engine.DeleteDocument(r.Context(), database, op.Path, op.IfMatch)
		default:
			writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "invalid write type")
			return
		}

		if err != nil {
			writeStorageError(w, err)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
}

func splitPath(path string) (string, string, error) {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", "", errInvalidPath
	}
	collection := strings.Join(parts[:len(parts)-1], "/")
	id := parts[len(parts)-1]
	if collection == "" || id == "" {
		return "", "", errInvalidPath
	}
	return collection, id, nil
}

// errInvalidPath is returned when a path cannot be split into collection and document ID
var errInvalidPath = stringError("invalid path")

// stringError is a simple error type for static error messages
type stringError string

func (e stringError) Error() string { return string(e) }
