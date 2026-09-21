package realtime

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/gateway/config"
)

func TestReplicaBudgetIndependentLifetimes(t *testing.T) {
	b := newReplicaBudget(config.ReplicaConfig{Connections: 1, Subscriptions: 2, SourceBytes: 8, PendingRegistrations: 1, ReadConcurrency: 1, PageBytes: 8})
	connection, ok := b.tryConnection()
	require.True(t, ok)
	_, ok = b.tryConnection()
	require.False(t, ok)
	source, ok := b.trySubscription(5)
	require.True(t, ok)
	_, ok = b.trySubscription(4)
	require.False(t, ok)
	require.EqualValues(t, 1, b.snapshot().Subscriptions)
	other, ok := b.trySubscription(3)
	require.True(t, ok)
	_, ok = b.trySubscription(1)
	require.False(t, ok)
	pending, ok := b.tryPending()
	require.True(t, ok)
	_, ok = b.tryPending()
	require.False(t, ok)
	read, ok := b.tryRead()
	require.True(t, ok)
	page, ok := b.tryPage(8)
	require.True(t, ok)
	connection()
	source()
	other()
	pending()
	// A retired connection still owns a running Query and its response buffer.
	_, ok = b.tryRead()
	require.False(t, ok)
	_, ok = b.tryPage(1)
	require.False(t, ok)
	newConnection, ok := b.tryConnection()
	require.True(t, ok)
	newConnection()
	read()
	_, ok = b.tryPage(1)
	require.False(t, ok)
	page()
	for _, release := range []func(){connection, source, other, pending, read, page, newConnection} {
		release()
	}
	require.Equal(t, replicaBudgetSnapshot{}, b.snapshot())
}

func TestReplicaBudgetRejectsInvalidAndOverflowingByteReservations(t *testing.T) {
	b := newReplicaBudget(config.ReplicaConfig{SourceBytes: math.MaxInt64, PageBytes: math.MaxInt64})
	for _, reserve := range []func(int64) (func(), bool){b.trySubscription, b.tryPage} {
		for _, n := range []int64{0, -1, math.MinInt64} {
			release, ok := reserve(n)
			require.False(t, ok)
			require.Nil(t, release)
		}
		release, ok := reserve(math.MaxInt64)
		require.True(t, ok)
		_, ok = reserve(1)
		require.False(t, ok)
		release()
	}
	require.Equal(t, replicaBudgetSnapshot{}, b.snapshot())
}

func TestReplicaBudgetReservationsAndReleaseAreConcurrentSafe(t *testing.T) {
	b := newReplicaBudget(config.ReplicaConfig{ReadConcurrency: 3})
	var callers sync.WaitGroup
	for range 100 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			if release, ok := b.tryRead(); ok {
				require.LessOrEqual(t, b.snapshot().Reads, int64(3))
				var releases sync.WaitGroup
				for range 3 {
					releases.Add(1)
					go func() { defer releases.Done(); release() }()
				}
				releases.Wait()
			}
		}()
	}
	callers.Wait()
	require.Equal(t, replicaBudgetSnapshot{}, b.snapshot())
}

func TestReplicaBudgetAuthFIFOAndCancellation(t *testing.T) {
	b := newReplicaBudget(config.ReplicaConfig{AuthConcurrency: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, err := b.acquireAuth(ctx)
	require.NoError(t, err)
	granted := make(chan struct {
		id      int
		release func()
	}, 3)
	cancels := make([]context.CancelFunc, 3)
	for i := range 3 {
		waitCtx, stop := context.WithCancel(ctx)
		cancels[i] = stop
		defer stop()
		go func(id int) {
			release, err := b.acquireAuth(waitCtx)
			if err == nil {
				granted <- struct {
					id      int
					release func()
				}{id, release}
			}
		}(i)
		require.Eventually(t, func() bool { return b.snapshot().AuthWaiting == i+1 }, time.Second, time.Millisecond)
	}
	cancels[1]()
	require.Eventually(t, func() bool { return b.snapshot().AuthWaiting == 2 }, time.Second, time.Millisecond)
	first()
	for _, id := range []int{0, 2} {
		select {
		case next := <-granted:
			require.Equal(t, id, next.id)
			require.Equal(t, 1, b.snapshot().AuthRunning)
			next.release()
			next.release()
		case <-ctx.Done():
			t.Fatal("authorization queue did not advance")
		}
	}
	require.Equal(t, replicaBudgetSnapshot{}, b.snapshot())
}

func TestReplicaBudgetAuthOverloadAndCanceledContext(t *testing.T) {
	b := newReplicaBudget(config.ReplicaConfig{AuthConcurrency: 1, PendingRegistrations: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, err := b.acquireAuth(ctx)
	require.NoError(t, err)
	waitCtx, cancelWait := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		release, err := b.acquireAuth(waitCtx)
		if release != nil {
			release()
		}
		done <- err
	}()
	require.Eventually(t, func() bool { return b.snapshot().AuthWaiting == 1 }, time.Second, time.Millisecond)
	_, err = b.acquireAuth(ctx)
	var busy *replicaSourceBusyError
	require.ErrorAs(t, err, &busy)
	require.Equal(t, 1, busy.RetryAfter)
	require.Contains(t, busy.Error(), "capacity")
	cancelWait()
	require.ErrorIs(t, <-done, context.Canceled)
	_, err = b.acquireAuth(waitCtx)
	require.ErrorIs(t, err, context.Canceled)
	first()
	require.Equal(t, replicaBudgetSnapshot{}, b.snapshot())
}

func TestReplicaBudgetAuthRateRefillsAndStopsIdleTimer(t *testing.T) {
	b := newReplicaBudget(config.ReplicaConfig{AuthConcurrency: 2, AuthRate: 10, AuthBurst: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first, err := b.acquireAuth(ctx)
	require.NoError(t, err)
	first()
	// Fix the refill origin after setup so the lower timing bound is independent
	// of a pause between the initial grant and this assertion.
	b.mu.Lock()
	b.authTokens = 0
	b.authUpdated = time.Now()
	b.mu.Unlock()
	granted := make(chan func(), 1)
	go func() {
		release, err := b.acquireAuth(ctx)
		if err == nil {
			granted <- release
		}
	}()
	select {
	case release := <-granted:
		release()
		t.Fatal("authorization exceeded the token rate")
	case <-time.After(20 * time.Millisecond):
	}
	select {
	case release := <-granted:
		release()
	case <-ctx.Done():
		t.Fatal("authorization token did not refill")
	}
	require.Equal(t, replicaBudgetSnapshot{}, b.snapshot())
	b.mu.Lock()
	require.Nil(t, b.authTimer)
	b.mu.Unlock()
}

func TestReplicaBudgetAuthGrantRacingCancellationReturnsAllSlots(t *testing.T) {
	b := newReplicaBudget(config.ReplicaConfig{AuthConcurrency: 1, AuthBurst: 1000})
	for range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		first, err := b.acquireAuth(ctx)
		require.NoError(t, err)
		done := make(chan struct{})
		go func() {
			defer close(done)
			release, _ := b.acquireAuth(ctx)
			if release != nil {
				release()
			}
		}()
		cancel()
		first()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("canceled authorization did not return")
		}
		require.Equal(t, replicaBudgetSnapshot{}, b.snapshot())
	}
}
