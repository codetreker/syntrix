package streamer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	pb "github.com/syntrixbase/syntrix/api/gen/streamer/v1"
	"github.com/syntrixbase/syntrix/pkg/model"
)

const registrationLimit = 256
const registrationTimeout = 10 * time.Second
const activeRegistrationLimit = 4096

var ErrStreamUnavailable = errors.New("stream transport generation is unavailable")
var ErrRegistrationCapacity = errors.New("stream registration capacity exceeded")

type subscriptionInfo struct{ request *pb.SubscribeRequest }
type pendingRegistration struct {
	attempt   *remoteAttempt
	response  chan *pb.SubscribeResponse
	withdrawn chan struct{}
	restoring bool
}
type sendOperation struct {
	ctx       context.Context
	message   *pb.GatewayMessage
	done      chan error
	cleanupID string
}
type receivedDelivery struct {
	generation uint64
	event      *EventDelivery
}

type remoteAttempt struct {
	owner       *remoteStream
	failure     error
	generation  uint64
	ctx         context.Context
	cancel      context.CancelFunc
	stream      pb.StreamerService_StreamClient
	sends       chan sendOperation
	errors      chan error
	mu          sync.Mutex
	lastMessage time.Time
}

func (a *remoteAttempt) reason() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failure != nil {
		return a.failure
	}
	if err := a.ctx.Err(); err != nil {
		return err
	}
	return ErrStreamUnavailable
}
func (a *remoteAttempt) fail(err error) {
	a.mu.Lock()
	if a.failure == nil {
		a.failure = err
	}
	a.mu.Unlock()
	a.owner.retire(a, err)
	select {
	case a.errors <- err:
	default:
	}
	a.cancel()
}

// One receiver and writer belong to each transport attempt. Registry and status
// remain on the Stream so reconnect cannot discard pending-cleanup accounting.
type remoteStream struct {
	ctx        context.Context
	cancel     context.CancelFunc
	client     *streamerClient
	stopClient func() bool
	mu         sync.Mutex
	status     StreamStatus
	changed    chan struct{}
	attempt    *remoteAttempt
	active     map[string]*subscriptionInfo
	pending    map[string]*pendingRegistration
	recv       chan receivedDelivery
	done       chan struct{}
}

func newRemoteStream(ctx context.Context, cancel context.CancelFunc, client *streamerClient, stream pb.StreamerService_StreamClient, attemptCtx context.Context, attemptCancel context.CancelFunc, stopClient func() bool) *remoteStream {
	rs := &remoteStream{ctx: ctx, cancel: cancel, client: client, stopClient: stopClient,
		changed: make(chan struct{}), active: make(map[string]*subscriptionInfo), pending: make(map[string]*pendingRegistration), recv: make(chan receivedDelivery, 100), done: make(chan struct{})}
	rs.status = StreamStatus{State: StateConnected, Generation: 1}
	rs.attempt = newRemoteAttempt(rs, 1, attemptCtx, attemptCancel, stream)
	go rs.run(rs.attempt)
	if client.config.OnStateChange != nil {
		go rs.observeLegacyState()
	}
	return rs
}

