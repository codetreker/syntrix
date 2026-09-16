package rest

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syntrixbase/syntrix/internal/core/identity/types"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type ReplicaChange struct {
	Action string `json:"action"` // "create", "update", "delete"

	// User facing document type, represents a JSON object.
	//
	//	"id" field is reserved for document ID.
	//	"version" field is reserved for document version.
	Doc model.Document `json:"document"`

	BaseVersion *int64 `json:"-"`
}

func (c *ReplicaChange) UnmarshalJSON(data []byte) error {
	var raw struct {
		Action string          `json:"action"`
		Doc    json.RawMessage `json:"document"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	decoded := ReplicaChange{Action: raw.Action}
	if len(raw.Doc) > 0 {
		// Preserve exact version bytes without changing business numbers to json.Number.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw.Doc, &fields); err != nil {
			return err
		}
		if version, exists := fields["version"]; exists {
			if err := json.Unmarshal(version, &decoded.BaseVersion); err != nil {
				return fmt.Errorf("invalid document.version: %w", err)
			}
			if decoded.BaseVersion == nil || *decoded.BaseVersion < 0 {
				return errors.New("document.version must be a non-negative int64 integer")
			}
		}
		if err := json.Unmarshal(raw.Doc, &decoded.Doc); err != nil {
			return err
		}
	}
	*c = decoded
	return nil
}

type ReplicaPushRequest struct {
	Collection string          `json:"collection"`
	Changes    []ReplicaChange `json:"changes"`
}

type ReplicaPushResponse struct {
	Conflicts []model.Document `json:"conflicts"`
}

type ReplicaPullRequest struct {
	Collection string `json:"collection"`
	Checkpoint string `json:"checkpoint"`
	Limit      int    `json:"limit"`
}

type ReplicaPullResponse struct {
	Documents  []json.RawMessage `json:"documents"`
	Checkpoint string            `json:"checkpoint"`
	CaughtUp   bool              `json:"caughtUp"`
}

type UpdateDocumentRequest struct {
	Doc     model.Document `json:"doc"`
	IfMatch model.Filters  `json:"ifMatch,omitempty"`
}

type DeleteDocumentRequest struct {
	IfMatch model.Filters `json:"ifMatch,omitempty"`
}

var (
	ContextKeyDBAdmin = types.ContextKeyDBAdmin
)
