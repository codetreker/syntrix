# Agent Note: Bilingual Developer Console Client

Status: implemented

## Problem

Console developers need a browser entrypoint that signs in, restores an existing
session, and confirms logout without treating instance users as platform
developers. Network failures can hide a successful authentication mutation, and
older requests can report a session that no longer owns the browser cookie.
Language changes must not discard typed credentials or replace account state.
Static deployment must preserve cookie, Origin, and CSRF checks while serving
direct application-route reloads.

## Decision

`packages/console-client` owns an independent React/TypeScript/Vite application.
Its login and authenticated Overview use the existing Console cookie API;
no instance JWT, employee authority, or new authentication endpoint is introduced.
The [backend decision](2026-10-11-console-server-postgres-kubernetes.md) continues
to own PostgreSQL account/session authority and Kubernetes deployment. This note
owns browser session coordination, localization, UI, and static distribution.
The [Console design](../../../../docs/design/server/console/01.console.md) retains
their local contracts.

The client keeps developer, expiry, and CSRF data in memory. The HttpOnly cookie
belongs to the backend; `/auth/v1/me` restores authority after reload. Passwords,
session records, cookies, and CSRF values are never persisted in browser storage.
Initial restoration hides protected content until confirmed. Login credential
denial, expired authentication, CSRF denial, dependency failure, and unknown
mutation outcome remain distinct states.

Auth reads carry a per-tab generation. Superseded reads are aborted and cannot
publish success or invalidate newer account state. Same-tab login/logout share a
pending-mutation barrier; expiry, visibility, and cross-tab revalidation mark a
dirty flag until that mutation settles. A read taken during a pending mutation
cannot supersede it. Route and locale changes do not cancel owned mutations.

Lost responses, timeouts, cancellation, and invalid success bodies can hide
remote effects. They trigger bounded authoritative `/me` reconciliation without
replaying the mutation. A confirmed 200 establishes current session; 401
establishes anonymous state. Failed reconciliation retains the last confirmed
session only as a snapshot, displays a retryable unavailable state, and blocks
protected content and mutations. A successful logout is never inferred from
local state or network failure.

Session-change notifications carry no account state or secrets. Visible tabs
revalidate; pending mutations coalesce that trigger until settlement. Browser
expiry timers and visibility changes also revalidate. Cross-tab cookie-writing
responses are not globally ordered: visible tabs converge after outstanding
actions settle and a later authoritative check succeeds. No last-action-wins
guarantee or refresh/sliding-renewal protocol is provided.

Lingui eagerly loads complete English and Simplified Chinese catalogs before
rendering. Language preference is `system | en | zh-CN`; only that preference is
stored under a Console-specific browser key. The language runtime resolves browser
locales, updates `<html lang>`, and owns storage/language event cleanup. Storage
failure preserves an in-memory preference and exposes localized persistence
feedback. Locale activation does not remount the router, session controller, or
forms. Error categories are translated at render time so existing failures change
language with the rest of the interface.

