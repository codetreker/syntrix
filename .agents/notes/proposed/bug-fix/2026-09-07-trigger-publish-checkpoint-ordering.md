# Agent Note: Advance Trigger Checkpoints After Durable Scheduling

Status: proposed

## Problem

The evaluator now [blocks progress beyond reported evaluation and publication
failures](../../implemented/bug-fix/2026-09-29-trigger-checkpoint-failure-gate.md).
That process-local gate has no durable record of which rules were selected or
which tasks were accepted. A crash during partial fan-out can replay successful
publications, and replay after a rule edit can select a different rule set.
Publisher success alone does not preserve a task across loss of an in-memory
queue. Source progress and task recovery therefore lack one durable ownership
boundary.

## Proposal

Track the highest contiguous event position whose durable scheduling is
complete. Use the same boundary in standalone and distributed modes: persist
rule selection and evaluation outcomes, create a durable outbox record for every
matched task, then mark scheduling complete. An event matching no rules needs a
persisted successful no-match outcome. The [delivery identity proposal](../architecture/2026-09-07-trigger-delivery-idempotency.md)
owns those records and their recovery; this proposal owns source progress.

An evaluation failure, outbox write failure, or incomplete fan-out blocks
advancement across that event and enters cancellable retry or an explicit
operator-visible failure state. Recovery uses the persisted rule selection and
outcomes; it must not combine partial scheduling with a newly loaded rule set.

Broker acknowledgement records dispatch progress. Once scheduling is durable,
the source checkpoint may advance while the broker is unavailable because the
outbox retains unpublished tasks. Memory enqueue or broker acknowledgement
alone cannot make an incompletely scheduled event checkpoint-eligible.

Keep asynchronous checkpoint coalescing, but save only completed progress and
retry failed checkpoint writes. Stop and join the saver when the source closes.
On shutdown, use a bounded flush context independent of the cancelled
processing context. Validate required storage and dispatcher dependencies during
service assembly. Establish a durable live admission boundary before a new
evaluator can receive its first event; the current empty-token `StartFromNow`
subscription has no such boundary. Recovery must reuse retained
scheduling records when a crash precedes checkpoint persistence. Previously
skipped work needs an explicit replay boundary chosen from retained source
history.

## Alternatives

**Keep the process-local failure gate as the final boundary.** It protects
against reported failures while the evaluator remains alive, but cannot recover
partial fan-out identity or tasks lost after a publisher reports success.

**Use broker acknowledgement as the checkpoint boundary.** It avoids scheduling
storage, but does not preserve rule selection during partial fan-out and cannot
provide the same recovery behavior for the standalone memory queue.

**Use one transaction across source, checkpoint, and broker.** This could offer
stronger atomicity, but the current storage and broker interfaces do not share
such a transaction. Durable scheduling plus replay separates these boundaries.

## Acceptance Criteria

- Incomplete rule selection, evaluation, or outbox fan-out never makes a later
  source position checkpoint-eligible.
- A broker outage permits progress after complete durable scheduling; restart
  and broker recovery dispatch retained tasks with their original identities.
- Crashes during scheduling, fan-out, publication, or checkpoint persistence
  reuse recorded rules and outcomes without silently omitting tasks.
- Storage and checkpoint failures remain observable, cancellable, and bounded
  by backpressure or an explicit failure state.
- Source closure and cancellation terminate checkpoint work within the
  documented deadline; aggregate progress preserves cross-database order.
- First startup survives a crash before any event is processed without
  silently restarting from a later `StartFromNow` admission point.

## Risks

Scheduling failure can delay unrelated databases behind the global checkpoint.
Broker outages grow the outbox, so admission bounds must propagate backpressure.
Retained task identity and rule selection require storage and cleanup policies.

## Dependencies

[Delivery identity](../architecture/2026-09-07-trigger-delivery-idempotency.md)
owns durable records and dispatch. [Puller subscription replay](../../implemented/architecture/2026-09-07-puller-subscription-state-machine.md)
and [history-gap recovery](../architecture/2026-09-07-puller-history-gap-recovery.md)
own source event availability.
