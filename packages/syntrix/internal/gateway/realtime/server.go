package realtime

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/core/database"
	"github.com/codetreker/syntrix/internal/core/origin"
	"github.com/codetreker/syntrix/internal/gateway/authentication"
	"github.com/codetreker/syntrix/internal/gateway/config"
	"github.com/codetreker/syntrix/internal/identity"
	"github.com/codetreker/syntrix/internal/query"
	"github.com/codetreker/syntrix/internal/streamer"
)

type Server struct {
	hub             *Hub
	queryService    query.Service
	streamer        streamer.Service
	dataCollection  string
	auth            identity.TokenVerifier
	cfg             config.RealtimeConfig
	database        database.Service
	replicaBudget   *replicaBudget
	lifecycleMu     sync.Mutex
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	lifecycleDone   chan struct{}
	receiving       bool
	terminal        error
	stopping        bool
	transports      sync.WaitGroup
}

func NewServer(qs query.Service, str streamer.Service, dataCollection string, auth identity.TokenVerifier, cfg config.RealtimeConfig) *Server {
	cfg.Replica.ApplyDefaults()
	h := NewHub()
	budget := newReplicaBudget(cfg.Replica)
	h.setReplicaCleanupAdmission(budget.tryPending)
	s := &Server{
		hub:            h,
		dataCollection: dataCollection,
		queryService:   qs,
		streamer:       str,
		auth:           auth,
		cfg:            cfg,
		replicaBudget:  budget,
	}
	return s
}

// SetDatabaseService supplies the shared authority before the server starts.
func (s *Server) SetDatabaseService(service database.Service) { s.database = service }

func (s *Server) HandleWS(w http.ResponseWriter, r *http.Request) {
	if !s.admitTransport(w) {
		return
	}
	defer s.transports.Done()
	s.wrapWS(w, r)
}

func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	if !s.admitTransport(w) {
		return
	}
	defer s.transports.Done()
	s.wrapSSE(w, r)
}

func (s *Server) admitTransport(w http.ResponseWriter) bool {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if s.stopping {
		http.Error(w, "Realtime transport unavailable", http.StatusServiceUnavailable)
		return false
	}
	s.transports.Add(1)
	return true
}

func (s *Server) wrapWS(w http.ResponseWriter, r *http.Request) {
	if err := checkRequestOrigin(r, s.cfg); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if tokenFromQueryParam(r) != "" {
		http.Error(w, "Query token not allowed", http.StatusUnauthorized)
		return
	}
	if modes, present := r.URL.Query()["mode"]; present {
		if len(modes) != 1 || modes[0] != ReplicaDataMode {
			http.Error(w, "Invalid realtime mode", http.StatusBadRequest)
			return
		}
		if err := s.cfg.Replica.Validate(); err != nil {
			http.Error(w, "Invalid replica capacity configuration", http.StatusInternalServerError)
			return
		}
		s.serveReplica(w, r)
		return
	}

	authentication.New(s.auth).MiddlewareOptional(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.hub.Done() == nil {
			http.Error(w, "Realtime transport unavailable", http.StatusServiceUnavailable)
			return
		}
		ServeWs(s.hub, s.queryService, s.auth, s.cfg, w, r)
	})).ServeHTTP(w, r)
}

func (s *Server) wrapSSE(w http.ResponseWriter, r *http.Request) {
	if err := checkRequestOrigin(r, s.cfg); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	authentication.New(s.auth).Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.hub.Done() == nil {
			http.Error(w, "Realtime transport unavailable", http.StatusServiceUnavailable)
			return
		}
		ServeSSE(s.hub, s.queryService, s.auth, s.cfg, w, r)
	})).ServeHTTP(w, r)
}

