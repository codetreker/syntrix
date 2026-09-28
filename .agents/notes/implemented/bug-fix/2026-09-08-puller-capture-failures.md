# Agent Note: Stop Puller Progress After Capture Failures

Status: implemented

## Problem

A failed cache batch could stop the inner flush function while leaving the
batcher loop running. Its shutdown flush could then commit a later queued batch
and advance the resume token past the failed events. Event decoding,
normalization, and admission errors could also be skipped. Checkpoint read or
resume-history failures could open a fresh stream, concealing missing history.

## Decision

The existing [Puller capture](../../../../docs/design/server/puller/01.architecture.md#42-capture-startup-and-failure)
keeps one native BSON resume token per backend, committed atomically with the
batch's events. Reconnection reloads that durable token.

| Failure | Handling |
|---|---|
| Batch preparation, commit, or close | Stop the batcher before any later batch; retain the failure |
| Checkpoint read, event decode/normalization, or admission | Stop the affected backend before later events can pass the failure |
| Unusable resume token or lost source history | Stop the backend and retain its checkpoint |
| Native connection failure | Keep the existing retry from the durable token |

Earlier successfully admitted events can still drain during normal shutdown.
After a batch failure, later writes and checkpoint reads expose the original
cause; repeated buffer close and Puller stop retain the failure. A stop timeout
leaves buffer ownership with the running backend until its worker finishes.

## Alternatives

**Require cache-owned progress and commit before publication.** The
[rejected publication proposal](../../rejected/architecture/2026-09-07-puller-persist-before-publish.md)
combined these architecture changes with capture error handling. The capture
failures are fixed without changing checkpoint authority or publication timing.

**Return event-processing errors through ordinary reconnect.** If no durable
token exists yet, the next `from_now` watch can skip the failed first event.
These failures therefore terminate the backend before ordinary network retry.

**Flush remaining writes after a failed batch.** That can commit a later token
without the failed earlier events. Final draining is reserved for a healthy
writer; a failed writer requires recovery from retained source progress.

## Consequences

Checkpoint bytes, cached event values, consumer progress, batching, and live
publication retain their existing formats and behavior. No migration is needed.
Live delivery may precede cache commit, so this repair alone does not establish
end-to-end consumer recovery.

Background write failure is observed on the next write/checkpoint read or during
shutdown. It does not immediately interrupt an otherwise idle native cursor.
Consumer-visible history errors, cross-Puller replay, and cache-miss source
replay remain in the [history recovery](../../proposed/architecture/2026-09-07-puller-history-gap-recovery.md)
proposal. The shared
[subscription replay state machine](../architecture/2026-09-07-puller-subscription-state-machine.md)
is implemented, while history recovery remains separate work. This repair adds
no new checkpoint authority or format to constrain either mechanism.
