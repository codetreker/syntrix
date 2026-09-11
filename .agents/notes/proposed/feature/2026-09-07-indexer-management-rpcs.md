# Agent Note: Make Indexer Management RPCs Operate on Live State

Status: proposed

## Problem

`Reload` still reports the existing template count without loading configuration,
and `GetState` has no real pending-operation lifecycle. The original inspection
also found ignored invalidation errors. The
[completed query decision](../../implemented/bug-fix/2026-09-07-indexed-query-filter-semantics.md)
now makes invalidation operate on active concrete generations and propagate storage
failures. `GetState` reports that inventory with `DocCount=-1` when unavailable;
it does not scan postings to invent a count.

Actual reload/reconciliation and invalidation-triggered rebuilding remain absent.
A successful invalidation fences query admission until maintenance bootstrap;
it does not establish that an online reconstruction job was scheduled.

## Proposal

Implement management operations through the service that owns configuration and reconciliation. Reload the configured template directory, validate the complete candidate configuration, and publish the desired generation only after validation succeeds. Keep the previous valid generation active when reading or validation fails; return actionable errors.

After a successful reload, trigger reconciliation and expose its real pending/running/failed operations through `GetState`. Return a consistent generation for desired and actual state so callers can distinguish configuration acceptance from completed reconstruction. Invalidation must preserve the delivered database/pattern/template selection and
state-write error propagation, and enqueue reconstruction only for successfully
invalidated indexes. Define counts as accepted changes, not completed rebuilds.

Preserve existing RPC names and selectors. Add generation or operation-identification fields only where the current protocol cannot express these distinctions, regenerating clients together. A reload request's deadline bounds loading and acceptance; accepted rebuilds belong to the service lifecycle and remain observable after the request ends. Record operation identity, selection, generation, and errors without configuration secrets or document payloads.

## Alternatives

**Use restart as the only configuration operation.** This has a straightforward operational model, but requires removing the advertised reload contract and imposes service interruption for each change.

**Wait inside each RPC until reconstruction finishes.** Callers receive a completed result, but long scans couple RPC deadlines to background work and obscure whether a timed-out request was accepted. Observable asynchronous reconciliation is preferred.

## Acceptance Criteria

- Editing configuration and calling Reload changes desired state; unchanged configuration does not duplicate jobs.
- Invalid configuration leaves the previous generation intact and returns a failure.
- State reports real operations during success, rebuild failure, and restart recovery.
- Scoped invalidation affects only selected indexes across multiple databases; injected state-write failures never produce a success-only count.
- Request cancellation and service shutdown have the documented ownership behavior.

## Risks

Concurrent reloads and invalidations need generation ordering to prevent stale jobs restoring removed indexes. Response extensions require synchronized protocol clients and reference documentation.

## Dependencies

[Indexer recovery lifecycle](../architecture/2026-09-07-indexer-recovery-lifecycle.md) owns the actual reconstruction and job lifecycle; this proposal owns its management contract.
