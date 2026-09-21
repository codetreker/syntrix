package realtime

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/syntrixbase/syntrix/internal/gateway/replication"
	"github.com/syntrixbase/syntrix/internal/query/wire"
)

const (
	replicaAuthTimeout     = 10 * time.Second
	replicaReadTimeout     = 30 * time.Second
	replicaACKTimeout      = 60 * time.Second
	replicaChangedInterval = 200 * time.Millisecond
	replicaDataFrameBytes  = wire.MaxPageBytes + replicaEnvelopeBytes
)

type replicaSubscription struct {
	id                               string
	gen                              uint64
	ctx                              context.Context
	cancel                           context.CancelFunc
	collection, identity, hash       string
	registered, registering, retired bool
	dirty, flushing, changedQueued   bool
	window                           bool
	sourceBytes                      int64
	releaseSource, releaseBackend    func()
	registrationTimer                *time.Timer
	page                             *replicaPendingPage
	lastACK                          string
}
type replicaPendingPage struct {
	sub                                                        *replicaSubscription
	id                                                         string
	ctx                                                        context.Context
	cancel                                                     context.CancelFunc
	releaseRead, releaseBytes                                  func()
	workDone, bufferHeld, sent, acked, retired, creditReleased bool
	ackTimer                                                   *time.Timer
}
type replicaOutbound struct {
	data                               []byte
	gen                                uint64
	sub                                *replicaSubscription
	page                               *replicaPendingPage
	authenticated, changed, closeAfter bool
}
type replicaClient struct {
	id                                    string
	server                                *Server
	conn                                  *websocket.Conn
	ctx                                   context.Context
	cancel                                context.CancelFunc
	mu                                    sync.Mutex
	wg                                    sync.WaitGroup
	closed, authenticated, authenticating bool
	authGen                               uint64
	authCancel                            context.CancelFunc
	principal                             replication.Principal
	database                              string
	expires                               time.Time
	expiryTimer, authTimer                *time.Timer
	subs                                  map[string]*replicaSubscription
	seen                                  map[string]struct{}
	sourceCount, pages                    int
	sourceBytes                           int64
	control, data                         chan *replicaOutbound
}

func (s *Server) serveReplica(w http.ResponseWriter, r *http.Request) {
	if tokenFromQueryParam(r) != "" {
		http.Error(w, "Query token not allowed", http.StatusUnauthorized)
		return
	}
	if s.cfg.Replica.Validate() != nil {
		http.Error(w, "Invalid replica configuration", http.StatusInternalServerError)
		return
	}
	if s.replicaBudget == nil || s.database == nil {
		http.Error(w, "Replica transport unavailable", http.StatusServiceUnavailable)
		return
	}
	release, ok := s.replicaBudget.tryConnection()
	if !ok {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "Replica transport busy", http.StatusServiceUnavailable)
		return
	}
	defer release()
	u := upgrader
	u.CheckOrigin = func(request *http.Request) bool {
		return checkAllowedOrigin(request.Header.Get("Origin"), request.Host, s.cfg, hasCredentials(request)) == nil
	}
	conn, err := u.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	c := &replicaClient{id: uuid.NewString(), server: s, conn: conn, ctx: ctx, cancel: cancel, subs: make(map[string]*replicaSubscription), seen: make(map[string]struct{}), control: make(chan *replicaOutbound, 32), data: make(chan *replicaOutbound, 4)}
	releaseHub, registered := s.hub.RegisterReplicaConnection(c.close)
	if !registered {
		cancel()
		_ = conn.Close()
		return
	}
	defer releaseHub()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.authTimer = time.AfterFunc(replicaAuthTimeout, func() { c.closeIf(func() bool { return c.authGen == 0 && !c.authenticated }) })
	c.wg.Add(2)
	c.mu.Unlock()
	go c.writePump()
	go c.flushPump()
	c.readPump()
	c.close()
	c.wg.Wait()
}

