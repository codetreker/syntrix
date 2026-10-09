# Agent Note: Integrate Puller Health Reporting

Status: proposed

## Problem

Puller health types and an HTTP handler exist but are not connected to the
running service. [The public constructor](../../../../packages/syntrix/internal/puller/interface.go#L139)
returns `health.NewChecker(logger)`, while
[service assembly](../../../../packages/syntrix/internal/services/manager_init.go#L466) creates the
Puller and its backends without creating or registering a checker. Production
callers do not invoke StartHealthServer or update Checker state.
The helper also hardcodes `mux.Handle("/health", checker)` despite the
[Health.Path configuration](../../../../packages/syntrix/internal/puller/config/puller.go).

This is a static integration finding. Bootstrap behavior is separate and already
applies `Bootstrap.Mode` in watchChangeStream; health work must preserve it.

## Proposal

Make Puller own a health snapshot derived from backend and buffer lifecycle:
starting, connected, reconnecting, history unavailable, failed, and stopped.
Report readiness only when required backends can deliver a continuous stream;
report process liveness separately so transient dependency failure does not
automatically trigger restart loops. A lack of document changes is not itself
a failure when the upstream stream remains healthy.

Have the service manager register the report and manage its endpoint in both
standalone and distributed modes. Honor the configured path and port, propagate
bind/start failures, and close listeners with the service shutdown deadline.
Define aggregation for partial backend failure, event-count versus byte units,
consumer counts, and startup readiness. Use the existing HTTP facilities where
appropriate; a new endpoint implementation or dependency is not predetermined.

## Alternatives

**Expose the existing checker unchanged.** This delivers an endpoint quickly,
but default-healthy registrations and event-only recovery cannot represent a
failed or idle upstream connection accurately.

**Use only generic process health.** This detects a dead process but cannot tell
operators whether one configured event source has stopped delivering.

## Acceptance Criteria

- Production standalone and distributed startup expose the configured health
  endpoint, and a bind failure reaches startup error handling.
- Deterministic backend connect, retry, fatal failure, and recovery transitions
  produce documented per-backend and aggregate readiness states.
- An idle connected backend stays healthy; history loss remains visible until
  recovery, and one failed backend cannot be masked by events from another.
- Consumer and buffer values use documented units, and cancellation removes the
  listener and health workers within a bounded timeout.

## Risks

Overly strict readiness can remove all replicas during dependency outages;
overly permissive readiness conceals broken ingestion. Health responses and
logs must omit resume tokens, credentials, and document payloads.

## Dependencies

[History-gap recovery](../architecture/2026-09-07-puller-history-gap-recovery.md)
owns continuity state;
[application observability](../architecture/2026-09-07-application-observability.md)
owns generic telemetry and deployment probe conventions.
