// Package core implements the local puller service.
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/codetreker/syntrix/internal/puller/buffer"
	"github.com/codetreker/syntrix/internal/puller/config"
	"github.com/codetreker/syntrix/internal/puller/cursor"
	"github.com/codetreker/syntrix/internal/puller/events"
	"github.com/codetreker/syntrix/internal/puller/flowcontrol"
	"github.com/codetreker/syntrix/internal/puller/metrics"
	"github.com/codetreker/syntrix/internal/puller/normalizer"
	"github.com/codetreker/syntrix/internal/puller/recovery"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// EventHandler is a function that handles events from the change stream.
// Note: This type must match the signature in grpc.EventSource interface.
type EventHandler = func(ctx context.Context, backendName string, event *events.StoreChangeEvent) error

// Retrying a local processing failure could reopen from_now before any token is saved.
var errCaptureFailed = errors.New("capture failed")

const (
	captureMaxAwait    = events.AdmissionRefresh
	capturePollTimeout = 2 * captureMaxAwait
)

// Backend represents a single MongoDB backend being watched.
type Backend struct {
	name            string
	client          *mongo.Client
	db              *mongo.Database
	config          config.PullerBackendConfig
	normalizer      *normalizer.Normalizer
	buffer          *buffer.Buffer
	gapDetector     *recovery.GapDetector
	recoveryHandler *recovery.Handler
	backpressure    *flowcontrol.BackpressureMonitor
	cleaner         *buffer.Cleaner

	// eventChan receives normalized events from the change stream
	eventChan       chan *events.StoreChangeEvent
	captureMu       sync.Mutex
	captureChanged  chan struct{}
	captureActive   bool
	captureError    error
	captureResponse time.Time
}

// Puller is the main puller service that watches MongoDB change streams
// and distributes events to consumers.
type Puller struct {
	cfg      config.Config
	backends map[string]*Backend
	logger   *slog.Logger

	// eventHandler is called for each normalized event
	eventHandler EventHandler

	// subs manages active subscribers
	subs *SubscriberManager

	// wg tracks running goroutines
	wg sync.WaitGroup

	// cancelMu protects cancel
	cancelMu sync.Mutex

	// cancel function to stop all backends
	cancel       context.CancelFunc
	lifetime     context.Context
	stopLifetime context.CancelFunc
	stopping     bool
	stopDone     chan struct{}

	// watchFunc is the function used to watch the change stream.
	// It can be replaced for testing purposes.
	watchFunc func(ctx context.Context, backend *Backend, logger *slog.Logger) error

	// openStream allows tests to inject a custom change stream implementation.
	openStream func(ctx context.Context, db *mongo.Database, pipeline mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error)

	// retryDelay is the time to wait before retrying after an error.
	retryDelay time.Duration

	// backpressureSlowDownDelay is the time to sleep when backpressure action is SlowDown.
	backpressureSlowDownDelay time.Duration

	// backpressurePauseDelay is the time to sleep when backpressure action is Pause.
	backpressurePauseDelay time.Duration
}

// New creates a new Puller instance.
func New(cfg config.Config, logger *slog.Logger) *Puller {
	if logger == nil {
		logger = slog.Default()
	}
	lifetime, stopLifetime := context.WithCancel(context.Background())
	p := &Puller{
		cfg:                       cfg,
		backends:                  make(map[string]*Backend),
		logger:                    logger.With("component", "puller"),
		retryDelay:                time.Second,
		backpressureSlowDownDelay: 100 * time.Millisecond,
		backpressurePauseDelay:    1 * time.Second,
		lifetime:                  lifetime, stopLifetime: stopLifetime,
	}
	p.subs = NewSubscriberManager(p.logger)
	p.watchFunc = p.watchChangeStream
	p.openStream = openMongoChangeStream
	return p
}

// changeStream defines the subset of mongo.ChangeStream used by the puller.
type changeStream interface {
	TryNext(context.Context) bool
	RemainingBatchLength() int
	ID() int64
	ResumeToken() bson.Raw
	Decode(any) error
	Err() error
	Close(context.Context) error
}

