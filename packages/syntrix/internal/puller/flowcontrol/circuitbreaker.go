package flowcontrol

import (
	"sync"
	"time"
)

// State represents the circuit breaker state.
type State int

const (
	// StateClosed means the circuit is closed and requests are allowed.
	StateClosed State = iota
	// StateOpen means the circuit is open and requests are blocked.
	StateOpen
	// StateHalfOpen means the circuit is half-open and probing is allowed.
	StateHalfOpen
)

// CircuitBreaker implements a circuit breaker pattern.
type CircuitBreaker struct {
	state        State
	failureCount int
	threshold    int
	timeout      time.Duration
	lastFailTime time.Time
	mu           sync.RWMutex
}

// CircuitBreakerOptions configures the circuit breaker.
type CircuitBreakerOptions struct {
	Threshold int
	Timeout   time.Duration
}

// NewCircuitBreaker creates a new circuit breaker.
func NewCircuitBreaker(opts CircuitBreakerOptions) *CircuitBreaker {
	threshold := opts.Threshold
	if threshold == 0 {
		threshold = 5
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &CircuitBreaker{
		state:     StateClosed,
		threshold: threshold,
		timeout:   timeout,
	}
}

// Allow checks if a request is allowed.
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateClosed {
		return true
	}

	if cb.state == StateOpen {
		if time.Since(cb.lastFailTime) > cb.timeout {
			cb.state = StateHalfOpen
			return true
		}
		return false
	}

	// StateHalfOpen: allow one request (or more depending on policy, here simple)
	// For simplicity, we allow in HalfOpen, if it fails it goes back to Open.
	return true
}

// RecordSuccess records a successful request.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == StateHalfOpen {
		cb.state = StateClosed
		cb.failureCount = 0
	} else if cb.state == StateClosed {
		cb.failureCount = 0
	}
}

// RecordFailure records a failed request.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failureCount++
	cb.lastFailTime = time.Now()

	if cb.state == StateClosed && cb.failureCount >= cb.threshold {
		cb.state = StateOpen
	} else if cb.state == StateHalfOpen {
		cb.state = StateOpen
	}
}

// State returns the current state.
func (cb *CircuitBreaker) State() State {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}
