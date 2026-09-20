# Agent Note: Preserve Typed Values Through HTTP Push

Status: implemented

## Problem

Pull and Query preserve int64 separately from float64, but HTTP Push previously
decoded ordinary document numbers as float64. A client could read an exact int64
and lose its value or numeric type when sending it back. Ordinary JSON conflict
documents also lacked a safe representation for consumers whose number type
cannot represent every int64. Preserving only the version precondition did not
protect nested business values.

## Decision

**Scope: Stage 1A — HTTP Push typed-value transport only.** This decision changes
the request document and non-null conflict document encoding. Existing actions,
create/version rules, conflict correlation, storage predicates, and batch behavior
remain owned by the [Push conditional-write decision](2026-09-07-replication-push-version-checks.md).

| HTTP field | Encoding |
|---|---|
| Each change's `document` | One recursive typed object containing flattened fields |
| Document `version` | Omitted, or a nonnegative typed int64 |
| Conflict `current`, present target | One recursive typed object containing real document metadata and business fields |
| Conflict `current`, absent target | Raw JSON null |
| `collection`, `action`, `changeIndex`, conflict `id` and `reason` | Existing outer JSON fields |

Reuse the shared typed-value codec used by Pull, Query, and Push's internal gRPC
transport. Nested int64 values use canonical decimal strings and remain distinct
from finite float64 values. Request documents must be objects; typed null,
arrays, scalar roots, malformed values, and ordinary untyped documents are invalid.

Version presence remains distinct from explicit zero. A present version must be
an int64 from zero through `9223372036854775807`; typed float64, null, string,
negative int64, noncanonical decimal strings, and out-of-range values fail before
any change reaches Query. Version extraction precedes protected-field stripping;
storage continues to assign resulting metadata.

The raw-field decoder from the [earlier HTTP repair](2026-09-07-http-push-version-preconditions.md)
solved precision only for version under the then-existing ordinary JSON contract.
This typed transport replaces that encoding while retaining exact precondition
presence and authoritative read routing. There is no legacy ordinary-JSON fallback.

### Limits and failures

- The HTTP request body limit remains 10 MiB and counts the entire typed JSON
  envelope, including every tag and outer request field.
- Encoded protobuf requests and conflict responses retain their existing 20 MiB
  budgets, including envelopes. Local and gRPC Query calls retain equal budgets;
  fitting the HTTP body limit does not waive the protobuf request check.
- Invalid input and oversized encoded requests fail before storage operations.
  Oversized conflict responses fail without truncation, using the existing HTTP
  422 `REPLICATION_BUDGET_EXCEEDED` result.
- Valid batches remain sequential and nontransactional. A runtime or response
  encoding failure can follow a completed write prefix; it does not imply rollback
  or safe automatic retry.

### Scope and remaining integration

The server accepts lossless typed HTTP Push values. The SDK's
[private upstream adapter](../architecture/2026-09-20-sdk-upstream-replication.md)
now provides the encoder, native acknowledgement and recovery path over
[typed alias storage](../architecture/2026-09-18-sdk-replica-storage.md).
The [offline replication decision](../feature/2026-09-07-sdk-offline-replication.md)
owns the public facade and its composition; automatic notification wiring remains
conditional on matching source authorization. This server transport decision owns the wire representation.
Ordinary document CRUD and Trigger write formats remain unchanged. The later
[create-conflict decision](2026-09-18-replication-push-create-conflict.md) changes
live-target create handling while preserving the typed transport.

## Alternatives

**Keep ordinary HTTP JSON and preserve only version separately.** This leaves
business int64 values and conflict documents exposed to rounding or type changes,
even though the optimistic-write precondition itself remains exact.

**Convert bigint values to Number.** This permits JSON serialization but loses
integers outside the safe-integer range and cannot preserve int64 versus float64.
The shared typed representation preserves both value and type.

## Consequences

- HTTP Push and its conflict documents preserve supported numeric types end to
  end, including nested business values and metadata.
- The HTTP wire change requires coordinated consumer upgrades. Existing untyped
  Push bodies are invalid; response consumers must decode non-null `current`.
- Typed tags consume the existing byte budgets, so the same business payload can
  reach a size limit earlier than under the old ordinary JSON format.
- No SDK internal Pusher, persistence mechanism, new creation policy, or exactly-once
  guarantee is delivered by this transport step.

The [replication reference](../../../../docs/reference/replication.md#push-changes)
owns the wire examples and validation rules.
