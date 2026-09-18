package rest

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syntrixbase/syntrix/internal/core/identity/types"
	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/pkg/model"
)

type ReplicaChange struct {
	Action string `json:"action"` // "create", "update", "delete"

	// Doc is an application object encoded as a typed value on the wire.
	Doc model.Document `json:"document"`

	BaseVersion     *int64                  `json:"-"`
	CreateCondition storage.CreateCondition `json:"createCondition,omitempty"`
}

func (c ReplicaChange) MarshalJSON() ([]byte, error) {
	doc, err := model.EncodeTypedValue(c.Doc)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Action          string                  `json:"action"`
		Doc             json.RawMessage         `json:"document"`
		CreateCondition storage.CreateCondition `json:"createCondition,omitempty"`
	}{Action: c.Action, Doc: doc, CreateCondition: c.CreateCondition})
}

func (c *ReplicaChange) UnmarshalJSON(data []byte) error {
	if err := model.ValidateJSONUnicode(data); err != nil {
		return err
	}
	var raw struct {
		Action          string          `json:"action"`
		Doc             json.RawMessage `json:"document"`
		CreateCondition json.RawMessage `json:"createCondition"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var condition storage.CreateCondition
	if len(raw.CreateCondition) > 0 {
		if err := json.Unmarshal(raw.CreateCondition, &condition); err != nil {
			return fmt.Errorf("invalid createCondition: %w", err)
		}
		if condition != storage.CreateIfAbsent && condition != storage.CreateIfTombstone {
			return errors.New("createCondition must be absent or tombstone")
		}
	}
	value, err := model.DecodeTypedValue(raw.Doc)
	if err != nil {
		return fmt.Errorf("invalid document: %w", err)
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return errors.New("document must be a typed object")
	}
	decoded := ReplicaChange{Action: raw.Action, Doc: model.Document(fields), CreateCondition: condition}
	if value, exists := fields["version"]; exists {
		version, ok := value.(int64)
		if !ok || version < 0 {
			return errors.New("document.version must be a non-negative int64 integer")
		}
		decoded.BaseVersion = &version
	}
	*c = decoded
	return nil
}

type ReplicaPushRequest struct {
	Collection string          `json:"collection"`
	Changes    []ReplicaChange `json:"changes"`
}

type ReplicaPushResponse struct {
	Conflicts []ReplicaPushConflict `json:"conflicts"`
}

type ReplicaPushConflict struct {
	ChangeIndex int             `json:"changeIndex"`
	ID          string          `json:"id"`
	Reason      string          `json:"reason"`
	Current     json.RawMessage `json:"current"`
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
