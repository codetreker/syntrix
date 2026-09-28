# Agent Note: Persist Puller Events Before Publication

Status: rejected - Requires cache-owned consumer progress and pre-publication commits beyond the capture bugfix delivered in [PR #128](https://github.com/codetreker/syntrix/pull/128).

## Problem

An event can reach consumers before its durable write succeeds. In
[Buffer.Write](../../../../internal/puller/buffer/writer.go#L51),
`b.pending = append(b.pending, req)` precedes a successful return. The background
batcher later calls `batch.Commit(pebble.Sync)`, while
[ingestion](../../../../internal/puller/core/puller.go#L382) immediately invokes
`p.invokeHandlerWithBackpressure(ctx, backend, evt)` after Write returns.
[ScanFrom](../../../../internal/puller/buffer/reader.go#L66) also includes a
snapshot of pending writes. Static inspection shows that neither publication
path establishes the durable premise required by overflow replay.

Replay ordering also lacks an ingestion position. The
[buffer key](../../../../internal/puller/events/types.go#L124) uses
`FormatBufferKey(e.ClusterTime, e.EventID)`, while
[event IDs](../../../../internal/puller/normalizer/normalizer.go) contain a hash.
A later event with the same timestamp and a smaller hash sorts behind an already
saved replay cursor and can be excluded after restart.

## Proposal

Assign a monotonically increasing position in observed ingestion order within
each backend and continuity generation. Atomically sync the event, its position,
the new committed frontier, and MongoDB resume token in one batch before
publication. Restore the frontier on startup and never reuse a committed
position within its generation. Key the replay buffer by this position and use
the same position in progress markers, replay bounds, and ordered live delivery;
cluster timestamps remain event metadata.

Preserve a stable identity derived from the complete upstream event identity
independently of the assigned position. Persist it with the event so re-reading
an already committed retained event reuses its identity and position rather than
allocating a second record. The event ID must distinguish distinct upstream
events even when their timestamp and document identity match. This handles
ingestion replay; it does not guarantee exactly-once downstream processing.

Preserve batching through an ordered per-backend commit pipeline with explicit
completion results. Live broadcast and replay expose only the committed
frontier. The sole ingestion loop must be able to form a batch before waiting
for that batch's completion.

On a batch failure, propagate the original error chain to ingestion, fail all
affected pending completions, and stop that backend from publishing later events.
Recovery resumes from the last durable token. Normal shutdown stops admission,
drains admitted writes within its deadline, and reports any flush failure;
cancellation releases waiting producers and publication workers. A crash after
commit but before delivery can cause replay duplicates, which consumers must
handle through their processed checkpoint or idempotent application.

## Alternatives

**Sync every event immediately.** This provides an obvious durable boundary but
pays a storage synchronization cost per event and discards the intended batching
benefit. It remains useful as a correctness reference for tests.

**Publish pending writes and rely on MongoDB replay.** This lowers latency but
allows consumers to save positions that the local replay store cannot supply
after a crash or commit failure.

**Order equal timestamps by event-ID hash.** This gives deterministic key sorting
but cannot represent arrival order; a future event can sort before a previously
published cursor. It cannot serve as the resume boundary.

## Acceptance Criteria

- A blocked batch commit prevents its events from appearing in local delivery,
  gRPC delivery, or replay; successful commit releases them in backend order.
- Injected commit failure reaches the owning backend and all affected waiters;
  no later event or checkpoint is published past that failure.
- Restart before and after commit preserves atomic event/position/frontier/token
  state and resumes from the durable token, including independent backend
  failures. Re-reading a committed retained event preserves its identity and
  position; a distinct upstream event receives a distinct identity and position.
- Equal-timestamp events ingested in reverse lexical event-ID/hash order receive
  increasing positions. After the first is processed and checkpointed, crashes
  before and after the second commits cannot make its replay disappear behind
  the checkpoint or cause a committed position to be reused.
- Cancellation and timed shutdown release workers and return flush failures.
- Batched ingestion retains configured batch size and interval behavior under
  sustained load.

## Risks

The commit interval becomes part of delivery latency. Position and identity
metadata change buffer and wire formats. Existing timestamp/hash keys cannot
reconstruct ingestion order: migrate through authoritative ordered upstream
history where available, otherwise invalidate continuity and require consumer
recovery. Disk stalls require bounded admission and visible backend failure.
Record batch counts, commit duration, backend, position, and stable event identity
without resume tokens or document payloads.

## Dependencies

[Pending-write bounds](../../implemented/bug-fix/2026-09-07-puller-pending-write-bound.md) owns
admission capacity. [Puller subscription replay](../../implemented/architecture/2026-09-07-puller-subscription-state-machine.md)
consumes the committed position and frontier.
[History-gap recovery](../../proposed/architecture/2026-09-07-puller-history-gap-recovery.md) owns continuity
generations, retention boundaries, and invalidation during migration.
