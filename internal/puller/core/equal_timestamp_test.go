package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"go.mongodb.org/mongo-driver/bson"
)

func TestNativeSubscriptionRejectsMalformedEventIDBeforeAdmission(t *testing.T) {
	t.Parallel()
	p, _, boundary := nativeBoundaryFixture(t)
	marker, err := cursor.DecodeProgressMarker(boundary)
	require.NoError(t, err)
	marker.Positions["source"] = "not-an-event-id"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, verified := range []bool{false, true} {
		t.Run(fmt.Sprint(verified), func(t *testing.T) {
			var stream <-chan *events.PullerEvent
			if verified {
				stream = p.SubscribeReady(ctx, "indexer", marker.Encode(), nil)
			} else {
				stream = p.Subscribe(ctx, "consumer", marker.Encode())
			}
			if verified {
				failure := <-stream
				require.NotNil(t, failure)
				require.ErrorContains(t, failure.Error, "invalid progress")
				require.False(t, failure.Retryable)
			}
			_, open := <-stream
			require.False(t, open)
			require.Zero(t, p.subs.Count())
		})
	}
}

func TestEqualTimestampOverflowAndReconnectReplayEveryDistinctEvent(t *testing.T) {
	t.Parallel()
	p, backend, boundary := nativeBoundaryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstCtx, cancelFirst := context.WithCancel(ctx)
	defer cancelFirst()
	entered, release := make(chan struct{}), make(chan struct{})
	first := p.SubscribeReady(firstCtx, "indexer", boundary, func(string) {
		close(entered)
		select {
		case <-release:
		case <-firstCtx.Done():
		}
	})
	select {
	case ready := <-first:
		require.True(t, ready.Ready)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	const count = 1001
	eventID := func(i int) string { return fmt.Sprintf("7-1-%04d", i) }
	for i := count; i > 0; i-- {
		evt := &events.StoreChangeEvent{Backend: "source", EventID: eventID(i), ClusterTime: events.ClusterTime{T: 7, I: 1}}
		require.NoError(t, backend.buffer.Write(ctx, evt, bson.Raw{5, 0, 0, 0, 0}))
		p.subs.Broadcast(evt)
	}
	require.NoError(t, backend.buffer.Flush(ctx))
	close(release)
	seen := make(map[string]bool, count)
	progress := ""
	for i := 0; i < count; i++ {
		select {
		case evt := <-first:
			require.NotNil(t, evt)
			require.NoError(t, evt.Error)
			require.NotNil(t, evt.Change)
			require.False(t, seen[evt.Change.EventID], "duplicate event %s", evt.Change.EventID)
			seen[evt.Change.EventID] = true
			progress = evt.Progress
			if i == 0 {
				require.Equal(t, eventID(count), evt.Change.EventID)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	for i := 1; i <= count; i++ {
		require.True(t, seen[eventID(i)], "missing equal-timestamp event %s", eventID(i))
	}
	cancelFirst()
	for evt := range first {
		t.Errorf("unexpected event after complete recovery: %+v", evt)
	}
	require.Zero(t, p.subs.Count())

	second := p.SubscribeReady(ctx, "indexer", progress, nil)
	for i := 1; i <= count; i++ {
		select {
		case evt := <-second:
			require.NotNil(t, evt.Change)
			require.Equal(t, eventID(i), evt.Change.EventID)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case ready := <-second:
		require.True(t, ready.Ready)
		marker, err := cursor.DecodeProgressMarker(ready.Progress)
		require.NoError(t, err)
		require.Equal(t, eventID(count), marker.Positions["source"])
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	for evt := range second {
		t.Errorf("unexpected event after replay readiness: %+v", evt)
	}
	require.Zero(t, p.subs.Count())
}
