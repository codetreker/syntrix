package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"github.com/syntrixbase/syntrix/internal/helper"
	"github.com/syntrixbase/syntrix/internal/indexer"
	"github.com/syntrixbase/syntrix/internal/indexer/encoding"
	"github.com/syntrixbase/syntrix/internal/indexer/manager"
	indexstore "github.com/syntrixbase/syntrix/internal/indexer/store"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

const maxQueryCandidates = 100000
const maxMaterializationDocuments = 128
const maxMaterializationBytes = 16 << 20
const maxCursorBytes = 16 << 10
const maxPositionBytes = 4096

type queryCursor struct {
	Version    int           `json:"v"`
	Scope      string        `json:"scope"`
	Route      string        `json:"route"`
	Order      []model.Order `json:"order"`
	Template   string        `json:"template"`
	Generation string        `json:"generation"`
	Branches   string        `json:"branches"`
	Position   []byte        `json:"position"`
}

func decodeCursor(token string) (queryCursor, error) {
	var cursor queryCursor
	invalid := fmt.Errorf("%w: invalid query cursor", model.ErrInvalidQuery)
	if len(token) > maxCursorBytes {
		return cursor, invalid
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil {
		return cursor, invalid
	}
	if err := model.ValidateJSONUnicode(data); err != nil {
		return cursor, invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return cursor, invalid
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cursor, invalid
	}
	if cursor.Version != 2 || cursor.Scope == "" || cursor.Route == "" || len(cursor.Position) == 0 || len(cursor.Position) > maxPositionBytes {
		return cursor, invalid
	}
	return cursor, nil
}

func encodeCursor(cursor queryCursor) (string, error) {
	if len(cursor.Position) == 0 || len(cursor.Position) > maxPositionBytes {
		return "", model.ErrQueryWorkLimit
	}
	data, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(data)
	if len(token) > maxCursorBytes {
		return "", model.ErrQueryWorkLimit
	}
	return token, nil
}

func queryScope(database string, q model.Query) (string, error) {
	predicates := make([]string, 0, len(q.Filters))
	for _, filter := range q.Filters {
		values := []any{filter.Value}
		if filter.Op == model.OpIn {
			values = filter.Value.([]any)
		}
		keys := make([]string, 0, len(values))
		for _, value := range values {
			key, err := model.ScalarKey(value)
			if err != nil {
				return "", err
			}
			keys = append(keys, hex.EncodeToString(key))
		}
		if filter.Op == model.OpIn {
			sort.Strings(keys)
		}
		encoded, err := json.Marshal([]any{filter.Field, filter.Op, keys})
		if err != nil {
			return "", err
		}
		predicates = append(predicates, string(encoded))
	}
	sort.Strings(predicates)
	encoded, err := json.Marshal([]any{2, database, q.Collection, predicates, q.OrderBy, q.ShowDeleted})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func normalizeQuery(q model.Query) (model.Query, error) {
	if err := helper.CheckCollectionPath(q.Collection); err != nil {
		return q, fmt.Errorf("%w: %w", model.ErrInvalidQuery, err)
	}
	if q.Limit < 0 || q.Limit > 1000 {
		return q, fmt.Errorf("%w: limit must be between 0 and 1000", model.ErrInvalidQuery)
	}
	if q.Limit == 0 {
		q.Limit = 100
	}
	filters, err := model.NormalizeFilters(q.Filters)
	if err != nil {
		return q, fmt.Errorf("%w: %w", model.ErrInvalidQuery, err)
	}
	q.Filters = filters
	for _, filter := range filters {
		if filter.Op == model.OpIn && len(filter.Value.([]any)) > 256 {
			return q, fmt.Errorf("%w: in exceeds 256 distinct values", model.ErrInvalidQuery)
		}
	}
	q.OrderBy = append([]model.Order(nil), q.OrderBy...)
	seen := map[string]bool{}
	for _, order := range q.OrderBy {
		if err := model.ValidateQueryField(order.Field); err != nil {
			return q, fmt.Errorf("%w: %w", model.ErrInvalidQuery, err)
		}
		if seen[order.Field] {
			return q, fmt.Errorf("%w: duplicate order field", model.ErrInvalidQuery)
		}
		seen[order.Field] = true
		if order.Direction != "asc" && order.Direction != "desc" {
			return q, fmt.Errorf("%w: invalid order direction", model.ErrInvalidQuery)
		}
	}
	return q, nil
}

func queryError(err error) error {
	switch {
	case errors.Is(err, manager.ErrStaleCursor):
		return fmt.Errorf("%w: %w", model.ErrStaleCursor, err)
	case errors.Is(err, indexstore.ErrWorkLimit) || errors.Is(err, types.ErrSourceScanBudget) || errors.Is(err, types.ErrReadBudget):
		return fmt.Errorf("%w: %w", model.ErrQueryWorkLimit, err)
	case errors.Is(err, manager.ErrInvalidPlan):
		return fmt.Errorf("%w: %w", model.ErrInvalidQuery, err)
	default:
		return err
	}
}

func (e *Engine) ExecuteQueryPage(ctx context.Context, database string, q model.Query) (page model.QueryPage, err error) {
	stats := &queryStats{start: time.Now()}
	defer func() {
		err = queryError(err)
		if err != nil {
			page = model.QueryPage{}
		}
		stats.record(ctx, page, err)
	}()
	if err = ctx.Err(); err != nil {
		return page, err
	}
	if database == "" {
		return page, fmt.Errorf("%w: database is required", model.ErrInvalidQuery)
	}
	q, err = normalizeQuery(q)
	if err != nil {
		return page, err
	}
	scope, err := queryScope(database, q)
	if err != nil {
		return page, err
	}
	stats.scopeID = scope
	cursor := queryCursor{Version: 2, Scope: scope}
	if q.StartAfter != "" {
		cursor, err = decodeCursor(q.StartAfter)
		if err != nil {
			return page, err
		}
		if cursor.Scope != scope {
			return page, fmt.Errorf("%w: cursor belongs to another query", model.ErrInvalidQuery)
		}
	}
	page.Documents = make([]model.Document, 0, q.Limit)
	if (len(q.Filters) == 0 && len(q.OrderBy) == 0) || e.isIDOnlyQuery(q) {
		return e.sourcePage(ctx, database, q, cursor, stats)
	}
	stats.route = "index-v2"
	if e.indexer == nil {
		return page, ErrIndexerRequired
	}
	candidateService, ok := e.indexer.(indexer.CandidateService)
	if !ok {
		return page, fmt.Errorf("%w: candidate stream service is required", indexer.ErrIndexNotReady)
	}
	if q.StartAfter != "" && (cursor.Route != "index-v2" || cursor.Template == "" || cursor.Generation == "" || cursor.Branches == "" || len(cursor.Order) == 0) {
		return page, fmt.Errorf("%w: cursor route mismatch", model.ErrInvalidQuery)
	}
	plan, err := e.queryToPlan(q)
	if err != nil {
		return page, err
	}
	plan.MaxExamined = maxQueryCandidates
	if q.StartAfter != "" {
		plan.AfterPosition = cursor.Position
		plan.TemplateFingerprint = cursor.Template
		plan.Generation = cursor.Generation
		plan.BranchHash = cursor.Branches
	}
	stream, err := candidateService.OpenCandidates(ctx, database, plan)
	if err != nil {
		return page, err
	}
	defer func() {
		if closeErr := stream.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	metadata := stream.Metadata()
	stats.planID = metadata.BranchHash
	stats.templateFingerprint = metadata.TemplateFingerprint
	stats.generationID = queryIdentity(metadata.Generation)
	stats.branchCount = len(metadata.Branches)
	for _, order := range metadata.EffectiveOrder {
		direction := "asc"
		if order.Direction == encoding.Desc {
			direction = "desc"
		}
		page.EffectiveOrder = append(page.EffectiveOrder, model.Order{Field: order.Field, Direction: direction})
	}
	if q.StartAfter != "" {
		sameOrder, _ := json.Marshal(page.EffectiveOrder)
		oldOrder, _ := json.Marshal(cursor.Order)
		if cursor.Template != metadata.TemplateFingerprint || cursor.Generation != metadata.Generation || cursor.Branches != metadata.BranchHash || !bytes.Equal(sameOrder, oldOrder) {
			return page, model.ErrStaleCursor
		}
	}
	cursor.Route = "index-v2"
	cursor.Order = page.EffectiveOrder
	cursor.Template = metadata.TemplateFingerprint
	cursor.Generation = metadata.Generation
	cursor.Branches = metadata.BranchHash
	seen := make(map[string]bool, q.Limit)
	pageBytes := 0
	exhausted := false
	for len(page.Documents) < q.Limit {
		batchLimit := min(maxMaterializationDocuments, q.Limit-len(page.Documents))
		groups := make([]manager.CandidateGroup, 0, batchLimit)
		paths := make([]string, 0, batchLimit)
		previous := cursor.Position
		for len(groups) < batchLimit {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			group, ok, err := stream.Next()
			stats.examined = stream.Examined()
			if err != nil {
				return page, err
			}
			if stats.examined > maxQueryCandidates {
				return page, model.ErrQueryWorkLimit
			}
			if !ok {
				exhausted = true
				break
			}
			stats.candidates++
			if len(group.Position) == 0 || len(group.Position) > maxPositionBytes {
				return page, model.ErrQueryWorkLimit
			}
			if len(previous) > 0 && bytes.Compare(group.Position, previous) <= 0 {
				return page, fmt.Errorf("candidate stream did not advance")
			}
			if group.ID == "" || strings.ContainsAny(group.ID, "/\x00") {
				return page, fmt.Errorf("candidate has invalid identity")
			}
			previous = group.Position
			groups = append(groups, group)
			paths = append(paths, q.Collection+"/"+group.ID)
		}
		if len(groups) == 0 {
			break
		}
		err = e.materialize(ctx, database, paths, stats, func(i int, stored *types.StoredDoc) error {
			group := groups[i]
			// Only materialized complete groups advance continuation. The batch
			// cannot contain more potential results than the page has free slots.
			cursor.Position = bytes.Clone(group.Position)
			doc, id, accepted, err := validateSource(stored, database, q, group.ID, stats)
			if err != nil {
				return err
			}
			if !accepted {
				return nil
			}
			if seen[id] {
				stats.duplicate++
				return nil
			}
			fields := make([]encoding.Field, 0, len(metadata.EffectiveOrder))
			order := metadata.EffectiveOrder
			if len(order) > 0 && order[len(order)-1].Field == "id" && order[len(order)-1].Direction == encoding.Asc {
				order = order[:len(order)-1]
			}
			for _, item := range order {
				value, present := indexer.DocumentField(stored, id, item.Field)
				fields = append(fields, encoding.Field{Value: value, Missing: !present, Direction: item.Direction})
			}
			position, err := encoding.Encode(fields, id)
			if err != nil {
				return fmt.Errorf("%w: invalid current order shape: %w", indexer.ErrIndexNotReady, err)
			}
			if !bytes.Equal(position, group.Position) {
				stats.position++
				return nil
			}
			// K alone cannot validate fixed membership/equality prefixes. A current
			// projection must still contain at least one reconstructed physical posting.
			projection, err := indexer.BuildDocumentProjection(stored, &metadata.Template, metadata.Generation, indexer.DefaultProjectionLimits())
			if err != nil {
				return fmt.Errorf("%w: current projection: %w", indexer.ErrIndexNotReady, err)
			}
			current := make(map[string]bool, len(projection.PostingKeys))
			for _, key := range projection.PostingKeys {
				current[string(key)] = true
			}
			validPosting := false
			for _, branch := range group.Branches {
				key, err := metadata.PostingKey(group, branch)
				if err != nil {
					return err
				}
				validPosting = validPosting || current[string(key)]
			}
			if !validPosting {
				stats.posting++
				return nil
			}
			if err := appendPageDocument(&page, doc, &pageBytes); err != nil {
				return err
			}
			seen[id] = true
			return nil
		})
		if err != nil {
			return page, err
		}
		if exhausted {
			break
		}
	}
	if !exhausted {
		token, err := encodeCursor(cursor)
		if err != nil {
			return page, err
		}
		page.NextCursor = &token
	}
	if err := wire.CheckJSONPageSize(page, pageBytes); err != nil {
		return page, err
	}
	return page, nil
}

func validateSource(stored *types.StoredDoc, database string, q model.Query, expectedID string, stats *queryStats) (model.Document, string, bool, error) {
	if stored == nil {
		stats.missing++
		return nil, "", false, nil
	}
	stats.sourceDocuments++
	if stored.Database != database || stored.Collection != q.Collection {
		stats.scope++
		return nil, "", false, nil
	}
	id, err := types.LogicalDocumentID(stored)
	if err != nil {
		return nil, "", false, err
	}
	if id != expectedID {
		stats.scope++
		return nil, id, false, nil
	}
	if stored.Deleted && !q.ShowDeleted {
		stats.deleted++
		return nil, id, false, nil
	}
	accepted, err := model.EvaluateFilters(q.Filters, func(field string) (any, bool) { return indexer.DocumentField(stored, id, field) })
	if err != nil {
		return nil, id, false, err
	}
	if !accepted {
		stats.predicate++
		return nil, id, false, nil
	}
	doc := make(model.Document, len(stored.Data)+6)
	if !stored.Deleted {
		for key, value := range stored.Data {
			doc[key] = value
		}
	}
	for _, field := range []string{"id", "collection", "version", "createdAt", "updatedAt", "deleted"} {
		value, _ := indexer.DocumentField(stored, id, field)
		doc[field] = value
	}
	return doc, id, true, nil
}

func appendPageDocument(page *model.QueryPage, doc model.Document, pageBytes *int) error {
	encoded, err := model.EncodeTypedValue(map[string]any(doc))
	if err != nil {
		return err
	}
	*pageBytes += len(encoded)
	if *pageBytes > wire.MaxPageBytes {
		return model.ErrQueryWorkLimit
	}
	page.Documents = append(page.Documents, doc)
	return nil
}

func (e *Engine) sourcePage(ctx context.Context, database string, q model.Query, cursor queryCursor, stats *queryStats) (model.QueryPage, error) {
	page := model.QueryPage{Documents: make([]model.Document, 0, q.Limit), EffectiveOrder: []model.Order{{Field: "id", Direction: "asc"}}}
	route := "source-list-v2"
	if len(q.Filters) > 0 {
		route = "source-ids-v2"
	}
	stats.route = route
	if q.StartAfter != "" {
		if cursor.Route != route || cursor.Template != "" || cursor.Generation != "" || cursor.Branches != "" || len(cursor.Order) != 1 || cursor.Order[0] != page.EffectiveOrder[0] {
			return page, fmt.Errorf("%w: source cursor mismatch", model.ErrInvalidQuery)
		}
		if !utf8.Valid(cursor.Position) || strings.ContainsAny(string(cursor.Position), "/\x00") {
			return page, fmt.Errorf("%w: invalid source position", model.ErrInvalidQuery)
		}
	}
	cursor.Route = route
	cursor.Order = page.EffectiveOrder
	after := string(cursor.Position)
	pageBytes, examined := 0, 0
	exhausted := false
	if len(q.Filters) > 0 {
		ids := make([]string, 0)
		filter := q.Filters[0]
		values := []any{filter.Value}
		if filter.Op == model.OpIn {
			values = filter.Value.([]any)
		}
		for _, value := range values {
			if id, ok := value.(string); ok && id != "" && !strings.ContainsAny(id, "/\x00") && id > after {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids)
		for offset := 0; offset < len(ids) && len(page.Documents) < q.Limit; {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			count := min(maxMaterializationDocuments, q.Limit-len(page.Documents), len(ids)-offset)
			batchIDs := ids[offset : offset+count]
			paths := make([]string, len(batchIDs))
			for i, id := range batchIDs {
				paths[i] = q.Collection + "/" + id
			}
			stats.candidates += int64(count)
			stats.examined += int64(count)
			err := e.materialize(ctx, database, paths, stats, func(i int, stored *types.StoredDoc) error {
				id := batchIDs[i]
				cursor.Position = []byte(id)
				doc, _, accepted, err := validateSource(stored, database, q, id, stats)
				if err != nil {
					return err
				}
				if accepted {
					return appendPageDocument(&page, doc, &pageBytes)
				}
				return nil
			})
			if err != nil {
				return page, err
			}
			offset += count
			if offset == len(ids) {
				exhausted = true
			}
		}
		if len(ids) == 0 {
			exhausted = true
		}
	} else {
		scanner, ok := e.storage.(types.DocumentScanner)
		if !ok {
			return page, fmt.Errorf("source does not support bounded query scans")
		}
		sourceBatchLimit := maxMaterializationDocuments
		for len(page.Documents) < q.Limit {
			if err := ctx.Err(); err != nil {
				return page, err
			}
			if examined >= maxQueryCandidates {
				return page, model.ErrQueryWorkLimit
			}
			batchLimit := min(sourceBatchLimit, q.Limit-len(page.Documents), maxQueryCandidates-examined)
			var batch types.SourceScanPage
			for {
				if err := ctx.Err(); err != nil {
					return page, err
				}
				stats.sourceReads++
				var err error
				batch, err = scanner.ScanDocuments(ctx, database, types.SourceScanRequest{Collection: q.Collection, AfterID: after, Limit: batchLimit, MaxBytes: maxMaterializationBytes, Consistency: types.ReadAuthoritative})
				if errors.Is(err, types.ErrSourceScanBudget) && batchLimit > 1 {
					batchLimit /= 2
					sourceBatchLimit = batchLimit
					continue
				}
				if err != nil {
					return page, err
				}
				break
			}
			if len(batch.Documents) > batchLimit {
				return page, fmt.Errorf("source exceeded candidate batch limit")
			}
			for _, stored := range batch.Documents {
				examined++
				stats.candidates++
				stats.examined++
				id, err := types.LogicalDocumentID(stored)
				if err != nil {
					return page, err
				}
				if id <= after {
					return page, fmt.Errorf("source cursor did not advance")
				}
				after = id
				cursor.Position = []byte(id)
				doc, _, accepted, err := validateSource(stored, database, q, id, stats)
				if err != nil {
					return page, err
				}
				if accepted {
					if err := appendPageDocument(&page, doc, &pageBytes); err != nil {
						return page, err
					}
				}
			}
			if batch.Exhausted {
				exhausted = true
				break
			}
			if len(batch.Documents) == 0 {
				return page, fmt.Errorf("source returned an empty nonterminal page")
			}
		}
	}
	if !exhausted {
		token, err := encodeCursor(cursor)
		if err != nil {
			return page, err
		}
		page.NextCursor = &token
	}
	if err := wire.CheckJSONPageSize(page, pageBytes); err != nil {
		return page, err
	}
	return page, nil
}

// materialize splits only explicit source byte-budget failures. Each retry keeps
// the same unconsumed candidate positions and decreases the batch count, so at
// most seven halvings are possible for a batch of 128 paths.
func (e *Engine) materialize(ctx context.Context, database string, paths []string, stats *queryStats, consume func(int, *types.StoredDoc) error) error {
	batchLimit := len(paths)
	for offset := 0; offset < len(paths); {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(offset+batchLimit, len(paths))
		stats.sourceReads++
		docs, err := e.storage.GetMany(ctx, database, paths[offset:end], types.ReadOptions{Consistency: types.ReadAuthoritative, ShowDeleted: true, MaxBytes: maxMaterializationBytes})
		if errors.Is(err, types.ErrReadBudget) && end-offset > 1 {
			batchLimit = (end - offset) / 2
			continue
		}
		if err != nil {
			return err
		}
		if len(docs) != end-offset {
			return fmt.Errorf("source batch violated positional contract")
		}
		for i, doc := range docs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := consume(offset+i, doc); err != nil {
				return err
			}
		}
		offset = end
	}
	return nil
}
