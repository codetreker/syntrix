package core

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	pb "github.com/syntrixbase/syntrix/api/gen/query/v1"
	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	"github.com/syntrixbase/syntrix/internal/helper"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
	"google.golang.org/protobuf/proto"
)

const (
	MaxPullCursorBytes        = wire.MaxPullCursorBytes
	MaxPullRequestBytes       = 1 << 20
	DefaultPullLimit          = 100
	PullHardTimeout           = 30 * time.Second
	pullSoftTimeout           = 5 * time.Second
	maxPullFrames             = 10000
	maxPullSourceBytes  int64 = 16 << 20
)

type pullCursor struct {
	Version          int                   `json:"version"`
	Database         string                `json:"database"`
	DatabaseIdentity string                `json:"databaseIdentity"`
	Collection       string                `json:"collection"`
	Phase            string                `json:"phase"`
	Position         types.WatchCheckpoint `json:"position"`
	AfterID          string                `json:"afterId,omitempty"`
	SourceHash       string                `json:"sourceHash,omitempty"`
	GenerationID     string                `json:"generationId,omitempty"`
}

var numericCheckpoint = regexp.MustCompile(`^[+-]?[0-9]+$`)

func pullError(code types.WatchErrorCode, message string) error {
	return &types.WatchError{Code: code, Cause: errors.New(message)}
}

func pullDatabaseIdentity(database string, req types.ReplicationPullRequest) string {
	if req.DatabaseIdentity == "" {
		return database
	}
	return req.DatabaseIdentity
}

