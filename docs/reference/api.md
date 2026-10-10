# Syntrix API Documentation

This document describes the currently supported REST API of a Syntrix runtime
instance. It does not describe the developer-facing Console or employee-facing
Management API. The [system architecture](../architecture.md) owns those
separate authority domains.

The confirmed target [Identity architecture](../design/server/core/identity/01.architecture.md)
isolates application end users by project inside each instance, with several
logical Syntrix databases per project. Identity/OAuth/session, project, and
database configuration records belong to the instance's PostgreSQL system
store; MongoDB backs the logical databases' business documents. Project-scoped
accounts and complete OAuth/OIDC flows are pending implementation and do not
add project parameters or new grants to the API below.

## Base URL

Document and query routes use `/api/v1/databases/{database}`. Authentication
uses `/auth/v1`, replication uses `/replication/v1`, Trigger RPC uses
`/trigger/v1`, and health uses `/health`. Preserve any deployment prefix before
these paths.

## Authentication

Current authentication uses RS256 JWTs issued by the instance Identity module.
Its local runtime owns account/repository composition, while Gateway owns the
HTTP Bearer adapter; this does not add an independently deployed Identity API. Signup/login accept username and password without a database or project
selector. Database selection belongs to data-request paths. Existing instance
`admin`/`db_admin` claims are current runtime permissions, not Console developer
or Management employee credentials. The
[authentication design](../design/server/core/identity/02.authentication.md)
distinguishes implemented behavior from the target OAuth/OIDC and project model.

### Sign Up

Create a new user account and receive a token pair. Response tokens and lifetimes
below are illustrative; actual `expires_in` follows the instance configuration.

**Endpoint:** `POST /auth/v1/signup`

**Request Body:**

```json
{
  "username": "newuser",
  "password": "SecurePassword123!"
}
```

**Response (200 OK):**

```json
{
  "access_token": "eyJhbGciOiJSUzI1Ni...",
  "refresh_token": "dGhpcyBpcyBhIHJlZnJlc2ggdG9rZW4...",
  "expires_in": 3600
}
```

**Error Responses:**

- `400 Bad Request`: Invalid request body or signup failure, including duplicate
  usernames or a password rejected by the configured policy. Current signup does
  not expose a separate duplicate-username `409` response.

### Login

Authenticate a user and receive a token pair (Access Token and Refresh Token).

**Endpoint:** `POST /auth/v1/login`

**Request Body:**

```json
{
  "username": "user1",
  "password": "password123"
}
```

**Response (200 OK):**

```json
{
  "access_token": "eyJhbGciOiJSUzI1Ni...",
  "refresh_token": "dGhpcyBpcyBhIHJlZnJlc2ggdG9rZW4...",
  "expires_in": 3600
}
```

### Refresh Token

Get a new Access Token using a valid Refresh Token.

**Endpoint:** `POST /auth/v1/refresh`

**Request Body:**

```json
{
  "refresh_token": "dGhpcyBpcyBhIHJlZnJlc2ggdG9rZW4..."
}
```

**Response (200 OK):**

```json
{
  "access_token": "eyJhbGciOiJSUzI1Ni...",
  "refresh_token": "new_refresh_token...",
  "expires_in": 3600
}
```

### Logout

Invalidate a Refresh Token.

**Endpoint:** `POST /auth/v1/logout`

**Request Body:**

```json
{
  "refresh_token": "dGhpcyBpcyBhIHJlZnJlc2ggdG9rZW4..."
}
```

**Response (200 OK):** Empty body.

## Document Operations

These endpoints allow you to perform CRUD operations on documents.

**Following document fields are reserved by system for special purpose:**

- `id`: Document ID (immutable).
- `version`: Document version (auto-incremented).
- `createdAt`: Database creation timestamp (Unix milliseconds).
- `updatedAt`: Database last update timestamp (Unix milliseconds).
- `collection`: Collection path.

### Get Document

Retrieve a document by its full path.

**Endpoint:** `GET /api/v1/databases/{database}/documents/{path...}`

**Example:** `GET /api/v1/databases/orders/documents/rooms/room-1/messages/msg-1`

**Response (200 OK):**

```json
{
  "id": "msg-1",
  "text": "Hello World",
  "sender": "alice",
  "version": 1,
  "createdAt": 1700000000000,
  "updatedAt": 1700000000000,
  "collection": "rooms/room-1/messages"
}
```

### Create Document

Create a new document in a collection. The ID is automatically generated if not provided.

**Endpoint:** `POST /api/v1/databases/{database}/documents/{collection_path...}`

**Example:** `POST /api/v1/databases/orders/documents/rooms/room-1/messages`

**Request Body:**

```json
{
  "text": "Hello World",
  "sender": "alice"
}
```

**Response (201 Created):** Returns the created document.

### Replace Document (Upsert)

Replace an existing document or create it if it doesn't exist.

**Endpoint:** `PUT /api/v1/databases/{database}/documents/{document_path...}`

**Example:** `PUT /api/v1/databases/orders/documents/rooms/room-1/messages/msg-1`

