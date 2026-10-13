package grpc

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/codetreker/syntrix/api/gen/puller/v1"
	"github.com/codetreker/syntrix/internal/puller/config"
	"github.com/codetreker/syntrix/internal/puller/core"
	"github.com/codetreker/syntrix/internal/puller/cursor"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type admissionEventSource struct {
	retainedReplaySource
	ready atomic.Bool
	fatal atomic.Bool
}

func (s *admissionEventSource) Readiness(context.Context) error {
	if s.fatal.Load() {
		return errors.New("private source credentials")
	}
	if !s.ready.Load() {
		return core.ErrCaptureUnavailable
	}
	return nil
}

func TestAdmissionWireIdleRefreshRecoveryAndSanitizedFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	source := &admissionEventSource{}
	source.ready.Store(true)
	server := NewServer(config.GRPCConfig{HeartbeatInterval: time.Minute}, source, nil)
	server.Init()
	defer server.Shutdown()
	frames := make(chan *pb.PullerEvent, 10)
	done := make(chan error, 1)
	go func() {
		done <- server.Subscribe(&pb.SubscribeRequest{ConsumerId: "ordinary", ReportAdmissionReady: true}, &mockSubscribeServer{ctx: ctx, sendFunc: func(frame *pb.PullerEvent) error {
			select {
			case frames <- frame:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}})
	}()
	await := func(state string, minGeneration uint64) *pb.PullerEvent {
		for {
			select {
			case frame := <-frames:
				require.Nil(t, frame.ChangeEvent)
				require.Empty(t, frame.Progress)
				if frame.AdmissionState == state && frame.AdmissionGeneration >= minGeneration {
					return frame
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
				return nil
			}
		}
	}
	first := await(events.AdmissionAdmitted, 1)
	require.True(t, first.Ready)
	refresh := await(events.AdmissionAdmitted, first.AdmissionGeneration)
	require.Equal(t, first.AdmissionGeneration, refresh.AdmissionGeneration)
	source.ready.Store(false)
	recovery := await(events.AdmissionRecovering, first.AdmissionGeneration+1)
	require.Equal(t, "source_unavailable", recovery.AdmissionReason)
	require.False(t, recovery.Ready)
	source.ready.Store(true)
	next := await(events.AdmissionAdmitted, recovery.AdmissionGeneration)
	require.True(t, next.Ready)
	source.fatal.Store(true)
	select {
	case err := <-done:
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.NotContains(t, err.Error(), "credentials")
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestAdmissionWireRejectsUnsupportedOrConflictingMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unsupported := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	defer unsupported.cancel()
	err := unsupported.Subscribe(&pb.SubscribeRequest{ReportAdmissionReady: true}, &mockSubscribeServer{ctx: ctx})
	require.Equal(t, codes.Unimplemented, status.Code(err))
	source := &admissionEventSource{}
	server := NewServer(config.GRPCConfig{}, source, nil)
	defer server.cancel()
	err = server.Subscribe(&pb.SubscribeRequest{ReportAdmissionReady: true, RequireReady: true}, &mockSubscribeServer{ctx: ctx})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Zero(t, server.SubscriberCount())
}

type nativePrefixSource struct {
	mockEventSource
	emitted bool
}

func (s *nativePrefixSource) Readiness(ctx context.Context) error {
	if !s.emitted {
		s.emitted = true
		return s.handler(ctx, "backend", &events.StoreChangeEvent{Backend: "backend", EventID: "1-1-live", OpType: events.StoreOperationInsert, ClusterTime: events.ClusterTime{T: 1, I: 1}})
	}
	return nil
}

func TestAdmissionCannotOvertakeAcknowledgedNativeIngress(t *testing.T) {
	processors := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(processors)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := &nativePrefixSource{}
	server := NewServer(config.GRPCConfig{ChannelSize: 10}, source, nil)
	server.Init()
	defer server.Shutdown()
	var order []string
	err := server.Subscribe(&pb.SubscribeRequest{ReportAdmissionReady: true}, &mockSubscribeServer{ctx: ctx, sendFunc: func(frame *pb.PullerEvent) error {
		if frame.ChangeEvent != nil {
			order = append(order, "business:"+frame.ChangeEvent.EventId)
		} else {
			order = append(order, frame.AdmissionState)
		}
		if frame.Ready {
			cancel()
		}
		return nil
	}})
	require.NoError(t, err)
	require.Equal(t, []string{events.AdmissionStarting, "business:1-1-live", events.AdmissionAdmitted}, order)
}

func TestIngressFenceAndNativeEnqueueCancelWithServer(t *testing.T) {
	for _, stage := range []string{"request", "acknowledgment", "enqueue"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			source := &mockEventSource{}
			server := NewServer(config.GRPCConfig{ChannelSize: 1}, source, nil)
			server.initialized = true // Hold the dispatcher before a fence can complete.
			result := make(chan error, 1)
			if stage == "enqueue" {
				server.eventChan <- &events.StoreChangeEvent{}
				go func() { result <- server.enqueueNativeEvent(ctx, "backend", &events.StoreChangeEvent{}) }()
				require.Eventually(t, func() bool { return len(server.ingressGate) == 1 }, time.Second, time.Millisecond)
				server.Shutdown()
			} else {
				go func() { result <- server.fenceIngress(ctx) }()
				if stage == "acknowledgment" {
					select {
					case <-server.ingressBarriers:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				server.Shutdown()
			}
			select {
			case err := <-result:
				require.ErrorIs(t, err, context.Canceled)
			case <-ctx.Done():
				t.Fatal("shutdown did not cancel ingress")
			}
		})
	}
}

func TestServerShutdownCancelsIdleHandlerWithLiveWireContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	source := &admissionEventSource{}
	source.ready.Store(true)
	server := NewServer(config.GRPCConfig{}, source, nil)
	server.Init()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- server.Subscribe(&pb.SubscribeRequest{ReportAdmissionReady: true}, &mockSubscribeServer{ctx: ctx, sendFunc: func(frame *pb.PullerEvent) error {
			if frame.Ready {
				select {
				case <-ready:
				default:
					close(ready)
				}
			}
			return nil
		}})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	server.Shutdown()
	select {
	case err := <-done:
		require.Equal(t, codes.Canceled, status.Code(err))
	case <-ctx.Done():
		t.Fatal("handler survived server shutdown")
	}
	require.NoError(t, ctx.Err(), "wire context must remain active")
}

func TestIngressReservationHonorsIndependentCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := NewServer(config.GRPCConfig{ChannelSize: 1}, &mockEventSource{}, nil)
	server.initialized = true
	server.eventChan <- &events.StoreChangeEvent{}
	producer := make(chan error, 1)
	go func() { producer <- server.enqueueNativeEvent(ctx, "backend", &events.StoreChangeEvent{}) }()
	require.Eventually(t, func() bool { return len(server.ingressGate) == 1 }, time.Second, time.Millisecond)
	deadline, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	require.ErrorIs(t, server.fenceIngress(deadline), context.DeadlineExceeded)
	require.NoError(t, server.ctx.Err(), "subscription deadline must not close shared source")
	server.Shutdown()
	require.ErrorIs(t, <-producer, context.Canceled)
}

