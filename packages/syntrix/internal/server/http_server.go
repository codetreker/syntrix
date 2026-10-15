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
