package storage

import (
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
)

type StoredDoc = types.StoredDoc
type User = types.User
type RevokedToken = types.RevokedToken
type DocumentStore = types.DocumentStore
type ReadOptions = types.ReadOptions
type ReadConsistency = types.ReadConsistency
type CreateCondition = types.CreateCondition
type CreateOptions = types.CreateOptions
type UserStore = types.UserStore
type TokenRevocationStore = types.TokenRevocationStore
type DocumentProvider = types.DocumentProvider
type AuthProvider = types.AuthProvider
type OpKind = types.OpKind
type EventType = types.EventType
type Event = types.Event
type ReplicationPullRequest = types.ReplicationPullRequest
type ReplicationPullResponse = types.ReplicationPullResponse
type ReplicationPushChange = types.ReplicationPushChange
type ReplicationPushRequest = types.ReplicationPushRequest
type ReplicationPushResponse = types.ReplicationPushResponse
type ReplicationPushConflict = types.ReplicationPushConflict
type PushAction = types.PushAction
type PushConflictReason = types.PushConflictReason
type WatchOptions = types.WatchOptions
type WatchCheckpoint = types.WatchCheckpoint
type WatchFrame = types.WatchFrame
type WatchStream = types.WatchStream
type WatchError = types.WatchError
type WatchErrorCode = types.WatchErrorCode
type Router = types.Router
type DocumentRouter = types.DocumentRouter
type UserRouter = types.UserRouter
type RevocationRouter = types.RevocationRouter

const (
	ReadDefault       = types.ReadDefault
	ReadAuthoritative = types.ReadAuthoritative
)

const (
	CreateIfAbsent    = types.CreateIfAbsent
	CreateIfTombstone = types.CreateIfTombstone
)

const (
	PushCreate             = types.PushCreate
	PushUpdate             = types.PushUpdate
	PushDelete             = types.PushDelete
	PushVersionMismatch    = types.PushVersionMismatch
	PushMissing            = types.PushMissing
	PushTombstoned         = types.PushTombstoned
	PushAlreadyExists      = types.PushAlreadyExists
	PushPreconditionFailed = types.PushPreconditionFailed
)

const (
	OpRead    = types.OpRead
	OpWrite   = types.OpWrite
	OpMigrate = types.OpMigrate
	OpWatch   = types.OpWatch
)

const (
	EventCreate = types.EventCreate
	EventUpdate = types.EventUpdate
	EventDelete = types.EventDelete
)

const (
	WatchInvalidScope       = types.WatchInvalidScope
	WatchInvalidCheckpoint  = types.WatchInvalidCheckpoint
	WatchSourceMismatch     = types.WatchSourceMismatch
	WatchScopeMismatch      = types.WatchScopeMismatch
	WatchHistoryUnavailable = types.WatchHistoryUnavailable
	WatchPayloadUnavailable = types.WatchPayloadUnavailable
	WatchInvalidEvent       = types.WatchInvalidEvent
	WatchUnsupported        = types.WatchUnsupported
	WatchPermissionDenied   = types.WatchPermissionDenied
	WatchSourceUnavailable  = types.WatchSourceUnavailable
)

var (
	ErrUserNotFound        = types.ErrUserNotFound
	ErrUserExists          = types.ErrUserExists
	ErrTokenAlreadyRevoked = types.ErrTokenAlreadyRevoked
)
