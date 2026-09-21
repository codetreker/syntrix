package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/storage"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
	querycore "github.com/syntrixbase/syntrix/internal/query/core"
	"github.com/syntrixbase/syntrix/internal/query/wire"
	"github.com/syntrixbase/syntrix/pkg/model"
)

func (c *replicaClient) handle(message BaseMessage, frameBytes int) {
	if message.Type == TypeAuth {
		c.startAuth(message, frameBytes)
		return
	}
	c.mu.Lock()
	gen := c.authGen
	authenticated := c.authCurrentLocked(gen)
	c.mu.Unlock()
	if !authenticated {
		c.sendError(message.ID, "", "", gen, "UNAUTHORIZED", 0, false)
		return
	}
	switch message.Type {
	case TypeSubscribe:
		c.startSubscribe(message, gen)
	case TypeReplicaRead:
		c.startRead(message, frameBytes, gen)
	case TypeReplicaAck:
		c.acknowledge(message, gen)
	case TypeUnsubscribe:
		c.unsubscribe(message, gen)
	}
}

func (c *replicaClient) startAuth(message BaseMessage, frameBytes int) {
	var payload replicaAuthPayload
	if frameBytes > maxMessageSize || decodeReplicaObject(message.Payload, &payload) != nil || payload.Mode != ReplicaDataMode ||
		!replicaText(payload.Database, 1024) || payload.Token == "" {
		c.mu.Lock()
		gen := c.authGen
		c.mu.Unlock()
		c.sendError(message.ID, "", "", gen, "BAD_REQUEST", 0, false)
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.authenticating {
		gen := c.authGen
		c.mu.Unlock()
		c.sendError(message.ID, "", "", gen, "REPLICATION_SOURCE_BUSY", 1, false)
		return
	}
	releasePending, ok := c.server.replicaBudget.tryPending()
	if !ok {
		gen := c.authGen
		c.mu.Unlock()
		c.sendError(message.ID, "", "", gen, "REPLICATION_SOURCE_BUSY", 1, false)
		return
	}
	c.authGen++
	gen := c.authGen
	c.authenticated = false
	c.authenticating = true
	if c.authTimer != nil {
		c.authTimer.Stop()
	}
	c.authTimer = time.AfterFunc(replicaAuthTimeout, func() { c.closeIf(func() bool { return c.authGen == gen && !c.authenticated }) })
	if c.expiryTimer != nil {
		c.expiryTimer.Stop()
		c.expiryTimer = nil
	}
	releases := make([]func(), 0, len(c.subs))
	for _, sub := range c.subs {
		if release := c.retireSubLocked(sub); release != nil {
			releases = append(releases, release)
		}
	}
	ctx, cancel := context.WithTimeout(c.ctx, replicaAuthTimeout)
	c.authCancel = cancel
	c.wg.Add(1)
	c.mu.Unlock()
	for _, release := range releases {
		release()
	}
	go func() {
		defer c.wg.Done()
		defer releasePending()
		defer cancel()
		defer func() {
			c.mu.Lock()
			if c.authGen == gen {
				c.authenticating = false
			}
			c.mu.Unlock()
		}()
		release, err := c.server.replicaBudget.acquireAuth(ctx)
		if err != nil {
			c.failure(message.ID, nil, "", gen, err)
			return
		}
		defer release()
		claims, err := c.server.auth.ValidateToken(payload.Token)
		if err != nil || claims == nil || claims.Disabled || claims.Subject == "" || (claims.ExpiresAt != nil && !time.Now().Before(claims.ExpiresAt.Time)) {
			c.sendError(message.ID, "", "", gen, "UNAUTHORIZED", 0, false)
			return
		}
		principal := replication.Principal{Subject: claims.Subject, DBAdmin: append([]string(nil), claims.DBAdmin...)}
		if _, err = replication.AuthorizeDatabase(ctx, c.server.database, payload.Database, principal, nil, true); err != nil {
			c.failure(message.ID, nil, "", gen, err)
			return
		}
		c.mu.Lock()
		if c.closed || c.authGen != gen || ctx.Err() != nil {
			c.mu.Unlock()
			return
		}
		c.database = payload.Database
		c.principal = principal
		c.authenticated = true
		c.authenticating = false
		c.expires = time.Time{}
		if c.authTimer != nil {
			c.authTimer.Stop()
		}
		if claims.ExpiresAt != nil {
			c.expires = claims.ExpiresAt.Time
			expires := c.expires
			c.expiryTimer = time.AfterFunc(time.Until(expires), func() { c.closeIf(func() bool { return c.authGen == gen && c.expires.Equal(expires) }) })
		}
		c.mu.Unlock()
		data, _ := encodeReplicaFrame(message.ID, TypeAuthAck, struct {
			Mode string `json:"mode"`
		}{ReplicaDataMode})
		c.sendControl(&replicaOutbound{data: data, gen: gen, authenticated: true})
	}()
}

func (c *replicaClient) startSubscribe(message BaseMessage, gen uint64) {
	var payload replicaSubscribePayload
	if len(message.Payload) > querycore.MaxPullRequestBytes || decodeReplicaObject(message.Payload, &payload) != nil {
		c.sendError(message.ID, message.ID, "", gen, "INVALID_REPLICATION_SOURCE", 0, false)
		return
	}
	request, err := decodeReplicaSource(payload, message.ID)
	if err != nil {
		failure := replication.ClassifyPullError(request, err)
		if failure.Code == "INTERNAL_ERROR" {
			failure.Code = "INVALID_REPLICATION_SOURCE"
		}
		c.failure(message.ID, nil, "", gen, &failure)
		return
	}
	c.mu.Lock()
	if !c.authCurrentLocked(gen) {
		c.mu.Unlock()
		return
	}
	if _, seen := c.seen[message.ID]; seen {
		c.mu.Unlock()
		c.sendError(message.ID, message.ID, "", gen, "REPLICATION_PROTOCOL_ERROR", 0, false)
		return
	}
	if len(c.seen) >= c.server.replicaBudget.cfg.Subscriptions {
		c.mu.Unlock()
		c.sendError(message.ID, message.ID, "", gen, "REPLICATION_TRANSPORT_BUSY", 0, true)
		return
	}
	c.seen[message.ID] = struct{}{}
	sourceBytes := int64(len(message.Payload))
	if c.sourceCount >= c.server.replicaBudget.cfg.SubscriptionsPerConnection || sourceBytes > c.server.replicaBudget.cfg.SourceBytesPerConnection-c.sourceBytes {
		c.mu.Unlock()
		c.sendError(message.ID, message.ID, "", gen, "REPLICATION_TRANSPORT_BUSY", 0, false)
		return
	}
	releaseSource, ok := c.server.replicaBudget.trySubscription(sourceBytes)
	if !ok {
		c.mu.Unlock()
		c.sendError(message.ID, message.ID, "", gen, "REPLICATION_TRANSPORT_BUSY", 0, false)
		return
	}
	releasePending, ok := c.server.replicaBudget.tryPending()
	if !ok {
		releaseSource()
		c.mu.Unlock()
		c.sendError(message.ID, message.ID, "", gen, "REPLICATION_SOURCE_BUSY", 1, false)
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	sub := &replicaSubscription{id: message.ID, gen: gen, ctx: ctx, cancel: cancel, collection: payload.Collection, registering: true,
		sourceBytes: sourceBytes, releaseSource: releaseSource, window: request.Source.Limit != nil}
	c.subs[sub.id] = sub
	c.sourceCount++
	c.sourceBytes += sourceBytes
	sub.registrationTimer = time.AfterFunc(replicaAuthTimeout, func() {
		c.mu.Lock()
		pending := !sub.registered && !sub.retired
		c.mu.Unlock()
		if pending {
			c.invalidateSub(sub, "REPLICATION_TRANSPORT_UNAVAILABLE")
		}
	})
	namespace, principal := c.database, c.principal
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer releasePending()
		defer func() { c.mu.Lock(); sub.registering = false; c.finishSourceLocked(sub); c.mu.Unlock() }()
		work, cancel := context.WithTimeout(ctx, replicaAuthTimeout)
		defer cancel()
		releaseAuth, err := c.server.replicaBudget.acquireAuth(work)
		if err != nil {
			c.subscriptionFailure(sub, err)
			return
		}
		db, err := replication.AuthorizeDatabase(work, c.server.database, namespace, principal, payload.ExpectedDatabaseIdentity, true)
		releaseAuth()
		if err != nil {
			c.subscriptionFailure(sub, err)
			return
		}
		request.DatabaseIdentity = db.ID
		if err = querycore.ValidatePullRequest(namespace, request); err != nil {
			failure := replication.ClassifyPullError(request, err)
			c.subscriptionFailure(sub, &failure)
			return
		}
		hash, err := querycore.ReplicationSourceHash(request)
		if err != nil {
			failure := replication.ClassifyPullError(request, err)
			c.subscriptionFailure(sub, &failure)
			return
		}
		c.mu.Lock()
		if !c.subCurrentLocked(sub) || work.Err() != nil {
			c.mu.Unlock()
			return
		}
		sub.identity = db.ID
		sub.hash = hash
		c.mu.Unlock()
		releaseBackend, err := c.server.hub.SubscribeReplica(sub.ctx, namespace, payload.Collection, func() { c.changed(sub) }, func(error) { c.invalidateSub(sub, "REPLICATION_TRANSPORT_UNAVAILABLE") })
		if err != nil {
			c.subscriptionFailure(sub, err)
			return
		}
		c.mu.Lock()
		if !c.subCurrentLocked(sub) || work.Err() != nil {
			c.mu.Unlock()
			releaseBackend()
			return
		}
		sub.registered = true
		sub.releaseBackend = releaseBackend
		sub.registrationTimer.Stop()
		c.mu.Unlock()
		data, _ := encodeReplicaFrame(sub.id, TypeSubscribeAck, struct {
			SubID            string `json:"subId"`
			DatabaseIdentity string `json:"databaseIdentity"`
		}{sub.id, sub.identity})
		c.sendControl(&replicaOutbound{data: data, gen: gen, sub: sub, authenticated: true})
		slog.Debug("Replica source registered", "connection", c.id, "subId", sub.id, "databaseIdentity", sub.identity)
	}()
}

func (c *replicaClient) subscriptionFailure(sub *replicaSubscription, err error) {
	c.mu.Lock()
	current := c.subCurrentLocked(sub)
	release := c.retireSubLocked(sub)
	c.mu.Unlock()
	if release != nil {
		release()
	}
	if current {
		c.failure(sub.id, sub, "", sub.gen, err)
	}
}

func (c *replicaClient) unsubscribe(message BaseMessage, gen uint64) {
	var payload replicaUnsubscribePayload
	if len(message.Payload) > replicaEnvelopeBytes || decodeReplicaObject(message.Payload, &payload) != nil || payload.SubID != message.ID || !replicaText(payload.SubID, replicaIDBytes) {
		c.sendError(message.ID, "", "", gen, "REPLICATION_PROTOCOL_ERROR", 0, false)
		return
	}
	c.mu.Lock()
	sub := c.subs[payload.SubID]
	release := c.retireSubLocked(sub)
	c.mu.Unlock()
	if release != nil {
		release()
	}
	data, _ := encodeReplicaFrame(message.ID, TypeUnsubscribeAck, payload)
	c.sendControl(&replicaOutbound{data: data, gen: gen, authenticated: true})
}

func (c *replicaClient) acknowledge(message BaseMessage, gen uint64) {
	var payload replicaAckPayload
	if len(message.Payload) > replicaEnvelopeBytes || decodeReplicaObject(message.Payload, &payload) != nil || payload.RequestID != message.ID || !replicaText(payload.SubID, replicaIDBytes) {
		c.sendError(message.ID, "", "", gen, "REPLICATION_PROTOCOL_ERROR", 0, false)
		return
	}
	c.mu.Lock()
	sub := c.subs[payload.SubID]
	if !c.subCurrentLocked(sub) || !sub.registered {
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "REPLICATION_PROTOCOL_ERROR", 0, false)
		return
	}
	if sub.lastACK == payload.RequestID {
		c.mu.Unlock()
		return
	}
	page := sub.page
	if page == nil || page.id != payload.RequestID || !page.sent {
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "REPLICATION_PROTOCOL_ERROR", 0, false)
		return
	}
	page.acked = true
	if page.ackTimer != nil {
		page.ackTimer.Stop()
	}
	page.cancel()
	sub.lastACK = page.id
	sub.page = nil
	c.releasePageLocked(page)
	c.mu.Unlock()
	slog.Debug("Replica page acknowledged", "connection", c.id, "subId", payload.SubID, "requestId", payload.RequestID)
}

