# Agent Note: Enforce Puller gRPC Subscription Admission

Status: implemented

## Problem

`puller.grpc.max_connections` was loaded and validated but did not limit
subscriptions. Each additional `Subscribe` RPC allocated a queue and could open
replay work, so the configured limit did not bound these per-subscription costs.

## Decision

The [gRPC subscription contract](../../../../docs/design/server/puller/01.architecture.md#53-grpc-admission)
limits active `Subscribe` RPCs independently in each Puller gRPC Server. The
configuration name remains `max_connections`, with an effective default of 100.
The constructor resolves nonpositive values to that default, matching existing
configuration defaulting; zero does not mean unlimited.

| Case | Admission behavior |
|---|---|
| Several streams share a TCP connection | Each RPC consumes one slot |
| Equal or empty `consumer_id` labels | Each RPC consumes one slot |
| One stream covers several backends | One slot in total |
| Catch-up switches to live, or live overflows | Retain the same slot |
| Capacity is full | Return `ResourceExhausted` before allocating the subscriber queue or starting replay |
| An admitted handler returns | Remove its registration and release the slot |
| An initialized server starts shutdown | Reject later admission with `Unavailable`; close existing registrations |

The existing Server lifecycle lock serializes the stopping check, manager count,
and registration. Admission is atomic with other admissions and shutdown. The
registry remains keyed by the subscription object, as specified by
[subscription identity isolation](2026-09-09-puller-subscription-identity.md), and
the handler's deferred removal owns ordinary cleanup. No separate quota counter
or lease is needed.

For server-streaming gRPC, successful creation of a client stream does not imply
admission. A rejected stream reports its status through `Recv`. Existing Puller
clients retry stream failures using their current progress marker; capacity
exhaustion can therefore delay the start or resumption of consumption.

## Alternatives

**Count TCP connections.** One transport can carry several subscriptions, each
with its own queue and replay work. Transport counts cannot bound those costs.

**Rename the setting.** A subscription-specific name would remove ambiguity,
but requires operator configuration changes. An explicit RPC-count contract
preserves the existing setting while making its unit clear.

**Unify local and remote admission.** A common quota would require choosing an
owner and extending the local channel-only subscription API to expose rejection.
Those decisions belong to the deferred local integration work.

## Consequences

- Healthy registered streams retain their delivery behavior. Excess
  subscriptions fail immediately; the server does not queue requests waiting
  for a slot.
- Admission and cleanup acquire locks per subscription lifecycle transition;
  event delivery does not acquire an additional admission lock.
- A count limit bounds registered subscription queues and concurrent replay
  handlers, not payload bytes, TCP connections, or total process memory.
- Shutdown seals admission after initialization, but does not promise immediate
  interruption of replay or transport operations already blocked in I/O.
- Local subscriptions remain outside the gRPC quota. Their error contract and
  shared quota ownership require later integration; the existing gRPC quota
  stays owned by the Server so it does not silently become a process-wide limit.
- The [remaining settings proposal](../../proposed/bug-fix/2026-09-07-puller-admission-and-catch-up-settings.md)
  owns lag thresholds, coalescing policy, and local integration. Those settings
  retain their current behavior until their measurement and precedence contracts
  are decided. Checkpoints, replay ordering, and the wire schema are unchanged.
