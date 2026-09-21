package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/ctxkeys"
	api_config "github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/query"
	"github.com/syntrixbase/syntrix/pkg/model"

	"github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer.
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer.
	pongWait = 60 * time.Second
	// Maximum message size allowed from peer.
	// Increased from 512 to 64KB to accommodate JWT tokens and larger payloads
	maxMessageSize = 64 * 1024
)

// contextKeyDatabase uses the unified context key for database
var contextKeyDatabase = ctxkeys.KeyDatabase

// Send pings to peer with this period. Must be less than pongWait.
var pingPeriod = (pongWait * 9) / 10

// Application-level heartbeat interval.
// This sends a JSON heartbeat message that browsers can see (unlike WebSocket ping frames).
// Set to 30s to be well under the SDK's default 90s activity timeout.
var heartbeatInterval = 30 * time.Second

// Heartbeat interval for SSE clients.
var sseHeartbeatInterval = 15 * time.Second

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     safeCheckOrigin,
}

// safeCheckOrigin validates WebSocket connection origins.
// It allows:
// - Empty origin (non-browser clients)
// - Same host:port as the request
// - Same host (ignoring port) for development scenarios
// This mitigates cross-site WebSocket abuse while keeping same-origin and local dev clients working.
func safeCheckOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}

	u, err := url.Parse(origin)
	if err != nil {
		return false
	}

	// Compare host (includes port) to ensure exact match with request host.
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}

	// Allow same-host connections across different ports for development.
	// This covers localhost, 127.0.0.1, and LAN IPs like 192.168.x.x.
	originHost := strings.Split(u.Host, ":")[0]
	requestHost := strings.Split(r.Host, ":")[0]

	return strings.EqualFold(originHost, requestHost)
}

// Client is a middleman between the websocket connection and the hub.
type Client struct {
	hub          *Hub
	queryService query.Service
	auth         identity.AuthN
	cfg          api_config.RealtimeConfig

	// The websocket connection.
	conn *websocket.Conn

	// Buffered channel of outbound messages.
	send chan BaseMessage

	// Snapshot enqueue and Hub-owned closure share this per-client lifecycle.
	sendMu        sync.RWMutex
	sendStopOnce  sync.Once
	sendCloseOnce sync.Once
	sendStop      chan struct{}
	transportMu   sync.Mutex
	sseDeadline   func(time.Time) error

	// Subscriptions
	subscriptions  map[string]Subscription // clientSubID -> Subscription
	streamerSubIDs map[string]hubRegistration
	mu             sync.Mutex

	database          string
	authenticated     bool
	allowAllDatabases bool
}

type Subscription struct {
	Query       model.Query
	IncludeData bool
}

func (c *Client) outboundDone() <-chan struct{} {
	c.sendStopOnce.Do(func() { c.sendStop = make(chan struct{}) })
	return c.sendStop
}

// The Hub calls closeOutbound once while removing a registered client. Signal
// before acquiring sendMu so a full queue cannot hold up the Hub's close path.
func (c *Client) closeOutbound() {
	c.sendCloseOnce.Do(func() {
		c.outboundDone()
		close(c.sendStop)
		if c.conn != nil {
			_ = c.conn.Close()
		}
		c.transportMu.Lock()
		if c.sseDeadline != nil {
			_ = c.sseDeadline(time.Now())
		}
		c.transportMu.Unlock()
		c.sendMu.Lock()
		close(c.send)
		c.sendMu.Unlock()
	})
}

func (c *Client) enqueue(message BaseMessage, wait time.Duration) bool {
	stopped := c.outboundDone()
	var hubDone <-chan struct{}
	if c.hub != nil {
		hubDone = c.hub.Done()
	}
	c.sendMu.RLock()
	defer c.sendMu.RUnlock()
	select {
	case <-stopped:
		return false
	default:
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case c.send <- message:
		return true
	case <-stopped:
	case <-hubDone:
	case <-timer.C:
	}
	return false
}

func (c *Client) sendControl(message BaseMessage) {
	if !c.enqueue(message, writeWait) && c.conn != nil {
		_ = c.conn.Close()
	}
}

