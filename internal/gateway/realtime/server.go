package realtime

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/syntrixbase/syntrix/internal/core/database"
	"github.com/syntrixbase/syntrix/internal/core/identity"
	"github.com/syntrixbase/syntrix/internal/gateway/config"
	"github.com/syntrixbase/syntrix/internal/query"
	"github.com/syntrixbase/syntrix/internal/streamer"
)

type Server struct {
	hub            *Hub
	queryService   query.Service
	streamer       streamer.Service
	dataCollection string
	auth           identity.AuthN
	cfg            config.RealtimeConfig
	database       database.Service
	replicaBudget  *replicaBudget
}

func NewServer(qs query.Service, str streamer.Service, dataCollection string, auth identity.AuthN, cfg config.RealtimeConfig) *Server {
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
	s.wrapWS(w, r)
}

func (s *Server) HandleSSE(w http.ResponseWriter, r *http.Request) {
	s.wrapSSE(w, r)
}

func (s *Server) wrapWS(w http.ResponseWriter, r *http.Request) {
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

	s.auth.MiddlewareOptional(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWs(s.hub, s.queryService, s.auth, s.cfg, w, r)
	})).ServeHTTP(w, r)
}

func (s *Server) wrapSSE(w http.ResponseWriter, r *http.Request) {
	s.auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	stream, err := s.streamer.Stream(ctx)
	if err != nil {
		return err
	}

	s.hub.setRunCtx(ctx)
	s.hub.SetStream(stream)
	go s.hub.Run(ctx)
	go s.superviseStream(ctx, stream)
	return nil
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
		if time.Since(started) >= 30*time.Second {
			delay = time.Second
		}
		slog.Warn("Realtime backend stream ended", "component", "realtime", "phase", "backend-retired", "error", err)
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
			slog.Warn("Realtime backend reconnect failed", "component", "realtime", "phase", "backend-reconnect", "error", err)
		}
	}
}

func (s *Server) receiveStream(ctx context.Context, stream streamer.Stream) error {
	received := make(chan error, 1)
	go func() {
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
			slog.Warn("Realtime backend close failed", "component", "realtime", "phase", "backend-close", "error", err)
		}
	}
	for {
		status := stream.Status()
		s.hub.InvalidateReplicaStream(stream, status)
		if status.Terminal {
			closeStream()
			<-received
			if status.Err != nil {
				return status.Err
			}
			return io.EOF
		}
		select {
		case <-ctx.Done():
			closeStream()
			<-received
			return ctx.Err()
		case err := <-received:
			closeStream()
			return fmt.Errorf("receive realtime backend: %w", err)
		case <-status.Changed:
		}
	}
}
