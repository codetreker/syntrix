package core

import (
	"context"
	"errors"
	"time"

	"github.com/syntrixbase/syntrix/internal/puller/cursor"
	"github.com/syntrixbase/syntrix/internal/puller/events"
)

// ErrNilSubscriptionEvent reports an invalid iterator or live queue event.
var ErrNilSubscriptionEvent = errors.New("subscription source returned a nil event")

// SubscriptionPhase identifies the operation active when a subscription exits.
type SubscriptionPhase uint8

const (
	SubscriptionPhaseReplay SubscriptionPhase = iota + 1
	SubscriptionPhaseReady
	SubscriptionPhaseLive
	SubscriptionPhaseMaintenance
)

// SubscriptionExitKind identifies why a subscription stopped.
type SubscriptionExitKind uint8

const (
	SubscriptionExitContextCanceled SubscriptionExitKind = iota + 1
	SubscriptionExitSubscriberClosed
	SubscriptionExitReplayFailed
	SubscriptionExitDeliveryFailed
	SubscriptionExitReadyFailed
	SubscriptionExitMaintenanceFailed
)

// SubscriptionExit preserves the primary operation failure separately from an
// iterator cleanup failure so adapters can retain their transport semantics.
type SubscriptionExit struct {
	Kind       SubscriptionExitKind
	Phase      SubscriptionPhase
	Err        error
	CleanupErr error
}

// SubscriptionDriver supplies transport-specific operations to the shared
// subscription state machine.
type SubscriptionDriver struct {
	OpenReplay  func(context.Context, *cursor.ProgressMarker) (events.Iterator, error)
	Deliver     func(context.Context, *events.StoreChangeEvent, string) error
	Ready       func(context.Context, string) error
	Maintenance <-chan time.Time
	Maintain    func(context.Context, string) error
	EnterLive   func()
}

// RunSubscription delivers replay and live events until the context or
// subscriber closes, or an adapter operation fails.
func RunSubscription(ctx context.Context, sub *Subscriber, initialCatchUp bool, driver SubscriptionDriver) SubscriptionExit {
	recovering := sub.RecoveryPending()
	catchUp := initialCatchUp || recovering
	enteredLive := false
	ready := false

	for {
		if catchUp {
			if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseReplay); stopped {
				return exit
			}
			if recovering {
				sub.drainEvents()
				if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseReplay); stopped {
					return exit
				}
				sub.BeginRecovery()
			}
			if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseReplay); stopped {
				return exit
			}

			exit, stopped := runReplay(ctx, sub, driver)
			if stopped {
				return exit
			}
			if sub.RecoveryPending() {
				recovering = true
				continue
			}
			catchUp = false
			recovering = false
		}

		if !enteredLive {
			if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseLive); stopped {
				return exit
			}
			if driver.EnterLive != nil {
				driver.EnterLive()
			}
			enteredLive = true
		}

		if !ready {
			if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseReady); stopped {
				return exit
			}
			if driver.Ready != nil {
				progress := sub.CurrentProgress().Encode()
				if err := driver.Ready(ctx, progress); err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return canceledSubscriptionExit(SubscriptionPhaseReady, ctxErr, nil)
					}
					return SubscriptionExit{Kind: SubscriptionExitReadyFailed, Phase: SubscriptionPhaseReady, Err: err}
				}
			}
			ready = true
		}

		select {
		case <-ctx.Done():
			return canceledSubscriptionExit(SubscriptionPhaseLive, ctx.Err(), nil)
		case <-sub.Done():
			if err := ctx.Err(); err != nil {
				return canceledSubscriptionExit(SubscriptionPhaseLive, err, nil)
			}
			return SubscriptionExit{Kind: SubscriptionExitSubscriberClosed, Phase: SubscriptionPhaseLive}
		case <-sub.Recovery():
			catchUp = true
			recovering = true
			enteredLive = false
		case <-driver.Maintenance:
			if driver.Maintain == nil {
				continue
			}
			if err := driver.Maintain(ctx, sub.CurrentProgress().Encode()); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return canceledSubscriptionExit(SubscriptionPhaseMaintenance, ctxErr, nil)
				}
				return SubscriptionExit{Kind: SubscriptionExitMaintenanceFailed, Phase: SubscriptionPhaseMaintenance, Err: err}
			}
		case evt := <-sub.Events():
			if exit, stopped := deliverSubscriptionEvent(ctx, sub, driver, evt, SubscriptionPhaseLive); stopped {
				return exit
			}
			if sub.RecoveryPending() {
				catchUp = true
				recovering = true
				enteredLive = false
			}
		}
	}
}