func openMongoChangeStream(ctx context.Context, db *mongo.Database, pipeline mongo.Pipeline, opts *options.ChangeStreamOptions) (changeStream, error) {
	return db.Watch(ctx, pipeline, opts)
}

// SetRetryDelay sets the retry delay for testing purposes.
func (p *Puller) SetRetryDelay(d time.Duration) {
	p.retryDelay = d
}

// SetBackpressureSlowDownDelay sets the slow down delay for testing purposes.
func (p *Puller) SetBackpressureSlowDownDelay(d time.Duration) {
	p.backpressureSlowDownDelay = d
}

// SetBackpressurePauseDelay sets the pause delay for testing purposes.
func (p *Puller) SetBackpressurePauseDelay(d time.Duration) {
	p.backpressurePauseDelay = d
}

// SetEventHandler sets the event handler for processing events.
func (p *Puller) SetEventHandler(handler EventHandler) {
	p.eventHandler = handler
}

// AddBackend adds a MongoDB backend to watch.
func (p *Puller) AddBackend(name string, client *mongo.Client, dbName string, cfg config.PullerBackendConfig) error {
	if _, exists := p.backends[name]; exists {
		return fmt.Errorf("backend %q already exists", name)
	}

	logger := p.logger.With("backend", name)
	db := client.Database(dbName)
	buf, err := buffer.New(buffer.Options{
		Path:          filepath.Join(p.cfg.Buffer.Path, name),
		Logger:        logger,
		BatchSize:     p.cfg.Buffer.BatchSize,
		BatchInterval: p.cfg.Buffer.BatchInterval,
		QueueSize:     p.cfg.Buffer.QueueSize,
	})
	if err != nil {
		return fmt.Errorf("failed to create buffer: %w", err)
	}

	gapDetector := recovery.NewGapDetector(recovery.GapDetectorOptions{
		Logger: logger,
	})

	recoveryHandler := recovery.NewHandler(recovery.HandlerOptions{
		Checkpoint:           buf,
		MaxConsecutiveErrors: 10, // TODO: Make configurable
		Logger:               logger,
	})

	maxSize, err := parseSize(p.cfg.Buffer.MaxSize)
	if err != nil {
		logger.Warn("invalid buffer max size, using unlimited", "error", err)
		maxSize = 0
	}

	cleaner := buffer.NewCleaner(buffer.CleanerOptions{
		Buffer:    buf,
		Retention: p.cfg.Cleaner.Retention,
		MaxSize:   maxSize,
		Interval:  p.cfg.Cleaner.Interval,
		Logger:    logger,
	})

	bpMonitor := flowcontrol.NewBackpressureMonitor(flowcontrol.BackpressureOptions{
		PublishLatency: metrics.PublishLatency,
		QueueDepth:     metrics.QueueDepth,
	})

	p.backends[name] = &Backend{
		name:            name,
		client:          client,
		db:              db,
		config:          cfg,
		normalizer:      normalizer.New(),
		buffer:          buf,
		gapDetector:     gapDetector,
		recoveryHandler: recoveryHandler,
		backpressure:    bpMonitor,
		cleaner:         cleaner,
		eventChan:       make(chan *events.StoreChangeEvent, 1000),
		captureChanged:  make(chan struct{}),
	}

	p.logger.Info("added backend", "name", name, "database", dbName)
	return nil
}

// Start starts watching all backends.
func (p *Puller) Start(ctx context.Context) error {
	if len(p.backends) == 0 {
		return fmt.Errorf("no backends configured")
	}

	p.cancelMu.Lock()
	defer p.cancelMu.Unlock()
	if p.stopping {
		return errors.New("puller is stopped")
	}
	if p.cancel != nil {
		return errors.New("puller already started")
	}
	ctx, p.cancel = context.WithCancel(ctx)

	for name, backend := range p.backends {
		p.wg.Add(1)
		go p.runBackend(ctx, name, backend)
	}

	p.logger.Info("puller started", "backends", len(p.backends))
	return nil
}

