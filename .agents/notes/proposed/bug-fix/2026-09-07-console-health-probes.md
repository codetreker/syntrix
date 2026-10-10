# Agent Note: Truthful Console Health Probes

Status: proposed

## Problem

The current embedded instance administration [Settings page](../../../../packages/console/src/pages/SettingsPage.tsx) probes `/auth/v1/health` and the obsolete `/api/v1/query` route. When either request fails, `results[0]?.status === 'healthy'` converts the failed component check into a healthy result if the general API responds. Both the [public health handler](../../../../packages/syntrix/internal/gateway/rest/handler_health.go) and [admin health handler](../../../../packages/syntrix/internal/gateway/rest/handler_admin.go) simply return `OK`; they do not check authentication or database dependencies. The UI therefore reports inferred component health as observed health. This conclusion follows from static control flow.

## Proposal

Own and deliver the authenticated instance readiness aggregation API together with its administration consumer and tests. Under the [accepted platform architecture](../../../../docs/architecture.md), the developer Console selects an owned instance and, where applicable, a project and logical database. Platform Management serves employee fleet operations; application end-user availability views do not inherit either administrative authority. Extend the existing administrative health endpoint to report Gateway request-serving readiness, Auth and its identity-store prerequisites, configured storage backends, and database metadata/routing readiness. A selected database must use its canonical identifier and server-validated administrative scope. Keep `/health` as gateway process liveness.

Define component reports with ready, unavailable, or unknown status, observation time, freshness deadline, latency, and a bounded diagnostic category. The aggregate is ready only when all required components in the requested scope have fresh ready reports; failed, missing, or expired reports must remain visible and prevent a ready aggregate. In standalone and distributed modes, service assembly must connect the actual component checks or reports to this endpoint. Bound check concurrency, request deadlines, cached-result age, and cancellation. Auth checks verify configured prerequisites without synthetic sign-ins or credential mutations; storage and database checks must avoid reading application documents merely to infer availability.

Retire speculative auth and document-query probes. Limit application end users to authorized availability information, and authorize developer diagnostics against the selected instance and project. Display unhealthy, unknown, permission-denied, checking, and stale states separately; a successful gateway response cannot convert another component's failed check into success.

Allow one console refresh cycle at a time and cancel requests on navigation. Preserve the previous result with its timestamp while refreshing, mark expired results stale, and reject late responses for an earlier instance, project, or database selection.

## Alternatives

**Real document queries as readiness checks:** exercise more of the data path but depend on collection permissions and query semantics, creating misleading failures and unnecessary load.

**Gateway liveness only:** accurately proves that the process responds but cannot satisfy the component health presentation. It remains appropriate for the public availability view.

## Acceptance Criteria

- Backend tests cover Gateway, Auth, storage, and database readiness transitions, dependency timeouts, missing reports, stale reports, recovery, and aggregate results in standalone and distributed wiring.
- A healthy gateway with unavailable Auth or storage never returns or displays a ready aggregate for the affected scope.
- Two databases with different backend readiness remain distinguishable; unauthorized requests cannot retrieve administrative diagnostics or another database's reports.
- UI tests distinguish permission denial, unknown, unavailable, and stale states; old responses cannot replace newer results or a different database's selection.
- Refresh cancellation and repeated navigation leave no overlapping cycles or timers; backend cancellation bounds outstanding check work.

## Dependencies

[Puller health](../feature/2026-09-07-puller-health-reporting.md) supplies the ingestion component report when Puller participates in the readiness scope. [Application observability](../architecture/2026-09-07-application-observability.md) supplies telemetry and shared diagnostic correlation. This proposal owns the readiness API, aggregation semantics, non-Puller component integration, and their validation.

## Risks

Aggressive dependency probes can amplify outages. Check frequency, concurrency, deadlines, and cached-result age must be bounded, and diagnostic responses must avoid credentials, connection strings, and sensitive tenant information. Readiness reports establish the stated checks, not a guarantee that every subsequent data operation will succeed.
