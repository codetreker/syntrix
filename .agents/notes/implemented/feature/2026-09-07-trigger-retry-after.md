# Agent Note: Honor Retry-After for Trigger Deliveries

Status: implemented

## Problem

The [429 classification repair](../bug-fix/2026-09-07-trigger-retryable-http-statuses.md)
restored rule-based retries, but did not use an endpoint's requested pause.
Retries could arrive during that pause and amplify overload. Honoring response
hints requires an explicit relationship between endpoint delay, rule backoff,
and attempt limits.

## Decision

[The HTTP worker](../../../../packages/syntrix/internal/trigger/delivery/worker/worker.go) parses
`Retry-After` only on HTTP 429. Parsing accepts the current time as an argument,
so date handling is deterministic under a controlled clock. A positive hint is
carried as a relative `time.Duration` in `RetryAfterError`, which wraps the
existing status error and preserves its error chain. The task wire format is
unchanged.

| Response hint | Outcome |
|---|---|
| One nonnegative decimal delay-seconds value | Use the positive duration; zero leaves rule backoff in control |
| One supported HTTP-date in the future | Use the positive duration relative to response parsing time |
| Missing, empty, malformed, signed, negative, zero, or past/equal date | Use rule backoff |
| Multiple physical header fields or a malformed combined value | Use rule backoff |
| A valid delay beyond `time.Duration` range | Return `FatalError` and record a fatal delivery failure |
| A hint on any response other than 429 | Ignore the hint; preserve status classification |

Leading and trailing space or tab is ignored. HTTP-date parsing accepts the
standard library's supported HTTP date formats. The parser never includes raw
header content in errors. An unrepresentable valid delay produces the sanitized
message `Retry-After exceeds the maximum supported delay`, retaining HTTP 429
in the surrounding error. This distinction prevents overflow or saturation from
silently scheduling an earlier retry.

[The consumer](../../../../packages/syntrix/internal/trigger/delivery/consumer.go) keeps ownership
of attempts and scheduling. It first computes existing rule backoff, applying a
positive `maxBackoff` to that component, then schedules:

```text
next delay = max(capped rule backoff, Retry-After hint)
```

A hint is a minimum delay and can exceed `maxBackoff`. No new business ceiling
is imposed; every representable positive hint is honored. The technical limit
is Go's signed 64-bit nanosecond duration, approximately 292 years. Attempt
limits and defaults are unchanged: exhaustion logs and terminates before another
retry is scheduled. `NakWithDelay` releases the worker without a local sleep.
Its errors are logged without acknowledging or terminating the message as a
substitute for successful scheduling.

Scheduling diagnostics include the rule backoff, parsed hint, selected delay,
next attempt, limit, and existing task fields: trigger, database, collection,
document, LSN, and sequence. Scheduling failures include the delay and those
fields. These fields aid correlation without establishing a new stable delivery
identity. Raw response headers, bodies, and request payloads are not recorded
by hint parsing or retry scheduling.

## Alternatives

**Use rule backoff only** was the initial classification repair's contract. It
fixed lost retries independently of hint policy, but could send during a
requested pause. Hint-aware scheduling now closes that gap for 429.

**Deliver hint support together with the classification repair** would have
removed the gap immediately, but coupled the existing retry contract to new
parsing and scheduling decisions. Separating them preserved the consumer's retry
ownership and made the extension independent of the original status repair.

**Cap the endpoint hint at `maxBackoff`** would limit queue delay using existing
configuration, but could retry before the endpoint's requested pause ends. The
rule cap therefore applies before combining the two delays.

**Introduce an additional business delay ceiling** would limit unusually long
waits, but requires a new policy value and changes which endpoint pauses can be
honored. The current limit is representability; a future operational ceiling
would need an explicit public contract.

## Consequences

429 followed by success uses queue redelivery within the existing attempt budget.
Ordinary terminal 4xx responses stop immediately, and 5xx, transport failures,
and timeouts retain their retry behavior. Malformed hints preserve rule backoff;
valid unrepresentable hints terminate visibly instead of becoming short retries.

Large representable hints can retain delayed work for very long periods. In-memory
delays retain timer callbacks and task data, and queue scheduling or retention
limits remain the selected backend's responsibility. The in-memory queue does
not gain shutdown or process-restart recovery. HTTP-date interpretation depends
on the worker's clock, so clock skew can change the effective pause. Relative
hint propagation does not promise exact wall-clock execution at the given date.

Retries can repeat external side effects. Stable identity, cooperative receiver
deduplication, and durable redrive remain owned by the proposed
[delivery idempotency design](../../proposed/architecture/2026-09-07-trigger-delivery-idempotency.md).
This change preserves existing queue durability and does not establish exactly-once
effects. The [public guide](../../../../docs/reference/trigger_rules.md#delivery-retries)
owns the observable scheduling contract.
