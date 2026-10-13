package core

import (
	"context"
	"errors"
	"time"

	"github.com/codetreker/syntrix/internal/puller/cursor"
	"github.com/codetreker/syntrix/internal/puller/events"
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
// subscription state machine. Replay callbacks return raw iterators so the
// runner can filter admission history before coalescing.
type SubscriptionDriver struct {
	OpenReplay          func(context.Context, *cursor.ProgressMarker) (events.Iterator, error)
	OpenAdmissionReplay func(context.Context, *cursor.ProgressMarker, map[string]events.ClusterTime) (events.Iterator, error)
	Deliver             func(context.Context, *events.StoreChangeEvent, string) error
	Ready               func(context.Context, string) error
	Maintenance         <-chan time.Time
	Maintain            func(context.Context, string) error
	EnterLive           func()
	Admission           func(context.Context, string, uint64, string) error
	CheckAdmission      func(context.Context) error
	FenceAdmission      func(context.Context) error
}

// RunSubscription delivers replay and live events until the context or
// subscriber closes, or an adapter operation fails.
func RunSubscription(ctx context.Context, sub *Subscriber, initialCatchUp bool, driver SubscriptionDriver) SubscriptionExit {
	recovering := sub.RecoveryPending()
	catchUp := initialCatchUp || recovering
	enteredLive := false
	ready := false
	generation := uint64(1)
	admissionState := events.AdmissionStarting
	emitAdmission := func(state, reason string) (SubscriptionExit, bool) {
		admissionState = state
		if driver.Admission != nil {
			if err := driver.Admission(ctx, state, generation, reason); err != nil {
				return SubscriptionExit{Kind: SubscriptionExitReadyFailed, Phase: SubscriptionPhaseReady, Err: err}, true
			}
		}
		return SubscriptionExit{}, false
	}
	if driver.Admission != nil {
		if exit, stop := emitAdmission(events.AdmissionStarting, ""); stop {
			return exit
		}
	}

	for {
		if driver.CheckAdmission != nil {
			if exit, stop := subscriptionStopped(ctx, sub, SubscriptionPhaseReady); stop {
				return exit
			}
			if err := driver.CheckAdmission(ctx); err != nil {
				if ctx.Err() != nil {
					return canceledSubscriptionExit(SubscriptionPhaseReady, ctx.Err(), nil)
				}
				if !errors.Is(err, ErrCaptureUnavailable) {
					return SubscriptionExit{Kind: SubscriptionExitMaintenanceFailed, Phase: SubscriptionPhaseMaintenance, Err: err}
				}
				if admissionState != events.AdmissionRecovering {
					generation++
					ready = false
					enteredLive = false
					if exit, stop := emitAdmission(events.AdmissionRecovering, "source_unavailable"); stop {
						return exit
					}
				}
				select {
				case <-ctx.Done():
					return canceledSubscriptionExit(SubscriptionPhaseReady, ctx.Err(), nil)
				case <-sub.Done():
					return SubscriptionExit{Kind: SubscriptionExitSubscriberClosed, Phase: SubscriptionPhaseReady}
				case <-driver.Maintenance:
				}
				continue
			}
		}
		if catchUp {
			if driver.Admission != nil {
				if exit, stop := emitAdmission(events.AdmissionReplaying, ""); stop {
					return exit
				}
			}
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
			if driver.Admission != nil {
				if driver.CheckAdmission != nil {
					if err := driver.CheckAdmission(ctx); err != nil {
						continue
					}
				}
				if driver.FenceAdmission != nil {
					if err := driver.FenceAdmission(ctx); err != nil {
						if ctx.Err() != nil {
							return canceledSubscriptionExit(SubscriptionPhaseReady, ctx.Err(), nil)
						}
						return SubscriptionExit{Kind: SubscriptionExitReadyFailed, Phase: SubscriptionPhaseReady, Err: err}
					}
				}
				// The admission control follows all business events already queued
				// at the live barrier, including capture recovery's pending prefix.
				draining := true
				for draining && !sub.RecoveryPending() {
					select {
					case evt := <-sub.Events():
						if exit, stop := deliverSubscriptionEvent(ctx, sub, driver, evt, SubscriptionPhaseLive); stop {
							return exit
						}
					default:
						draining = false
					}
				}
				if sub.RecoveryPending() {
					catchUp = true
					recovering = true
					enteredLive = false
					generation++
					if exit, stop := emitAdmission(events.AdmissionRecovering, "overflow"); stop {
						return exit
					}
					continue
				}
			}
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
			if driver.Admission != nil {
				if exit, stop := emitAdmission(events.AdmissionAdmitted, ""); stop {
					return exit
				}
			}
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
			if driver.Admission != nil {
				ready = false
			}
			generation++
			if driver.Admission != nil {
				if exit, stop := emitAdmission(events.AdmissionRecovering, "overflow"); stop {
					return exit
				}
			}
		case <-driver.Maintenance:
			if driver.Maintain != nil {
				if err := driver.Maintain(ctx, sub.CurrentProgress().Encode()); err != nil {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return canceledSubscriptionExit(SubscriptionPhaseMaintenance, ctxErr, nil)
					}
					return SubscriptionExit{Kind: SubscriptionExitMaintenanceFailed, Phase: SubscriptionPhaseMaintenance, Err: err}
				}
			}
			if driver.Admission != nil && (driver.CheckAdmission == nil || driver.CheckAdmission(ctx) == nil) {
				ready = false
			}
		case evt := <-sub.Events():
			if exit, stopped := deliverSubscriptionEvent(ctx, sub, driver, evt, SubscriptionPhaseLive); stopped {
				return exit
			}
			if sub.RecoveryPending() {
				catchUp = true
				recovering = true
				enteredLive = false
				if driver.Admission != nil {
					ready = false
				}
				generation++
				if driver.Admission != nil {
					if exit, stop := emitAdmission(events.AdmissionRecovering, "overflow"); stop {
						return exit
					}
				}
			}
		}
	}
}

func runReplay(ctx context.Context, sub *Subscriber, driver SubscriptionDriver) (SubscriptionExit, bool) {
	progress := sub.CurrentProgress()
	var iter events.Iterator
	var err error
	if sub.startFromNow {
		iter, err = driver.OpenAdmissionReplay(ctx, progress, sub.AdmissionFloors())
	} else {
		iter, err = driver.OpenReplay(ctx, progress)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return canceledSubscriptionExit(SubscriptionPhaseReplay, ctxErr, nil), true
		}
		return SubscriptionExit{Kind: SubscriptionExitReplayFailed, Phase: SubscriptionPhaseReplay, Err: err}, true
	}
	if sub.startFromNow {
		iter = &admittedReplayIterator{ctx: ctx, source: iter, sub: sub}
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
			if exit, stopped := subscriptionStopped(ctx, sub, SubscriptionPhaseReplay); stopped {
				exit.CleanupErr = iter.Close()
				return exit, true
			}
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
	ctx     context.Context
	source  events.Iterator
	sub     *Subscriber
	current *events.StoreChangeEvent
}

func (i *admittedReplayIterator) Next() bool {
	for {
		if i.ctx.Err() != nil {
			return false
		}
		select {
		case <-i.sub.Done():
			return false
		default:
		}
		if !i.source.Next() {
			return false
		}
		candidate := i.source.Event()
		if i.sub.replayAdmitted(candidate) {
			i.current = candidate
			return true
		}
	}
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