// Stop stops all backends gracefully.
func (p *Puller) Stop(ctx context.Context) error {
	p.cancelMu.Lock()
	if !p.stopping {
		p.stopping = true
		p.stopLifetime()
		if p.cancel != nil {
			p.cancel()
		}
		p.stopDone = make(chan struct{})
		go func() { p.subs.CloseAll(); p.wg.Wait(); close(p.stopDone) }()
	}
	done := p.stopDone
	p.cancelMu.Unlock()

	select {
	case <-done:
		p.logger.Info("puller stopped gracefully")
	case <-ctx.Done():
		p.logger.Warn("puller stop timed out")
		return ctx.Err()
	}

	// Captures and subscription iterators share these buffers. Their producer
	// lifetimes must settle before a physical close, including fatal capture exits.
	var closeErr error
	for name, backend := range p.backends {
		if backend.buffer != nil {
			if err := backend.buffer.Close(); err != nil {
				closeErr = errors.Join(closeErr, fmt.Errorf("failed to close backend %q buffer: %w", name, err))
			}
		}
	}

	return errors.Join(ctx.Err(), closeErr)
}

// runBackend runs the change stream consumer for a single backend.
func (p *Puller) runBackend(ctx context.Context, name string, backend *Backend) {
	defer p.wg.Done()

	logger := p.logger.With("backend", name)
	logger.Info("starting backend")

	backend.cleaner.Start(ctx)
	defer backend.cleaner.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("backend stopped")
			return
		default:
		}

		err := p.watchFunc(ctx, backend, logger)
		if err != nil {
			if ctx.Err() != nil {
				// Context cancelled, normal shutdown
				return
			}
			if errors.Is(err, errCaptureFailed) {
				backend.setCaptureState(false, err)
				logger.Error("capture failed, stopping backend", "error", err)
				return
			}
			if errors.Is(err, ErrCaptureUnavailable) {
				backend.setCaptureState(false, err)
				if !waitCaptureRetry(ctx, p.retryDelay) {
					return
				}
				continue
			}

			action := backend.recoveryHandler.HandleError(err)
			switch action {
			case recovery.ActionRestart, recovery.ActionFatal:
				backend.setCaptureState(false, err)
				logger.Error("fatal error, stopping backend", "error", err)
				return
			case recovery.ActionReconnect:
				backend.setCaptureState(false, fmt.Errorf("%w: %w", ErrCaptureUnavailable, err))
				logger.Warn("transient error, reconnecting", "error", err)
				if !waitCaptureRetry(ctx, p.retryDelay) {
					return
				}
			}
		} else {
			// Reset error count on successful run (if watchChangeStream returns nil, it means it finished normally, e.g. context done)
			backend.recoveryHandler.ResetErrorCount()
		}
	}
}

func waitCaptureRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// watchChangeStream watches the MongoDB change stream for a backend.
func (p *Puller) watchChangeStream(ctx context.Context, backend *Backend, logger *slog.Logger) (result error) {
	defer func() {
		stateErr := result
		if stateErr != nil && !errors.Is(stateErr, errCaptureFailed) {
			stateErr = fmt.Errorf("%w: %w", ErrCaptureUnavailable, stateErr)
		}
		backend.setCaptureState(false, stateErr)
	}()
	// Load resume token if exists
	resumeToken, err := backend.buffer.LoadCheckpoint()
	if err != nil {
		return fmt.Errorf("%w: failed to load checkpoint: %w", errCaptureFailed, err)
	}

	// Build watch pipeline for collection filtering
	pipeline := p.buildWatchPipeline(backend.config)

	// Configure watch options
	opts := options.ChangeStream().
		SetFullDocument(options.UpdateLookup).SetMaxAwaitTime(captureMaxAwait)

	if resumeToken != nil {
		opts.SetResumeAfter(resumeToken)
		logger.Info("resuming from checkpoint")
	} else {
		logger.Info("starting fresh (no checkpoint)")
		if p.cfg.Bootstrap.Mode == "from_beginning" {
			opts.SetStartAtOperationTime(&primitive.Timestamp{T: 1, I: 1})
		}
	}

	// Start watching at database level
	stream, err := p.openStream(ctx, backend.db, pipeline, opts)
	if err != nil {
		return fmt.Errorf("failed to open change stream: %w", err)
	}
	backend.recordCaptureResponse(time.Now())
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), capturePollTimeout)
		defer cancel()
		result = errors.Join(result, stream.Close(closeCtx))
	}()

	logger.Info("change stream opened")

	// Only an empty live batch establishes that the native cursor has caught up.
	// A cached ResumeAfter token alone can precede an unconsumed initial batch.
	ready := false
	firstPoll := true
	for {
		// The first poll consumes the aggregate's cached batch. Later polls with
		// no cached documents fetch getMore; draining a batch proves no new response.
		response := !firstPoll && stream.RemainingBatchLength() == 0
		pollCtx, cancel := context.WithTimeout(ctx, capturePollTimeout)
		hasNext := stream.TryNext(pollCtx)
		streamErr, pollErr := stream.Err(), pollCtx.Err()
		cancel()
		if pollErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return fmt.Errorf("%w: native response deadline: %w", ErrCaptureUnavailable, pollErr)
		}
		if streamErr != nil {
			return fmt.Errorf("change stream error: %w", streamErr)
		}
		if response && (hasNext || stream.ID() != 0) {
			backend.recordCaptureResponse(time.Now())
		}
		firstPoll = false
		if !hasNext {
			if stream.ID() == 0 {
				return fmt.Errorf("change stream closed")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !ready {
				token := stream.ResumeToken()
				if len(token) == 0 {
					return fmt.Errorf("%w: source returned no resume boundary", errCaptureFailed)
				}
				if err := backend.buffer.Flush(ctx); err != nil {
					return fmt.Errorf("%w: flush source boundary: %w", errCaptureFailed, err)
				}
				if err := backend.buffer.SaveCheckpoint(token); err != nil {
					return fmt.Errorf("%w: save source boundary: %w", errCaptureFailed, err)
				}
				backend.setCaptureState(true, nil)
				ready = true
			}
			continue
		}

		var raw normalizer.RawEvent
		if err := stream.Decode(&raw); err != nil {
			return fmt.Errorf("%w: failed to decode event: %w", errCaptureFailed, err)
		}

		// Normalize event
		evt, err := backend.normalizer.Normalize(&raw)
		if err != nil {
			return fmt.Errorf("%w: failed to normalize event: %w", errCaptureFailed, err)
		}
		evt.Backend = backend.name

		logger.Info("Puller: received event from mongo",
			"eventID", evt.EventID,
			"op", evt.OpType,
			"mgoid", evt.MgoDocID,
		)

		// Check for gaps
		backend.gapDetector.RecordEvent(evt)

		if err := backend.buffer.Write(ctx, evt, raw.ResumeToken); err != nil {
			return fmt.Errorf("%w: failed to write event to buffer: %w", errCaptureFailed, err)
		}
		logger.Debug("Puller: buffered event", "eventID", evt.EventID)

		p.invokeHandlerWithBackpressure(ctx, backend, evt)
	}

}

func (p *Puller) invokeHandlerWithBackpressure(ctx context.Context, backend *Backend, evt *events.StoreChangeEvent) {
	p.logger.Debug("Puller: broadcasting event", "eventID", evt.EventID)

	// Broadcast to subscribers
	p.subs.Broadcast(evt)

	if p.eventHandler != nil {
		start := time.Now()
		if err := p.eventHandler(ctx, backend.name, evt); err != nil {
			p.logger.Error("failed to handle event", "error", err)
			// Continue processing other events
		}
		latency := time.Since(start)

		// Handle backpressure
		action := backend.backpressure.HandleBackpressure(latency)
		switch action {
		case flowcontrol.ActionSlowDown:
			time.Sleep(p.backpressureSlowDownDelay)
		case flowcontrol.ActionPause:
			p.logger.Warn("pausing due to high backpressure")
			metrics.BackpressureEvents.WithLabelValues(backend.name, "pause").Inc()
			time.Sleep(p.backpressurePauseDelay)
		}
	}
}