// This optional diagnostic callback does not own transport drain. Observing
// through the same bounded change signal permits a callback to close its Stream
// without waiting for itself. Lifecycle consumers must use Status directly.
func (rs *remoteStream) observeLegacyState() {
	last := StateConnected
	for {
		status := rs.Status()
		if status.State != last || status.Err != nil {
			last = status.State
			rs.client.config.OnStateChange(status.State, status.Err)
		}
		if status.Terminal {
			return
		}
		<-status.Changed
	}
}
func newRemoteAttempt(owner *remoteStream, generation uint64, ctx context.Context, cancel context.CancelFunc, stream pb.StreamerService_StreamClient) *remoteAttempt {
	return &remoteAttempt{owner: owner, generation: generation, ctx: ctx, cancel: cancel, stream: stream, sends: make(chan sendOperation, registrationLimit), errors: make(chan error, 1), lastMessage: time.Now()}
}
func (rs *remoteStream) Status() StreamStatus {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.attempt != nil && rs.attempt.ctx.Err() != nil {
		rs.retireLocked(rs.attempt, rs.attempt.reason())
	}
	status := rs.status
	status.Changed = rs.changed
	return status
}
func (rs *remoteStream) changeLocked(state ConnectionState, terminal bool, err error) {
	rs.status.State = state
	rs.status.Terminal = terminal
	rs.status.Err = err
	close(rs.changed)
	rs.changed = make(chan struct{})
}
func (rs *remoteStream) retireLocked(a *remoteAttempt, err error) {
	if rs.attempt != a {
		return
	}
	rs.attempt = nil
	rs.status.Generation++
	rs.changeLocked(StateReconnecting, false, err)
}
func (rs *remoteStream) retire(a *remoteAttempt, err error) {
	rs.mu.Lock()
	rs.retireLocked(a, err)
	rs.mu.Unlock()
}
func (rs *remoteStream) unavailableLocked() error {
	if rs.ctx.Err() != nil {
		return rs.ctx.Err()
	}
	if rs.status.Terminal && rs.status.Err != nil {
		return rs.status.Err
	}
	return ErrStreamUnavailable
}
func (rs *remoteStream) Subscribe(ctx context.Context, database, collection string, filters []model.Filter) (Registration, error) {
	if err := ctx.Err(); err != nil {
		return Registration{}, err
	}
	rs.mu.Lock()
	a := rs.attempt
	if a == nil || rs.status.State != StateConnected || rs.status.Terminal {
		err := rs.unavailableLocked()
		rs.mu.Unlock()
		return Registration{}, err
	}
	rs.mu.Unlock()
	request := &pb.SubscribeRequest{SubscriptionId: uuid.NewString(), Database: database, Collection: collection, Filters: filtersToProto(filters)}
	return rs.register(ctx, a, request, false)
}
func (rs *remoteStream) register(ctx context.Context, a *remoteAttempt, request *pb.SubscribeRequest, restoring bool) (Registration, error) {
	ctx, cancel := context.WithTimeout(ctx, registrationTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Registration{}, err
	}
	entry := &pendingRegistration{attempt: a, response: make(chan *pb.SubscribeResponse, 1), withdrawn: make(chan struct{}), restoring: restoring}
	rs.mu.Lock()
	if rs.attempt != a || a.ctx.Err() != nil || rs.status.Terminal {
		rs.mu.Unlock()
		return Registration{}, ErrStreamUnavailable
	}
	if len(rs.pending) >= registrationLimit || (!restoring && len(rs.active)+len(rs.pending) >= activeRegistrationLimit) {
		rs.mu.Unlock()
		return Registration{}, ErrRegistrationCapacity
	}
	if restoring && rs.active[request.SubscriptionId] == nil {
		rs.mu.Unlock()
		return Registration{}, nil
	}
	rs.pending[request.SubscriptionId] = entry
	rs.mu.Unlock()
	message := &pb.GatewayMessage{Payload: &pb.GatewayMessage_Subscribe{Subscribe: request}}
	if err := rs.send(ctx, a, message); err != nil {
		rs.abandon(a, request.SubscriptionId, entry)
		return Registration{}, err
	}
	select {
	case <-entry.withdrawn:
		rs.abandon(a, request.SubscriptionId, entry)
		return Registration{}, nil
	case response := <-entry.response:
		rs.mu.Lock()
		if restoring && rs.active[request.SubscriptionId] == nil {
			rs.mu.Unlock()
			rs.abandon(a, request.SubscriptionId, entry)
			return Registration{}, nil
		}
		current := rs.attempt == a && a.ctx.Err() == nil && !rs.status.Terminal && ctx.Err() == nil && rs.pending[request.SubscriptionId] == entry
		if current && response.Success && (!restoring || rs.active[request.SubscriptionId] != nil) {
			rs.active[request.SubscriptionId] = &subscriptionInfo{request: request}
			delete(rs.pending, request.SubscriptionId)
			rs.mu.Unlock()
			return Registration{ID: request.SubscriptionId, Generation: a.generation}, nil
		}
		if current && !response.Success {
			delete(rs.pending, request.SubscriptionId)
		}
		rs.mu.Unlock()
		if !current {
			rs.abandon(a, request.SubscriptionId, entry)
			if err := ctx.Err(); err != nil {
				return Registration{}, err
			}
			return Registration{}, ErrStreamUnavailable
		}
		if !response.Success {
			return Registration{}, fmt.Errorf("subscription rejected: %s", response.Error)
		}
		rs.abandon(a, request.SubscriptionId, entry)
		return Registration{}, nil
	case <-ctx.Done():
		rs.abandon(a, request.SubscriptionId, entry)
		return Registration{}, ctx.Err()
	case <-a.ctx.Done():
		rs.abandon(a, request.SubscriptionId, entry)
		return Registration{}, a.reason()
	}
}

