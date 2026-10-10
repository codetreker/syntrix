# Gateway Document Authorization

**Status:** Document rules are implemented by `gateway/authorization`; project
admission and safeguards explicitly marked as proposed are not implemented
contracts.

The [system architecture](../../../architecture.md) and
[Identity architecture](../core/identity/01.architecture.md) define the authority domains. These
rules govern developer business documents in a Syntrix instance. Management
employees and Console developers have separate credentials and administrative
permissions.

Gateway owns rule evaluation beside its request/resource authorization path.
Identity supplies account authentication and token claims; it does not own the
Query-backed CEL evaluator. The
[ownership decision](../../../../.agents/notes/implemented/architecture/2026-10-10-gateway-document-authorization.md)
records this separation and its preserved behavior.

## 1. Overview

Data-driven attribute-based access control (ABAC) expresses document ownership,
room membership, and application roles through CEL expressions. The target
model isolates end-user identity by project inside each instance; one project
can own multiple logical Syntrix databases while sharing its end-user directory.

Gateway must establish the credential's instance/project authority and resolve
the database's project before evaluating database-specific document rules. A
project membership does not itself allow all data access. Rule helpers and data
lookups must remain within the authorized project; any future cross-database
policy needs an explicit scope contract. An application role named `admin`
confers no Management employee or Console developer privileges.

Current users have instance-wide `roles` and `db_admin` claims, without project
binding. The engine supports per-database YAML rules, CEL compilation/evaluation,
`get`/`exists`, and a database-admin bypass. This implementation does not provide
the target project admission checks. Query-wide authorization, publish-time
ambiguity rejection, resource limits, and error contracts below are design
requirements where not explicitly described as current behavior; the
[API reference](../../../reference/api.md) owns supported responses.

## 2. Configuration

### 2.1 Per-Database Rules (Directory-Based)

Current security rules are configured per database using a directory-based
structure. Each database has its own rules file. In the target project model,
these rules describe application document policy; the project/database directory
and Identity records are instance-local PostgreSQL system data. Publishing rules
is an authorized instance/project configuration operation, not platform account
administration. Persistent rule versioning and publication transactions remain
to be designed.

The runtime keeps the existing YAML `identity.authz.rules_path` setting, default
`security_rules`, and configuration-directory path resolution. The root runtime
configuration composes Identity account settings with Gateway authorization
configuration; the rule configuration type and its lifecycle belong to
`internal/gateway/authorization`. This layout changes Go ownership without
changing the deployment configuration contract.

**Directory structure:**
```
config/
  security_rules/
    default.yml           # database: default
    analytics.yml         # database: analytics
    orders.yml            # database: orders
```

**YAML format:**
```yaml
database: default
rules_version: '1'
service: syntrix
match:
  /databases/{database}/documents:
    match:
      /{document=**}:
        allow:
          read, write: "true"
```

### 2.2 Database Placeholder Handling

The `{database}` placeholder in match paths is handled as follows:

1. **Using `{database}` placeholder** (recommended):
   - At load time, `{database}` is automatically replaced with the `database` field value
   - Example: `database: analytics` + `/databases/{database}/documents` → `/databases/analytics/documents`

2. **Using explicit database name**:
   - If a concrete database name is used instead of `{database}`, it MUST match the `database` field
   - Example: `database: analytics` + `/databases/analytics/documents` → Valid
   - Example: `database: analytics` + `/databases/default/documents` → **Error: mismatch**

### 2.3 Conflict Detection

- Duplicate `database` definitions across files are rejected at startup
- This ensures fail-fast behavior and prevents silent rule overwrites

## 3. Rule Model

Rules are defined separately from the code and loaded by the API Gateway. They evaluate whether a request should be allowed or denied based on:

1. **Request**: Who is asking? (`request.auth`)
2. **Resource**: What is being accessed? (`resource.data` for reads)
3. **Future State**: What will the data look like? (`request.resource.data` for writes)

### 3.1 Structure

Rules are hierarchical, matching the document path structure.

```yaml
rules_version: '1'
service: syntrix

match:
  /databases/{database}/documents:
    match:
      /users/{userId}:
        allow:
          read, write: "request.auth.userId == userId"

      /rooms/{roomId}:
        allow:
          read: "resource.data.public == true || request.auth.userId in resource.data.members"
          write: "request.auth.userId in resource.data.members"

        match:
          /messages/{messageId}:
            allow:
              read: "request.auth.userId in get('/databases/' + database + '/documents/rooms/' + roomId).data.members"
              create: "request.auth.userId in get('/databases/' + database + '/documents/rooms/' + roomId).data.members"

```

### 3.2 Path precedence & conflicts

- The most specific match wins (longest concrete path, then fewer wildcards, then order of declaration as final tie-breaker).
- Detect and reject overlapping ambiguous rules at publish time (e.g., `/rooms/{id}` alongside `/rooms/public` without explicit priority). **Current implementation:** `matchPath` sorts by concreteness (non-wildcards first), longer path, then alphabetical, but does not perform publish-time ambiguity checks. **Planned change:** add explicit ambiguity validation and use declaration order as the final tie-breaker when specificity is equal.
- No implicit inheritance: parent blocks do not apply to children unless explicitly matched (see 9.4/9.5).

