package realtime

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/syntrixbase/syntrix/internal/gateway/config"
)

type replicaSourceBusyError struct{ RetryAfter int }

func (e *replicaSourceBusyError) Error() string {
	return "replication source authorization capacity exhausted"
}

type replicaBudgetSnapshot struct {
	Connections, Subscriptions, Pending, Reads, SourceBytes, PageBytes int64
	AuthRunning, AuthWaiting                                           int
}

type replicaAuthWaiter struct {
	ready   chan struct{}
	granted bool
}

// One ledger belongs to the server, not a Stream generation. Retiring an owner
// does not return its reservations until its work and buffers actually exit.
type replicaBudget struct {
	mu          sync.Mutex
	cfg         config.ReplicaConfig
	used        replicaBudgetSnapshot
	authWaiters []*replicaAuthWaiter
	authTokens  float64
	authUpdated time.Time
	authTimer   *time.Timer
}

func newReplicaBudget(cfg config.ReplicaConfig) *replicaBudget {
	cfg.ApplyDefaults()
	return &replicaBudget{cfg: cfg, authTokens: float64(cfg.AuthBurst), authUpdated: time.Now()}
}

func (b *replicaBudget) reserve(counter *int64, amount, maximum int64) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if amount <= 0 || amount > maximum-*counter {
		return nil, false
	}
	*counter += amount
	var once sync.Once
	return func() { once.Do(func() { b.mu.Lock(); *counter -= amount; b.mu.Unlock() }) }, true
}

func (b *replicaBudget) tryConnection() (func(), bool) {
	return b.reserve(&b.used.Connections, 1, int64(b.cfg.Connections))
}

func (b *replicaBudget) trySubscription(sourceBytes int64) (func(), bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sourceBytes <= 0 || b.used.Subscriptions >= int64(b.cfg.Subscriptions) || sourceBytes > b.cfg.SourceBytes-b.used.SourceBytes {
		return nil, false
	}
	b.used.Subscriptions++
	b.used.SourceBytes += sourceBytes
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.used.Subscriptions--
			b.used.SourceBytes -= sourceBytes
			b.mu.Unlock()
		})
	}, true
}

func (b *replicaBudget) tryPending() (func(), bool) {
	return b.reserve(&b.used.Pending, 1, int64(b.cfg.PendingRegistrations))
}
func (b *replicaBudget) tryRead() (func(), bool) {
	return b.reserve(&b.used.Reads, 1, int64(b.cfg.ReadConcurrency))
}
func (b *replicaBudget) tryPage(bytes int64) (func(), bool) {
	return b.reserve(&b.used.PageBytes, bytes, b.cfg.PageBytes)
}

func (b *replicaBudget) snapshot() replicaBudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.used
	out.AuthWaiting = len(b.authWaiters)
	return out
}

func (b *replicaBudget) releaseAuth() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used.AuthRunning--
	b.dispatchAuthLocked()
}

// A bounded FIFO queue shares a single refill timer. Only admitted callers
// wait; overload cannot create an unbounded scheduler queue or timer set.
func (b *replicaBudget) acquireAuth(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	waiter := &replicaAuthWaiter{ready: make(chan struct{})}
	b.mu.Lock()
	if len(b.authWaiters) >= b.cfg.PendingRegistrations {
		retry := max(1, int(math.Ceil(float64(len(b.authWaiters)+1)/float64(b.cfg.AuthRate))))
		b.mu.Unlock()
		return nil, &replicaSourceBusyError{RetryAfter: retry}
	}
	b.authWaiters = append(b.authWaiters, waiter)
	b.dispatchAuthLocked()
	b.mu.Unlock()
	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			b.releaseAuth()
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(b.releaseAuth) }, nil
	case <-ctx.Done():
		b.mu.Lock()
		if waiter.granted {
			b.used.AuthRunning--
		} else {
			for i, queued := range b.authWaiters {
				if queued == waiter {
					copy(b.authWaiters[i:], b.authWaiters[i+1:])
					b.authWaiters[len(b.authWaiters)-1] = nil
					b.authWaiters = b.authWaiters[:len(b.authWaiters)-1]
					break
				}
			}
		}
		b.dispatchAuthLocked()
		b.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (b *replicaBudget) dispatchAuthLocked() {
	if b.authTimer != nil {
		b.authTimer.Stop()
		b.authTimer = nil
	}
	now := time.Now()
	b.authTokens = math.Min(float64(b.cfg.AuthBurst), b.authTokens+now.Sub(b.authUpdated).Seconds()*float64(b.cfg.AuthRate))
	b.authUpdated = now
	for len(b.authWaiters) > 0 && b.used.AuthRunning < b.cfg.AuthConcurrency && b.authTokens >= 1 {
		waiter := b.authWaiters[0]
		b.authWaiters[0] = nil
		b.authWaiters = b.authWaiters[1:]
		b.authTokens--
		b.used.AuthRunning++
		waiter.granted = true
		close(waiter.ready)
	}
	if len(b.authWaiters) > 0 && b.used.AuthRunning < b.cfg.AuthConcurrency {
		delay := time.Duration(math.Ceil((1 - b.authTokens) / float64(b.cfg.AuthRate) * float64(time.Second)))
		b.authTimer = time.AfterFunc(max(delay, time.Nanosecond), func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.dispatchAuthLocked()
		})
	}
}
