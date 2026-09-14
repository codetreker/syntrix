package mongo

import (
	"context"
	"errors"

	"github.com/syntrixbase/syntrix/internal/core/storage/types"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func (m *documentStore) ReadBootstrapPage(ctx context.Context, database, collectionName string, after types.ReplicationPosition, budget types.ReplicationBudget) (page types.ReplicationPage, err error) {
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
	cp, collection, err := m.resumeReplication(ctx, database, collectionName, after, types.ReplicationScan)
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
	page.End = after
	page.Usage.PageBytes = int64(len(after.Opaque) + 128)
	if page.Usage.PageBytes > budget.MaxPageBytes {
		return page, replicationFailure(database, collectionName, types.ReplicationBudgetExceeded, errors.New("cursor exceeds internal page budget"))
	}
	// Probe responses can observe a newer, not-yet-committed operationTime. Keep
	// that session separate from the scan's fixed committed lower bound.
	page.Usage.ProbeBytes, err = m.probeReplicationHistory(ctx, collection, cp, budget.MaxProbeBytes)
	if err != nil {
		return page, err
	}
	filter := bson.D{{Key: "database", Value: database}, {Key: "collection", Value: collectionName}}
	if cp.AfterID != "" {
		filter = append(filter, bson.E{Key: "fullpath", Value: bson.M{"$gt": collectionName + "/" + cp.AfterID}})
	}
	cursor, err := collection.Find(sctx, filter, options.Find().SetSort(bson.D{{Key: "fullpath", Value: 1}}).SetHint(sourceScanIndexName).SetCollation(&options.Collation{Locale: "simple"}).SetLimit(int64(budget.Limit)).SetBatchSize(1).SetAllowDiskUse(false))
	if err != nil {
		return page, err
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), watchCleanupTimeout)
		defer closeCancel()
		err = errors.Join(err, cursor.Close(closeCtx))
	}()
	for {
		if replicationSoftStopped(budget) {
			if len(page.Frames) == 0 {
				return page, context.DeadlineExceeded
			}
			page.EndReason = types.ReplicationEndSoftDeadline
			return page, nil
		}
		if page.Usage.FramesExamined >= budget.MaxFrames {
			page.EndReason = types.ReplicationEndFrames
			return page, nil
		}
		if !cursor.Next(sctx) {
			if err = cursor.Err(); err != nil {
				return page, err
			}
			cp.Phase = types.ReplicationChanges
			cp.AfterID = ""
			page.End, err = cp.position()
			page.EndReason = types.ReplicationEndScan
			return page, err
		}
		page.Usage.FramesExamined++
		rawBytes := int64(len(cursor.Current))
		if rawBytes > budget.MaxSourceBytes-page.Usage.SourceBytes {
			if len(page.Frames) == 0 {
				return page, replicationFailure(database, collectionName, types.ReplicationBudgetExceeded, errors.New("scan document exceeds source budget"))
			}
			page.EndReason = types.ReplicationEndBytes
			return page, nil
		}
		var doc types.StoredDoc
		if err = cursor.Decode(&doc); err != nil {
			return page, err
		}
		state, stateErr := replicationState(database, collectionName, &doc)
		if stateErr != nil {
			return page, stateErr
		}
		if state.ID <= cp.AfterID {
			return page, replicationFailure(database, collectionName, types.ReplicationInvalidState, errors.New("source scan is not strictly ordered"))
		}
		cp.AfterID = state.ID
		position, posErr := cp.position()
		if posErr != nil {
			return page, posErr
		}
		accepted, appendErr := appendReplicationFrame(&page, types.ReplicationFrame{State: state, After: position}, rawBytes, budget)
		if appendErr != nil {
			return page, appendErr
		}
		if !accepted {
			return page, nil
		}
		if len(page.Frames) >= budget.Limit {
			page.EndReason = types.ReplicationEndCount
			return page, nil
		}
	}
}
