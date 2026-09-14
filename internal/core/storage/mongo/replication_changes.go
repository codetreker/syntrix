package mongo

import (
	"context"
	"errors"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const replicationMaterializationBatch = 128

type replicationMutation struct {
	token       bson.Raw
	clusterTime primitive.Timestamp
	identity    *types.StoredDoc
	sourceBytes int64
}

func parseReplicationMutation(raw bson.Raw, cp replicationCheckpoint) (replicationMutation, error) {
	var event struct {
		ID        bson.Raw            `bson:"_id"`
		Operation string              `bson:"operationType"`
		Time      primitive.Timestamp `bson:"clusterTime"`
		Key       struct {
			ID string `bson:"_id"`
		} `bson:"documentKey"`
		Full   bson.Raw `bson:"fullDocument"`
		Before bson.Raw `bson:"fullDocumentBeforeChange"`
	}
	if err := bson.Unmarshal(raw, &event); err != nil {
		return replicationMutation{}, err
	}
	mutation := replicationMutation{token: event.ID, clusterTime: event.Time, sourceBytes: int64(len(raw))}
	if err := validateWatchToken(event.ID); err != nil {
		return mutation, err
	}
	// Physical cleanup has no logical deletion semantics and needs no preimage.
	if event.Operation == "delete" {
		return mutation, nil
	}
	switch event.Operation {
	case "drop", "rename", "dropDatabase", "invalidate":
		return mutation, replicationFailure(cp.Database, cp.Collection, types.ReplicationSourceMismatch, errors.New("source lifecycle event"))
	case "insert", "update", "replace":
	default:
		return mutation, replicationFailure(cp.Database, cp.Collection, types.ReplicationInvalidState, errors.New("unknown source operation"))
	}
	image := event.Full
	if len(image) == 0 {
		image = event.Before
	}
	if len(image) == 0 {
		return mutation, replicationFailure(cp.Database, cp.Collection, types.ReplicationIdentityUnavailable, errors.New("source mutation has no recoverable logical identity"))
	}
	// Decode only metadata here. Business data is fetched once per bounded batch
	// from a majority read causally covering every triggering source position.
	var identity struct {
		ID         string `bson:"_id"`
		Database   string `bson:"database"`
		Collection string `bson:"collection"`
		Fullpath   string `bson:"fullpath"`
	}
	if err := bson.Unmarshal(image, &identity); err != nil {
		return mutation, err
	}
	doc := &types.StoredDoc{Id: identity.ID, Database: identity.Database, Collection: identity.Collection, Fullpath: identity.Fullpath}
	if _, err := types.LogicalDocumentID(doc); err != nil {
		return mutation, replicationFailure(cp.Database, cp.Collection, types.ReplicationIdentityUnavailable, err)
	}
	if doc.Id != event.Key.ID || doc.Id != types.CalculateDatabase(doc.Database, doc.Fullpath) {
		return mutation, replicationFailure(cp.Database, cp.Collection, types.ReplicationIdentityUnavailable, errors.New("source identity differs from document key"))
	}
	if doc.Database == cp.Database && doc.Collection == cp.Collection {
		mutation.identity = doc
	}
	return mutation, nil
}

func (m *documentStore) materializeReplication(ctx mongo.SessionContext, collection *mongo.Collection, cp replicationCheckpoint, mutations []replicationMutation, maxBytes int64) (states map[string]*types.ReplicationState, size int64, err error) {
	ids := make([]string, 0, len(mutations))
	seen := make(map[string]bool, len(mutations))
	for _, mutation := range mutations {
		if mutation.identity != nil && !seen[mutation.identity.Id] {
			seen[mutation.identity.Id] = true
			ids = append(ids, mutation.identity.Id)
		}
		if timestampBefore(cp.OperationTime, mutation.clusterTime) {
			cp.OperationTime = mutation.clusterTime
		}
	}
	states = make(map[string]*types.ReplicationState, len(ids))
	if len(ids) == 0 {
		return states, 0, nil
	}
	if err = ctx.AdvanceOperationTime(&cp.OperationTime); err != nil {
		return nil, 0, err
	}
	cursor, err := collection.Find(ctx, bson.D{{Key: "_id", Value: bson.M{"$in": ids}}, {Key: "database", Value: cp.Database}, {Key: "collection", Value: cp.Collection}}, options.Find().SetBatchSize(1))
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), watchCleanupTimeout)
		defer cancel()
		err = errors.Join(err, cursor.Close(closeCtx))
	}()
	for cursor.Next(ctx) {
		rowBytes := int64(len(cursor.Current))
		if rowBytes > maxBytes-size {
			return nil, size, replicationFailure(cp.Database, cp.Collection, types.ReplicationBudgetExceeded, errors.New("materialization exceeds source budget"))
		}
		size += rowBytes
		var doc types.StoredDoc
		if err = cursor.Decode(&doc); err != nil {
			return nil, size, err
		}
		state, stateErr := replicationState(cp.Database, cp.Collection, &doc)
		if stateErr != nil {
			return nil, size, stateErr
		}
		states[doc.Id] = state
	}
	if err = cursor.Err(); err != nil {
		return nil, size, err
	}
	for _, mutation := range mutations {
		identity := mutation.identity
		if identity == nil || states[identity.Id] != nil {
			continue
		}
		id, idErr := types.LogicalDocumentID(identity)
		if idErr != nil {
			return nil, size, idErr
		}
		states[identity.Id] = &types.ReplicationState{ID: id, Collection: cp.Collection, Deleted: true}
	}
	return states, size, nil
}