**Planned ordering algorithm:** sort matches by (1) more concrete segments (non-wildcard) first, (2) fewer wildcards, (3) longer path length, (4) declaration order as final tie-break. Ambiguity detection runs after sorting; conflicting patterns with identical specificity but different semantics are rejected at publish time.

### 3.3 Construction

- Current constructor: `authorization.NewEngine(config, queryService)` accepts
  the Gateway-owned `authorization.Config` and returns `authorization.Engine`,
  keeping the CEL environment and program cache internal.
- RuleSet, MatchBlock, Request, Authenticated, and Resource belong to the
  authorization package. Gateway projects the existing user ID, username,
  roles, database-admin assignments, and full claims into that evaluation input.
- Project-aware policy administration and additional publish-time validation
  require their own instance contracts; relocating the evaluator does not
  establish those capabilities.

## 4. Evaluation Context

The existing evaluation model uses the following objects. Project binding is a
target admission requirement; this document does not add an implemented project
claim or CEL variable.

### `request`

- `request.auth.userId`: The authenticated local end-user ID; target identity is scoped to its instance and project.
- `request.auth.username`: The authenticated user's username.
- `request.auth.roles`: Application roles assigned to the user; never employee or developer authority.
- `request.auth.claims`: Full JWT claims map for rule evaluation.
- `request.time`: Server timestamp.
- `request.resource`: The new resource data (for `create` and `update` operations).

### `resource`

- `resource.data`: The *existing* document data (for `read`, `update`, `delete`).
- `resource.id`: The document ID.

### Data availability semantics

- `create`: `resource.data` is empty; use `request.resource.data` for proposed content.
- `update`: both `resource.data` (old) and `request.resource.data` (new) are available.
- `delete`: only `resource.data` is available; `request.resource` is empty.
- `read` when document is missing: treat `resource.data` as empty; if the rule needs existence, require explicit `exists()`.

### Helper Functions

- `get(path)`: Fetches a document from the database (crucial for checking parent permissions, e.g., room membership).
- `exists(path)`: Checks if a document exists.

## 5. Implementation Strategy

### 5.1 Language

The current engine uses CEL (`github.com/google/cel-go`) for compiled document-policy expressions.

### 5.1.1 Proposed CEL subset and execution limits

- Primitives: bool, int, uint, double, string, timestamp, list, map; no bytes.
- Allowed ops: logical, comparison, arithmetic, list/map access; string ops limited to `size`, `contains`, `startsWith`, `endsWith`; time ops limited to comparisons and `duration` literals.
- Forbidden: reflection, dynamic dispatch, regex, network/file I/O, randomness, custom functions except `get`, `exists`, and explicitly whitelisted helpers.
- Execution guardrails: per-eval CPU/step limit (e.g., 5ms wall, 10k instructions) and memory cap; timeout yields `RULE_EVAL_ERROR`.

### 5.2 Execution Flow (API Gateway)

1. **Incoming Request**: `GET /api/v1/databases/orders/documents/rooms/123/messages/msg1`
2. **Auth Check**: Validate the credential and local user identity; target admission also checks instance/project binding.
3. **Rule Match**: Find the rule matching path `/rooms/123/messages/msg1`.
4. **Proposed pre-fetch optimization**:
   - If rule uses `resource.data`, fetch the target document.
   - If rule uses `get()`, fetch the referenced documents (with caching).
5. **Evaluate**: Run the CEL program.
6. **Decision**:
   - `true`: Proceed to Query Engine.
   - `false`: Return `403 Forbidden`.

### 5.3 Proposed rule validation and limits (publish-time)

- Enforce static checks before activating a rule set:
  - Max file size: 256 KB; max AST depth: 20; forbid unbounded glob patterns beyond `**` at tail.
  - Max `get()`/`exists()` invocations referenced per rule: 5; reject rules exceeding this.
  - Forbid dangerous CEL functions: reflection, dynamic dispatch, non-deterministic time (only `request.time` allowed), I/O.
  - Require path patterns to be prefix-bounded (no mid-segment `**`).
  - Reject overlapping ambiguous matches (see 3.2).
  - Validate all referenced paths in `get()`/`exists()` are absolute and stay within `/databases/{db}/documents/...`.
  - Max ruleset cardinality: e.g., 1,000 `match` blocks and 5,000 `allow` statements to keep load/validation bounded.

## 6. Performance Considerations

- **`get()` Cost**: Rules that require fetching other documents (e.g., checking room membership for every message read) are expensive.
- **Optimization**:
  - **JWT Claims**: Embed common permissions (e.g., `role: admin`) in the JWT to avoid DB lookups.
  - **Denormalization**: Duplicate `memberIds` array into the message document (trade-off: storage vs. compute).
  - **Caching**: Cache `get()` results within the scope of a request or short-term.