func runReplay(ctx context.Context, sub *Subscriber, driver SubscriptionDriver) (SubscriptionExit, bool) {
	iter, err := driver.OpenReplay(ctx, sub.CurrentProgress())
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return canceledSubscriptionExit(SubscriptionPhaseReplay, ctxErr, nil), true
		}
		return SubscriptionExit{Kind: SubscriptionExitReplayFailed, Phase: SubscriptionPhaseReplay, Err: err}, true
	}
	if sub.startFromNow {
		iter = &admittedReplayIterator{source: iter, sub: sub}
	}
	if sub.CoalesceOnCatchUp {
		iter = NewCoalescingIterator(iter, 100)
	}

	for {
		if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseReplay); stopped {
			exit.CleanupErr = iter.Close()
			return exit, true
		}
		if !iter.Next() {
			break
		}
		if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseReplay); stopped {
			exit.CleanupErr = iter.Close()
			return exit, true
		}
		evt := iter.Event()
		if evt == nil {
			return SubscriptionExit{
				Kind:       SubscriptionExitReplayFailed,
				Phase:      SubscriptionPhaseReplay,
				Err:        ErrNilSubscriptionEvent,
				CleanupErr: iter.Close(),
			}, true
		}
		if exit, stopped := deliverSubscriptionEvent(ctx, sub, driver, evt, SubscriptionPhaseReplay); stopped {
			exit.CleanupErr = iter.Close()
			return exit, true
		}
	}
	replayErr := iter.Err()
	closeErr := iter.Close()
	if replayErr != nil || closeErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return canceledSubscriptionExit(SubscriptionPhaseReplay, ctxErr, closeErr), true
		}
		return SubscriptionExit{
			Kind:       SubscriptionExitReplayFailed,
			Phase:      SubscriptionPhaseReplay,
			Err:        replayErr,
			CleanupErr: closeErr,
		}, true
	}
	return SubscriptionExit{}, false
}

type admittedReplayIterator struct {
	source  events.Iterator
	sub     *Subscriber
	current *events.StoreChangeEvent
}

func (i *admittedReplayIterator) Next() bool {
	for i.source.Next() {
		candidate := i.source.Event()
		if i.sub.replayAdmitted(candidate) {
			i.current = candidate
			return true
		}
	}
	return false
}

func (i *admittedReplayIterator) Event() *events.StoreChangeEvent { return i.current }
func (i *admittedReplayIterator) Err() error                      { return i.source.Err() }
func (i *admittedReplayIterator) Close() error                    { return i.source.Close() }

func deliverSubscriptionEvent(
	ctx context.Context,
	sub *Subscriber,
	driver SubscriptionDriver,
	evt *events.StoreChangeEvent,
	phase SubscriptionPhase,
) (SubscriptionExit, bool) {
	if exit, stopped := subscriptionStopped(ctx, sub, phase); stopped {
		return exit, true
	}
	if evt == nil {
		return SubscriptionExit{Kind: SubscriptionExitDeliveryFailed, Phase: phase, Err: ErrNilSubscriptionEvent}, true
	}
	if !sub.ShouldSend(evt.Backend, evt.EventID, evt.ClusterTime) {
		return SubscriptionExit{}, false
	}
	progress := sub.CurrentProgress()
	progress.SetPosition(evt.Backend, evt.EventID)
	if err := driver.Deliver(ctx, evt, progress.Encode()); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return canceledSubscriptionExit(phase, ctxErr, nil), true
		}
		return SubscriptionExit{Kind: SubscriptionExitDeliveryFailed, Phase: phase, Err: err}, true
	}
	sub.UpdatePosition(evt.Backend, evt.EventID, evt.ClusterTime)
	return SubscriptionExit{}, false
}

func canceledSubscriptionExit(phase SubscriptionPhase, err, cleanupErr error) SubscriptionExit {
	return SubscriptionExit{
		Kind:       SubscriptionExitContextCanceled,
		Phase:      phase,
		Err:        err,
		CleanupErr: cleanupErr,
	}
}

func subscriptionStopped(ctx context.Context, sub *Subscriber, phase SubscriptionPhase) (SubscriptionExit, bool) {
	if err := ctx.Err(); err != nil {
		return canceledSubscriptionExit(phase, err, nil), true
	}
	select {
	case <-sub.Done():
		if err := ctx.Err(); err != nil {
			return canceledSubscriptionExit(phase, err, nil), true
		}
		return SubscriptionExit{Kind: SubscriptionExitSubscriberClosed, Phase: phase}, true
	default:
		return SubscriptionExit{}, false
	}
}
