# Agent Note: Durable Security Rule Publication

Status: proposed

## Problem

The administrator push endpoint updates the receiving process only. In the
[rule engine](../../../../packages/syntrix/internal/gateway/authorization/engine.go),
`e.dbRules[database] = &rules` replaces an in-memory entry, while startup loads
the configured rules directory. Static inspection therefore establishes no
durable publication history, rollback, dry-run, or propagation to other nodes.
The [Gateway authorization design](../../../../docs/design/server/gateway/authorization.md)
already calls for versioned publication and rollback, and the
[console design](../../../../docs/design/server/console/01.console.md) requires
idempotent push/rollback results for 24 hours. A successful push cannot currently
establish which revision other nodes or a restarted process serve, or recover its
original result after the response is lost.

## Proposal

Make an immutable, database-scoped rule revision and a compare-and-swap active
revision the publication authority. Store source, digest, author, creation time,
validation result, and activation history in the existing PostgreSQL metadata
backend. Import configured files through an explicit migration/bootstrap action;
after adoption, startup reads the authoritative active revision.

Separate validation, dry-run evaluation, and activation. Dry-run accepts bounded
request/resource fixtures and reports decisions without updating active rules.
Activation and rollback create durable history entries conditional on the
expected active revision. Rollback reactivates an existing validated revision.

Require `Idempotency-Key` for every push, including dry-run, and rollback. Own each
durable operation by authenticated actor, database, operation kind, and key; bind
it to a canonical request fingerprint covering source/target revision, expected
active revision, and options. A matching completed retry returns the original
status and result before rechecking the expected revision. Reuse with different
request content returns a conflict without applying changes. Concurrent matching
requests share one operation; bounded waits may return its pending operation ID.

Persist activation, history, and the completed idempotency result in one
PostgreSQL transaction. Persist terminal validation failures and dry-run results
without activation. Retain completed results and fingerprints for 24 hours from
completion, and recover pending operations after restart. This preserves retries
after a lost response without treating the operation's own activation as a CAS
conflict. After expiration, a request is newly evaluated against the required
expected revision; it receives no promise of recovering the earlier result.

Each serving node compiles a complete revision before swapping its local engine
and reports its applied revision. Publication responses distinguish durable
acceptance from node convergence; deployments can reject admission to nodes
that exceed the documented revision-lag limit. Reconciliation must recover after
disconnects and restart, with bounded retries and cancellation on shutdown.

## Alternatives

**Distribute versioned files through deployment tooling.** This provides reviewable
artifacts but makes the existing runtime publication API depend on external
deployment completion and complicates per-database rollback.

**Require all nodes to acknowledge synchronously.** This gives a stronger completion
signal but lets an unavailable node block every rule change. Retain explicit
convergence status and readiness admission as the proposed operational boundary.

## Acceptance Criteria

- Published rules survive process restart and retain inspectable revision history.
- Distinct operations activating from the same expected revision permit one winner;
  invalid revisions and dry-runs leave the active revision unchanged.
- Lost-response retries, process restart, and concurrent matching push/rollback
  retries return the original completed result within 24 hours, creating one
  activation/history entry. Matching dry-run retries return their original result.
- Missing keys and conflicting request fingerprints fail without activation;
  interrupted transactions cannot persist activation without its recoverable result.
- Rollback, offline-node recovery, and failed compilation expose applied revision
  and error status without serving a partially compiled ruleset.
- Independent databases retain independent histories and activation decisions.
- Publication, convergence, and rollback records correlate by database, revision,
  actor, and operation ID without recording fixture payloads.

## Risks

Durable storage, idempotency retention, and node reconciliation add operational
work and an explicit availability cost for stale nodes. Key ownership and request
fingerprinting must remain stable across nodes and restarts. Deferral retains
restart loss and divergent authorization decisions; immutable revisions and an
explicit bootstrap boundary keep later adoption possible without hidden file
fallback.

## Dependencies

[Publication validation](../feature/2026-09-07-security-rule-publication-validation.md)
owns static rule checks; [administrative audit records](../feature/2026-09-07-administrative-audit-records.md)
own retained audit events.
