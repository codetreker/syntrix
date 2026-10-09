# Agent Note: Honor Trigger Rule Timeouts

Status: implemented

## Problem

Task construction replaced each rule's timeout with the service default, losing
configured values before delivery. The HTTP worker also imposed an implicit
5-second client timeout, which could truncate a longer task budget. The former
10-second task default and the documented 30-second default disagreed.

## Decision

[Rule validation](../../../../packages/syntrix/internal/trigger/evaluator/validation.go) rejects
negative timeouts before a new rule set replaces the active set. Malformed or
out-of-range duration strings remain configuration parsing errors; no additional
upper bound is imposed. Positive values are accepted unchanged.

[Task construction](../../../../packages/syntrix/internal/trigger/evaluator/service.go) captures
the effective timeout into each delivery task:

| Rule timeout | Task timeout |
|---|---|
| Omitted or zero | 30 seconds |
| Positive duration | The configured duration |
| Negative duration | Rule activation fails; the previous active rules remain |

Normalization occurs when constructing the task, without mutating the loaded
rule. Retry deliveries retain that captured value; later rule updates affect
newly created tasks. The 30-second default is explicit and replaces the earlier
10-second value.

[The consumer](../../../../packages/syntrix/internal/trigger/delivery/consumer.go) starts a fresh
attempt context after queue waiting, immediately before calling the worker.
Its deadline covers request preparation, secret resolution, system token
signing, and HTTP work together. Retries each receive a fresh budget from the
same task timeout. An earlier parent cancellation or deadline still applies.
A zero timeout in an existing task uses the same 30-second default.

[The worker](../../../../packages/syntrix/internal/trigger/delivery/worker/worker.go) uses the
caller's context for its execution deadline. Zero `HTTPClientOptions.Timeout`
adds no total HTTP client cap; an explicitly positive option retains its cap.
The implicit 5-second fallback and its constant are removed. Direct worker
callers are responsible for supplying a bounded or cancellable context;
`ProcessTask` does not derive a deadline from the task field itself.

Context-aware secret resolution and HTTP work observe cancellation. Local RSA
system-token signing is synchronous and cannot be preempted through the current
identity API. Its elapsed time consumes the attempt budget, but completion can
occur after that deadline. This repair does not change the identity API or
promise hard interruption of synchronous preparation.

## Alternatives

**A single service timeout** simplifies operations but cannot meet the existing
per-rule setting's intent. It would require explicitly removing that setting.

**Silently use the minimum of rule and service timeouts** provides a global cap,
but truncates valid settings without a configuration error. The implicit cap is
removed. An explicit positive worker option remains an intentional caller limit;
a future rule-level maximum would require a visible validation contract.

## Consequences

Rules now control each captured attempt budget, including preparation and HTTP
work. Timeout errors retain their underlying context cause and enter the existing
retry policy; attempt counts, backoff, and queue operations are unchanged.
Longer budgets can retain sockets and worker capacity for longer, and the default
budget increases to 30 seconds.

Broker acknowledgement timing remains independent of this execution deadline.
Its window includes queue residence and execution, so long waits or attempts can
permit redelivery while an earlier attempt remains active. The deferred
[durable task handoff proposal](../../proposed/architecture/2026-09-07-trigger-acknowledgement-window.md)
explores persistence before broker Ack and execution from shared database state.
The timeout repair does not establish non-overlapping broker delivery.

Cancellation cannot undo remote side effects. Stable identity and receiver
cooperation remain owned by the proposed
[delivery idempotency design](../../proposed/architecture/2026-09-07-trigger-delivery-idempotency.md).
The [public timeout contract](../../../../docs/reference/trigger_rules.md#delivery-timeouts)
states the default, timing boundary, explicit client cap, and cancellation limits.