func (c *replicaClient) startRead(message BaseMessage, frameBytes int, gen uint64) {
	payload, request, err := decodeReplicaRead(message, frameBytes)
	if err != nil {
		failure := replication.ClassifyPullError(request, err)
		if failure.Code == "INTERNAL_ERROR" {
			failure.Code = "REPLICATION_PROTOCOL_ERROR"
		}
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, failure.Code, 0, false)
		return
	}
	c.mu.Lock()
	sub := c.subs[payload.SubID]
	if !c.subCurrentLocked(sub) || !sub.registered || sub.page != nil || sub.lastACK == payload.RequestID {
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "REPLICATION_PROTOCOL_ERROR", 0, false)
		return
	}
	if payload.ExpectedDatabaseIdentity != nil && *payload.ExpectedDatabaseIdentity != sub.identity {
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "DATABASE_IDENTITY_MISMATCH", 0, false)
		return
	}
	request.DatabaseIdentity = sub.identity
	hash, err := querycore.ReplicationSourceHash(request)
	if err != nil || request.Collection != sub.collection || hash != sub.hash || (payload.ExpectedSourceHash != nil && *payload.ExpectedSourceHash != sub.hash) {
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "INVALID_REPLICATION_SOURCE", 0, false)
		return
	}
	if c.pages >= min(c.server.replicaBudget.cfg.PageCreditsPerConnection, 4) {
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "REPLICATION_TRANSPORT_BUSY", 0, false)
		return
	}
	releaseRead, ok := c.server.replicaBudget.tryRead()
	if !ok {
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "REPLICATION_SOURCE_BUSY", 1, false)
		return
	}
	releaseBytes, ok := c.server.replicaBudget.tryPage(replicaDataFrameBytes)
	if !ok {
		releaseRead()
		c.mu.Unlock()
		c.sendError(message.ID, payload.SubID, payload.RequestID, gen, "REPLICATION_TRANSPORT_BUSY", 0, false)
		return
	}
	ctx, cancel := context.WithTimeout(sub.ctx, replicaReadTimeout)
	page := &replicaPendingPage{sub: sub, id: payload.RequestID, ctx: ctx, cancel: cancel, releaseRead: releaseRead, releaseBytes: releaseBytes}
	sub.page = page
	c.pages++
	namespace, principal := c.database, c.principal
	c.wg.Add(1)
	c.mu.Unlock()
	slog.Debug("Replica read admitted", "connection", c.id, "subId", sub.id, "requestId", page.id, "databaseIdentity", sub.identity)
	go c.readPage(page, namespace, principal, request)
}

