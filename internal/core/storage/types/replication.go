package types

import (
	"errors"

	"github.com/syntrixbase/syntrix/pkg/model"
)

var ErrInvalidReplicationSource = errors.New("invalid replication source")
var ErrReplicationWindowIncomplete = errors.New("replication window is incomplete")

// ReplicationSource defines a remote result set, independently of transport pages.
type ReplicationSource struct {
	Version int           `json:"version"`
	Filters model.Filters `json:"filters"`
	OrderBy []model.Order `json:"orderBy,omitempty"`
	Limit   *int          `json:"limit,omitempty"`
}

type ReplicationEventType string

const (
	ReplicationUpsert ReplicationEventType = "upsert"
	ReplicationLeave  ReplicationEventType = "leave"
	ReplicationDelete ReplicationEventType = "delete"
)

type ReplicationEvent struct {
	Type     ReplicationEventType `json:"type"`
	Document model.Document       `json:"document,omitempty"`
	ID       string               `json:"id,omitempty"`
}
