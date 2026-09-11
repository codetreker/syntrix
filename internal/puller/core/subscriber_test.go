package core

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

func TestSubscriber_ShouldSend(t *testing.T) {
	sub := testSubscriber(t, "test-sub", nil, false, 100)

	// Initial state: no history for backend "db1"
	// Should send any event
	ct1 := events.ClusterTime{T: 100, I: 1}
	assert.True(t, sub.ShouldSend("db1", "evt1", ct1), "Should send first event")

	// Update position
	sub.UpdatePosition("db1", "evt1", ct1)

	// Test older event
	ctOld := events.ClusterTime{T: 99, I: 1}
	assert.False(t, sub.ShouldSend("db1", "old", ctOld), "Should not send older event")

	// Test same event
	assert.False(t, sub.ShouldSend("db1", "evt1", ct1), "Should not send same event")

	// Test newer event
	ctNew := events.ClusterTime{T: 100, I: 2}
	assert.True(t, sub.ShouldSend("db1", "new", ctNew), "Should send newer event")

	// Test different backend
	assert.True(t, sub.ShouldSend("db2", "old", ctOld), "Should send event for new backend")
}

func TestSubscriber_Overflow(t *testing.T) {
	sub := testSubscriber(t, "test-sub", nil, false, 100)

	assert.False(t, sub.GetAndResetOverflow())

	sub.SetOverflow()
	assert.True(t, sub.GetAndResetOverflow())
	assert.False(t, sub.GetAndResetOverflow())

	// Concurrency test
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sub.SetOverflow()
		}()
	}
	wg.Wait()
	assert.True(t, sub.GetAndResetOverflow())
}

func TestSubscriberManager(t *testing.T) {
	logger := slog.Default() // Use default logger for tests
	mgr := NewSubscriberManager(logger)

	sub1 := testSubscriber(t, "sub1", nil, false, 10)
	mgr.Add(sub1)
	assert.Equal(t, 1, mgr.Count())
	assert.Equal(t, []*Subscriber{sub1}, mgr.All())

	// Test Broadcast
	evt := &events.StoreChangeEvent{
		Backend: "db1",
		EventID: "evt1",
	}
	mgr.Broadcast(evt)

	select {
	case received := <-sub1.ch:
		assert.Equal(t, evt, received)
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for event")
	}

	// Test Overflow
	// Fill the channel
	for i := 0; i < 10; i++ {
		sub1.ch <- evt
	}

	// Broadcast one more, should trigger overflow
	mgr.Broadcast(evt)
	assert.True(t, sub1.GetAndResetOverflow())

	// Test Remove
	mgr.Remove(sub1)
	assert.Equal(t, 0, mgr.Count())

	select {
	case <-sub1.Done():
	// Success, subscriber closed
	case <-time.After(time.Second):
		t.Fatal("Subscriber not closed after remove")
	}
}

func TestSubscriberManager_Race(t *testing.T) {
	mgr := NewSubscriberManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, run := range []func(){
		func() {
			for i := 0; i < 100; i++ {
				sub := testSubscriber(t, "same", nil, false, 10)
				mgr.Add(sub)
				mgr.Remove(sub)
				mgr.Remove(sub)
			}
		},
		func() {
			for i := 0; i < 100; i++ {
				mgr.Broadcast(&events.StoreChangeEvent{})
			}
		},
		func() {
			for i := 0; i < 100; i++ {
				mgr.CloseAll()
			}
		},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			run()
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent manager operations did not finish")
	}
	mgr.CloseAll()
	assert.Zero(t, mgr.Count())
}