func TestNativeQueueHonorsCallerDeadlineAndRejectsUninitializedFence(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := NewServer(config.GRPCConfig{ChannelSize: 1}, &mockEventSource{}, nil)
	defer server.cancel()
	require.ErrorContains(t, server.fenceIngress(ctx), "not initialized")
	server.eventChan <- &events.StoreChangeEvent{}
	deadline, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	require.ErrorIs(t, server.enqueueNativeEvent(deadline, "backend", &events.StoreChangeEvent{}), context.DeadlineExceeded)
	require.NoError(t, server.ctx.Err())
}

func TestIngressReservationRejectsCanceledCallerAndClosedServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	server := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	caller, stop := context.WithCancel(ctx)
	stop()
	require.ErrorIs(t, server.acquireIngress(caller), context.Canceled)
	server.ingressGate <- struct{}{}
	server.cancel()
	require.ErrorIs(t, server.acquireIngress(ctx), context.Canceled)
}

func TestIngressBarrierPublishesEntireBoundedNativePrefix(t *testing.T) {
	processors := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(processors)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := NewServer(config.GRPCConfig{ChannelSize: 1000}, &mockEventSource{}, nil)
	defer server.cancel()
	server.initialized = true
	sub, err := core.NewSubscriber("prefix", cursor.NewProgressMarker(), true, false, 1001)
	require.NoError(t, err)
	server.subs.Add(sub)
	defer server.subs.Remove(sub)
	for n := 1; n <= 1000; n++ {
		require.NoError(t, server.enqueueNativeEvent(ctx, "source", &events.StoreChangeEvent{EventID: fmt.Sprintf("1-%d-live", n), ClusterTime: events.ClusterTime{T: 1, I: uint32(n)}}))
	}
	done := make(chan error, 1)
	go func() { done <- server.fenceIngress(ctx) }()
	require.Eventually(t, func() bool { return len(server.ingressGate) == 1 }, time.Second, time.Millisecond)
	runtime.Gosched()
	go server.processEvents()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.Len(t, sub.Events(), 1000, "barrier acknowledgment must cover the whole native FIFO prefix")
	require.Empty(t, server.eventChan)
}
