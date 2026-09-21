# Agent Note: Unify Puller Subscription State Machines

Status: proposed

## Problem

Local and gRPC subscriptions now both implement retained catch-up, live delivery,
identity-aware boundary replay, verified readiness, and overflow recovery. Their
state machines remain separate in
[`Puller.subscribe`](../../../../internal/puller/core/puller.go) and
[`Server.Subscribe`](../../../../internal/puller/grpc/server.go).

The shared [`Subscriber`](../../../../internal/puller/core/subscriber.go) owns
delivery progress, boundary-group identities, bounded live admission, and the
latched recovery handoff. The surrounding loops still duplicate replay startup,
iterator cleanup, overflow-during-replay handling, stale-queue drainage, and the
catch-up-to-live transition. A future change to those rules can therefore update
one transport without the other.

The transport error surfaces differ legitimately. gRPC returns status errors and
sends protocol frames; local verified subscriptions send terminal errors through
`PullerEvent`, while the legacy unverified local API closes its channel for some
startup failures. Shared control flow must preserve those adapter-specific
contracts.

## Proposal

Extract one transport-neutral subscription state machine for catch-up and live
transitions. Transport adapters provide successful-delivery callbacks, readiness
publication, terminal error mapping, and cancellation. The shared loop owns:

- replay creation and iterator closure;
- boundary-group deduplication through `Subscriber`;
- recovery-latch begin/check ordering and stale live-queue drainage;
- successful-delivery progress updates;
- catch-up repetition when recovery is relatched;
- transition to live delivery after replay completes.

A nonempty valid marker starts replay; an empty marker retains current-head
semantics. Register live capture before opening replay so concurrent events are
either retained by replay or held by the live queue. Preserve the existing
subscriber admission fence from the
[overflow wakeup decision](../../implemented/bug-fix/2026-09-16-puller-overflow-wakeup.md).

Define a common terminal result that adapters must map without hiding malformed
markers, iterator failures, unavailable history, or cancellation. The caller
continues to own its processed checkpoint; Puller delivery progress records only
successful sends.

## Alternatives

**Keep the two state machines synchronized by tests.** Cross-adapter fixtures can
detect drift, but each transition change still needs duplicate implementation and
review. Tests do not create one owner for ordering and cleanup rules.

**Route standalone consumers through gRPC.** This reuses the remote state machine
but requires a listener and serialization in in-process deployments.

## Acceptance Criteria

- The same retained-history fixtures pass through local and gRPC adapters,
  including multiple backends, no-history subscriptions, and readiness barriers.
- Equal-timestamp events with reversed lexical EventID order survive replay/live
  transitions without skipping distinct identities.
- Overflow during replay and live delivery reaches the retained head without
  missing events or advancing delivery progress for undelivered events.
- Invalid markers, replay failures, and expired history remain distinguishable;
  cancellation releases subscriptions and iterators within bounded deadlines.
- Adapter tests prove the intended differences between local terminal events and
  gRPC status errors.

## Risks

An abstraction that owns transport operations can obscure blocking and error
semantics. Keep transport delivery explicit and make successful send the only
progress-advance boundary. The interface change affects Indexer, Streamer, and
Trigger callers if their local error contract changes; preserving current
adapter behavior avoids forcing that migration into this refactor.

## Dependencies

[History-gap recovery](2026-09-07-puller-history-gap-recovery.md) owns proposed
recovery when retained history is unavailable. [Consumer settings](../bug-fix/2026-09-07-puller-admission-and-catch-up-settings.md)
owns proposed lag, coalescing, and local quota policy. The implemented
[gRPC admission limit](../../implemented/bug-fix/2026-09-07-puller-grpc-admission.md)
remains scoped to active RPCs per Server.