// buildWatchPipeline builds the MongoDB aggregation pipeline for collection filtering.
func (p *Puller) buildWatchPipeline(cfg config.PullerBackendConfig) mongo.Pipeline {
	if len(cfg.Collections) == 0 {
		return nil
	}

	return mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"ns.coll": bson.M{"$in": cfg.Collections},
		}}},
	}
}

// BackendNames returns the names of all configured backends.
func (p *Puller) BackendNames() []string {
	names := make([]string, 0, len(p.backends))
	for name := range p.backends {
		names = append(names, name)
	}
	return names
}

// Replay returns an iterator that replays events from the given progress marker.
// If the marker is empty, it replays from the beginning of the buffer.
func (p *Puller) Replay(ctx context.Context, after map[string]string, coalesce bool) (events.Iterator, error) {
	return p.replay(ctx, after, nil, nil, coalesce, false)
}

// ReplayFromAdmission opens raw replay at each backend's first post-registration
// broadcast group or later delivered progress. A backend absent from
// firstBroadcast has no retained events to recover.
func (p *Puller) ReplayFromAdmission(ctx context.Context, after map[string]string, firstBroadcast map[string]events.ClusterTime) (events.Iterator, error) {
	if firstBroadcast == nil {
		return nil, fmt.Errorf("admission replay requires backend floors")
	}
	return p.replay(ctx, after, nil, firstBroadcast, false, false)
}

// ReplayRetained validates every required saved position or original broadcast
// floor before any surviving suffix can advance consumer progress.
func (p *Puller) ReplayRetained(ctx context.Context, progress *cursor.ProgressMarker, firstBroadcast map[string]events.ClusterTime) (events.Iterator, error) {
	for name := range progress.Positions {
		if _, ok := p.backends[name]; !ok {
			return nil, fmt.Errorf("requested replay backend %q is not configured", name)
		}
	}
	for name := range firstBroadcast {
		if _, ok := p.backends[name]; !ok {
			return nil, fmt.Errorf("requested recovery backend %q is not configured", name)
		}
	}
	return p.replay(ctx, progress.Positions, progress.Lineages, firstBroadcast, false, true)
}

func (p *Puller) replay(ctx context.Context, after, lineages map[string]string, firstBroadcast map[string]events.ClusterTime, coalesce, retained bool) (events.Iterator, error) {
	var iters []events.Iterator

	p.logger.Info("Replay called", "after", after, "coalesce", coalesce)

	for name, backend := range p.backends {
		floor, admitted := firstBroadcast[name]
		if firstBroadcast != nil && !admitted {
			continue
		}
		startID, groupStart := "", ""
		if after != nil {
			eventID := after[name]
			if eventID != "" {
				ct, err := normalizer.ParseEventID(eventID)
				if err != nil {
					// Close already opened iterators
					for _, it := range iters {
						it.Close()
					}
					return nil, fmt.Errorf("invalid event ID %q for backend %q: %w", eventID, name, err)
				}
				startID = events.FormatBufferKey(ct, eventID)
				groupStart = events.FormatBufferKey(ct, "")
				p.logger.Debug("Replay backend", "backend", name, "eventID", eventID, "startID", startID)
			}
		}
		savedGroup := groupStart
		if firstBroadcast != nil {
			admissionStart := events.FormatBufferKey(floor, "")
			if admissionStart > groupStart {
				groupStart = admissionStart
			}
		}

		var iter buffer.Iterator
		var err error
		if retained {
			lineage := backend.buffer.Lineage()
			if supplied, ok := lineages[name]; ok {
				lineage = supplied
			}
			if firstBroadcast != nil && (startID == "" || groupStart > savedGroup) {
				iter, err = backend.buffer.ScanFromGroup(floor, lineage)
			} else {
				iter, err = backend.buffer.ScanFromLineage(startID, lineage)
			}
		} else if lineages != nil {
			iter, err = backend.buffer.ScanFromLineage(startID, lineages[name])
		} else {
			iter, err = backend.buffer.ScanFrom(groupStart)
		}
		if err != nil {
			// Close already opened iterators
			for _, it := range iters {
				it.Close()
			}
			return nil, fmt.Errorf("failed to create iterator for backend %q: %w", name, err)
		}
		iters = append(iters, &backendInjectingIterator{
			Iterator:    iter,
			backendName: name,
		})
	}

	iter := NewMergeIterator(iters)
	if coalesce {
		return NewCoalescingIterator(iter, 100), nil
	}
	return iter, nil
}