func (c *replicaClient) readPage(page *replicaPendingPage, namespace string, principal replication.Principal, request storage.ReplicationPullRequest) {
	defer c.wg.Done()
	defer func() {
		page.cancel()
		c.mu.Lock()
		page.workDone = true
		page.releaseRead()
		page.releaseRead = nil
		if !page.bufferHeld && !page.sent && !page.acked {
			page.retired = true
			if page.sub.page == page {
				page.sub.page = nil
			}
		}
		c.releasePageLocked(page)
		c.mu.Unlock()
	}()
	ctx := page.ctx
	sub := page.sub
	releaseAuth, err := c.server.replicaBudget.acquireAuth(ctx)
	if err != nil {
		c.pageFailure(page, err)
		return
	}
	_, err = replication.AuthorizeDatabase(ctx, c.server.database, namespace, principal, &sub.identity, true)
	releaseAuth()
	if err == nil {
		err = querycore.ValidatePullRequest(namespace, request)
	}
	if err != nil {
		failure := replication.ClassifyPullError(request, err)
		c.pageFailure(page, &failure)
		return
	}
	c.mu.Lock()
	current := c.subCurrentLocked(sub) && sub.page == page && !page.retired
	c.mu.Unlock()
	if !current {
		return
	}
	if ctx.Err() != nil {
		failure := replication.ClassifyPullError(request, ctx.Err())
		c.pageFailure(page, &failure)
		return
	}
	response, err := c.server.queryService.Pull(ctx, namespace, request)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = querycore.ValidatePullResponseScope(request, response)
	}
	if err != nil {
		failure := replication.ClassifyPullError(request, err)
		c.pageFailure(page, &failure)
		return
	}
	c.mu.Lock()
	current = c.subCurrentLocked(sub) && sub.page == page && !page.retired
	c.mu.Unlock()
	if !current {
		return
	}
	raw, err := wire.EncodeJSONPullPage(response)
	if err != nil {
		failure := replication.ClassifyPullError(request, err)
		c.pageFailure(page, &failure)
		return
	}
	data, err := encodeReplicaFrame(page.id, TypeReplicaPage, struct {
		SubID     string          `json:"subId"`
		RequestID string          `json:"requestId"`
		Page      json.RawMessage `json:"page"`
	}{sub.id, page.id, raw})
	if err != nil || len(data) > replicaDataFrameBytes || len(data)-len(raw) > replicaEnvelopeBytes {
		failure := replication.ClassifyPullError(request, model.ErrQueryWorkLimit)
		c.pageFailure(page, &failure)
		return
	}
	c.mu.Lock()
	if !c.subCurrentLocked(sub) || sub.page != page || page.retired {
		c.mu.Unlock()
		return
	}
	if ctx.Err() != nil {
		c.mu.Unlock()
		failure := replication.ClassifyPullError(request, ctx.Err())
		c.pageFailure(page, &failure)
		return
	}
	page.bufferHeld = true
	select {
	case c.data <- &replicaOutbound{data: data, gen: sub.gen, sub: sub, page: page, authenticated: true}:
		c.mu.Unlock()
	default:
		page.bufferHeld = false
		c.mu.Unlock()
		c.close()
	}
}

