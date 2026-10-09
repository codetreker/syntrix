package server

import (
	"fmt"
	"net/http"
)

func (s *serverImpl) initHTTPServer() {
	s.httpServer = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.HTTPPort),
		Handler:      s.wrapMiddleware(s.httpMux),
		ReadTimeout:  s.cfg.HTTPReadTimeout,
		WriteTimeout: s.cfg.HTTPWriteTimeout,
		IdleTimeout:  s.cfg.HTTPIdleTimeout,
	}
}

func (s *serverImpl) runHTTPServer(errChan chan<- error) {
	s.logger.Info("Starting HTTP server", "port", s.cfg.HTTPPort)
	if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		errChan <- fmt.Errorf("http server error: %w", err)
	}
}
