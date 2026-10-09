# Agent Note: Enforce Trigger Execution Controls

Status: proposed

## Problem

[Trigger configuration](../../../../packages/syntrix/internal/trigger/types/types.go) accepts
`Concurrency` and `RateLimit`, but task construction does not carry them and
[the consumer](../../../../packages/syntrix/internal/trigger/delivery/consumer.go) uses only a
global worker count. A configured rule currently cannot bound its own concurrent
requests or dispatch rate. This finding comes from the configuration-to-consumer
call path, not a load test.

## Proposal

Define limits per `(database, triggerId)` across the delivery deployment. Carry
the evaluated rule version and effective controls in the task. `concurrency`
bounds active HTTP attempts; `rateLimit` means attempts per second, with an
explicit bounded burst. Retries consume both budgets. An omitted concurrency
uses the documented service limit, an omitted rate means unlimited, and negative
values fail validation.

Introduce one authoritative admission decision for each rule across replicas,
using bounded, fenced reservations whose owner releases them when an attempt
finishes. The implementation must select and validate its coordination mechanism
before shipping; local counters cannot satisfy the deployment-wide promise.
Expired ownership must cancel or fence the old attempt before admitting a
replacement, because lease expiry alone cannot stop an HTTP side effect.

Keep pending tasks in bounded queues and schedule eligible rules fairly so a
throttled endpoint cannot exhaust all workers. Cancellation releases admission
and returns uncompleted tasks for redelivery. Record admission wait, active
attempts, and throttling by rule using the shared observability facilities.

## Alternatives

**Per-process limits** require no coordination, but total capacity changes with
replica count and permits configured limits to be exceeded. They are acceptable
only if the public setting is explicitly defined as a per-process budget.

**Dedicated consumers per rule** simplify ownership but create broker resources
proportional to rule count and still require failover fencing. This remains an
option if measured rule cardinality and operations costs support it.

## Acceptance Criteria

- Concurrent delivery replicas never exceed a rule's declared active-attempt
  or rate budget under normal operation, retries, cancellation, and owner loss.
- Identical trigger IDs in different databases have independent budgets.
- A saturated rule leaves another eligible rule able to deliver; queued work
  stays within documented bounds and survives a worker restart as specified.
- Invalid controls are rejected before rule activation; effective controls and
  rate units are documented and observable without logging document payloads.

## Risks

Strict deployment-wide admission adds coordination latency and availability
cost. HTTP cancellation cannot prove remote work stopped, so the guarantee must
state its client-attempt boundary precisely. Rule updates must define how queued
versions share the current budget.

## Dependencies

[Consumer shard scaling](../architecture/2026-09-07-consumer-shard-scaling.md)
owns distributed ownership and handoff; this proposal owns rule admission.
[Application observability](../architecture/2026-09-07-application-observability.md)
owns telemetry infrastructure.