// readPump pumps messages from the websocket connection to the hub.
//
// The application runs readPump in a per-connection goroutine. The application
// ensures that there is at most one reader on a connection by executing all
// reads from this goroutine.
func (c *Client) readPump() {
	defer func() {
		// Clean up subscriptions
		c.mu.Lock()
		for _, sid := range c.streamerSubIDs {
			c.hub.ReleaseSubscription(sid)
		}
		c.mu.Unlock()

		c.hub.Unregister(c)
		c.conn.Close()
	}()
	c.conn.SetReadLimit(maxMessageSize)
	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error { c.conn.SetReadDeadline(time.Now().Add(pongWait)); return nil })
	slog.Info("WS: WebSocket connection established")

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				slog.Warn("WS: Websocket connection closed", "error", err)
			} else {
				slog.Info("WS: WebSocket connection closed")
			}
			break
		}

		var msg BaseMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			slog.Warn("WS: unmarshalling message", "error", err)
			continue
		}

		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg BaseMessage) {
	slog.Info("WS: Received message", "type", msg.Type, "id", msg.ID)
	switch msg.Type {
	case TypeAuth:
		c.handleAuth(msg)
	case TypeSubscribe:
		if !c.authenticated {
			c.sendControl(BaseMessage{ID: msg.ID, Type: TypeError, Payload: mustMarshal(ErrorPayload{Code: "unauthorized", Message: "auth required"})})
			return
		}
		var payload SubscribePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			slog.Error("WS: unmarshalling subscribe payload", "error", err)
			return
		}

		// Subscribe to Streamer
		streamerSubID, err := c.hub.SubscribeToStream(c.database, payload.Query.Collection, payload.Query.Filters)
		if err != nil {
			slog.Error("WS: Failed to subscribe", "error", err)
			errPayload, _ := json.Marshal(map[string]string{"message": "Subscribe failed: " + err.Error()})
			c.sendControl(BaseMessage{
				ID:      msg.ID,
				Type:    TypeError,
				Payload: errPayload,
			})
			return
		}

		c.mu.Lock()
		c.subscriptions[msg.ID] = Subscription{
			Query:       payload.Query,
			IncludeData: payload.IncludeData,
		}
		c.streamerSubIDs[msg.ID] = streamerSubID
		c.mu.Unlock()

		if !c.hub.RegisterSubscription(streamerSubID, c, msg.ID) {
			c.mu.Lock()
			delete(c.subscriptions, msg.ID)
			delete(c.streamerSubIDs, msg.ID)
			c.mu.Unlock()
			c.sendControl(BaseMessage{ID: msg.ID, Type: TypeError, Payload: mustMarshal(ErrorPayload{Code: "unavailable", Message: "Subscription owner ended"})})
			return
		}
		slog.Info("WS: Subscribed", "collection", payload.Query.Collection, "id", msg.ID, "includeData", payload.IncludeData)

		// Send Ack
		c.sendControl(BaseMessage{ID: msg.ID, Type: TypeSubscribeAck})

		if payload.SendSnapshot {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c.sendSnapshot(ctx, msg.ID, payload.Query.Collection)
		}
	case TypeUnsubscribe:
		if !c.authenticated {
			c.sendControl(BaseMessage{ID: msg.ID, Type: TypeError, Payload: mustMarshal(ErrorPayload{Code: "unauthorized", Message: "auth required"})})
			return
		}
		var payload UnsubscribePayload
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			slog.Warn("WS: unmarshalling unsubscribe payload", "error", err)
			return
		}
		c.mu.Lock()
		delete(c.subscriptions, payload.ID)
		sid, ok := c.streamerSubIDs[payload.ID]
		if ok {
			delete(c.streamerSubIDs, payload.ID)
		}
		c.mu.Unlock()

		if ok {
			c.hub.ReleaseSubscription(sid)
		}

		slog.Info("WS: Unsubscribed", "id", payload.ID)
		c.sendControl(BaseMessage{ID: msg.ID, Type: TypeUnsubscribeAck})
	}
}

