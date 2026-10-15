package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/codetreker/syntrix/internal/puller/cursor"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/internal/puller/normalizer"
)

// ErrCaptureUnavailable denotes capture that may resume without rebuilding history.
var ErrCaptureUnavailable = errors.New("source capture temporarily unavailable")

func (p *Puller) TerminalError() error {
	for name, b := range p.backends {
		b.captureMu.Lock()
		err := b.captureError
		b.captureMu.Unlock()
		if err != nil && !errors.Is(err, ErrCaptureUnavailable) && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("backend %q capture: %w", name, err)
		}
	}
	return nil
}

// Readiness requires the native idle barrier and a durable usable source buffer.
func (p *Puller) Readiness(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.lifetime.Err(); err != nil {
		return err
	}
	if len(p.backends) == 0 {
		return fmt.Errorf("%w: no capture backends configured", ErrCaptureUnavailable)
	}
	checkCapture := func(name string, b *Backend) error {
		active, captureErr, _ := b.captureState()
		if !active {
			if captureErr != nil {
				return fmt.Errorf("backend %q: %w", name, captureErr)
			}
			return fmt.Errorf("%w: backend %q not admitted", ErrCaptureUnavailable, name)
		}
		return nil
	}
	for name, b := range p.backends {
		if err := checkCapture(name, b); err != nil {
			return err
		}
		token, err := b.buffer.LoadCheckpoint()
		if err != nil {
			return fmt.Errorf("backend %q checkpoint: %w", name, err)
		}
		if len(token) == 0 {
			return fmt.Errorf("%w: backend %q missing checkpoint", ErrCaptureUnavailable, name)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := p.lifetime.Err(); err != nil {
		return err
	}
	for name, b := range p.backends {
		if err := checkCapture(name, b); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) setCaptureState(active bool, err error) {
	b.captureMu.Lock()
	defer b.captureMu.Unlock()
	b.captureActive, b.captureError = active, err
	if b.captureChanged != nil {
		close(b.captureChanged)
	}
	b.captureChanged = make(chan struct{})
}

func (b *Backend) recordCaptureResponse(at time.Time) {
	b.captureMu.Lock()
	defer b.captureMu.Unlock()
	b.captureResponse = at
}

func (b *Backend) captureState() (bool, error, <-chan struct{}) {
	b.captureMu.Lock()
	defer b.captureMu.Unlock()
	if b.captureChanged == nil {
		b.captureChanged = make(chan struct{})
	}
	active, err := b.captureActive, b.captureError
	if active && time.Since(b.captureResponse) >= events.AdmissionFreshness {
		active, err = false, ErrCaptureUnavailable
	}
	return active, err, b.captureChanged
}

func (b *Backend) waitCapture(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		active, err, changed := b.captureState()
		if active {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// BootstrapBoundary requires quiesced writers and explicitly reset event buffers.
// The returned marker also identifies empty backends, so reconnect requests replay.
func (p *Puller) BootstrapBoundary(ctx context.Context) (string, error) {
	if len(p.backends) == 0 {
		return "", fmt.Errorf("no capture backends configured")
	}
	pm := cursor.NewProgressMarker()
	pm.Lineages = make(map[string]string, len(p.backends))
	for name, b := range p.backends {
		if err := b.waitCapture(ctx); err != nil {
			return "", fmt.Errorf("backend %q capture unavailable: %w", name, err)
		}
		if err := b.buffer.Flush(ctx); err != nil {
			return "", err
		}
		if err := b.buffer.ValidatePosition("", b.buffer.Lineage()); err != nil {
			return "", err
		}
		head, err := b.buffer.Head()
		if err != nil {
			return "", err
		}
		if head != "" {
			return "", fmt.Errorf("backend %q buffer is not empty; explicit offline buffer reset required", name)
		}
		token, err := b.buffer.LoadCheckpoint()
		if err != nil {
			return "", err
		}
		if len(token) == 0 {
			return "", fmt.Errorf("backend %q has no durable source boundary", name)
		}
		pm.Positions[name] = ""
		pm.Lineages[name] = b.buffer.Lineage()
	}
	return pm.Encode(), nil
}

func (p *Puller) parseBoundary(after string) (*cursor.ProgressMarker, error) {
	pm, err := cursor.DecodeProgressMarker(after)
	if err != nil {
		return nil, err
	}
	if len(pm.Positions) != len(p.backends) || len(pm.Lineages) != len(p.backends) || len(p.backends) == 0 {
		return nil, fmt.Errorf("boundary backend set does not match capture configuration")
	}
	for name := range p.backends {
		if _, ok := pm.Positions[name]; !ok {
			return nil, fmt.Errorf("boundary missing backend %q", name)
		}
		if pm.Lineages[name] == "" {
			return nil, fmt.Errorf("boundary missing lineage for backend %q", name)
		}
	}
	return pm, nil
}

func boundaryKey(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	ct, err := normalizer.ParseEventID(id)
	if err != nil {
		return "", err
	}
	return events.FormatBufferKey(ct, id), nil
}

// ValidateBoundary checks source readiness, buffer lineage, and retained history.
func (p *Puller) ValidateBoundary(ctx context.Context, after string) error {
	pm, err := p.parseBoundary(after)
	if err != nil {
		return err
	}
	for name, b := range p.backends {
		if err := b.waitCapture(ctx); err != nil {
			return fmt.Errorf("backend %q capture unavailable: %w", name, err)
		}
		key, err := boundaryKey(pm.Positions[name])
		if err != nil {
			return err
		}
		if err := b.buffer.ValidatePosition(key, pm.Lineages[name]); err != nil {
			return fmt.Errorf("backend %q: %w", name, err)
		}
	}
	return nil
}

// ReplayBoundary opens validated replay snapshots without a retention race.
func (p *Puller) ReplayBoundary(ctx context.Context, after string, coalesce bool) (events.Iterator, error) {
	pm, err := p.parseBoundary(after)
	if err != nil {
		return nil, err
	}
	return p.replay(ctx, pm.Positions, pm.Lineages, nil, coalesce, false)
}