func (c *replicaClient) authCurrentLocked(gen uint64) bool {
	return !c.closed && c.authenticated && c.authGen == gen && (c.expires.IsZero() || time.Now().Before(c.expires))
}
func (c *replicaClient) subCurrentLocked(sub *replicaSubscription) bool {
	return sub != nil && !sub.retired && c.subs[sub.id] == sub && c.authCurrentLocked(sub.gen) && sub.ctx.Err() == nil
}
func (c *replicaClient) releasePageLocked(page *replicaPendingPage) {
	if (page.acked || page.retired) && page.workDone && !page.bufferHeld && !page.creditReleased {
		page.creditReleased = true
		c.pages--
	}
	if (page.acked || page.retired) && page.workDone && !page.bufferHeld && page.releaseBytes != nil {
		page.releaseBytes()
		page.releaseBytes = nil
	}
}
func (c *replicaClient) finishSourceLocked(sub *replicaSubscription) {
	if sub.retired && !sub.registering && sub.releaseSource != nil {
		sub.releaseSource()
		sub.releaseSource = nil
		c.sourceCount--
		c.sourceBytes -= sub.sourceBytes
	}
}
func (c *replicaClient) retireSubLocked(sub *replicaSubscription) func() {
	if sub == nil || sub.retired {
		return nil
	}
	sub.retired = true
	sub.cancel()
	if sub.registrationTimer != nil {
		sub.registrationTimer.Stop()
	}
	if c.subs[sub.id] == sub {
		delete(c.subs, sub.id)
	}
	if page := sub.page; page != nil {
		page.retired = true
		page.cancel()
		if page.ackTimer != nil {
			page.ackTimer.Stop()
		}
		c.releasePageLocked(page)
		sub.page = nil
	}
	c.finishSourceLocked(sub)
	release := sub.releaseBackend
	sub.releaseBackend = nil
	return release
}
func (c *replicaClient) close() { c.closeIf(nil) }
func (c *replicaClient) closeIf(owns func() bool) {
	c.mu.Lock()
	if c.closed || (owns != nil && !owns()) {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.cancel()
	if c.authCancel != nil {
		c.authCancel()
	}
	if c.authTimer != nil {
		c.authTimer.Stop()
	}
	if c.expiryTimer != nil {
		c.expiryTimer.Stop()
	}
	releases := make([]func(), 0, len(c.subs))
	for _, sub := range c.subs {
		if release := c.retireSubLocked(sub); release != nil {
			releases = append(releases, release)
		}
	}
	c.mu.Unlock()
	// Closing the socket interrupts a blocked write as well as the reader.
	_ = c.conn.Close()
	slog.Debug("Replica transport retired", "connection", c.id)
	for _, release := range releases {
		release()
	}
}

func (c *replicaClient) sendControl(frame *replicaOutbound) bool {
	if len(frame.data) > replicaEnvelopeBytes {
		c.close()
		return false
	}
	c.mu.Lock()
	if c.closed || frame.gen != c.authGen {
		c.mu.Unlock()
		return false
	}
	select {
	case c.control <- frame:
		c.mu.Unlock()
		return true
	default:
		c.mu.Unlock()
		c.close()
		return false
	}
}
func (c *replicaClient) sendError(id, subID, requestID string, gen uint64, code string, retryAfter int, closeAfter bool) {
	if !replicaText(subID, replicaIDBytes) {
		subID = ""
	}
	if !replicaText(requestID, replicaIDBytes) {
		requestID = ""
	}
	slog.Debug("Replica request rejected", "connection", c.id, "subId", subID, "requestId", requestID, "code", code, "retryAfter", retryAfter)
	data, err := encodeReplicaFrame(id, TypeError, replicaErrorPayload{SubID: subID, RequestID: requestID, Code: code, Message: "Replica request rejected", RetryAfter: retryAfter})
	if err != nil {
		c.close()
		return
	}
	c.sendControl(&replicaOutbound{data: data, gen: gen, closeAfter: closeAfter})
}
func (c *replicaClient) failure(id string, sub *replicaSubscription, requestID string, gen uint64, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	code := "REPLICATION_TRANSPORT_UNAVAILABLE"
	retry := 0
	var failure *replication.Failure
	var busy *replicaSourceBusyError
	if errors.As(err, &failure) {
		code = failure.Code
	} else if errors.As(err, &busy) {
		code = "REPLICATION_SOURCE_BUSY"
		retry = busy.RetryAfter
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "DEADLINE_EXCEEDED"
	}
	subID := ""
	if sub != nil {
		subID = sub.id
	}
	c.sendError(id, subID, requestID, gen, code, retry, false)
}
func (c *replicaClient) readPump() {
	c.conn.SetReadLimit(replicaInputBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { return c.conn.SetReadDeadline(time.Now().Add(pongWait)) })
	for {
		kind, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		message, err := decodeReplicaEnvelope(data)
		if err != nil || kind != websocket.TextMessage {
			c.mu.Lock()
			gen := c.authGen
			c.mu.Unlock()
			c.sendError("", "", "", gen, "REPLICATION_PROTOCOL_ERROR", 0, true)
			<-c.ctx.Done()
			return
		}
		c.handle(message, len(data))
	}
}

func (c *replicaClient) discard(frame *replicaOutbound) {
	if frame.page == nil {
		return
	}
	c.mu.Lock()
	frame.data = nil
	frame.page.bufferHeld = false
	frame.page.retired = true
	frame.page.cancel()
	if frame.page.sub.page == frame.page {
		frame.page.sub.page = nil
	}
	c.releasePageLocked(frame.page)
	c.mu.Unlock()
}
func (c *replicaClient) writePump() {
	defer c.wg.Done()
	defer func() {
		c.close()
		for {
			select {
			case frame := <-c.data:
				c.discard(frame)
			default:
				return
			}
		}
	}()
	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()
	for {
		var frame *replicaOutbound
		select {
		case <-c.ctx.Done():
			return
		case frame = <-c.control:
		default:
			select {
			case <-c.ctx.Done():
				return
			case frame = <-c.control:
			case frame = <-c.data:
			case <-ping.C:
				if c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)) != nil {
					return
				}
				continue
			case <-heartbeat.C:
				data, _ := encodeReplicaFrame("", TypeHeartbeat, struct{}{})
				frame = &replicaOutbound{data: data}
				c.mu.Lock()
				frame.gen = c.authGen
				c.mu.Unlock()
			}
		}
		c.mu.Lock()
		valid := !c.closed && frame.gen == c.authGen && (!frame.authenticated || c.authCurrentLocked(frame.gen)) && (frame.sub == nil || c.subCurrentLocked(frame.sub))
		if frame.page != nil {
			valid = valid && frame.sub.page == frame.page && !frame.page.retired
			if valid {
				frame.page.sent = true
			}
		}
		c.mu.Unlock()
		if !valid {
			c.discard(frame)
			continue
		}
		_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
		err := c.conn.WriteMessage(websocket.TextMessage, frame.data)
		c.mu.Lock()
		if frame.changed && frame.sub != nil {
			frame.sub.changedQueued = false
		}
		if page := frame.page; page != nil {
			frame.data = nil
			page.bufferHeld = false
			if err == nil && !page.acked && !page.retired {
				page.ackTimer = time.AfterFunc(replicaACKTimeout, func() { c.expirePage(page) })
			}
			c.releasePageLocked(page)
		}
		c.mu.Unlock()
		if err != nil || frame.closeAfter {
			return
		}
		if frame.page != nil {
			slog.Debug("Replica page sent", "connection", c.id, "subId", frame.sub.id, "requestId", frame.page.id)
		}
	}
}

func (c *replicaClient) invalidateSub(sub *replicaSubscription, code string) {
	c.mu.Lock()
	current := c.subCurrentLocked(sub)
	release := c.retireSubLocked(sub)
	c.mu.Unlock()
	if release != nil {
		release()
	}
	if current {
		c.sendError(sub.id, sub.id, "", sub.gen, code, 0, false)
	}
}

func (c *replicaClient) expirePage(page *replicaPendingPage) {
	c.mu.Lock()
	if !c.subCurrentLocked(page.sub) || page.sub.page != page || page.acked || page.retired {
		c.mu.Unlock()
		return
	}
	release := c.retireSubLocked(page.sub)
	c.mu.Unlock()
	if release != nil {
		release()
	}
	c.sendError(page.id, page.sub.id, page.id, page.sub.gen, "REPLICATION_TRANSPORT_UNAVAILABLE", 0, false)
}
