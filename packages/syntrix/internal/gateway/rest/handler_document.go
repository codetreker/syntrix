package rest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/codetreker/syntrix/internal/helper"
	"github.com/codetreker/syntrix/pkg/model"
)

// decodeBody decodes request body into dst, using cached body from context if available.
// This avoids double JSON parsing when authorized middleware has already parsed the body.
func decodeBody[T any](r *http.Request, dst *T) error {
	if cached := getParsedBody(r.Context()); cached != nil {
		// Re-marshal and unmarshal to properly convert to the target type
		// This is still more efficient than parsing from stream twice
		bytes, err := json.Marshal(cached)
		if err != nil {
			return err
		}
		return json.Unmarshal(bytes, dst)
	}
	// Fallback to decoding from body stream
	return json.NewDecoder(r.Body).Decode(dst)
}

// decodeBodyAsDocument decodes request body directly into model.Document.
// Optimized path that avoids re-marshaling when cached body is available.
func decodeBodyAsDocument(r *http.Request) (model.Document, error) {
	if cached := getParsedBody(r.Context()); cached != nil {
		// Direct conversion - most efficient path
		return model.Document(cached), nil
	}
	// Fallback to decoding from body stream
	var data model.Document
	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

func (h *Handler) handleGetDocument(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")

	if err := helper.CheckDocumentPath(path); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document path")
		return
	}

	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	doc, err := h.engine.GetDocument(r.Context(), database, path)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

func (h *Handler) handleCreateDocument(w http.ResponseWriter, r *http.Request) {
	collection := r.PathValue("path")

	if err := helper.CheckCollectionPath(collection); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid collection path")
		return
	}

	data, err := decodeBodyAsDocument(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	if err := data.ValidateDocument(); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document data")
		return
	}

	data.StripProtectedFields()
	data.GenerateIDIfEmpty()
	data.SetCollection(collection)

	path := collection + "/" + data.GetID()

	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	if err := h.engine.CreateDocument(r.Context(), database, data); err != nil {
		writeStorageError(w, err)
		return
	}

	doc, err := h.engine.GetDocument(r.Context(), database, path)
	if err != nil {
		writeInternalError(w, err, "Failed to retrieve created document")
		return
	}

	writeJSON(w, http.StatusCreated, doc)
}

func (h *Handler) handleReplaceDocument(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")

	collection, docID, err := helper.ExplodeFullpath(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document path")
		return
	}
	if docID == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document path: missing document ID")
		return
	}

	var data UpdateDocumentRequest
	if err := decodeBody(r, &data); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	if err := data.Doc.ValidateDocument(); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document data")
		return
	}

	data.Doc.StripProtectedFields()

	if id := data.Doc.GetID(); id != "" && id != docID {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Document ID cannot be changed")
		return
	}

	data.Doc.SetID(docID)
	data.Doc.SetCollection(collection)

	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	doc, err := h.engine.ReplaceDocument(r.Context(), database, data.Doc, data.IfMatch)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

func (h *Handler) handlePatchDocument(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")

	collection, docID, err := helper.ExplodeFullpath(path)
	if err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document path")
		return
	}
	if docID == "" {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document path: missing document ID")
		return
	}

	var data UpdateDocumentRequest
	if err := decodeBody(r, &data); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
		return
	}

	if err := data.Doc.ValidateDocument(); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document data")
		return
	}

	data.Doc.StripProtectedFields()

	if id := data.Doc.GetID(); id != "" && id != docID {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Document ID cannot be changed")
		return
	}

	if data.Doc.IsEmpty() {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "No data to update")
		return
	}

	data.Doc.SetID(docID)
	data.Doc.SetCollection(collection)
	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	doc, err := h.engine.PatchDocument(r.Context(), database, data.Doc, data.IfMatch)
	if err != nil {
		writeStorageError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, doc)
}

func (h *Handler) handleDeleteDocument(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")

	if err := helper.CheckDocumentPath(path); err != nil {
		writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid document path")
		return
	}

	var data DeleteDocumentRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			if !errors.Is(err, io.EOF) {
				writeError(w, http.StatusBadRequest, ErrCodeBadRequest, "Invalid request body")
				return
			}
		}
	}

	database, ok := h.databaseOrError(w, r)
	if !ok {
		return
	}

	if err := h.engine.DeleteDocument(r.Context(), database, path, data.IfMatch); err != nil {
		writeStorageError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
