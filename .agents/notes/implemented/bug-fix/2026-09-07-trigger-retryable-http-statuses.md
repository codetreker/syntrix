# Agent Note: Retry Rate-Limited Trigger Deliveries

Status: implemented

## Problem

The HTTP worker classified every 400–499 response as fatal, so the consumer
terminated HTTP 429 tasks before applying their retry policy. This contradicted
the [consumer design](../../../../docs/design/server/trigger/delivery/01.consumer.md),
which requires rate-limited deliveries to retry with backoff.

## Decision

[The worker](../../../../packages/syntrix/internal/trigger/delivery/worker/worker.go) classifies
400–499 responses other than 429 as fatal. HTTP 2xx succeeds; 429 and all other
non-2xx responses retain their retryable status classification. The failure
metric's fatal flag and returned `FatalError` agree. Transport errors and attempt
timeouts retain their error chains and retry behavior.

The original repair returned an ordinary status error for 429 and used rule
backoff only. The subsequent [Retry-After decision](../feature/2026-09-07-trigger-retry-after.md)
adds a typed relative hint and one terminal exception: a valid 429 hint beyond
the supported duration range returns `FatalError` with a sanitized explanation
and a fatal metric. Invalid or absent hints retain rule-only retry behavior.

[The consumer](../../../../packages/syntrix/internal/trigger/delivery/consumer.go) remains the
owner of attempt accounting and scheduling. Fatal failures terminate immediately.
Retryable failures use queue delivery metadata and the task's total attempt
limit, including the initial delivery. Reaching the limit logs exhaustion and
terminates the message. Otherwise the consumer combines existing rule backoff
with a supported hint and calls `NakWithDelay`, releasing the worker without
sleeping. Rule defaults, attempt limits, and the task format are unchanged.

## Alternatives

**Retry every 4xx response** would repeatedly call endpoints with invalid
credentials, routes, or payloads and consume attempts without evidence that
waiting helps.

**Deliver Retry-After support together with status classification** would also
have respected endpoint-requested pauses, but required choosing hint parsing and
delay rules beyond the existing 429 retry contract. The repair accepted rule-only
scheduling and deferred those decisions independently. The later Retry-After
decision records the delivered extension and its costs.

## Consequences

The classification repair routed rate-limited tasks through the same
bounded-attempt retry path as other retryable failures, while ordinary terminal
4xx responses still stopped immediately. It preserved retry arithmetic, queue
operations, redirect handling, and task format.

Its accepted gap was a possible retry during an endpoint's requested pause.
Hint support now extends the worker-to-consumer error boundary and scheduling
without transferring retry ownership. Its representability limit and terminal
exception are explicit in the [public guide](../../../../docs/reference/trigger_rules.md).
Retries can repeat external side effects. Stable delivery identity and durable
redrive remain owned by the proposed
[delivery idempotency design](../../proposed/architecture/2026-09-07-trigger-delivery-idempotency.md).
Queue-delayed retries retain the selected backend's existing recovery limits.
