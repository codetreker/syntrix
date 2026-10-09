# Agent Note: Resolve Trigger Signing Secrets

Status: proposed

## Problem

The delivery worker supports signing, but service assembly injects `Secrets: nil`
in both deployment modes in [manager_init.go](../../../../packages/syntrix/internal/services/manager_init.go).
[The worker](../../../../packages/syntrix/internal/trigger/delivery/worker/worker.go) returns a
fatal error when a task contains `secretsRef` without a provider. Static inspection
therefore establishes that configured signed webhooks cannot use the normal
runtime path; it does not establish a live secret-store failure.

## Proposal

Wire a configured signing-secret provider into delivery assembly. Use mounted
secret files, bound by server configuration to logical `(database, reference)`
identifiers. Rule authors supply identifiers, never arbitrary filesystem paths.
Extend the resolution contract to carry the task's database, preventing identical
reference names in different databases from sharing keys accidentally.

Validate bindings and readable key material before activating a delivery worker.
Resolve the current configured key for each attempt so operational rotation does
not require rewriting queued tasks. Sign the exact serialized request bytes with
the existing timestamped HMAC format. An unresolved reference prevents HTTP
transmission; temporary read failures follow the task retry policy, while invalid
configuration prevents startup. Rules without `secretsRef` retain their documented
unsigned behavior. No secret value is written into task storage or diagnostics.

Update configuration and webhook signature documentation with binding, rotation,
and receiver verification requirements. External secret-store integrations remain
an alternative requiring an explicit provider and dependency decision.

## Alternatives

**Environment-variable bindings** avoid mounted files but generally require
process replacement for rotation and make independent key lifecycle management
harder. They remain useful where the deployment platform cannot mount secrets.

**An external secret manager** offers centralized rotation and access auditing,
but adds network availability and credentials to every resolution path. It becomes
preferable when that service is already an operational requirement.

## Acceptance Criteria

- Normal standalone and distributed assembly deliver a correctly signed request
  for a configured reference; a receiver verifies the exact body and timestamp.
- Two databases using the same reference name resolve only their own binding.
- Rotation changes subsequent signatures without placing key material in queued
  tasks, logs, errors, or metrics.
- Missing configuration prevents worker activation; transient resolution failures
  send no HTTP request and stop retrying at the configured attempt limit.

## Risks

Mounted-key replacement must be atomic, and receivers need an explicit overlap
window during rotation. Key lookup latency consumes the task timeout. Diagnostics
must identify the database, trigger, and resolution category without exposing
secret contents or sensitive paths.
