# Agent Note: Puller History-Gap Recovery

Status: proposed

## Problem

Consumers cannot distinguish a complete resume from a stream whose history is
unavailable. [Capture failure handling](../../implemented/bug-fix/2026-09-08-puller-capture-failures.md)
now stops the backend on unusable resume history and retains its checkpoint;
consumer-visible history errors and recovery remain unimplemented. Meanwhile,
[Replay](../../../../internal/puller/core/puller.go#L443) scans after the supplied
key without checking a durable retention boundary, although the
[cleaner](../../../../internal/puller/buffer/cleaner.go) evicts history.

The call `backend.gapDetector.RecordEvent(evt)` in
[ingestion](../../../../internal/puller/core/puller.go#L373) ignores its boolean
result. That detector measures elapsed event time; quiet databases can produce
the same interval as lost history. Its warning alone cannot establish loss.
These findings come from static inspection.

## Proposal

The boundary and progress-marker mechanisms remain draft after rejection of the
[publication proposal](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md).
Its cache-generation/sequence scheme is not an implementation prerequisite.
Retain the requirement to detect unavailable source history and report a
structured history-unavailable result identifying the affected backend and
recovery reason, without presenting a discontinuity as a successful resume.
Keep idle-time warnings diagnostic; confirm continuity failure through storage
history or an authoritative change-stream failure.

Expose the same result locally and over gRPC. Consumers stop advancing their
checkpoint and invoke their own recovery policy: Indexer rebuild, Streamer
subscription resynchronization, and an explicit Trigger lost-history incident.
A current snapshot cannot reconstruct historical webhook side effects.
Incompatible markers must fail visibly and require deliberate recovery; sorting
old timestamp/hash keys cannot establish a valid continuation. A concrete
marker format and upgrade procedure require a separately confirmed decision.

## Alternatives

**Increase retention.** This reduces expiry frequency but cannot cover every
outage, explicit eviction, or invalid upstream resume token.

**Rebuild on every long event interval.** This requires little new metadata but
turns normal inactivity into expensive recovery and still misses shorter losses.

## Acceptance Criteria

- Retention eviction, upstream history loss, and restart each preserve a durable
  boundary; a stale marker never resumes as if continuity were intact.
- A quiet backend resumes normally when its history remains valid.
- Multi-backend markers identify the failed backend; unaffected database state
  is preserved while each consumer reports its required recovery action.
- Crash tests around eviction and source-history loss expose either the prior
  valid state or the new discontinuity, never a falsely continuous stream.
- Equal-timestamp events with reversed event-ID/hash order remain ordered by
  source event order after restart; resuming cannot skip a later event merely
  because its timestamp equals an earlier event's timestamp. Unsupported
  markers fail explicitly.
- Local and remote callers receive the same failure meaning and cannot advance
  their processed checkpoint across the gap.

## Risks

Metadata and marker changes require coordinated consumer rollout. Rebuilds cost
storage reads; webhook history may be irrecoverable. Record backend,
recovery reason, and affected consumer identity without raw tokens or payloads.

## Dependencies

[Puller subscription replay](../../implemented/architecture/2026-09-07-puller-subscription-state-machine.md)
carries errors to local and gRPC adapters;
[Indexer recovery](2026-09-07-indexer-recovery-lifecycle.md) and
[Streamer durable progress](2026-09-07-streamer-durable-progress.md) own their
recovery workflows.