**Request Body:**

```json
{
  "doc": {
    "id": "msg-1",
    "text": "Hello World Updated",
    "sender": "alice"
  },
  "ifMatch": "Filters"
}
```

**Response (200 OK):** Returns the replaced document.

### Update Document (Patch)

Update specific fields of an existing document.

**Endpoint:** `PATCH /api/v1/databases/{database}/documents/{document_path...}`

**Example:** `PATCH /api/v1/databases/orders/documents/rooms/room-1/messages/msg-1`

**Request Body:**

```json
{
  "doc": {
    "text": "Hello World Patched"
  },
  "ifMatch": "Filters"
}
```

**Response (200 OK):** Returns the updated document.

### Delete Document

Logically delete a document: retain its tombstone and metadata, clear business
data, and advance version/time. Later physical cleanup does not generate another
business deletion. See [deletion semantics](../design/server/core/storage/03.stores.md#document-deletion-and-physical-cleanup).

**Endpoint:** `DELETE /api/v1/databases/{database}/documents/{document_path...}`

**Example:** `DELETE /api/v1/databases/orders/documents/rooms/room-1/messages/msg-1`

**Response (204 No Content):** Empty body.

## Query Operations

**Endpoints:**

- `POST /api/v1/databases/{database}/query`
- `POST /trigger/v1/databases/{database}/query` for Trigger clients

Both return one ordered page. A query requires a complete index plan except for
unordered listing and ID-only equality/membership lookups. The
[filter guide](filters.md) defines operators, field types, ordering, and index
requirements.

**Request:**

```json
{
  "collection": "messages",
  "filters": [
    {
      "field": "version",
      "op": ">=",
      "value": {"type": "int64", "value": "9007199254740993"}
    }
  ],
  "orderBy": [{"field": "version", "direction": "asc"}],
  "limit": 20,
  "showDeleted": false
}
```

`limit` defaults to 100 when omitted or zero, with a maximum of 1000. Every filter
must include `value`. Operands may be ordinary JSON scalar/null/array values or
[typed values](filters.md#typed-values); typed int64 avoids client-side rounding.
The request body is limited to 1 MiB and the HTTP request timeout is 30 seconds.

**Response (200 OK):**

```json
{
  "documents": [
    {
      "type": "object",
      "value": {
        "id": {"type": "string", "value": "message-1"},
        "collection": {"type": "string", "value": "messages"},
        "version": {"type": "int64", "value": "9007199254740993"},
        "createdAt": {"type": "int64", "value": "1789084800000"},
        "updatedAt": {"type": "int64", "value": "1789084800000"},
        "text": {"type": "string", "value": "Hello"}
      }
    }
  ],
  "nextCursor": "opaque-continuation",
  "effectiveOrder": [
    {"field": "version", "direction": "asc"},
    {"field": "id", "direction": "asc"}
  ]
}
```

Every document is a complete typed object, including metadata and nested business
values. HTTP has no separate wire-version field. The encoded page is limited to
16 MiB. Authoritative materialization is separately bounded to 128 documents
and 16 MiB of source document bytes per batch. This page envelope replaces the array-only query response; release clients
and servers together.

### Continuation

Pass `nextCursor` as `startAfter` in the next request, retaining the same query
scope and ordering. `nextCursor: null` means exhaustion. A non-null cursor records
consumed candidates and may lead to an empty terminal page. The page limit may
change between requests. Treat cursors as opaque: raw IDs, old order keys, and
cursors belonging to another query are invalid.

Cursors bind database, collection, normalized predicates, deletion visibility,
route, effective ordering, template definition, and build generation. A generation
change requires restarting the query. Candidate materialization reads the
configured write source without replica fallback. This is not a snapshot across
reads or pages: index lag may omit recent writes, and concurrent sort-key changes
may cause omissions or repeats across pages. Returned documents must still match
the query and their candidate position at materialization time.

### Query Errors

Errors use `{"code":"...","message":"..."}`. A failed query returns no partial
page or replacement continuation.

| HTTP status | Code | Meaning |
|---|---|---|
| 400 | `BAD_REQUEST` | Invalid operand, field, request, branch manifest, or cursor scope/format |
| 400 | `NO_MATCHING_INDEX` | No complete access and ordering plan |
| 409 | `STALE_CURSOR` | Index definition or generation changed; restart the query |
| 422 | `QUERY_WORK_LIMIT` | Candidate, source-read, overlay, cursor, or page-byte budget exceeded |
| 503 | `INDEX_UNAVAILABLE` | Index is not ready or is rebuilding |
| 504 | `DEADLINE_EXCEEDED` | Query deadline expired |
| 500 | `INTERNAL_ERROR` | Source, transport, or other execution failure |

Unknown operators are rejected by request validation; all eight recognized
operators have execution strategies subject to index eligibility. Unsupported
source shapes fail visibly rather than being silently coerced or omitted.

## Health Check

Check if the service is running.

**Endpoint:** `GET /health`

**Response (200 OK):** `OK`