func (m *documentStore) ReadChangesPage(ctx context.Context, database, collectionName string, after types.ReplicationPosition, budget types.ReplicationBudget) (page types.ReplicationPage, err error) {
	ctx, cancel, budget, err := replicationRequest(ctx, database, collectionName, budget)
	if err != nil {
		return page, err
	}
	defer cancel()
	defer func() {
		err = replicationClassify(database, collectionName, err)
		if err != nil {
			page = types.ReplicationPage{}
		}
	}()
	cp, collection, err := m.resumeReplication(ctx, database, collectionName, after, types.ReplicationChanges)
	if err != nil {
		return page, err
	}
	defer func() {
		if err == nil {
			err = m.checkReplicationSource(ctx, collection, cp)
		}
	}()
	sctx, session, err := m.replicationSession(ctx, &cp)
	if err != nil {
		return page, err
	}
	defer session.EndSession(ctx)
	stream, err := m.openReplicationStream(sctx, collection, cp)
	if err != nil {
		return page, err
	}
	defer func() { err = errors.Join(err, closeReplicationStream(stream)) }()
	page.End = after
	page.Usage.PageBytes = int64(len(after.Opaque) + 128)
	if page.Usage.PageBytes > budget.MaxPageBytes {
		return page, replicationFailure(database, collectionName, types.ReplicationBudgetExceeded, errors.New("cursor exceeds internal page budget"))
	}
	documents := 0
	for {
		mutations := make([]replicationMutation, 0, replicationMaterializationBatch)
		batchBytes := int64(0)
		batchAllowance := budget.MaxSourceBytes - page.Usage.SourceBytes
		batchLimit := replicationMaterializationBatch
		// Establish a deliverable first state before speculating on a batch whose
		// current images may have grown since native enrichment. A rejected later
		// batch resumes from the completed prefix on the next request.
		if documents == 0 {
			batchLimit = 1
		}
		watermark := false
		var watermarkToken bson.Raw
		for len(mutations) < batchLimit && documents+len(mutations) < budget.Limit && page.Usage.FramesExamined < budget.MaxFrames {
			if replicationSoftStopped(budget) {
				break
			}
			if !stream.TryNext(sctx) {
				if err = stream.Err(); err != nil {
					return page, replicationStreamClassify(database, collectionName, err)
				}
				if stream.ID() == 0 {
					return page, errors.New("replication source cursor exhausted")
				}
				if err = ctx.Err(); err != nil {
					return page, err
				}
				watermark = true
				watermarkToken = append(bson.Raw(nil), stream.ResumeToken()...)
				break
			}
			page.Usage.FramesExamined++
			var raw bson.Raw
			if err = stream.Decode(&raw); err != nil {
				return page, err
			}
			if int64(len(raw)) > budget.MaxSourceBytes-page.Usage.SourceBytes {
				if len(mutations) == 0 && len(page.Frames) == 0 {
					return page, replicationFailure(database, collectionName, types.ReplicationBudgetExceeded, errors.New("source mutation exceeds page budget"))
				}
				page.EndReason = types.ReplicationEndBytes
				break
			}
			mutation, parseErr := parseReplicationMutation(raw, cp)
			if parseErr != nil {
				return page, parseErr
			}
			batchBytes += mutation.sourceBytes
			page.Usage.SourceBytes += mutation.sourceBytes
			mutations = append(mutations, mutation)
			// Reserve half the remaining work for current-state reads. This bounds
			// normal materialization without multiplying large enrichment payloads.
			if batchBytes >= batchAllowance/2 {
				break
			}
		}
		for _, mutation := range mutations {
			if timestampBefore(cp.OperationTime, mutation.clusterTime) {
				cp.OperationTime = mutation.clusterTime
			}
		}
		cp.ClusterTime = append(bson.Raw(nil), session.ClusterTime()...)
		readContext, readSession, sessionErr := m.replicationSession(ctx, &cp)
		if sessionErr != nil {
			return page, sessionErr
		}
		states, materializedBytes, readErr := m.materializeReplication(readContext, collection, cp, mutations, budget.MaxSourceBytes-page.Usage.SourceBytes)
		readSession.EndSession(ctx)
		// The allowance counts admitted source payload, including speculative work
		// discarded from the response. A rejected raw BSON row is never decoded;
		// its single-document driver buffer has the separate native wire bound.
		page.Usage.SourceBytes += materializedBytes
		if readErr != nil {
			var budgetErr *types.ReplicationError
			if len(page.Frames) > 0 && errors.As(readErr, &budgetErr) && budgetErr.Code == types.ReplicationBudgetExceeded {
				page.EndReason = types.ReplicationEndBytes
				return page, nil
			}
			return page, readErr
		}
		for _, mutation := range mutations {
			cp.Token = mutation.token
			position, posErr := cp.position()
			if posErr != nil {
				return page, posErr
			}
			frame := types.ReplicationFrame{After: position}
			if mutation.identity != nil {
				frame.State = states[mutation.identity.Id]
			}
			accepted, appendErr := appendReplicationFrame(&page, frame, 0, budget)
			if appendErr != nil {
				return page, appendErr
			}
			if !accepted {
				return page, nil
			}
			if frame.State != nil {
				documents++
			}
		}
		if watermark {
			cp.Token = watermarkToken
			page.End, err = cp.position()
			if err != nil {
				return page, err
			}
			page.CaughtUp = true
			page.EndReason = types.ReplicationEndWatermark
			return page, nil
		}
		if page.EndReason == types.ReplicationEndBytes {
			return page, nil
		}
		if documents >= budget.Limit {
			page.EndReason = types.ReplicationEndCount
			return page, nil
		}
		if page.Usage.FramesExamined >= budget.MaxFrames {
			page.EndReason = types.ReplicationEndFrames
			return page, nil
		}
		if replicationSoftStopped(budget) {
			if len(page.Frames) == 0 {
				return page, context.DeadlineExceeded
			}
			page.EndReason = types.ReplicationEndSoftDeadline
			return page, nil
		}
	}
}