func tokenFromQueryParam(r *http.Request) string {
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

// StartBackgroundTasks starts the hub and the change stream watcher.
// It returns an error if watching fails to start.
// The background tasks run until ctx is cancelled.
func (s *Server) StartBackgroundTasks(ctx context.Context) error {
	if err := s.cfg.Replica.Validate(); err != nil {
		return err
	}
	if err := (origin.Policy{AllowedOrigins: s.cfg.AllowedOrigins, AllowDevelopment: s.cfg.AllowDevOrigin}).Validate(false); err != nil {
		return err
	}
	s.lifecycleMu.Lock()
	if s.lifecycleCtx != nil || s.stopping {
		s.lifecycleMu.Unlock()
		return errors.New("realtime background tasks already started")
	}
	ctx, cancel := context.WithCancel(ctx)
	s.lifecycleCtx, s.lifecycleCancel, s.lifecycleDone = ctx, cancel, make(chan struct{})
	done := s.lifecycleDone
	s.lifecycleMu.Unlock()
	stream, err := s.streamer.Stream(ctx)
	if err != nil {
		s.failedSetup(ctx, cancel, done)
		return err
	}
	if err := ctx.Err(); err != nil {
		_ = stream.Close()
		s.failedSetup(ctx, cancel, done)
		return err
	}

	s.hub.setRunCtx(ctx)
	s.hub.SetStream(stream)
	var tasks sync.WaitGroup
	tasks.Add(2)
	go func() {
		defer tasks.Done()
		s.hub.Run(ctx)
		s.failUnexpected(ctx, errors.New("realtime hub stopped unexpectedly"))
	}()
	go func() {
		defer tasks.Done()
		s.superviseStream(ctx, stream)
		s.failUnexpected(ctx, errors.New("realtime ingestion stopped unexpectedly"))
	}()
	go func() { tasks.Wait(); close(done) }()
	return nil
}

func (s *Server) failedSetup(ctx context.Context, cancel context.CancelFunc, done chan struct{}) {
	cancel()
	s.lifecycleMu.Lock()
	if s.lifecycleCtx == ctx && !s.stopping {
		s.lifecycleCtx, s.lifecycleCancel, s.lifecycleDone = nil, nil, nil
	}
	close(done)
	s.lifecycleMu.Unlock()
}

func (s *Server) failUnexpected(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	s.lifecycleMu.Lock()
	if s.terminal == nil {
		s.terminal = err
	}
	s.lifecycleCancel()
	s.lifecycleMu.Unlock()
}

func (s *Server) Readiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.lifecycleMu.Lock()
	active := s.receiving && s.lifecycleCtx != nil && s.lifecycleCtx.Err() == nil
	terminal := s.terminal
	s.lifecycleMu.Unlock()
	if terminal != nil {
		return terminal
	}
	if !active {
		return errors.New("realtime ingestion is not active")
	}
	s.hub.streamMu.Lock()
	stream := s.hub.stream
	s.hub.streamMu.Unlock()
	if stream == nil {
		return errors.New("realtime backend is unavailable")
	}
	status := stream.Status()
	if status.Terminal || status.State != streamer.StateConnected {
		return errors.New("realtime backend is not connected")
	}
	if dependency, ok := s.streamer.(interface{ Readiness(context.Context) error }); ok {
		return dependency.Readiness(ctx)
	}
	return nil
}

func (s *Server) TerminalError() error {
	s.lifecycleMu.Lock()
	err := s.terminal
	s.lifecycleMu.Unlock()
	if err != nil {
		return err
	}
	if dependency, ok := s.streamer.(interface{ TerminalError() error }); ok {
		return dependency.TerminalError()
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.lifecycleMu.Lock()
	s.stopping = true
	cancel, done := s.lifecycleCancel, s.lifecycleDone
	s.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	drained := make(chan struct{})
	go func() { s.transports.Wait(); close(drained) }()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) superviseStream(ctx context.Context, stream streamer.Stream) {
	delay := time.Second
	for {
		started := time.Now()
		err := s.receiveStream(ctx, stream)
		s.hub.SetStream(nil)
		if ctx.Err() != nil {
			return
		}
		if fatal := s.TerminalError(); fatal != nil {
			s.failUnexpected(ctx, fatal)
			return
		}
		if time.Since(started) >= 30*time.Second {
			delay = time.Second
		}
		slog.Warn("Realtime backend stream ended", "component", "realtime", "phase", "backend-retired", "category", "backend_unavailable")
		for {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(delay*2, 30*time.Second)
			stream, err = s.streamer.Stream(ctx)
			if err == nil {
				if ctx.Err() != nil {
					_ = stream.Close()
					return
				}
				s.hub.SetStream(stream)
				break
			}
			slog.Warn("Realtime backend reconnect failed", "component", "realtime", "phase", "backend-reconnect", "category", "backend_unavailable")
		}
	}
}

func (s *Server) receiveStream(ctx context.Context, stream streamer.Stream) error {
	received := make(chan error, 1)
	receiverDone := make(chan struct{})
	go func() {
		defer close(receiverDone)
		s.lifecycleMu.Lock()
		s.receiving = true
		s.lifecycleMu.Unlock()
		defer func() {
			s.lifecycleMu.Lock()
			s.receiving = false
			s.lifecycleMu.Unlock()
		}()
		for {
			if err := ctx.Err(); err != nil {
				received <- err
				return
			}
			delivery, err := stream.Recv()
			if err != nil {
				received <- err
				return
			}
			if delivery == nil || delivery.Event == nil {
				continue
			}
			s.hub.BroadcastStreamDelivery(stream, delivery)
		}
	}()
	closeStream := func() {
		if err := stream.Close(); err != nil {
			slog.Warn("Realtime backend close failed", "component", "realtime", "phase", "backend-close", "category", "backend_close_failed")
		}
	}
	for {
		status := stream.Status()
		s.hub.InvalidateReplicaStream(stream, status)
		if status.Terminal {
			closeStream()
			<-received
			<-receiverDone
			if status.Err != nil {
				return status.Err
			}
			return io.EOF
		}
		select {
		case <-ctx.Done():
			closeStream()
			<-received
			<-receiverDone
			return ctx.Err()
		case err := <-received:
			closeStream()
			<-receiverDone
			return fmt.Errorf("receive realtime backend: %w", err)
		case <-status.Changed:
		}
	}
}