- **Per-request enforcement**: runtime cap of 5 `get()`/`exists()` evaluations per request (deny with `RESOURCE_EXHAUSTED` if exceeded); cache hits do not count toward the cap.
- **Prefetch planning**: the gateway precomputes the set of `get()` targets from the rule AST to parallelize fetches; missing documents are treated as `exists() == false` with empty data.

## 7. Target query evaluation semantics

- Queries are allowed only if **all** candidate documents satisfy the matched `allow read` rule; any denial rejects the entire query with `PERMISSION_DENIED` (no partial results) to avoid leakage.
- Where possible, push rule predicates that are pure field comparisons into the query planner; otherwise fall back to per-document evaluation.
- If rule evaluation needs the document body, the engine will fetch documents and may incur higher latency; surface metrics for “query with rule eval”.
- Index-miss errors (`MISSING_INDEX`) are returned **before** rule evaluation to avoid wasted work.
- Pagination/streaming: if a later page hits a deny, the request fails with `PERMISSION_DENIED` and no further data is streamed; clients must handle full failure on any page.

## 8. Example Scenarios

### Scenario A: User Profile

*Only the user can edit their own profile. Public can read.*

```cel
match /users/{userId} {
  allow read: if true;
  allow write: if request.auth.userId == userId;
}
```

### Scenario B: Private Chat Room

*Only members can read/write messages.*

#### **Option 1: Parent Lookup (Normalized)**

```cel
match /rooms/{roomId}/messages/{msgId} {
  allow read, write: if request.auth.userId in get(/databases/$(database)/documents/rooms/$(roomId)).data.members
}
```

#### **Option 2: Denormalized (Faster)**

*Requires `members` array to be copied to every message.*

```cel
match /rooms/{roomId}/messages/{msgId} {
  allow read: if request.auth.userId in resource.data.members
}
```

## 9. Additional Rule Patterns

### 9.1 Role-gated admin collection

```cel
match /admin/logs/{logId} {
  allow read: if "admin" in request.auth.roles;
  allow write, delete: if false; // read-only even for admins
}
```

### 9.2 Denormalized membership on message

```cel
match /rooms/{roomId}/messages/{msgId} {
  allow read: if request.auth.userId in resource.data.members;
  allow write: if request.auth.userId in resource.data.members;
}
```

### 9.3 Query restriction (only own docs)

```cel
match /users/{userId}/todos/{todoId} {
  allow query: if request.auth.userId == userId;
}
```

### 9.4 Explicit inheritance pattern (opt-in)

Define a shared predicate and add a wildcard sub-match to “inherit” parent semantics:

```cel
function roomMember(roomId) {
  request.auth.userId in get(/databases/$(database)/documents/rooms/$(roomId)).data.members
}

match /rooms/{roomId} {
  allow read, write: if roomMember(roomId);

  // Inherit to all sub-collections unless overridden
  match /{sub=**} {
    allow read, write: if roomMember(roomId);
  }
}
```

Override by adding a more specific `match` below this block; the most specific match wins.

### 9.5 Sub-collection inheritance rule

There is **no implicit inheritance**. If a sub-collection has no matching rule, access is denied. Use explicit patterns (e.g., 9.4) when inheritance is desired.

## 10. Performance & Safety

- Cache `get()` results per request; limit max `get()` calls (e.g., 5) and deny if exceeded.
- Forbid unbounded path patterns in rules; validate at publish time.
- If `resource.data` missing due to not-found on read: treat as empty and apply rule; if rule requires existence, use `exists()` explicitly.
- Sub-collection inheritance: no implicit inheritance; parent rules do not automatically apply downward.
- Ordering: ensure specific paths (e.g., `/users/{userId}`) are defined before any catch-all `{sub=**}` blocks to prevent unintended allows.

## 11. Proposed rule error shape

- On deny: `403` with `code: PERMISSION_DENIED` and minimal message; do not leak rule internals.
- On rule evaluation error: also `403` with reason `RULE_EVAL_ERROR`.

- On exceeding runtime limits (e.g., `get()` cap): `403` with `code: RESOURCE_EXHAUSTED`.

## 12. Observability requirements

- Metrics: allow/deny counts by collection prefix, rule evaluation latency, `get()` cache hit/miss; include tags for `rules_version` and `action` (read/write/query).
- Logs: sampled denies with path, action, reason (no sensitive data). Trace tag `authz.result`.

## 13. Remaining design

- Project/database admission and target administrative permissions.
- Validation of rule helper paths against the authorized database/project.
- Publish-time ambiguity rejection, bounded CEL execution, and query-wide rules.
- Versioned rule publication and rollback with retained audit records.
- Field-level masks and further rule compilation optimizations.

OAuth and session state belong to the instance Identity module's PostgreSQL
system data. Their protocol and freshness choices are covered by the
[authentication design](../core/identity/02.authentication.md), not document rule expressions.
