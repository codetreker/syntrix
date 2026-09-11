package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
	"github.com/syntrixbase/syntrix/internal/puller/normalizer"
)

// ErrCaptureUnavailable denotes capture that may resume without rebuilding history.
var ErrCaptureUnavailable = errors.New("source capture temporarily unavailable")

func (b *Backend) setCaptureState(active bool, err error) {
	b.captureMu.Lock()
	defer b.captureMu.Unlock()
	b.captureActive, b.captureError = active, err
	if b.captureChanged != nil {
		close(b.captureChanged)
	}
	b.captureChanged = make(chan struct{})
}

func (b *Backend) waitCapture(ctx context.Context) error {
	for {
		b.captureMu.Lock()
		if b.captureChanged == nil {
			b.captureChanged = make(chan struct{})
		}
		active, err, changed := b.captureActive, b.captureError, b.captureChanged
		b.captureMu.Unlock()
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
	return p.replay(ctx, pm.Positions, pm.Lineages, coalesce)
}