func (c *Client) handleAuth(msg BaseMessage) {
	var payload AuthPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		c.sendControl(BaseMessage{ID: msg.ID, Type: TypeError, Payload: mustMarshal(ErrorPayload{Code: "invalid_auth", Message: "invalid payload"})})
		return
	}

	if payload.Database == "" {
		c.sendControl(BaseMessage{ID: msg.ID, Type: TypeError, Payload: mustMarshal(ErrorPayload{Code: "invalid_auth", Message: "database is required"})})
		return
	}

	claims, err := c.auth.ValidateToken(payload.Token)
	if err != nil || claims == nil {
		c.sendControl(BaseMessage{ID: msg.ID, Type: TypeError, Payload: mustMarshal(ErrorPayload{Code: "unauthorized", Message: "invalid token"})})
		return
	}

	c.mu.Lock()
	c.database = payload.Database // Database from auth payload, not token
	c.allowAllDatabases = hasSystemRoleFromClaims(claims)
	c.authenticated = true
	c.mu.Unlock()

	c.sendControl(BaseMessage{ID: msg.ID, Type: TypeAuthAck})
}

// writePump pumps messages from the hub to the websocket connection.
//
// A goroutine running writePump is started for each connection. The
// application ensures that there is at most one writer to a connection by
// executing all writes from this goroutine.
func (c *Client) writePump() {
	pingTicker := time.NewTicker(pingPeriod)
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer func() {
		pingTicker.Stop()
		heartbeatTicker.Stop()
		c.conn.Close()
	}()
	for {
		select {
		case <-c.outboundDone():
			return
		default:
		}
		select {
		case <-c.outboundDone():
			return
		case message, ok := <-c.send:
			slog.Debug("Client writePump: received message", "type", message.Type, "id", message.ID)
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// The hub closed the channel.
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteJSON(message); err != nil {
				return
			}

		case <-pingTicker.C:
			// WebSocket protocol-level ping (browser auto-responds with pong)
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}

		case <-heartbeatTicker.C:
			// Application-level heartbeat (visible to browser's onmessage handler)
			// This keeps SDK's activity timer updated since browsers don't expose ping/pong frames
			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteJSON(BaseMessage{Type: TypeHeartbeat}); err != nil {
				return
			}
		}
	}
}

func databaseFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	// Database is no longer in token claims, must be set explicitly in context
	database, _ := ctx.Value(contextKeyDatabase).(string)
	allowAll := hasSystemRole(ctx)
	return database, allowAll
}

func databaseFromContextMust(ctx context.Context, w http.ResponseWriter) string {
	database, _ := databaseFromContext(ctx)
	if database == "" {
		http.Error(w, "database required", http.StatusUnauthorized)
	}
	return database
}

func hasSystemRole(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	if roles, ok := ctx.Value(identity.ContextKeyRoles).([]string); ok {
		for _, r := range roles {
			if strings.EqualFold(r, "system") {
				return true
			}
		}
	}
	if claims, ok := ctx.Value(identity.ContextKeyClaims).(*identity.Claims); ok {
		return hasSystemRoleFromClaims(claims)
	}
	return false
}

func hasSystemRoleFromClaims(claims *identity.Claims) bool {
	if claims == nil {
		return false
	}
	for _, r := range claims.Roles {
		if strings.EqualFold(r, "system") {
			return true
		}
	}
	return false
}

func tokenFromQuery(r *http.Request) string {
	if r == nil {
		return ""
	}
	q := r.URL.Query()
	if v := q.Get("access_token"); v != "" {
		return v
	}
	if v := q.Get("token"); v != "" {
		return v
	}
	return ""
}

func hasCredentials(r *http.Request) bool {
	if r == nil {
		return false
	}
	return r.Header.Get("Authorization") != ""
}

func checkAllowedOrigin(origin string, reqHost string, cfg api_config.RealtimeConfig, credentialed bool) error {
	if origin == "" {
		if credentialed && !cfg.AllowDevOrigin {
			return errors.New("origin required when using credentials")
		}
		return nil
	}

	parsed, err := url.Parse(origin)
	if err != nil {
		return errors.New("origin not allowed")
	}

	// Allow same host origin
	originHost := strings.Split(parsed.Host, ":")[0]
	reqHostPart := strings.Split(reqHost, ":")[0]
	if strings.EqualFold(originHost, reqHostPart) {
		return nil
	}

	if cfg.AllowDevOrigin {
		if originHost == "localhost" || originHost == "127.0.0.1" {
			return nil
		}
	}

	trimmedOrigin := strings.TrimRight(origin, "/")
	for _, allowed := range cfg.AllowedOrigins {
		if allowed == "" {
			continue
		}
		if strings.EqualFold(strings.TrimRight(allowed, "/"), trimmedOrigin) {
			return nil
		}
	}

	return errors.New("origin not allowed")
}

