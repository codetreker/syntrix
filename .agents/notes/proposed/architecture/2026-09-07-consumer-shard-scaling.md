# Agent Note: Consumer Shard Scaling

Status: proposed

## Problem

Horizontal consumer partitioning remains planned capability.
[The existing design](../../../../docs/design/server/puller/future/001.shard-scaling.md)
explicitly calls it a "future enhancement" and proposes per-subscription field
hashing. [SubscribeRequest](../../../../api/proto/puller.proto) currently contains
consumer_id, after, coalesce_on_catch_up, and require_ready, with no shard
configuration.
The live [broadcast implementation](../../../../internal/puller/core/subscriber.go)
sends each event to every subscription. Adding replicas alone therefore does
not partition the downstream workload.

## Proposal

Deliver a versioned shard contract covering routing, progress, and ownership.
Specify canonical field encoding, ordered field selection, hash algorithm,
shard count, and subscribed shard IDs. Apply identical filtering during replay
and live delivery, including heartbeats that advance the scanned backend
position when no matching events are delivered. Bind consumer checkpoints to
the routing generation so changing the shard set cannot reuse an incompatible
position silently.

Support fields whose values are available for every affected operation. The
future design permits arbitrary document fields; that choice needs an explicit
constraint: mutable keys require old/new owner transitions, and missing routing
data on deletion requires a reliable stored routing value or before-image.
Reject unsupported field configurations at subscription creation rather than
silently hashing an empty value.

Coordinate shard ownership and fenced handoff through the cluster control plane,
with per-shard processed checkpoints and bounded draining. Indexer needs state
transfer or rebuild and query routing; Streamer needs gateway subscription
routing; Trigger needs independent consumer groups and retry-safe delivery.
These integrations are part of usable scaling, not guarantees supplied by a
hash filter alone.

## Alternatives

**Filter in each consumer.** This preserves the protocol but repeats network
traffic and decoding across replicas; it remains useful as a correctness oracle.

**Static database assignment.** This makes ownership and deletion routing easier,
but a single high-volume database remains a bottleneck. Reconsider when measured
workloads fit database-level placement without finer partitioning.

## Acceptance Criteria

- Local and remote live/replay paths assign every supported event to the same
  shard across process restarts, including updates and deletes.
- Disjoint owners collectively process the complete input across multiple
  databases; each shard preserves backend ordering.
- Filtered intervals advance resumable progress without claiming unprocessed
  matching events, and routing-generation mismatch is rejected explicitly.
- Owner failure and reassignment fence stale owners and recover from processed
  checkpoints; query and realtime clients continue reaching the correct owners.
- Invalid fields, shard IDs, and missing required routing data fail visibly;
  supported mutable-key changes remove obsolete owner state.

## Risks

Skew, routing metadata, state transfer, and duplicate work during recovery add
operational cost. This proposal changes protocol and checkpoint formats and
requires a migration for existing consumers. Diagnose routing generation, shard,
owner, handoff, and lag without document payloads or raw tokens.

## Dependencies

[Cluster control plane](2026-09-07-cluster-control-plane.md),
[Indexer recovery](2026-09-07-indexer-recovery-lifecycle.md),
[Streamer durable progress](2026-09-07-streamer-durable-progress.md), and
[Trigger delivery idempotency](2026-09-07-trigger-delivery-idempotency.md) own the
corresponding recovery and ownership mechanisms.
