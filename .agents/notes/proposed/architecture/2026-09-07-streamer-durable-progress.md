# Agent Note: Durable Streamer Consumption Progress

Status: proposed

## Problem

The [Streamer requirements](../../../../docs/design/server/streamer/00.requirements.md)
require replay after restart. In [the service](../../../../internal/streamer/service.go),
`s.progress = evt.Progress` updates process memory, and startup passes that field
to Puller without loading durable state. The assignment also follows failed
transformation or processing. Static inspection therefore shows an incomplete
restart contract and a checkpoint ordering problem.

Subscriptions intentionally remain soft state. Gateway re-registration is
implemented in [the remote stream](../../../../internal/streamer/remote_stream.go).
A persisted ingestion marker cannot recover subscriptions or prove delivery to
disconnected clients.

## Proposal

Persist the opaque Puller progress marker under a stable Streamer consumer
identity and load it before subscribing. Keep the aggregate marker intact;
logical databases must not overwrite separate portions independently. Establish
single-writer ownership of each checkpoint, with an explicit bootstrap policy
for a missing record and a surfaced error for unreadable or invalid state.

Advance durable progress only after an event reaches a defined processing
outcome. Transformation failures and interrupted processing retain the earlier
marker; intentionally ignored event categories must be distinguished from
failures. A gateway delivery timeout must explicitly invalidate that gateway's
continuity before ingestion can advance. Saving progress acknowledges Streamer
processing, not client receipt. Recovery may repeat events.

Serialize processing and checkpoint writes, stop consumption on unresolved
processing or persistence failure, and join the consumer during shutdown under
a bounded deadline. Record consumer
identity, checkpoint generation, event correlation, and failure category without
document bodies. Document the new checkpoint schema and bootstrap procedure.

## Alternatives

**Periodic checkpoints.** Batching reduces writes but increases replay after a
crash. It is worth reconsidering after measuring persistence cost, provided the
replay interval is bounded and duplicates remain safe.

**Per-client durable delivery queues.** These can retain deliveries after
disconnection, but introduce subscription storage and retention ownership beyond
the upstream consumption contract. Client continuity belongs to the linked
resume proposal.

## Acceptance Criteria

- A restart passes the last committed aggregate marker to local and remote
  Puller paths; alternating database events do not overwrite each other's
  progress.
- Injected transformation, checkpoint-write, and cancellation failures cannot
  commit progress past unresolved work; restart can replay it.
- Concurrent ownership is rejected, missing-state bootstrap is explicit, and
  expired history produces an actionable recovery state.
- Gateway timeout and Streamer restart cannot be reported as successful
  end-client continuity solely because a checkpoint exists.

## Risks

Persistence adds ingestion latency and operational state. Deferral leaves
restarts dependent on an empty marker; adding storage without defining the
acknowledgment boundary would preserve silent loss. Stable consumer identity
must survive deployment changes without allowing concurrent writers.

## Dependencies

[Puller subscription replay](../../implemented/architecture/2026-09-07-puller-subscription-state-machine.md) and
[history-gap recovery](2026-09-07-puller-history-gap-recovery.md) provide upstream
recovery. [Client resume](../feature/2026-09-07-realtime-client-resume.md) owns
downstream continuity; [consumer scaling](2026-09-07-consumer-shard-scaling.md)
owns future ownership transfer.
