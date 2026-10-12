# Console Server API

The developer Console API belongs to `packages/console-server`. It uses its own
PostgreSQL accounts, sessions and workload records, independently of instance
end-user Identity and employee Management. All JSON fields use snake_case.

## Authentication

`POST /auth/v1/login` accepts `{"username":"developer","password":"..."}`.
Successful login sets the host-only `console_session` HttpOnly cookie and returns:

```json
{
  "developer": {"id": "development", "username": "developer"},
  "expires_at": "2026-10-12T12:00:00Z",
  "csrf_token": "opaque-value"
}
```

`GET /auth/v1/me` returns the authenticated session. `POST /auth/v1/logout`
revokes it and returns 204. Sessions have an absolute expiry and survive server
restart because PostgreSQL stores them. The API does not issue instance JWTs.

Every mutation requires an `Origin` matching configured `server.public_origin`.
Authenticated mutations also require `X-CSRF-Token` from the session response.
Login requires trusted Origin but has no prior session token. HTTPS origins
produce Secure cookies; plain HTTP is permitted only on localhost. Cookies use
SameSite Strict. CORS is not enabled; browser clients use the Console origin.

## Templates and Workloads

`GET /api/v1/templates` lists predefined template IDs. Images, environment,
resource quantities, ports and probes are configured on the server. The API does
not accept arbitrary manifests or expose the template environment.

`POST /api/v1/workloads` accepts `{"template_id":"http-test"}` and requires an
`Idempotency-Key` header. It returns 202 and a Location pointing at the workload.
PostgreSQL acceptance precedes any Kubernetes operation. Repeating the same key
and template returns the existing record and its original snapshot; a different
template for that key returns 409. Deletion does not release the key.

`GET /api/v1/workloads` returns at most 100 of the developer's records.
`GET /api/v1/workloads/{id}` returns one owned record. A foreign or missing ID
returns 404. The public record includes ID, developer ID, template ID, target,
desired state, lifecycle status, revision, object names/UIDs, creation/update
times, last observation time, and a sanitized error. Frozen template data and
credentials are excluded.

`DELETE /api/v1/workloads/{id}` accepts deletion with 202; a retained deleted
record returns 200. Deletion intent is persisted before Kubernetes cleanup.
Pending deletion survives restart. Completion requires confirmed cleanup of the
owned resources, not only acceptance of Kubernetes delete calls. Deleted records
continue to be reconciled because a timed-out Kubernetes create can commit late.
If an owned resource appears after the absence observation, its status returns to
deleting until cleanup completes. The observation time states the receipt's limit.

Lifecycle statuses are `pending`, `provisioning`, `ready`, `failed`, `deleting`,
and `deleted`. `ready` requires the intended Deployment generation to be observed
and updated/available replicas with a correctly bound ready Service endpoint.
Kubernetes execution errors remain observable and are retried from persisted
intent. `observed_at` identifies when status was last observed; it is not a live
availability guarantee.

## Errors and Limits

Authentication failures return 401; invalid Origin or CSRF returns 403. Invalid
workload requests return 400, foreign/missing resources 404, and conflicting
idempotent requests 409. Dependency failures return 503 without raw database or
Kubernetes error bodies. Mutating JSON requests require `application/json`, allow
one JSON object, reject unknown fields, and are limited to 4096 bytes.

HTTP operations have a configured request deadline (`server.request_timeout`,
25 seconds by default) that cancels database waits. It must be shorter than the
HTTP write timeout.

Responses include `X-Request-ID`, `Cache-Control: no-store`, and JSON content type.
Logs correlate HTTP failures with that request ID. Workload execution logs use
developer/workload identity and operation phase. Secrets and session tokens are
not logged.

`GET /health/live` checks the HTTP process. `GET /health/ready` checks PostgreSQL;
it does not claim Kubernetes workload readiness. A failed dependency yields 503.

## Deployment Contract

The [Console design](../design/server/console/01.console.md) owns lifecycle and
authority rationale; the [package README](../../packages/console-server/README.md)
owns configuration and deployment commands. Kubernetes credentials and context
are explicit, and the provisioner operates only in the configured namespace.
Workload rows retain the accepted target and snapshot. PostgreSQL and Kubernetes
do not participate in one atomic transaction. Stable identities and retries
provide recovery after uncertain remote effects.

This API provisions HTTP test workloads. Full MongoDB/PostgreSQL/Syntrix instance
provisioning, project management, developer OAuth and an interactive Console UI
remain separate capabilities.