// ServeReplicationStream handles websocket requests from the peer.
func ServeWs(hub *Hub, qs query.Service, auth identity.AuthN, cfg api_config.RealtimeConfig, w http.ResponseWriter, r *http.Request) {
	r = r.WithContext(r.Context())

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("Failed to upgrade WebSocket connection", "error", err)
		return
	}

	database, allowAll := databaseFromContext(r.Context())

	client := &Client{
		hub:               hub,
		queryService:      qs,
		auth:              auth,
		cfg:               cfg,
		conn:              conn,
		send:              make(chan BaseMessage, 256),
		subscriptions:     make(map[string]Subscription),
		streamerSubIDs:    make(map[string]hubRegistration),
		database:          database,
		authenticated:     database != "",
		allowAllDatabases: allowAll,
	}

	if !client.hub.Register(client) {
		conn.Close()
		return
	}

	// Allow collection of memory referenced by the caller by doing all work in
	// new goroutines.
	go client.writePump()
	go client.readPump()
}

// ServeSSE handles Server-Sent Events requests.
func ServeSSE(hub *Hub, qs query.Service, auth identity.AuthN, cfg api_config.RealtimeConfig, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	origin := r.Header.Get("Origin")
	if err := checkAllowedOrigin(origin, r.Host, cfg, hasCredentials(r)); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	_, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Add("Vary", "Origin")

	// Create client without websocket connection
	database, allowAll := databaseFromContext(ctx)
	// Fall back to query parameter if not in context
	if database == "" {
		database = r.URL.Query().Get("database")
	}
	if database == "" {
		http.Error(w, "database required", http.StatusUnauthorized)
		return
	}

	client := &Client{
		hub:               hub,
		queryService:      qs,
		auth:              auth,
		cfg:               cfg,
		conn:              nil,
		send:              make(chan BaseMessage, 256),
		subscriptions:     make(map[string]Subscription),
		streamerSubIDs:    make(map[string]hubRegistration),
		database:          database,
		authenticated:     database != "",
		allowAllDatabases: allowAll,
	}
	controller := http.NewResponseController(w)
	client.sseDeadline = controller.SetWriteDeadline
	write := func(format string, values ...any) bool {
		client.transportMu.Lock()
		select {
		case <-client.outboundDone():
			client.transportMu.Unlock()
			return false
		default:
		}
		err := controller.SetWriteDeadline(time.Now().Add(writeWait))
		client.transportMu.Unlock()
		if err != nil && !errors.Is(err, http.ErrNotSupported) {
			return false
		}
		if _, err := fmt.Fprintf(w, format, values...); err != nil {
			return false
		}
		return controller.Flush() == nil
	}

	if !client.hub.Register(client) {
		return
	}
	defer func() {
		for _, sid := range client.streamerSubIDs {
			client.hub.ReleaseSubscription(sid)
		}
		client.hub.Unregister(client)
		slog.Info("SSE: connection closed")
	}()

	// Handle initial subscription from query params
	collection := r.URL.Query().Get("collection")

	// Subscribe to stream
	streamerSubID, err := client.hub.SubscribeToStream(client.database, collection, nil)
	if err != nil {
		http.Error(w, "failed to subscribe: "+err.Error(), http.StatusInternalServerError)
		return
	}

	client.subscriptions["default"] = Subscription{
		Query:       model.Query{Collection: collection},
		IncludeData: true, // SSE clients typically expect data
	}
	client.streamerSubIDs["default"] = streamerSubID
	if !client.hub.RegisterSubscription(streamerSubID, client, "default") {
		http.Error(w, "subscription owner ended", http.StatusServiceUnavailable)
		return
	}

	// Send initial comment to establish connection
	if !write(": connected\n\n") {
		return
	}

	// Heartbeat ticker
	ticker := time.NewTicker(sseHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-client.outboundDone():
			return
		case <-ctx.Done():
			slog.Info("SSE: context cancelled, closing connection")
			return
		case <-ticker.C:
			if !write(": heartbeat\n\n") {
				return
			}
		case message, ok := <-client.send:
			if !ok {
				slog.Info("SSE: send channel closed")
				return
			}
			data, err := json.Marshal(message)
			if err != nil {
				continue
			}
			if !write("data: %s\n\n", data) {
				return
			}
		}
	}
}
