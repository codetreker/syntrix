# Agent Note: Integrate Remaining Puller Consumer Settings

Status: proposed

## Problem

Operators can configure catch-up policies that do not govern subscriptions.
[`Configuration`](../../../../internal/puller/config/puller.go) defines
`CatchUpThreshold` and `Consumer.CoalesceOnCatchUp`, but production usage stops
at configuration initialization and validation. gRPC Subscribe passes the
request's `coalesce_on_catch_up` value directly into its subscriber, and local
subscriptions always disable coalescing.

Queue overflow remains the implemented runtime catch-up trigger. The shared
subscriber recovery latch wakes idle local and gRPC consumers and prevents later
live events from crossing missing retained history, as defined by the
[overflow wakeup decision](../../implemented/bug-fix/2026-09-16-puller-overflow-wakeup.md).
Configured backlog thresholds and server-side coalescing policy are still not
integrated.

[gRPC admission](../../implemented/bug-fix/2026-09-07-puller-grpc-admission.md)
enforces `MaxConnections` as active `Subscribe` RPCs per Server. Local
subscriptions remain outside that quota and expose no equivalent rejection
contract.

## Proposal

Apply `catch_up_threshold` to each subscription's per-backend backlog. Define the
reference head, measurement units, and bounded measurement frequency before
implementation. Crossing the threshold enters the same retained-replay state
machine as queue overflow without weakening the existing immediate overflow
trigger.

Treat server `coalesce_on_catch_up` as permission. An individual subscription
must also opt in before replay may merge events. This preserves every transition
for Trigger and other consumers that require non-coalesced delivery.

Carry the effective policies through local and remote construction and document
their precedence. Extending admission to local subscriptions requires an
explicit quota owner, counting unit, and local rejection surface. Preserve the
delivered per-Server gRPC quota until those decisions are made. Any shared quota
must count subscription registrations independently of diagnostic consumer
labels, as required by
[subscription identity isolation](../../implemented/bug-fix/2026-09-09-puller-subscription-identity.md).

## Alternatives

**Remove the unused settings.** This makes configuration reflect current runtime
behavior but removes intended operator control over catch-up cost. Reconsider if
load tests show backlog measurement costs more than its operational benefit.

**Coalesce every catch-up stream.** This reduces replay traffic but changes event
semantics for consumers that require intermediate transitions.

**Apply the gRPC connection limit directly to local subscriptions.** The setting
currently counts active RPCs owned by one gRPC Server. Reusing it locally would
change its counting unit and lacks a caller-visible rejection contract.

## Acceptance Criteria

- A controlled backlog crosses the configured threshold and enters catch-up
  without requiring queue overflow; backend progress remains independent.
- Coalescing occurs only when both server policy and the caller permit it;
  non-coalescing consumers retain every event locally and remotely.
- Invalid settings fail validation, and documented values survive configuration
  loading and production service assembly to observable runtime behavior.
- Local admission exposes rejection through an explicitly defined quota without
  weakening the existing per-Server gRPC limit or subscription identity rules.

## Risks

Counting backlog can amplify retained-buffer reads across many consumers.
Coalescing changes the number of delivered events and requires diagnostics for
effective policy and mode transitions. Deferring local admission leaves
in-process subscriptions outside the gRPC quota. Deferring lag policy leaves
queue overflow as the only runtime catch-up trigger; its recovery guarantee must
remain independent of future threshold measurement.

## Dependencies

[Subscription state-machine unification](../../implemented/architecture/2026-09-07-puller-subscription-state-machine.md)
owns the delivered control-flow consolidation. Lag measurement requires its own
confirmed reference-head definition; the rejected
[publication scheme](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md)
supplies none.
