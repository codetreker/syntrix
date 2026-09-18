# Agent Note: Preserve HTTP Push Version Preconditions

Status: implemented

## Problem

The HTTP Push handler stripped `document.version` before extracting it and passed
no `BaseVersion` to Query. The documented optional version precondition was lost,
so existing live-target version checks could not protect HTTP writes. Decoding
the value only through the generic document map would also round integers beyond
JSON's commonly used floating-point precision. Read/write splitting could also
feed Push a stale replica version or apparent absence before a write, even when
the HTTP precondition was preserved.

## Decision

The original [HTTP change decoder](../../../../internal/gateway/rest/types.go)
extracted the exact, case-sensitive `document.version` from raw ordinary JSON
before decoding business numbers as float64. This preserved optional int64
preconditions without changing the then-existing document-number representation.
The [typed HTTP Push decision](2026-09-18-http-push-typed-values.md) replaces that
wire encoding with a complete typed object so nested business values also retain
their numeric types.

The decoded precondition remains an internal `BaseVersion *int64` field excluded
from document output:

| Supplied typed version | Internal result |
|---|---|
| Omitted | `nil`; preserve the optional unconditional-write behavior |
| Nonnegative typed int64, including explicit zero | Preserve exact value and presence |
| Null, string, bool, float64, negative/out-of-range int64, or noncanonical decimal string | Reject with HTTP 400 before any Engine call |

A reused change value is replaced only after successful decoding, so a later
valid change without a version clears any prior precondition. A malformed version
anywhere in a batch prevents the entire request from reaching the Engine; input
validation does not make writes transactional. Ordinary document CRUD retains its
existing number representation.

[The handler](../../../../internal/gateway/rest/handler_replication.go) forwards
the extracted precondition separately while continuing to strip protected fields.
`NewStoredDoc` retains its version-1 initialization; client versions are not copied
into stored metadata. Storage owns resulting versions.

The original repair reused Query's live-target comparison and atomic write
predicates without changing the action or conflict-response contract. It used
`-1` to preserve omitted versions across the then-existing gRPC contract. The
[complete conditional-write decision](2026-09-07-replication-push-version-checks.md)
now owns explicit actions, protobuf optional version presence, tombstone-aware
reads, and structured conflicts. Version semantics remain unchanged by typed
encoding: `create` with version 1 is accepted, and zero is an equality precondition
on live targets.

Push's initial and conflict lookups explicitly request
`ReadOptions{Consistency: ReadAuthoritative, ShowDeleted: true}`. The routed store selects that
logical database's `OpWrite` source and forwards the option. Mongo clones the
collection handle for this call with primary read preference, preserving shared
client/collection settings. Ordinary `Get`, `GetMany`, and `Query` retain their
configured read behavior.

`Get` accepts zero or one options value. Omission or `ReadDefault` uses ordinary
read routing; unsupported modes and multiple values fail. Authoritative-source
selection and read errors propagate without a replica fallback. Push also
propagates non-`ErrNotFound` conflict-read errors instead of returning an
incomplete success response. The complete conditional-write decision defines
missing and tombstoned outcomes independently of the read-routing option.

Authoritative selection requires a writer-capable backend topology. A Mongo
connection explicitly pinned to a secondary is not made writer-capable by setting
primary read preference. The option selects the write source; it does not lock a
document, create a transaction, or establish linearizable reads. Atomic write
predicates remain necessary because data can change after the read.

## Alternatives

**Add a separate public `baseVersion` field** would distinguish payload from
metadata explicitly, but changes the established flattened protocol when
`document.version` already owns the optional precondition.

**Decode all document numbers as exact-number wrappers** would preserve version
precision, but would also have changed business-data runtime types throughout the
then-existing document path. The original repair used a local raw-field decoder;
the later typed HTTP decision explicitly changes Push's document representation.

**Add a separate `GetForWrite` method** would name the intent explicitly but
duplicate the single-document read API. A typed per-call option keeps the routing
requirement visible and forwards it through the existing interface.

**Carry consistency in context values** would avoid an explicit parameter but
hide a correctness requirement from the storage call. `ReadOptions` exposes the
request and supports validation without changing ordinary read routing.

**Implement strict create/update/delete semantics with the HTTP repair** would
also have addressed absent and tombstoned targets, but required Query/storage
predicates and coordinated conflict/protocol changes outside that repair.
The [later conditional-write decision](2026-09-07-replication-push-version-checks.md)
delivers the missing-target safety; strict insert-only create remains separately
[proposed](../../proposed/feature/2026-09-07-replication-push-insert-only.md).

## Consequences

HTTP writes now reach existing live-target checks with their exact optional
precondition. A stale live version conflicts; a matching version can proceed
subject to the write predicate and other storage outcomes. The original
[replication reference](../../../../docs/reference/replication.md#version-preconditions)
now describes the accepted input and current limits.

The original extraction repair did not close the not-found/Create branch or
represent absent conflict targets. The later conditional-write decision closes
those gaps while preserving exact precondition presence and the authoritative
read option. The later typed transport extends precision to business values.
Strict insert-only create remains a separate semantic decision. Retries after a
lost response remain ambiguous; version checks do not establish exactly-once effects.
