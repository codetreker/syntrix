# Agent Note: Capture Trigger Before Images

Status: proposed

## Problem

Rules expose `includeBefore`, and
[CEL evaluation](../../../../packages/syntrix/internal/trigger/evaluator/cel/evaluator.go) can read
`event.before`. However, [the raw event](../../../../packages/syntrix/internal/puller/normalizer/normalizer.go)
and [buffered event](../../../../packages/syntrix/internal/puller/events/types.go) do not carry a
previous document, and [transformation](../../../../packages/syntrix/internal/puller/events/transform.go)
never populates `Before`. The supported expression therefore lacks upstream data.
This is a missing integration established by static inspection.

## Proposal

Capture genuine storage pre-images and carry them through normalization, durable
Puller buffering, local and gRPC transport, and event transformation. Verify the
supported MongoDB deployment's pre-image capability and retention prerequisites
before selecting the concrete collection configuration. Do not reconstruct a
previous document by reading its current state after the event.

Define `includeBefore` as permission to expose the captured previous image to
that rule's evaluator and delivery payload. A create has no previous image;
updates and logical soft deletes expose the previous stored document when the
rule requests it. Preserve the existing exclusion of physical cleanup deletes.
Reject activation of a before-dependent rule when the storage path cannot supply
its required images. If retention or source failure makes an expected image
unavailable later, report an explicit event failure with recoverable position.

Version the buffered event representation and transport schema together. Existing
buffer records cannot acquire missing historical images through a format change:
define an activation boundary and an explicit operator recovery or rejection
policy for older history. Scope storage enablement and retention by backend,
accounting for multiple logical databases sharing a collection.

## Alternatives

**Maintain a consumer-side previous-document cache** avoids source pre-image
configuration but requires complete bootstrap and gap-free history; after a gap,
it can mistake stale data for the previous version.

**Use only update deltas** reduces payload and storage cost but cannot recover
removed fields and cannot satisfy arbitrary comparisons with the old document.

## Acceptance Criteria

- Create, update, and soft-delete events expose the documented before/after
  values consistently in live, replayed, local, and remote delivery paths.
- Two logical databases sharing a backend retain distinct document identities
  and cannot receive each other's images.
- Unsupported source configuration rejects rule activation; expired images and
  pre-activation history produce explicit failures, never fabricated images.
- A restart preserves captured images and their event identity; a rule without
  `includeBefore` receives no previous document in evaluation or delivery.

## Risks

Pre-images increase source retention and buffer size and retain historical
sensitive data. Retention and access policy must cover the complete event path.

## Dependencies

[History-gap recovery](../architecture/2026-09-07-puller-history-gap-recovery.md)
owns unavailable-history recovery;
[delivery envelopes](../bug-fix/2026-09-07-trigger-delivery-event-envelope.md)
own the webhook representation.
