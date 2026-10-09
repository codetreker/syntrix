package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthStatus(t *testing.T) {
	t.Parallel()
	if StatusOK != "ok" {
		t.Errorf("StatusOK = %q, want 'ok'", StatusOK)
	}
	if StatusDegraded != "degraded" {
		t.Errorf("StatusDegraded = %q, want 'degraded'", StatusDegraded)
	}
	if StatusUnhealthy != "unhealthy" {
		t.Errorf("StatusUnhealthy = %q, want 'unhealthy'", StatusUnhealthy)
	}
}

func TestChecker_NewChecker(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	if h == nil {
		t.Fatal("NewChecker returned nil")
	}
	if h.startedAt.IsZero() {
		t.Error("startedAt should be set")
	}
}

func TestChecker_RegisterBackend(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")

	report := h.GetReport()
	if len(report.Backends) != 1 {
		t.Errorf("Backends count = %d, want 1", len(report.Backends))
	}
	if report.Backends[0].Name != "backend-1" {
		t.Errorf("Backend name = %q, want 'backend-1'", report.Backends[0].Name)
	}
	if report.Backends[0].Status != StatusOK {
		t.Errorf("Backend status = %q, want 'ok'", report.Backends[0].Status)
	}
}

func TestChecker_RecordEvent(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")

	h.RecordEvent("backend-1")
	h.RecordEvent("backend-1")

	report := h.GetReport()
	if len(report.Backends) != 1 {
		t.Fatalf("Backends count = %d, want 1", len(report.Backends))
	}
	if report.Backends[0].EventsTotal != 2 {
		t.Errorf("EventsTotal = %d, want 2", report.Backends[0].EventsTotal)
	}
	if report.Backends[0].LastEvent == nil {
		t.Error("LastEvent should be set")
	}
}

func TestChecker_RecordError(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")

	// Less than 5 errors - should stay OK
	for i := 0; i < 5; i++ {
		h.RecordError("backend-1")
	}
	report := h.GetReport()
	if report.Backends[0].Status != StatusOK {
		t.Errorf("Status after 5 errors = %q, want 'ok'", report.Backends[0].Status)
	}

	// 6th error should set to degraded
	h.RecordError("backend-1")
	report = h.GetReport()
	if report.Backends[0].Status != StatusDegraded {
		t.Errorf("Status after 6 errors = %q, want 'degraded'", report.Backends[0].Status)
	}
	if report.Backends[0].Errors != 6 {
		t.Errorf("Errors = %d, want 6", report.Backends[0].Errors)
	}
}

func TestChecker_RecordGap(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")

	h.RecordGap("backend-1")
	h.RecordGap("backend-1")

	report := h.GetReport()
	if report.Backends[0].GapsDetected != 2 {
		t.Errorf("GapsDetected = %d, want 2", report.Backends[0].GapsDetected)
	}
}

func TestChecker_SetConsumerCount(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.SetConsumerCount(5)

	report := h.GetReport()
	if report.Consumers != 5 {
		t.Errorf("Consumers = %d, want 5", report.Consumers)
	}
}

func TestChecker_SetBufferSize(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.SetBufferSize(1000)

	report := h.GetReport()
	if report.BufferSize != 1000 {
		t.Errorf("BufferSize = %d, want 1000", report.BufferSize)
	}
}

func TestChecker_GetReport_AggregateStatus(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")
	h.RegisterBackend("backend-2")

	// All OK
	report := h.GetReport()
	if report.Status != StatusOK {
		t.Errorf("Status = %q, want 'ok'", report.Status)
	}

	// Make one degraded
	for i := 0; i < 6; i++ {
		h.RecordError("backend-1")
	}
	report = h.GetReport()
	if report.Status != StatusDegraded {
		t.Errorf("Status = %q, want 'degraded'", report.Status)
	}
}

func TestChecker_Check(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")

	status := h.Check()
	if status != StatusOK {
		t.Errorf("Check() = %q, want 'ok'", status)
	}
}

func TestChecker_ServeHTTP_OK(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Status code = %d, want %d", w.Code, http.StatusOK)
	}

	var report Report
	if err := json.NewDecoder(w.Body).Decode(&report); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}
	if report.Status != StatusOK {
		t.Errorf("Report status = %q, want 'ok'", report.Status)
	}
}

func TestChecker_ServeHTTP_Degraded(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)
	h.RegisterBackend("backend-1")
	for i := 0; i < 10; i++ {
		h.RecordError("backend-1")
	}

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	// Degraded still returns 200
	if w.Code != http.StatusOK {
		t.Errorf("Status code = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestChecker_RecordEvent_UnknownBackend(t *testing.T) {
	t.Parallel()
	h := NewChecker(nil)

	// Should not panic
	h.RecordEvent("unknown-backend")
	h.RecordError("unknown-backend")
	h.RecordGap("unknown-backend")
}
