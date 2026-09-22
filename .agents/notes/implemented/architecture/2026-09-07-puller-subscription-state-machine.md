# Agent Note: Unify Puller Subscription State Machines

Status: implemented

## Problem

Local and gRPC subscriptions both require retained catch-up, live delivery,
identity-aware boundary replay, verified readiness, and overflow recovery. The
transport adapters previously implemented those state transitions separately in
[`Puller.subscribe`](../../../../internal/puller/core/puller.go) and
[`Server.Subscribe`](../../../../internal/puller/grpc/server.go).

The shared [`Subscriber`](../../../../internal/puller/core/subscriber.go) already
owned delivery progress, boundary-group identities, bounded live admission, and
the latched recovery handoff. Duplicating replay startup, iterator cleanup,
overflow-during-replay handling, stale-queue drainage, and the catch-up-to-live
transition still allowed one transport to acquire a different ordering or
cleanup rule.

The transport error surfaces differ legitimately. gRPC returns status errors and
sends protocol frames. Local verified subscriptions send terminal errors through
`PullerEvent`, while the legacy unverified local API closes its channel for
startup and replay failures. Shared control flow must preserve these
adapter-specific contracts.

## Decision

[`RunSubscription`](../../../../internal/puller/core/subscription.go) is the
transport-neutral owner of replay, live delivery, recovery, readiness, and
maintenance transitions. Local and gRPC adapters supply replay opening,
successful delivery, readiness publication, periodic maintenance, live-entry
notification, and terminal-result mapping.

The runner owns these invariants:

- Every replay iterator is closed exactly once. Its primary failure remains
  separate from a cleanup failure so transport mapping cannot hide either.
- A nil replay or live-queue event is a terminal source contract violation,
  not an empty replay result.
- Boundary-group deduplication is delegated to `Subscriber.ShouldSend` before
  delivery. A prospective progress marker accompanies the event, and
  `Subscriber.UpdatePosition` runs only after delivery succeeds.
- Recovery drains stale live entries while admission remains fenced, then clears
  the consumed latch immediately before opening replay from the last successful
  delivery position. Overflow during replay relatches recovery and repeats the
  transition.
- The verified Ready barrier is published only after the initial replay reaches
  live mode and at most once per subscription. Later overflow recovery does not
  publish another Ready barrier.
- Maintenance runs only in live mode. Local verified subscriptions use it for
  boundary validation; gRPC uses it for heartbeats and validates verified
  boundaries before sending each heartbeat.

A nonempty marker starts retained replay. An empty marker starts an ordinary
subscription at the current head. Verified subscriptions require a nonempty
marker accepted by boundary validation. Live capture registration still occurs
before replay opens, so concurrent events are covered by retained replay or the
bounded live queue. The existing admission fence remains owned by the
[overflow wakeup decision](../bug-fix/2026-09-16-puller-overflow-wakeup.md).

The runner returns a structured terminal result containing the exit kind, active
phase, primary error, and iterator cleanup error. Adapters preserve their
transport-specific cancellation and error mappings. Verified local subscriptions
now report malformed markers as non-retryable terminal events, consistently with
their other startup validation failures; legacy local subscriptions retain silent
closure. Puller delivery progress records only successful sends; downstream
consumers continue to own their processed checkpoints.

## Alternatives

**Keep the two state machines synchronized by tests.** Cross-adapter fixtures can
detect drift, but each transition change would still require duplicate
implementation and review. Tests would not create one owner for ordering and
cleanup rules.

**Route standalone consumers through gRPC.** This would reuse the remote state
machine but require a listener and serialization in in-process deployments.

## Consequences

- Local and gRPC adapters now share the same replay/live/recovery ordering while
  retaining their distinct admission, framing, heartbeat, cancellation, and
  terminal-error contracts.
- Equal-timestamp events remain distinguishable by EventID. Replaying the
  complete boundary group can redeliver already processed events to a new
  subscription, so consumers remain responsible for idempotency.
- A failed send cannot advance delivery progress. Overflow during live delivery
  or replay cannot allow newer live events to cross the retained-recovery fence.
- Iterator iteration and cleanup failures remain separately observable. This
  increases adapter responsibility because both errors may require reporting or
  logging even though only the primary failure determines the transport result.
- One shared runner makes transition changes affect both transports. Adapter
  callbacks therefore remain explicit so blocking sends and transport-specific
  status mapping are visible at the integration boundary.
- The shared runner does not define retained-history reconstruction, lag
  measurement, catch-up thresholds, coalescing policy, or local subscription
  quotas. [History-gap recovery](../../proposed/architecture/2026-09-07-puller-history-gap-recovery.md)
  and [consumer settings](../../proposed/bug-fix/2026-09-07-puller-admission-and-catch-up-settings.md)
  retain those decisions. The implemented
  [gRPC admission limit](../bug-fix/2026-09-07-puller-grpc-admission.md) remains
  scoped to active RPCs per Server.

The [adaptive consumption design](../../../../docs/design/server/puller/02.adaptive-consumption.md)
defines the complete runtime and adapter contracts.
