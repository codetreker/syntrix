package events

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	AdmissionStarting   = "starting"
	AdmissionReplaying  = "replaying"
	AdmissionAdmitted   = "admitted"
	AdmissionRecovering = "recovering"
	AdmissionRefresh    = time.Second
	AdmissionFreshness  = 3 * time.Second
)

var ErrAdmissionUnavailable = errors.New("subscription admission unavailable")

type AdmissionSnapshot struct {
	State                                    string
	Reason                                   string
	TransportGeneration, AdmissionGeneration uint64
	Ready, Terminal                          bool
	Err                                      error
	Changed                                  <-chan struct{}
}

// SubscriptionStatus fences received controls from consumption acknowledgments.
// Admission is serving only after the consumer has applied the preceding prefix.
type SubscriptionStatus struct {
	mu                    sync.Mutex
	state                 string
	reason                string
	transport, generation uint64
	consumed              bool
	refreshed             time.Time
	observed              time.Time
	terminal              bool
	err                   error
	changed               chan struct{}
	done                  chan struct{}
}

func NewSubscriptionStatus(ctx context.Context) *SubscriptionStatus {
	s := &SubscriptionStatus{state: AdmissionStarting, changed: make(chan struct{}), done: make(chan struct{})}
	go func() {
		timer := time.NewTicker(AdmissionRefresh / 4)
		defer timer.Stop()
		for {
			select {
			case <-s.done:
				return
			case <-ctx.Done():
				s.Fail(ctx.Err(), true)
				return
			case <-timer.C:
				s.mu.Lock()
				s.expire()
				s.mu.Unlock()
			}
		}
	}()
	return s
}

func (s *SubscriptionStatus) signal() { close(s.changed); s.changed = make(chan struct{}) }
func (s *SubscriptionStatus) expire() {
	if s.consumed && time.Since(s.observed) >= AdmissionFreshness {
		s.consumed = false
		s.err = ErrAdmissionUnavailable
		s.signal()
	}
}

func (s *SubscriptionStatus) BeginAttempt() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return s.transport
	}
	s.transport++
	s.generation = 0
	s.state = AdmissionStarting
	s.reason = ""
	s.consumed = false
	s.refreshed = time.Time{}
	s.observed = time.Time{}
	s.err = nil
	s.signal()
	return s.transport
}

// Receive stamps each immutable control before it can block behind business
// delivery; invalidations become visible without waiting for consumption.
func (s *SubscriptionStatus) Receive(e *PullerEvent) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal || e.TransportGeneration != s.transport || e.AdmissionGeneration == 0 || e.AdmissionGeneration < s.generation {
		return false
	}
	switch e.AdmissionState {
	case AdmissionStarting, AdmissionReplaying, AdmissionAdmitted, AdmissionRecovering:
	default:
		return false
	}
	if e.Ready != (e.AdmissionState == AdmissionAdmitted) {
		return false
	}
	switch e.AdmissionReason {
	case "", "source_unavailable", "overflow":
	default:
		return false
	}
	changed := s.generation != e.AdmissionGeneration || s.state != e.AdmissionState
	if changed || e.AdmissionState != AdmissionAdmitted {
		s.consumed = false
	}
	s.generation = e.AdmissionGeneration
	s.state = e.AdmissionState
	s.reason = e.AdmissionReason
	s.refreshed = time.Now()
	e.admissionReceived = s.refreshed
	s.err = nil
	if changed {
		s.signal()
	}
	return true
}

// Observe acknowledges the control in event order, never an older generation.
func (s *SubscriptionStatus) Observe(e *PullerEvent) {
	if e == nil || e.AdmissionState == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	if s.terminal || e.TransportGeneration != s.transport || e.AdmissionGeneration != s.generation || e.AdmissionState != s.state || e.admissionReceived.IsZero() || time.Since(e.admissionReceived) >= AdmissionFreshness || time.Since(s.refreshed) >= AdmissionFreshness {
		return
	}
	if e.AdmissionState == AdmissionAdmitted && e.Ready {
		wasReady := s.consumed
		s.consumed = true
		if e.admissionReceived.After(s.observed) {
			s.observed = e.admissionReceived
		}
		s.err = nil
		if !wasReady {
			s.signal()
		}
	}
}

func (s *SubscriptionStatus) Fail(err error, terminal bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return
	}
	s.consumed = false
	s.transport++
	s.state = AdmissionRecovering
	s.reason = "transport_unavailable"
	if terminal {
		s.reason = "terminal"
	}
	s.err = err
	if terminal && !s.terminal {
		close(s.done)
	}
	s.terminal = s.terminal || terminal
	s.signal()
}

func (s *SubscriptionStatus) Snapshot() AdmissionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expire()
	return AdmissionSnapshot{State: s.state, Reason: s.reason, TransportGeneration: s.transport, AdmissionGeneration: s.generation, Ready: s.consumed && !s.terminal, Terminal: s.terminal, Err: s.err, Changed: s.changed}
}

func (s *SubscriptionStatus) Err(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snap := s.Snapshot()
	if snap.Ready {
		return nil
	}
	if snap.Err != nil {
		return snap.Err
	}
	return ErrAdmissionUnavailable
}