func TestSubscriberManager_SameLabel(t *testing.T) {
	for _, label := range []string{"same", ""} {
		t.Run("label="+label, func(t *testing.T) {
			mgr := NewSubscriberManager(nil)
			t.Cleanup(mgr.CloseAll)
			a := testSubscriber(t, label, nil, false, 2)
			b := testSubscriber(t, label, nil, false, 2)
			mgr.Add(a)
			mgr.Add(b)
			mgr.Add(a)
			require.Equal(t, 2, mgr.Count())
			require.ElementsMatch(t, []*Subscriber{a, b}, mgr.All())

			absent := testSubscriber(t, label, nil, false, 2)
			mgr.Remove(absent)
			require.Equal(t, 2, mgr.Count())
			select {
			case <-absent.Done():
				t.Fatal("unregistered subscriber was closed")
			default:
			}

			first := &events.StoreChangeEvent{EventID: "first"}
			mgr.Broadcast(first)
			for _, sub := range []*Subscriber{a, b} {
				select {
				case got := <-sub.Events():
					require.Same(t, first, got)
				case <-time.After(time.Second):
					t.Fatal("subscriber did not receive broadcast")
				}
				assert.Empty(t, sub.ch, "duplicate registration must not duplicate delivery")
			}

			mgr.Remove(a)
			mgr.Remove(a)
			require.Equal(t, 1, mgr.Count())
			require.Equal(t, []*Subscriber{b}, mgr.All())
			select {
			case <-a.Done():
			default:
				t.Fatal("removed subscriber remains open")
			}
			select {
			case <-b.Done():
				t.Fatal("same-label subscriber was closed")
			default:
			}

			second := &events.StoreChangeEvent{EventID: "second"}
			mgr.Broadcast(second)
			select {
			case got := <-b.Events():
				require.Same(t, second, got)
			case <-time.After(time.Second):
				t.Fatal("remaining subscriber did not receive broadcast")
			}
			assert.Empty(t, a.ch, "removed subscriber must not receive new broadcasts")

			mgr.CloseAll()
			mgr.Remove(a)
			mgr.Remove(b)
			require.Zero(t, mgr.Count())
			select {
			case <-b.Done():
			default:
				t.Fatal("CloseAll left subscriber open")
			}
		})
	}
}

func TestSubscriberManager_All(t *testing.T) {
	m := NewSubscriberManager(nil)
	sub1 := testSubscriber(t, "sub1", nil, false, 100)
	sub2 := testSubscriber(t, "sub2", nil, false, 100)

	m.Add(sub1)
	m.Add(sub2)

	all := m.All()
	assert.Len(t, all, 2)
	assert.Contains(t, all, sub1)
	assert.Contains(t, all, sub2)
}

func TestSubscriberManager_CloseAll(t *testing.T) {
	m := NewSubscriberManager(nil)
	sub1 := testSubscriber(t, "sub1", nil, false, 100)
	sub2 := testSubscriber(t, "sub2", nil, false, 100)

	m.Add(sub1)
	m.Add(sub2)

	m.CloseAll()

	assert.Equal(t, 0, m.Count())

	select {
	case <-sub1.Done():
	case <-time.After(time.Second):
		t.Fatal("sub1 not closed")
	}

	select {
	case <-sub2.Done():
	case <-time.After(time.Second):
		t.Fatal("sub2 not closed")
	}
}

func testSubscriber(t *testing.T, id string, after *cursor.ProgressMarker, coalesce bool, size int) *Subscriber {
	t.Helper()
	sub, err := NewSubscriber(id, after, coalesce, size)
	require.NoError(t, err)
	return sub
}

func TestSubscriberResumedGroupAcknowledgesEachIdentityIndependently(t *testing.T) {
	boundary := cursor.NewProgressMarker()
	boundary.SetPosition("a", "8-3-z")
	sub := testSubscriber(t, "resume", boundary, false, 8)
	group := events.ClusterTime{T: 8, I: 3}
	require.False(t, sub.ShouldSend("a", "7-9-before", events.ClusterTime{T: 7, I: 9}))
	for _, id := range []string{"8-3-z", "8-3-a", "8-3-m"} {
		require.True(t, sub.ShouldSend("a", id, group))
		require.True(t, sub.ShouldSend("a", id, group), "checking eligibility does not acknowledge delivery")
		sub.UpdatePosition("a", id, group)
		require.False(t, sub.ShouldSend("a", id, group))
	}
	require.Equal(t, "8-3-m", sub.CurrentProgress().Positions["a"])
	require.True(t, sub.ShouldSend("b", "8-3-z", group), "backend histories are independent")
	sub.UpdatePosition("a", "8-4-later", events.ClusterTime{T: 8, I: 4})
	require.False(t, sub.ShouldSend("a", "8-3-unseen", group), "a completed older timestamp group cannot arrive after a newer group")
	require.False(t, sub.ShouldSend("a", "8-4-later", events.ClusterTime{T: 8, I: 4}))
	require.True(t, sub.ShouldSend("a", "8-4-sibling", events.ClusterTime{T: 8, I: 4}))
}

func TestSubscriberRejectsInvalidResumeIdentityBeforeAdmission(t *testing.T) {
	marker := cursor.NewProgressMarker()
	marker.SetPosition("source", "invalid-event-id")
	subscriber, err := NewSubscriber("invalid", marker, false, 1)
	require.Error(t, err)
	require.Nil(t, subscriber)
	marker.SetPosition("source", "")
	subscriber, err = NewSubscriber("empty-source", marker, false, 1)
	require.NoError(t, err)
	require.True(t, subscriber.ShouldSend("source", "1-1-a", events.ClusterTime{T: 1, I: 1}))
}