Native semantic controls and Console-owned light CSS tokens adapt Borgee's
[palette and controls](https://github.com/gim-home/borgee/blob/ad07849c9cd904748dcea04ab111d92e3b915498/packages/client/src/index.css#L5-L55),
compact forms, login card, and
[simple sidebar/main layout](https://github.com/gim-home/borgee/blob/ad07849c9cd904748dcea04ab111d92e3b915498/packages/client/src/admin/AdminApp.tsx#L62-L103).
The final
components also power a frontend-only interactive mockup. Its fixture entry and
scenario controls are excluded from production builds. The overview describes
the current developer; it shows no invented resource counts or unavailable
navigation targets. Mobile navigation owns focus, Escape/backdrop dismissal,
scroll behavior, and restoration to its trigger.

The frontend ships as a separate static nginx image. Auth/API paths proxy to
Console server under the browser origin, preserve Host including port, Origin,
and fetch metadata, and never fall through to SPA HTML. Missing assets return
404; application routes reload through the entry document. Version-matched
HTML/assets form one distribution. The backend continues to serve APIs only.
HTML is not stored, while hashed assets are immutable. The example uses one
Recreate replica; update downtime is accepted and older open clients may need
reload. The static container has a read-only root with writable configuration
and temporary volumes.
Vite development defaults to strict port 5173 at `http://localhost:5173`, relative
same-origin requests, and the backend listener at 8090. Explicit port/target
overrides still bind one strict port; `server.public_origin` must match the
selected browser origin.

The frontend deployment has no Kubernetes provisioning credentials or mounted
ServiceAccount token. The backend manifest remains in its own package. Operator
TLS termination and the exact public HTTPS origin determine Secure-cookie use.
pnpm owns locked dependencies; Bun runs scripts. Local/pre-push/CI share bounded
catalog, type, behavior, coverage, and production-build checks. Real-backend
browser validation supplements those checks; coverage cannot establish request
ordering, focus, or cookie acceptance on its own. Build and test share Lingui's
native Vite macro/catalog pipeline. Browser fixtures use isolated PostgreSQL
schemas through Bun SQL and the prebuilt static image. Spawned processes are
immediately owned with their POSIX process groups. Closing rejects acquisitions
and drains active work before resource disposal; cleanup commands have separate
ownership. Interrupted work cannot continue acquiring resources after cleanup.
Process/container/profile/certificate cleanup and its failure status are recorded
in retained package-local receipts/screenshots. A separate marker-controlled
SIGTERM probe requires interrupted work to fail, descendants to stop, and late
containers to remain absent before reporting probe success.

## Alternatives

**Embed assets in the Go binary.** This would couple frontend generation and
backend releases. Independent package/image delivery keeps API and static build
ownership explicit and retains the existing backend.

**Read a sibling frontend dist directory from Go.** This would add a backend
asset-path and deployment contract tied to repository layout. A static origin
with explicit API proxy owns these rules without expanding backend routes.

**Adopt Ant Design or headless controls for this shell.** Both provide useful
future primitives but require adapters/providers and do not supply the required
visual layout or translation policy. Native controls match the referenced login
and simple shell; a library remains an option when domain pages justify its
interaction complexity.

**Asynchronously load locale catalogs.** The two small catalogs do not justify
locale-loading races or an extra pending state. Eager catalogs guarantee complete
source-locale messages and immediate switching. Bundle measurements can justify
splitting them later.

## Consequences

The browser exposes only confirmed authority, and recovery distinguishes uncertain
logout from expired authentication. Request generations and mutation barriers
add state-machine complexity; teardown must cancel owned reads, mutations,
timers, subscriptions, and channels. Network cancellation cannot undo a remote
mutation, so the next mount always restores backend authority.

Static routing must preserve backend status and public-origin configuration.
HTTP development routing does not establish HTTPS/Secure-cookie correctness.
Missing or mixed release assets fail visibly; operators must deploy coherent
assets and configure TLS/public origin together.

The [workload UI](../../proposed/feature/2026-10-12-console-workload-interface.md)
remains an addition to the existing backend API. It needs owned
resource queries, cache invalidation, idempotent actions, timestamped observations,
and polling cleanup. Separating auth from page data preserves account-change
cleanup without implementing those pages. Full instance/project/database
administration and employee Management retain the [platform proposal](../../proposed/architecture/2026-10-10-platform-console-instance-boundaries.md).

Registration, developer OAuth, and profile/credential self-service require new
backend contracts and forms; the existing [self-service proposal](../../proposed/feature/2026-09-07-console-account-self-service.md)
remains their owner. Isolating transport from forms preserves configured-password
login when those mechanisms are added.

Dark theme requires another semantic token set and contrast/browser validation;
centralized tokens retain that option without exposing a theme control.
Cross-tab global mutation ordering needs coordination beyond local generations
and shared-cookie revalidation. It is not claimed by this shell.