type backendInjectingIterator struct {
	events.Iterator
	backendName string
}

func (i *backendInjectingIterator) Event() *events.StoreChangeEvent {
	evt := i.Iterator.Event()
	if evt != nil && evt.Backend == "" {
		evt.Backend = i.backendName
	}
	return evt
}

// Subscribe subscribes to events from the puller with the given progress marker.
// Returns a channel of events that will be closed when the context is canceled.
func (p *Puller) Subscribe(ctx context.Context, consumerID string, after string) <-chan *events.PullerEvent {
	return p.subscribe(ctx, consumerID, after, nil, false, nil)
}

func (p *Puller) SubscribeReady(ctx context.Context, consumerID, after string, onReady func(string)) <-chan *events.PullerEvent {
	return p.subscribe(ctx, consumerID, after, onReady, true, nil)
}

func (p *Puller) SubscribeStatus(ctx context.Context, consumerID, after string) (<-chan *events.PullerEvent, *events.SubscriptionStatus) {
	status := events.NewSubscriptionStatus(ctx)
	return p.subscribe(ctx, consumerID, after, nil, false, status), status
}

func (p *Puller) subscribe(ctx context.Context, consumerID, after string, onReady func(string), verified bool, monitor ...*events.SubscriptionStatus) <-chan *events.PullerEvent {
	var admission *events.SubscriptionStatus
	if len(monitor) > 0 {
		admission = monitor[0]
	}
	out := make(chan *events.PullerEvent, 1000)
	pm, err := cursor.DecodeProgressMarker(after)
	if err != nil {
		p.logger.Error("invalid subscription progress", "error", err)
		if admission != nil {
			admission.Fail(err, true)
		}
		if verified || admission != nil {
			out <- &events.PullerEvent{Error: err}
		}
		close(out)
		return out
	}
	sub, err := NewSubscriber(consumerID, pm, after == "", false, 1000)
	if err != nil {
		if admission != nil {
			admission.Fail(err, true)
		}
		if verified || admission != nil {
			out <- &events.PullerEvent{Error: err}
		}
		close(out)
		return out
	}
	p.cancelMu.Lock()
	if p.stopping {
		p.cancelMu.Unlock()
		if admission != nil {
			admission.Fail(context.Canceled, true)
		}
		if verified || admission != nil {
			out <- &events.PullerEvent{Error: context.Canceled}
		}
		close(out)
		return out
	}
	ctx, cancel := context.WithCancel(ctx)
	stopLifetime := context.AfterFunc(p.lifetime, cancel)
	p.wg.Add(1)
	p.subs.Add(sub)
	p.cancelMu.Unlock()
	go func() {
		defer p.wg.Done()
		defer func() { stopLifetime(); cancel() }()
		defer close(out)
		defer p.subs.Remove(sub)
		transport := uint64(0)
		if admission != nil {
			transport = admission.BeginAttempt()
			defer admission.Fail(errors.New("subscription closed"), true)
		}
		fail := func(err error, retryable bool) {
			if admission != nil {
				admission.Fail(err, !retryable)
			}
			if verified || admission != nil {
				select {
				case out <- &events.PullerEvent{Error: err, Retryable: retryable}:
				case <-ctx.Done():
				}
			}
		}
		if verified {
			if err := p.ValidateBoundary(ctx, after); err != nil {
				p.logger.Error("subscription boundary rejected", "error", err)
				fail(err, errors.Is(err, ErrCaptureUnavailable))
				return
			}
		}
		var healthTicker *time.Ticker
		var maintenance <-chan time.Time
		if verified || admission != nil {
			healthTicker = time.NewTicker(time.Second)
			maintenance = healthTicker.C
			defer healthTicker.Stop()
		}
		driver := SubscriptionDriver{
			OpenReplay: func(ctx context.Context, progress *cursor.ProgressMarker) (events.Iterator, error) {
				if verified {
					return p.ReplayBoundary(ctx, progress.Encode(), false)
				}
				if admission != nil {
					return p.ReplayRetained(ctx, progress, nil)
				}
				return p.Replay(ctx, progress.Positions, false)
			},
			OpenAdmissionReplay: func(ctx context.Context, progress *cursor.ProgressMarker, floors map[string]events.ClusterTime) (events.Iterator, error) {
				if admission != nil {
					return p.ReplayRetained(ctx, progress, floors)
				}
				return p.ReplayFromAdmission(ctx, progress.Positions, floors)
			},
			Deliver: func(ctx context.Context, evt *events.StoreChangeEvent, progress string) error {
				select {
				case out <- &events.PullerEvent{Change: evt, Progress: progress}:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			},
			Maintenance: maintenance,
		}
		if admission != nil {
			driver.CheckAdmission = p.Readiness
			driver.Admission = func(ctx context.Context, state string, generation uint64, reason string) error {
				frame := &events.PullerEvent{Ready: state == events.AdmissionAdmitted, AdmissionState: state, AdmissionGeneration: generation, AdmissionReason: reason, TransportGeneration: transport}
				if !admission.Receive(frame) {
					return errors.New("invalid admission frame")
				}
				select {
				case out <- frame:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		if verified {
			driver.Ready = func(ctx context.Context, progress string) error {
				select {
				case out <- &events.PullerEvent{Ready: true, Progress: progress}:
					if onReady != nil {
						onReady(progress)
					}
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			driver.Maintain = func(ctx context.Context, progress string) error {
				return p.ValidateBoundary(ctx, progress)
			}
		} else if onReady != nil {
			driver.Ready = func(_ context.Context, progress string) error {
				onReady(progress)
				return nil
			}
		}

		exit := RunSubscription(ctx, sub, after != "", driver)
		if exit.CleanupErr != nil && exit.Err != nil {
			p.logger.Error("subscription replay cleanup failed", "error", exit.CleanupErr)
		}
		switch exit.Kind {
		case SubscriptionExitReplayFailed:
			err := exit.Err
			if err == nil {
				err = exit.CleanupErr
			}
			p.logger.Error("subscription replay failed", "error", err)
			fail(err, false)
		case SubscriptionExitMaintenanceFailed:
			p.logger.Error("subscription boundary unavailable", "error", exit.Err)
			fail(exit.Err, errors.Is(exit.Err, ErrCaptureUnavailable))
		case SubscriptionExitDeliveryFailed, SubscriptionExitReadyFailed:
			p.logger.Error("subscription delivery failed", "error", exit.Err)
			fail(exit.Err, false)
		}
	}()
	return out
}

func parseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}

	var size int64
	var unit string
	// Try to parse number and unit
	n, err := fmt.Sscanf(s, "%d%s", &size, &unit)
	if err != nil {
		// Try just number
		_, err = fmt.Sscanf(s, "%d", &size)
		if err != nil {
			return 0, fmt.Errorf("invalid size format: %s", s)
		}
		return size, nil
	}

	if n == 1 {
		return size, nil
	}

	switch unit {
	case "KB", "KiB":
		size *= 1024
	case "MB", "MiB":
		size *= 1024 * 1024
	case "GB", "GiB":
		size *= 1024 * 1024 * 1024
	case "TB", "TiB":
		size *= 1024 * 1024 * 1024 * 1024
	}
	return size, nil
}
