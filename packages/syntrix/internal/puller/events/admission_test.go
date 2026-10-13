package events

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdmissionRequiresConsumptionAndRejectsInvalidatedGenerations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s := NewSubscriptionStatus(ctx)
	transport := s.BeginAttempt()
	frame := &PullerEvent{AdmissionState: AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: transport, Ready: true}
	require.True(t, s.Receive(frame))
	require.Error(t, s.Err(ctx))
	s.Observe(frame)
	require.NoError(t, s.Err(ctx))
	changed := s.Snapshot().Changed
	recovery := &PullerEvent{AdmissionState: AdmissionRecovering, AdmissionGeneration: 2, TransportGeneration: transport}
	require.True(t, s.Receive(recovery))
	require.Error(t, s.Err(ctx))
	select {
	case <-changed:
	default:
		t.Fatal("invalidation did not signal")
	}
	s.Observe(frame)
	require.Error(t, s.Err(ctx))
	require.False(t, s.Receive(frame))
	next := &PullerEvent{AdmissionState: AdmissionAdmitted, AdmissionGeneration: 2, TransportGeneration: transport, Ready: true}
	require.True(t, s.Receive(next))
	s.Observe(next)
	require.NoError(t, s.Err(ctx))
	cause := errors.New("lost transport")
	s.Fail(cause, false)
	require.ErrorIs(t, s.Err(ctx), cause)
	s.Observe(next)
	require.Error(t, s.Err(ctx))
	require.False(t, s.Receive(next))
	transport = s.BeginAttempt()
	next.TransportGeneration = transport
	next.AdmissionGeneration = 1
	require.True(t, s.Receive(next))
	s.Observe(next)
	require.NoError(t, s.Err(ctx))
	s.Fail(cause, true)
	require.True(t, s.Snapshot().Terminal)
	require.False(t, s.Receive(next))
}

func TestAdmissionFreshnessExpiresWithoutReceiveOrDispatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	s := NewSubscriptionStatus(ctx)
	frame := &PullerEvent{AdmissionState: AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: s.BeginAttempt(), Ready: true}
	require.True(t, s.Receive(frame))
	s.Observe(frame)
	queued := &PullerEvent{AdmissionState: AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: frame.TransportGeneration, Ready: true}
	require.True(t, s.Receive(queued))
	changed := s.Snapshot().Changed
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("freshness did not expire independently")
	}
	require.False(t, s.Snapshot().Ready)
	require.Eventually(t, func() bool { return time.Since(queued.admissionReceived) >= AdmissionFreshness }, time.Second, time.Millisecond)
	s.Observe(frame)
	require.False(t, s.Snapshot().Ready, "queued stale ready must not restore admission")
	fresh := &PullerEvent{AdmissionState: AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: frame.TransportGeneration, Ready: true}
	require.True(t, s.Receive(fresh))
	require.False(t, s.Snapshot().Ready)
	s.Observe(queued)
	require.False(t, s.Snapshot().Ready, "newer unconsumed receipt must not refresh older queued control")
	s.Observe(fresh)
	require.True(t, s.Snapshot().Ready)
}

func TestAdmissionInvalidControlsDoNotRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewSubscriptionStatus(ctx)
	generation := s.BeginAttempt()
	for _, frame := range []*PullerEvent{{}, {AdmissionState: "unknown", TransportGeneration: generation, AdmissionGeneration: 1}, {AdmissionState: AdmissionAdmitted, TransportGeneration: generation + 1, AdmissionGeneration: 1}} {
		require.False(t, s.Receive(frame))
		require.False(t, s.Snapshot().Ready)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	require.ErrorIs(t, s.Err(canceled), context.Canceled)
}

func TestUnconsumedRefreshCannotExtendObservedAdmissionLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	s := NewSubscriptionStatus(ctx)
	first := &PullerEvent{Ready: true, AdmissionState: AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: s.BeginAttempt()}
	require.True(t, s.Receive(first))
	s.Observe(first)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	newer := &PullerEvent{Ready: true, AdmissionState: AdmissionAdmitted, AdmissionGeneration: 1, TransportGeneration: first.TransportGeneration}
	require.True(t, s.Receive(newer))
	changed := s.Snapshot().Changed
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("unconsumed refresh extended serving lease")
	}
	require.False(t, s.Snapshot().Ready)
	s.Observe(newer)
	require.True(t, s.Snapshot().Ready)
	changed = s.Snapshot().Changed
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("lease expiry used observation time instead of receipt")
	}
	require.False(t, s.Snapshot().Ready)
}