func decodePullCursor(database, identity, collection, encoded string, sourceHash ...string) (pullCursor, error) {
	var cursor pullCursor
	invalid := pullError(types.WatchInvalidCheckpoint, "invalid pull checkpoint")
	if len(encoded) > MaxPullCursorBytes {
		return cursor, invalid
	}
	if numericCheckpoint.MatchString(encoded) {
		return cursor, pullError(types.WatchHistoryUnavailable, "timestamp checkpoints require bootstrap")
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || model.ValidateJSONUnicode(data) != nil {
		return cursor, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF {
		return cursor, invalid
	}
	version, phases := 3, cursor.Phase == "scan" || cursor.Phase == "changes"
	if len(sourceHash) > 0 {
		version, phases = 4, cursor.Phase == "scan" || cursor.Phase == "replay" || cursor.Phase == "live"
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(data, canonical) || cursor.Version != version || cursor.Position == "" || !phases || strings.ContainsAny(cursor.AfterID, "/\x00") ||
		(cursor.Phase != "scan" && cursor.AfterID != "") {
		return cursor, invalid
	}
	if version == 3 && (cursor.SourceHash != "" || cursor.GenerationID != "") {
		return cursor, invalid
	}
	if version == 4 {
		if !validSourceHash(cursor.SourceHash) || !validGenerationID(cursor.GenerationID) {
			return cursor, invalid
		}
		if cursor.SourceHash != sourceHash[0] {
			return cursor, pullError(types.WatchScopeMismatch, "checkpoint belongs to another source")
		}
	}
	if cursor.Database != database || cursor.DatabaseIdentity != identity || cursor.Collection != collection {
		return cursor, pullError(types.WatchScopeMismatch, "checkpoint belongs to another scope")
	}
	return cursor, nil
}

func encodePullCursor(cursor pullCursor) (string, error) {
	if cursor.Position == "" || !utf8.ValidString(string(cursor.Position)) {
		return "", pullError(types.WatchInvalidEvent, "source returned an invalid checkpoint")
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > MaxPullCursorBytes {
		return "", model.ErrQueryWorkLimit
	}
	return encoded, nil
}

// ValidatePullRequest validates both local and remote requests before source work.
func ValidatePullRequest(database string, req types.ReplicationPullRequest) error {
	identity := pullDatabaseIdentity(database, req)
	if database == "" || strings.ContainsRune(database, '\x00') || strings.ContainsRune(identity, '\x00') ||
		!utf8.ValidString(database) || !utf8.ValidString(identity) || !utf8.ValidString(req.Collection) ||
		types.ValidateConcreteCollection(req.Collection) != nil || helper.CheckCollectionPath(req.Collection) != nil {
		return pullError(types.WatchInvalidScope, "pull requires a concrete database and collection")
	}
	if req.Source != nil && (req.Limit < 0 || req.Limit > wire.MaxPullLimit) {
		return types.ErrInvalidReplicationSource
	}
	if req.Limit < 0 || req.Limit > wire.MaxPullLimit || len(database)+len(req.DatabaseIdentity)+len(req.Collection)+len(req.Checkpoint) > MaxPullRequestBytes {
		return pullError(types.WatchInvalidCheckpoint, "pull request exceeds limits")
	}
	source, err := normalizePullSource(req)
	if err != nil {
		return err
	}
	hashes := []string{}
	if source != nil {
		hashes = append(hashes, source.hash)
	}
	if req.Checkpoint != "" {
		_, err := decodePullCursor(database, identity, req.Collection, req.Checkpoint, hashes...)
		return err
	}
	return nil
}

func (e *Engine) Pull(ctx context.Context, database string, req types.ReplicationPullRequest) (response *types.ReplicationPullResponse, resultErr error) {
	started := time.Now()
	var cursor pullCursor
	defer func() {
		reason := "page"
		returned, checkpoint := 0, ""
		if response != nil {
			returned, checkpoint = len(response.Documents)+len(response.Events), response.Checkpoint
			if response.CaughtUp {
				reason = "watermark"
			}
		}
		if resultErr != nil {
			reason = "internal_error"
			var failure *types.WatchError
			if errors.As(resultErr, &failure) {
				reason = string(failure.Code)
			}
			if errors.Is(resultErr, model.ErrQueryWorkLimit) {
				reason = "page_budget"
			}
			if errors.Is(resultErr, context.Canceled) {
				reason = "canceled"
			}
			if errors.Is(resultErr, context.DeadlineExceeded) {
				reason = "deadline_exceeded"
			}
		}
		slog.DebugContext(ctx, "Replication pull completed", "request_id", ctxkeys.RequestID(ctx), "scope_id", queryIdentity(database+"\x00"+pullDatabaseIdentity(database, req)+"\x00"+req.Collection), "phase", cursor.Phase, "input_checkpoint_id", queryIdentity(req.Checkpoint), "output_checkpoint_id", queryIdentity(checkpoint), "returned", returned, "reason", reason, "duration_ms", float64(time.Since(started).Microseconds())/1000)
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidatePullRequest(database, req); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, PullHardTimeout)
	defer cancel()
	limit := req.Limit
	if limit == 0 {
		limit = DefaultPullLimit
	}
	source, err := normalizePullSource(req)
	if err != nil {
		return nil, err
	}
	if source != nil && req.Source.Limit != nil {
		return e.pullWindow(ctx, database, req, source)
	}
	hashes := []string{}
	if source != nil {
		hashes = append(hashes, source.hash)
	}
	if req.Checkpoint == "" {
		stream, err := e.storage.Watch(ctx, database, req.Collection, "", types.WatchOptions{StartMode: types.WatchStartForScan})
		if err != nil {
			return nil, err
		}
		cursor = pullCursor{Version: 3, Database: database, DatabaseIdentity: pullDatabaseIdentity(database, req), Collection: req.Collection, Phase: "scan", Position: stream.InitialCheckpoint()}
		if source != nil {
			cursor.Version, cursor.SourceHash, cursor.GenerationID = 4, source.hash, uuid.NewString()
		}
		if err := stream.Close(); err != nil {
			return nil, err
		}
	} else {
		cursor, err = decodePullCursor(database, pullDatabaseIdentity(database, req), req.Collection, req.Checkpoint, hashes...)
		if err != nil {
			return nil, err
		}
	}
	checkpoint, err := encodePullCursor(cursor)
	if err != nil {
		return nil, err
	}
	page := &pullPage{source: source, response: types.ReplicationPullResponse{Documents: []model.Document{}, Checkpoint: checkpoint}}
	if source != nil {
		page.response.Documents = nil
		page.response.ProtocolVersion, page.response.Mode = 1, "events"
		page.response.DatabaseIdentity, page.response.SourceHash = cursor.DatabaseIdentity, cursor.SourceHash
		page.response.GenerationID, page.response.Phase = cursor.GenerationID, cursor.Phase
		page.response.BootstrapComplete = cursor.Phase == "live"
		page.response.Events = []types.ReplicationEvent{}
	}
	if cursor.Phase == "scan" {
		err = e.pullScan(ctx, cursor, limit, page)
	} else {
		err = e.pullChanges(ctx, cursor, limit, started.Add(pullSoftTimeout), page)
	}
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &page.response, nil
}

// pullPage advances only after the full typed document and its continuation fit
// both transports. Prefetched records outside this prefix are read again later.
type pullPage struct {
	source     *pullSource
	response   types.ReplicationPullResponse
	jsonBytes  int
	protoBytes int
	accepted   int
}

func (p *pullPage) admit(cursor pullCursor, doc model.Document, caughtUp bool, events ...*types.ReplicationEvent) (bool, error) {
	checkpoint, err := encodePullCursor(cursor)
	if err != nil {
		return false, err
	}
	candidate := p.response
	candidate.Checkpoint, candidate.CaughtUp = checkpoint, caughtUp
	if p.source != nil {
		candidate.Phase, candidate.BootstrapComplete = cursor.Phase, cursor.Phase == "live"
	}
	addedJSON, addedProto := 0, 0
	var event *types.ReplicationEvent
	if len(events) > 0 {
		event = events[0]
	}
	if event != nil {
		addedJSON, addedProto, err = wire.MeasurePullEvent(*event)
		if err != nil {
			return false, err
		}
		candidate.Events = append(candidate.Events, *event)
	}
	if doc != nil {
		encoded, err := model.EncodeTypedValue(map[string]any(doc))
		if err != nil {
			return false, pullError(types.WatchInvalidEvent, "document cannot be encoded")
		}
		addedJSON = len(encoded)
		addedProto = proto.Size(&pb.PullResponse{Documents: []*pb.Document{{Data: encoded}}})
		candidate.Documents = append(candidate.Documents, doc)
	}
	if err := wire.CheckPullPageSize(&candidate, p.jsonBytes+addedJSON, p.protoBytes+addedProto); err != nil {
		if p.accepted > 0 && errors.Is(err, model.ErrQueryWorkLimit) {
			return false, nil
		}
		return false, err
	}
	if checkpoint != p.response.Checkpoint || doc != nil || event != nil || caughtUp {
		p.accepted++
	}
	p.response = candidate
	p.jsonBytes += addedJSON
	p.protoBytes += addedProto
	return true, nil
}

func (e *Engine) pullScan(ctx context.Context, cursor pullCursor, limit int, page *pullPage) error {
	scanner, ok := e.storage.(types.DocumentScanner)
	if !ok {
		return pullError(types.WatchUnsupported, "store does not support document scans")
	}
	request := types.SourceScanRequest{Collection: cursor.Collection, AfterID: cursor.AfterID, Limit: limit, MaxBytes: maxPullSourceBytes, Consistency: types.ReadAuthoritative, AtLeast: cursor.Position}
	var batch types.SourceScanPage
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var err error
		batch, err = scanner.ScanDocuments(ctx, cursor.Database, request)
		if err == nil {
			break
		}
		if !errors.Is(err, types.ErrSourceScanBudget) {
			return err
		}
		if request.Limit == 1 || attempt >= 9 {
			return model.ErrQueryWorkLimit
		}
		request.Limit = max(1, request.Limit/2)
	}
	if batch.Bytes < 0 || batch.Bytes > request.MaxBytes || len(batch.Documents) > request.Limit || (len(batch.Documents) == 0 && !batch.Exhausted) {
		return pullError(types.WatchInvalidEvent, "source returned an inconsistent scan page")
	}
	for _, stored := range batch.Documents {
		if err := ctx.Err(); err != nil {
			return err
		}
		doc, err := pullDocument(stored, cursor.Database, cursor.Collection)
		if err != nil {
			return err
		}
		id := doc.GetID()
		if id <= cursor.AfterID {
			return pullError(types.WatchInvalidEvent, "scan documents are not ordered")
		}
		cursor.AfterID = id
		var projected *types.ReplicationEvent
		if page.source != nil {
			projected, err = page.source.project(stored, doc, true)
			if err != nil {
				return err
			}
			doc = nil
		}
		accepted, err := page.admit(cursor, doc, false, projected)
		if err != nil || !accepted {
			return err
		}
	}
	if batch.NextAfter != cursor.AfterID && (len(batch.Documents) > 0 || batch.NextAfter != "") {
		return pullError(types.WatchInvalidEvent, "scan continuation differs from returned documents")
	}
	if batch.Exhausted {
		cursor.Phase, cursor.AfterID = "changes", ""
		if page.source != nil {
			cursor.Phase = "replay"
		}
		_, err := page.admit(cursor, nil, false)
		return err
	}
	return nil
}

func pullDocument(stored *types.StoredDoc, database, collection string) (model.Document, error) {
	id, err := types.LogicalDocumentID(stored)
	if err != nil || stored.Database != database || stored.Collection != collection {
		return nil, pullError(types.WatchInvalidEvent, "source document has inconsistent logical identity")
	}
	doc := model.Document{}
	if !stored.Deleted {
		for key, value := range stored.Data {
			doc[key] = value
		}
	}
	doc["id"], doc["collection"] = id, collection
	doc["version"], doc["createdAt"], doc["updatedAt"] = stored.Version, stored.CreatedAt, stored.UpdatedAt
	delete(doc, "deleted")
	if stored.Deleted {
		doc["deleted"] = true
	}
	return doc, nil
}

func pullEventDocument(event *types.Event, database, collection string) (model.Document, error) {
	if event.Database != database || event.Collection != collection || event.DocumentID == "" || strings.ContainsAny(event.DocumentID, "/\x00") || !utf8.ValidString(event.DocumentID) {
		return nil, pullError(types.WatchInvalidEvent, "event has inconsistent logical identity")
	}
	switch event.Type {
	case types.EventDelete:
		if event.Document != nil {
			return nil, pullError(types.WatchInvalidEvent, "delete event carries a document")
		}
		return model.Document{"id": event.DocumentID, "collection": collection, "deleted": true}, nil
	case types.EventCreate, types.EventUpdate:
		if event.Document == nil {
			return nil, pullError(types.WatchPayloadUnavailable, "event document is unavailable")
		}
		doc, err := pullDocument(event.Document, database, collection)
		if err != nil {
			return nil, err
		}
		if doc.GetID() != event.DocumentID {
			return nil, pullError(types.WatchInvalidEvent, "event and document identities differ")
		}
		return doc, nil
	default:
		return nil, pullError(types.WatchInvalidEvent, "unsupported event type")
	}
}

func (e *Engine) pullChanges(ctx context.Context, cursor pullCursor, limit int, softDeadline time.Time, page *pullPage) (resultErr error) {
	stream, err := e.storage.Watch(ctx, cursor.Database, cursor.Collection, cursor.Position, types.WatchOptions{MaxAwaitTime: 100 * time.Millisecond})
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, stream.Close()) }()
	if stream.InitialCheckpoint() != cursor.Position {
		return pullError(types.WatchInvalidEvent, "watch did not preserve the requested checkpoint")
	}
	var sourceBytes int64
	initialPosition := cursor.Position
	progressed := false
	for frames := 0; frames < maxPullFrames && len(page.response.Documents)+len(page.response.Events) < limit && (!progressed || time.Now().Before(softDeadline)); frames++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		frame, err := stream.Next(ctx)
		if err != nil {
			return err
		}
		if frame.SourceBytes < 0 || frame.Checkpoint == "" || (frame.CaughtUp && frame.Event != nil) {
			return pullError(types.WatchInvalidEvent, "watch returned an invalid frame")
		}
		if frame.SourceBytes > maxPullSourceBytes-sourceBytes {
			if !progressed {
				if sourceBytes > 0 {
					return pullError(types.WatchSourceUnavailable, "watch exhausted the source budget without advancing")
				}
				return model.ErrQueryWorkLimit
			}
			return nil
		}
		sourceBytes += frame.SourceBytes
		var doc model.Document
		if frame.Event != nil {
			doc, err = pullEventDocument(frame.Event, cursor.Database, cursor.Collection)
			if err != nil {
				return err
			}
		}
		var projected *types.ReplicationEvent
		if page.source != nil {
			if frame.Event != nil {
				projected, err = page.source.project(frame.Event.Document, doc, false)
				if err != nil {
					return err
				}
			}
			doc = nil
			if frame.CaughtUp {
				cursor.Phase = "live"
			}
		}
		cursor.Position = frame.Checkpoint
		accepted, err := page.admit(cursor, doc, frame.CaughtUp, projected)
		if err != nil || !accepted {
			return err
		}
		progressed = cursor.Position != initialPosition
		if frame.CaughtUp {
			return nil
		}
		if sourceBytes == maxPullSourceBytes {
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !progressed {
		return pullError(types.WatchSourceUnavailable, "watch exhausted the page budget without advancing")
	}
	return nil
}