// Cleanup is ordered behind the original subscribe on the same writer. The
// entry remains charged until that unsubscribe is written or the attempt exits.
func (rs *remoteStream) abandon(a *remoteAttempt, id string, entry *pendingRegistration) {
	rs.mu.Lock()
	if rs.pending[id] != entry {
		rs.mu.Unlock()
		return
	}
	if rs.attempt != a || a.ctx.Err() != nil {
		delete(rs.pending, id)
		rs.mu.Unlock()
		return
	}
	rs.mu.Unlock()
	operation := sendOperation{ctx: a.ctx, message: &pb.GatewayMessage{Payload: &pb.GatewayMessage_Unsubscribe{Unsubscribe: &pb.UnsubscribeRequest{SubscriptionId: id}}}, cleanupID: id}
	select {
	case a.sends <- operation:
	default:
		a.fail(ErrRegistrationCapacity)
	}
}
func (rs *remoteStream) send(ctx context.Context, a *remoteAttempt, message *pb.GatewayMessage) error {
	operation := sendOperation{ctx: ctx, message: message, done: make(chan error, 1)}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.ctx.Done():
		return a.reason()
	case a.sends <- operation:
	default:
		return ErrRegistrationCapacity
	}
	select {
	case err := <-operation.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-a.ctx.Done():
		return a.reason()
	}
}
func (rs *remoteStream) Unsubscribe(id string) error {
	rs.mu.Lock()
	if rs.status.Terminal || rs.ctx.Err() != nil {
		err := rs.unavailableLocked()
		rs.mu.Unlock()
		return err
	}
	delete(rs.active, id)
	if entry := rs.pending[id]; entry != nil && entry.restoring {
		select {
		case <-entry.withdrawn:
		default:
			close(entry.withdrawn)
		}
	}
	a := rs.attempt
	rs.mu.Unlock()
	if a == nil || a.ctx.Err() != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(rs.ctx, registrationTimeout)
	defer cancel()
	err := rs.send(ctx, a, &pb.GatewayMessage{Payload: &pb.GatewayMessage_Unsubscribe{Unsubscribe: &pb.UnsubscribeRequest{SubscriptionId: id}}})
	if err != nil {
		a.fail(err)
	}
	return err
}
func (rs *remoteStream) Recv() (*EventDelivery, error) {
	for {
		select {
		case delivery, ok := <-rs.recv:
			status := rs.Status()
			if !ok {
				if status.Err != nil {
					return nil, status.Err
				}
				return nil, io.EOF
			}
			if !status.Terminal && delivery.generation == status.Generation {
				return delivery.event, nil
			}
		case <-rs.ctx.Done():
			return nil, rs.ctx.Err()
		}
	}
}
func (rs *remoteStream) Close() error {
	rs.mu.Lock()
	a := rs.attempt
	if a != nil {
		rs.retireLocked(a, context.Canceled)
	}
	rs.mu.Unlock()
	rs.cancel()
	if a != nil {
		a.cancel()
	}
	<-rs.done
	return nil
}
func (rs *remoteStream) reader(a *remoteAttempt) {
	for {
		message, err := a.stream.Recv()
		if err != nil {
			a.fail(err)
			return
		}
		a.mu.Lock()
		a.lastMessage = time.Now()
		a.mu.Unlock()
		switch payload := message.Payload.(type) {
		case *pb.StreamerMessage_SubscribeResponse:
			response := payload.SubscribeResponse
			if response == nil {
				a.fail(errors.New("missing subscription response"))
				return
			}
			rs.mu.Lock()
			entry := rs.pending[response.SubscriptionId]
			if rs.attempt == a && entry != nil && entry.attempt == a {
				select {
				case entry.response <- response:
				default:
				}
			}
			rs.mu.Unlock()
		case *pb.StreamerMessage_Delivery:
			delivery := protoToEventDelivery(payload.Delivery)
			if delivery == nil || delivery.Event == nil {
				a.fail(errors.New("missing stream delivery"))
				return
			}
			select {
			case rs.recv <- receivedDelivery{generation: a.generation, event: delivery}:
			case <-a.ctx.Done():
				return
			}
		case *pb.StreamerMessage_HeartbeatAck:
		default:
			a.fail(errors.New("unknown stream response"))
			return
		}
	}
}
func (rs *remoteStream) writer(a *remoteAttempt) {
	interval := rs.client.config.HeartbeatInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	heartbeat := time.NewTicker(interval)
	defer heartbeat.Stop()
	for {
		var operation sendOperation
		select {
		case <-a.ctx.Done():
			return
		case operation = <-a.sends:
		case <-heartbeat.C:
			if rs.Status().State != StateConnected {
				continue
			}
			operation = sendOperation{ctx: a.ctx, message: &pb.GatewayMessage{Payload: &pb.GatewayMessage_Heartbeat{Heartbeat: &pb.Heartbeat{}}}}
		}
		if err := operation.ctx.Err(); err != nil {
			if operation.done != nil {
				operation.done <- err
			}
			continue
		}
		// Caller cancellation must interrupt a Send already blocked in gRPC. Cancel
		// this transport attempt, not the shared client or another Stream object.
		stop := context.AfterFunc(operation.ctx, func() { a.fail(operation.ctx.Err()) })
		err := a.stream.Send(operation.message)
		stop()
		if operation.done != nil {
			operation.done <- err
		}
		if err != nil {
			a.fail(err)
			return
		}
		if operation.cleanupID != "" {
			rs.mu.Lock()
			if p := rs.pending[operation.cleanupID]; p != nil && p.attempt == a {
				delete(rs.pending, operation.cleanupID)
			}
			rs.mu.Unlock()
		}
	}
}
func (rs *remoteStream) monitor(a *remoteAttempt) {
	timeout := rs.client.config.ActivityTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	tick := time.NewTicker(max(time.Millisecond, timeout/3))
	defer tick.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
			a.mu.Lock()
			last := a.lastMessage
			a.mu.Unlock()
			if time.Since(last) > timeout {
				a.fail(errors.New("stream activity timeout"))
				return
			}
		}
	}
}
func (rs *remoteStream) serve(a *remoteAttempt, restoring bool) (err error, ready bool) {
	ready = !restoring
	var workers sync.WaitGroup
	workers.Add(3)
	go func() { defer workers.Done(); rs.reader(a) }()
	go func() { defer workers.Done(); rs.writer(a) }()
	go func() { defer workers.Done(); rs.monitor(a) }()
	defer func() { rs.retire(a, err); a.cancel(); workers.Wait() }()
	if restoring {
		rs.mu.Lock()
		requests := make([]*pb.SubscribeRequest, 0, len(rs.active))
		for _, entry := range rs.active {
			requests = append(requests, entry.request)
		}
		rs.mu.Unlock()
		for _, request := range requests {
			if _, err := rs.register(a.ctx, a, request, true); err != nil {
				return err, ready
			}
		}
		rs.mu.Lock()
		if a.ctx.Err() == nil && rs.attempt == a && rs.ctx.Err() == nil {
			rs.changeLocked(StateConnected, false, nil)
			ready = true
		}
		rs.mu.Unlock()
	}
	select {
	case err := <-a.errors:
		return err, ready
	case <-a.ctx.Done():
		select {
		case err := <-a.errors:
			return err, ready
		default:
			return a.reason(), ready
		}
	}
}
func (rs *remoteStream) run(initial *remoteAttempt) {
	defer close(rs.done)
	defer close(rs.recv)
	defer rs.stopClient()
	a := initial
	var failure error
	attempts := 0
	for {
		var ready bool
		failure, ready = rs.serve(a, a != initial)
		if ready {
			attempts = 0
		}
		rs.mu.Lock()
		for id, p := range rs.pending {
			if p.attempt == a {
				delete(rs.pending, id)
			}
		}
		rs.mu.Unlock()
		if rs.ctx.Err() != nil {
			failure = rs.ctx.Err()
			break
		}
		var next *remoteAttempt
		for rs.ctx.Err() == nil && (rs.client.config.MaxRetries <= 0 || attempts < rs.client.config.MaxRetries) {
			delay := rs.client.config.InitialBackoff
			if delay <= 0 {
				delay = time.Second
			}
			maximum := rs.client.config.MaxBackoff
			if maximum <= 0 {
				maximum = 30 * time.Second
			}
			multiplier := rs.client.config.BackoffMultiplier
			if multiplier <= 1 {
				multiplier = 2
			}
			for n := 0; n < attempts && delay < maximum; n++ {
				delay = min(maximum, time.Duration(float64(delay)*multiplier))
			}
			attempts++
			timer := time.NewTimer(time.Duration(float64(delay) * (0.8 + rand.Float64()*0.4)))
			select {
			case <-timer.C:
			case <-rs.ctx.Done():
				timer.Stop()
			}
			if rs.ctx.Err() != nil {
				break
			}
			ctx, cancel := context.WithCancel(rs.ctx)
			wire, err := rs.client.client.Stream(ctx)
			if err == nil && ctx.Err() != nil {
				err = ctx.Err()
			}
			if err != nil {
				cancel()
				failure = err
				continue
			}
			rs.mu.Lock()
			next = newRemoteAttempt(rs, rs.status.Generation, ctx, cancel, wire)
			rs.attempt = next
			rs.mu.Unlock()
			break
		}
		if next == nil {
			if rs.ctx.Err() != nil {
				failure = rs.ctx.Err()
			}
			break
		}
		a = next
	}
	rs.mu.Lock()
	rs.attempt = nil
	rs.changeLocked(StateDisconnected, true, failure)
	rs.mu.Unlock()
	rs.cancel()
}

var _ Stream = (*remoteStream)(nil)
