package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pullerv1 "github.com/syntrixbase/syntrix/api/gen/puller/v1"
	"github.com/syntrixbase/syntrix/internal/puller/config"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEqualTimestampDeliveryAcknowledgesOnlySuccessfulSends(t *testing.T) {
	t.Parallel()
	server := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	sub := testSubscriber(t, "identity-ack", nil, false, 4)
	first := &events.StoreChangeEvent{Backend: "source", EventID: "10-1-z", ClusterTime: events.ClusterTime{T: 10, I: 1}}
	failed := errors.New("send interrupted")
	var sent []*pullerv1.PullerEvent
	transport := &mockSubscribeServer{ctx: context.Background(), sendFunc: func(evt *pullerv1.PullerEvent) error {
		if failed != nil {
			return failed
		}
		sent = append(sent, evt)
		return nil
	}}
	require.ErrorIs(t, server.sendEvent(transport, sub, "source", first), failed)
	require.Empty(t, sub.CurrentProgress().Positions)
	require.True(t, sub.ShouldSend("source", first.EventID, first.ClusterTime))
	failed = nil
	require.NoError(t, server.sendEvent(transport, sub, "source", first))
	sibling := *first
	sibling.EventID = "10-1-a"
	require.True(t, sub.ShouldSend("source", sibling.EventID, sibling.ClusterTime))
	require.NoError(t, server.sendEvent(transport, sub, "source", &sibling))
	require.False(t, sub.ShouldSend("source", first.EventID, first.ClusterTime))
	require.False(t, sub.ShouldSend("source", sibling.EventID, sibling.ClusterTime))
	require.Equal(t, "10-1-a", sub.CurrentProgress().Positions["source"])
	require.Len(t, sent, 2)
	progress, err := cursor.DecodeProgressMarker(sent[1].Progress)
	require.NoError(t, err)
	require.Equal(t, "10-1-a", progress.Positions["source"])
}

type equalTimestampReplaySource struct {
	mockEventSource
	calls int
	group []*events.StoreChangeEvent
}

func (s *equalTimestampReplaySource) BootstrapBoundary(context.Context) (string, error) {
	return "", nil
}
func (s *equalTimestampReplaySource) ValidateBoundary(context.Context, string) error { return nil }
func (s *equalTimestampReplaySource) ReplayBoundary(context.Context, string, bool) (events.Iterator, error) {
	s.calls++
	if s.calls == 1 {
		return &controllableIterator{events: s.group[:1]}, nil
	}
	return &controllableIterator{events: []*events.StoreChangeEvent{s.group[1], s.group[2], s.group[0]}}, nil
}

func TestVerifiedOverflowReplaysUnseenSiblingsBeforeReady(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	source := &equalTimestampReplaySource{}
	for _, id := range []string{"10-1-z", "10-1-a", "10-1-m"} {
		source.group = append(source.group, &events.StoreChangeEvent{Backend: "source", EventID: id, ClusterTime: events.ClusterTime{T: 10, I: 1}})
	}
	server := NewServer(config.GRPCConfig{ChannelSize: 1}, source, nil)
	marker := cursor.NewProgressMarker()
	marker.Positions["source"] = "10-1-z"
	marker.Lineages = map[string]string{"source": "lineage"}
	var delivered []string
	lastProgress := ""
	ready := false
	transport := &mockSubscribeServer{ctx: ctx, sendFunc: func(frame *pullerv1.PullerEvent) error {
		if frame.Ready {
			require.Equal(t, 2, source.calls)
			require.Equal(t, lastProgress, frame.Progress)
			require.Len(t, delivered, 3)
			ready = true
			cancel()
			return nil
		}
		require.NotNil(t, frame.ChangeEvent)
		delivered = append(delivered, frame.ChangeEvent.EventId)
		lastProgress = frame.Progress
		if len(delivered) == 1 {
			subscribers := server.subs.All()
			require.Len(t, subscribers, 1)
			subscribers[0].SetOverflow()
		}
		return nil
	}}
	require.NoError(t, server.Subscribe(&pullerv1.SubscribeRequest{After: marker.Encode(), RequireReady: true}, transport))
	require.True(t, ready)
	require.Equal(t, []string{"10-1-z", "10-1-a", "10-1-m"}, delivered)
	final, err := cursor.DecodeProgressMarker(lastProgress)
	require.NoError(t, err)
	require.Equal(t, "10-1-m", final.Positions["source"])
}

func TestInvalidEventPositionNeverConsumesSubscriberSlot(t *testing.T) {
	t.Parallel()
	server := NewServer(config.GRPCConfig{}, &mockEventSource{}, nil)
	marker := cursor.NewProgressMarker()
	marker.Positions["source"] = "invalid-native-position"
	err := server.Subscribe(&pullerv1.SubscribeRequest{After: marker.Encode()}, &mockSubscribeServer{ctx: context.Background()})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Zero(t, server.SubscriberCount())
}
