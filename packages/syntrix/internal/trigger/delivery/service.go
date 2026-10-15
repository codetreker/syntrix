package delivery

import (
	"context"
	"errors"
)

// Service consumes delivery tasks and executes HTTP webhooks.
type Service interface {
	// Start begins consuming and processing delivery tasks.
	// Blocks until context is cancelled.
	Start(ctx context.Context) error
}

func (s *service) Readiness(ctx context.Context) error {
	if source, ok := s.consumer.(interface{ Readiness(context.Context) error }); ok {
		return source.Readiness(ctx)
	}
	return errors.New("trigger delivery pool state is unavailable")
}

func (s *service) TerminalError() error {
	if source, ok := s.consumer.(interface{ TerminalError() error }); ok {
		return source.TerminalError()
	}
	return nil
}

func (s *service) Shutdown(ctx context.Context) error {
	if source, ok := s.consumer.(interface{ Shutdown(context.Context) error }); ok {
		return source.Shutdown(ctx)
	}
	return errors.New("trigger delivery pool settlement is unavailable")
}
