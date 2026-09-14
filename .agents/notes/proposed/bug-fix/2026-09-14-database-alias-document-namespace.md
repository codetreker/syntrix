# Agent Note: Unify Document Namespaces across Database Identifiers

Status: proposed

## Problem

Database validation resolves a URL identifier to a database entity, but existing
document CRUD and Push pass the original URL identifier to Store. Two accepted
identifiers for the same entity can therefore select different document
namespaces. Reading the resolved entity ID while writing the URL alias produces
an empty result despite a successful write.

This mismatch was observed during end-to-end Pull integration. The
[Pull implementation decision](../../implemented/bug-fix/2026-09-07-replication-pull-cursor-progress.md)
preserves the existing document namespace and separately binds the resolved
entity identity for authorization and continuation. It does not make aliases
interchangeable across all document operations.

## Proposal

Choose one durable document namespace for each database entity and apply it
consistently across CRUD, Query, Push, Pull, realtime subscriptions, source
routing, and database lifecycle operations. Keep display aliases separate from
that namespace. Resolve authorization against the same entity throughout.

Audit configured database bindings and data already stored under URL aliases
before changing routing. Preserving populated namespaces needs an explicit
conversion or reset procedure; do not add silent dual reads or fallback writes.
Invalidate incompatible replication cursors deliberately. Include alias changes
and database deletion in the namespace lifecycle contract.

## Alternatives

**Canonicalize Pull alone.** This breaks valid Push-to-Pull synchronization while
other writes still target the URL namespace. The live integration failure ruled
out this partial correction.

**Continue treating each URL identifier as a distinct namespace.** This retains
current storage routing but makes identifiers for one database noninterchangeable
and requires the same distinction in configuration, permissions, and lifecycle
operations. It should not be presented as canonical database resolution.

## Acceptance Criteria

- Every supported identifier for one entity reads and mutates the same data.
- Different entities remain isolated through alias changes and lifecycle work.
- Source routing and realtime capture use the same namespace as document writes.
- Existing data and cursors receive an explicit upgrade outcome without hidden
  compatibility reads or silently empty replicas.

## Risks

This changes routing across several services and may affect populated source
namespaces. It is separate from the native-source Pull protocol so that the
write-path, configuration, and lifecycle consequences receive their own review.
