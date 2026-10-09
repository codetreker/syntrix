package recovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/codetreker/syntrix/internal/puller/events"
)

func TestGapDetector_FirstEvent(t *testing.T) {
	g := NewGapDetector(GapDetectorOptions{})

	evt := &events.StoreChangeEvent{
		EventID: "evt-1",
		ClusterTime: events.ClusterTime{
			T: uint32(time.Now().Unix()),
			I: 1,
		},
	}

	if g.RecordEvent(evt) {
		t.Error("RecordEvent should return false for first event")
	}
	if g.GapsDetected() != 0 {
		t.Errorf("GapsDetected() = %d, want 0", g.GapsDetected())
	}
}

func TestGapDetector_NoGap(t *testing.T) {
	g := NewGapDetector(GapDetectorOptions{
		Threshold: 5 * time.Minute,
	})

	now := time.Now()

	evt1 := &events.StoreChangeEvent{
		EventID:     "evt-1",
		ClusterTime: events.ClusterTime{T: uint32(now.Unix()), I: 1},
	}
	g.RecordEvent(evt1)

	// 1 minute later - no gap
	evt2 := &events.StoreChangeEvent{
		EventID:     "evt-2",
		ClusterTime: events.ClusterTime{T: uint32(now.Add(time.Minute).Unix()), I: 1},
	}

	if g.RecordEvent(evt2) {
		t.Error("RecordEvent should return false for no gap")
	}
}

func TestGapDetector_GapDetected(t *testing.T) {
	g := NewGapDetector(GapDetectorOptions{
		Threshold: 5 * time.Minute,
	})

	now := time.Now()

	evt1 := &events.StoreChangeEvent{
		EventID:     "evt-1",
		ClusterTime: events.ClusterTime{T: uint32(now.Unix()), I: 1},
	}
	g.RecordEvent(evt1)

	// 10 minutes later - gap!
	evt2 := &events.StoreChangeEvent{
		EventID:     "evt-2",
		ClusterTime: events.ClusterTime{T: uint32(now.Add(10 * time.Minute).Unix()), I: 1},
	}

	if !g.RecordEvent(evt2) {
		t.Error("RecordEvent should return true for gap")
	}
	if g.GapsDetected() != 1 {
		t.Errorf("GapsDetected() = %d, want 1", g.GapsDetected())
	}
}

func TestGapDetector_Reset(t *testing.T) {
	g := NewGapDetector(GapDetectorOptions{})

	now := time.Now()

	evt1 := &events.StoreChangeEvent{
		EventID:     "evt-1",
		ClusterTime: events.ClusterTime{T: uint32(now.Unix()), I: 1},
	}
	g.RecordEvent(evt1)

	evt2 := &events.StoreChangeEvent{
		EventID:     "evt-2",
		ClusterTime: events.ClusterTime{T: uint32(now.Add(10 * time.Minute).Unix()), I: 1},
	}
	g.RecordEvent(evt2)

	g.Reset()
	if g.GapsDetected() != 0 {
		t.Errorf("GapsDetected() = %d, want 0 after reset", g.GapsDetected())
	}
}

func TestRecoveryAction_String(t *testing.T) {
	tests := []struct {
		action Action
		want   string
	}{
		{ActionNone, "none"},
		{ActionReconnect, "reconnect"},
		{ActionRestart, "restart"},
		{ActionFatal, "fatal"},
		{Action(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.action.String(); got != tt.want {
				t.Errorf("String() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestHandler_HandleError_Nil(t *testing.T) {
	h := NewHandler(HandlerOptions{})

	action := h.HandleError(nil)
	if action != ActionNone {
		t.Errorf("HandleError(nil) = %s, want none", action)
	}
}

func TestHandler_HandleError_ResumeToken(t *testing.T) {
	h := NewHandler(HandlerOptions{})

	err := errors.New("resume token was not found")
	action := h.HandleError(err)
	if action != ActionFatal {
		t.Errorf("HandleError() = %s, want fatal", action)
	}
	if h.ResumeTokenErrors() != 1 {
		t.Errorf("ResumeTokenErrors() = %d, want 1", h.ResumeTokenErrors())
	}
}

func TestHandler_HandleError_Transient(t *testing.T) {
	h := NewHandler(HandlerOptions{})

	err := errors.New("connection reset by peer")
	action := h.HandleError(err)
	if action != ActionReconnect {
		t.Errorf("HandleError() = %s, want reconnect", action)
	}
}

func TestHandler_HandleError_MaxConsecutive(t *testing.T) {
	h := NewHandler(HandlerOptions{
		MaxConsecutiveErrors: 3,
	})

	err := errors.New("some unknown error")

	// First 2 should reconnect
	for i := 0; i < 2; i++ {
		action := h.HandleError(err)
		if action != ActionReconnect {
			t.Errorf("HandleError() = %s, want reconnect", action)
		}
	}

	// 3rd should be fatal
	action := h.HandleError(err)
	if action != ActionFatal {
		t.Errorf("HandleError() = %s, want fatal", action)
	}
}

func TestHandler_ResetErrorCount(t *testing.T) {
	h := NewHandler(HandlerOptions{
		MaxConsecutiveErrors: 3,
	})

	err := errors.New("some error")
	h.HandleError(err)
	h.HandleError(err)

	h.ResetErrorCount()

	// After reset, should start counting again
	action := h.HandleError(err)
	if action != ActionReconnect {
		t.Errorf("HandleError() = %s after reset, want reconnect", action)
	}
}

type mockCheckpoint struct {
	deleted   bool
	deleteErr error
}

func (m *mockCheckpoint) DeleteCheckpoint() error {
	m.deleted = true
	return m.deleteErr
}

func TestHandler_RecoverFromResumeTokenError(t *testing.T) {
	mock := &mockCheckpoint{}
	h := NewHandler(HandlerOptions{
		Checkpoint: mock,
	})

	err := h.RecoverFromResumeTokenError(context.Background())
	if err != nil {
		t.Errorf("RecoverFromResumeTokenError() error = %v", err)
	}
	if !mock.deleted {
		t.Error("checkpoint.Delete() should have been called")
	}
}

func TestHandler_RecoverFromResumeTokenError_NilCheckpoint(t *testing.T) {
	h := NewHandler(HandlerOptions{
		Checkpoint: nil,
	})

	err := h.RecoverFromResumeTokenError(context.Background())
	if err != nil {
		t.Errorf("RecoverFromResumeTokenError() error = %v", err)
	}
}
