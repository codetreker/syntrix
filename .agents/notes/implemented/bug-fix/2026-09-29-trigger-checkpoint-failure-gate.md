# Agent Note: Hold Trigger Progress Across Evaluation and Publication Failures

Status: implemented

## Problem

The evaluator previously continued after a rule evaluation or task publication
failed and could persist the source event's progress token. Resuming after that
token omitted the task that had not been published. Its checkpoint writes also
returned through Puller as document changes, causing repeated checkpoint
writes and potentially triggering rules on the internal document.

## Decision

The evaluator processes one source event at a time. For each rule, it retries
a failed evaluation or publication with cancellable exponential backoff from
one second to 30 seconds. A successful rule is not repeated while the process
keeps working on the same event. Progress becomes eligible for asynchronous
checkpoint saving only after every rule for the event has been evaluated and
every matching task has been accepted by the configured publisher. Cancellation
during a retry cannot advance past the failed event. A missing publisher for a
matching rule is an explicit service error.

The watcher excludes only the evaluator's checkpoint document, identified by
its logical database and full path. Other system documents remain observable.
This removes the self-generated event loop and keeps internal checkpoint data
out of user rule evaluation.

This gate protects the source checkpoint from known evaluation and publication
failures. It does not establish durable scheduling or delivery ownership. The
[durable scheduling proposal](../../proposed/bug-fix/2026-09-07-trigger-publish-checkpoint-ordering.md)
retains those responsibilities.

## Alternatives

**Stop the evaluator on the first failure.** This would preserve the last safe
checkpoint, but the current service manager only logs evaluator exits. A
transient broker outage would therefore require an external restart before any
more events could be handled.

**Persist scheduling before checkpointing.** A durable rule selection and outbox
would recover partial fan-out after a crash and make broker downtime independent
of the source reader. It requires stable task identity, persistence, recovery,
and dispatch ownership beyond this failure gate; the linked proposal owns that
work.

**Filter the entire system collection.** This would stop checkpoint feedback,
but would also hide other system document changes from explicitly configured
rules. The watcher filters only its own checkpoint document.

## Consequences

- A failing rule blocks this event and all later events until it succeeds or the
  service is cancelled. The retry log identifies the event, rule, operation,
  error, and next delay.
- A crash can replay previously published rules if the event's checkpoint was
  not saved. An ambiguous publication result can also be retried. Receivers
  must tolerate duplicate deliveries until stable identity and durable
  scheduling are implemented.
- If no checkpoint exists, `StartFromNow` still creates a process-local
  admission boundary. A crash before any successful checkpoint can restart
  from a later boundary and omit admitted work. Closing that window requires
  a durable live admission boundary from Puller. A first-event progress marker
  cannot replace it: normal replay would include pre-admission history from
  other backends and earlier events in the same timestamp group.
- A publisher that reports success without retaining a task, or a queue that
  later loses accepted work, remains outside this checkpoint gate.
- Checkpoint writes no longer generate trigger work or further evaluator
  checkpoints. Other system document events remain available to rules.