func (c *replicaClient) pageFailure(page *replicaPendingPage, err error) {
	c.mu.Lock()
	sub := page.sub
	current := c.subCurrentLocked(sub) && sub.page == page
	page.retired = true
	page.cancel()
	if sub.page == page {
		sub.page = nil
	}
	c.releasePageLocked(page)
	c.mu.Unlock()
	if current {
		c.failure(page.id, sub, page.id, sub.gen, err)
	}
}

func (c *replicaClient) changed(sub *replicaSubscription) {
	c.mu.Lock()
	if c.subCurrentLocked(sub) {
		sub.dirty = true
	}
	c.mu.Unlock()
}

func (c *replicaClient) flushPump() {
	defer c.wg.Done()
	ticker := time.NewTicker(replicaChangedInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			for _, sub := range c.subs {
				if !c.subCurrentLocked(sub) || !sub.registered || !sub.dirty || sub.flushing || sub.changedQueued {
					continue
				}
				release, ok := c.server.replicaBudget.tryPending()
				if !ok {
					continue
				}
				sub.flushing = true
				sub.dirty = false
				namespace, principal := c.database, c.principal
				c.wg.Add(1)
				go c.flushChanged(sub, namespace, principal, release)
			}
			c.mu.Unlock()
		}
	}
}
func (c *replicaClient) flushChanged(sub *replicaSubscription, namespace string, principal replication.Principal, releasePending func()) {
	defer c.wg.Done()
	defer releasePending()
	defer func() { c.mu.Lock(); sub.flushing = false; c.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(sub.ctx, replicaAuthTimeout)
	defer cancel()
	releaseAuth, err := c.server.replicaBudget.acquireAuth(ctx)
	if err == nil {
		_, err = replication.AuthorizeDatabase(ctx, c.server.database, namespace, principal, &sub.identity, true)
		releaseAuth()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		var busy *replicaSourceBusyError
		if errors.As(err, &busy) {
			c.mu.Lock()
			if c.subCurrentLocked(sub) {
				sub.dirty = true
			}
			c.mu.Unlock()
			c.failure(sub.id, sub, "", sub.gen, err)
			return
		}
		c.subscriptionFailure(sub, err)
		return
	}
	c.mu.Lock()
	if !c.subCurrentLocked(sub) {
		c.mu.Unlock()
		return
	}
	sub.changedQueued = true
	c.mu.Unlock()
	data, _ := encodeReplicaFrame(sub.id, TypeReplicaChanged, replicaUnsubscribePayload{SubID: sub.id})
	c.sendControl(&replicaOutbound{data: data, gen: sub.gen, sub: sub, authenticated: true, changed: true})
}
