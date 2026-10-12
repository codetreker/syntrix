# Developer Console Client

The independent developer Console browser application provides username/password
login, session restoration, logout, and an authenticated Overview in English and
Simplified Chinese. It uses the existing [Console server](../console-server/README.md)
cookie API. Instance end users and Management employees are separate account
domains. Workload administration, full instance provisioning, project pages,
OAuth, registration, profile editing, and theme switching are future capabilities.

## Local Development

Use Node.js 24 or later, pnpm 10.34.5 pinned by `package.json` for dependencies,
and Bun 1.4.3 for scripts:

```bash
cd packages/console-client
pnpm install --frozen-lockfile
bun run dev
```

Open `http://localhost:5173`. Vite fixes that origin with strict port binding;
another process using 5173 causes startup to fail. The development proxy forwards
relative `/auth/v1` and `/api/v1` requests to `127.0.0.1:8090` without changing
Host, Origin, or paths. `CONSOLE_API_TARGET` can select another explicit HTTP(S)
backend origin; `CONSOLE_CLIENT_PORT` can select another fixed port. When changing
the browser port, update backend `server.public_origin` to match. Configure the
default Console backend accordingly:

```yaml
server:
  address: "127.0.0.1:8090"
  public_origin: "http://localhost:5173"
```

The backend README owns PostgreSQL account setup, kubeconfig/context, configuration,
and startup commands. The existing Kubernetes development cluster is
operator-provided. No frontend command creates or initializes k3d. Login-only
use still requires valid backend provisioning configuration but does not create
a Kubernetes workload.

Use `localhost` consistently in the browser. Direct cross-origin API calls are
outside the backend's CORS contract. Production uses one public HTTPS origin and
matching `server.public_origin`; local HTTP routing does not prove Secure-cookie
acceptance.

## Interactive Mockup

Run `bun run mockup` and open `http://localhost:5173/mockup.html` for the final
login/shell components with frontend-only fixture transport. Scenario controls
exercise login/restoration/expiry, failures,
uncertain mutation reconciliation, language persistence, and mobile navigation.
It needs no backend, PostgreSQL, or cluster. Production builds use the real
transport and exclude fixture/scenario entrypoints.

## Session and Language Behavior

The browser keeps developer, expiry, and CSRF data in memory, restoring them
through `/auth/v1/me` after reload. The backend owns the HttpOnly cookie. Passwords,
session records, cookies, and CSRF values are not stored in localStorage or
sessionStorage. Protected content stays hidden while authority is being checked.

Login credential denial, session expiry, CSRF denial, dependency failure, and
unknown mutation outcome have distinct feedback. A failed logout remains
retryable; a lost or invalid success response triggers authoritative `/me`
reconciliation without replaying login/logout. Failed reconciliation blocks
protected content rather than authorizing it from the previous session snapshot.
Cross-tab notifications contain no secrets; they trigger authoritative checks
and do not guarantee global latest-action ordering of shared-cookie responses.

Language preference is Follow system, English, or 简体中文. Both Lingui catalogs
load before rendering. Explicit selection wins; system selection accepts English
variants and Chinese tags with the Hans script in browser-preference order,
falling back to English for unsupported languages. Only preference is persisted
under `syntrix.console.languagePreference`. Storage failure preserves working
in-memory selection and reports that persistence failed. Language changes
preserve forms, focus, routes, and session state; visible failures retranslate
without another request.

## Package Checks

| Command | Purpose |
|---|---|
| `bun run typecheck` | Check strict TypeScript application types |
| `bun run i18n:extract` | Update checked-in English and Chinese source catalogs |
| `bun run i18n:check` | Check fresh extraction, translation completeness, fuzzy/stale entries, and placeholders |
| `bun run test` | Run bounded behavior tests and enforced coverage |
| `bun run build` | Check catalogs/types and produce production assets |
| `bun run check` | Run the shared local/pre-push/CI package gate |
| `bun run test:browser` | Verify real-backend development and built-distribution browser flows |
| `bun run test:browser:cleanup` | Verify interrupted acquisition stops descendants and cannot create late resources |

The test gate enforces 90% statement/line, 85% function, and 80% branch coverage.
It includes application TypeScript/TSX and retains auth, locale, and transport
coverage. Generated catalogs, fixtures, test support, and CSS are not application
coverage targets. Browser validation separately verifies real PostgreSQL-backed
login, cookies, restoration/logout, language changes, routes, focus, and
production proxy behavior. A mockup or coverage result does not establish those
runtime contracts.

`bun run test:browser` requires a POSIX environment with process groups, Docker,
Git, OpenSSL, Go, Bun, and a dedicated PostgreSQL URL exported as
`CONSOLE_BROWSER_POSTGRES_DSN`. Its built-in Bun SQL client
creates isolated schemas; no system `psql` binary is needed. Build the tested
image first, from the repository root:

```bash
docker build -t console-client:acceptance packages/console-client
(cd packages/console-client && bun run test:browser)
```

Set `CONSOLE_BROWSER_IMAGE` to test another already-built image tag. The harness
starts owned backend/web fixtures and verifies Vite plus the exact nginx image,
including local HTTPS/Secure cookies; it does not create Kubernetes workloads.
Use `CONSOLE_BROWSER_EXECUTABLE` to select an installed Chromium executable;
otherwise it uses an installed Playwright Chromium. Keep browser installations
temporary under the repository's `.tmp` when a browser is needed for CI.
The harness registers spawned processes immediately and owns their process groups,
including descendants. Closing rejects new acquisitions, stops and drains active
work before disposing owned resources, and uses separately owned cleanup commands.
It removes owned schemas, processes, containers, profile/socket files, certificates,
and fixture configuration. Screenshots/logs and `receipts.json` remain under
package-local `.tmp/browser/<id>`. Cleanup failure is recorded separately from
test failure and makes the run fail.
Use a dedicated test database rather than shared production or developer source
data.

After building the same image and setting the same browser/database environment,
run `bun run test:browser:cleanup` from this package. The probe pauses Docker
acquisition behind a marker, starts a descendant that resists SIGTERM, and
interrupts the nested acceptance run. That nested run must fail while recording
completed cleanup; releasing the marker after exit must not create a late
container. The probe itself exits successfully only when descendant shutdown,
resource absence, and cleanup receipts agree. Its retained evidence lives in
`.tmp/browser-cleanup/<id>`. CI runs both browser commands and publishes both
artifact directories.

## Static Distribution

The package ships a separate nginx-unprivileged static image listening on 8080.
The Go backend remains API-only and does not read sibling assets. Static routing
proxies auth/API paths to the
Console server under the browser origin while preserving Host including port,
Origin, and fetch metadata. API errors never fall through to SPA HTML. Application
routes support direct reload; missing assets return 404. HTML uses `no-store`,
and hashed assets use immutable caching. Each image carries version-matched
entry/assets. The example deployment uses one replica with Recreate, accepting
rollout downtime while avoiding concurrent release bundles; older open clients
may need to reload after an update.

From the repository root:

```bash
docker build -t console-client:development packages/console-client
```

Runtime `CONSOLE_API_ORIGIN` selects the backend origin, defaulting to
`http://console-server:8090`. It is a web-container setting, not a browser API URL.
Configure backend `server.public_origin` with the externally visible browser
origin, not this internal upstream.

The frontend owns [deployment/default.conf.template](deployment/default.conf.template)
and [deployment/kubernetes.yml](deployment/kubernetes.yml). The backend manifest
remains at
[console-server/deployment/kubernetes.yml](../console-server/deployment/kubernetes.yml).
The static container has no provisioning credentials and disables ServiceAccount
token mounting. TLS termination and the exact public origin are operator settings.

With the existing Console backend configured and the image available to the
cluster, apply the frontend manifest from the repository root:

```bash
kubectl --context k3d-default apply -f packages/console-client/deployment/kubernetes.yml
kubectl --context k3d-default -n syntrix-platform rollout status deployment/console-client --timeout=120s
```

Replace the example context with the prepared cluster's explicit context. The
manifest expects `console-client:development` to be available there; use the
cluster's image import mechanism or a published image reference. Configure
`CONSOLE_API_ORIGIN` for the backend Service when its name/port differs. The
frontend root filesystem is read-only, with writable configuration and temporary
directories supplied by `emptyDir`; it receives no provisioning role.

The Service exposes internal port 8080. Publish the chosen HTTPS origin through
the operator's TLS terminator and configure backend `server.public_origin`
with exactly that origin before signing in. Certificates, public domains, and
cluster lifecycle remain operator-managed.

## Failure Investigation

Failures preserve the operation category, HTTP status, and sanitized
`X-Request-ID` when available. Use that ID to correlate Console server JSON logs
by `request_id` and inspect `phase`, `error_category`, and `error_type`.
Requests failing before the backend have no server ID; inspect the web proxy
failure and frontend operation category. Diagnostics exclude passwords, cookies,
CSRF values, request bodies, and raw backend error bodies.

The [Console design](../../docs/design/server/console/01.console.md),
[API reference](../../docs/reference/console_api.md), and
[client decision](../../.agents/notes/implemented/architecture/2026-10-12-console-client-session-and-distribution.md)
own detailed session, locale, distribution, and future-scope contracts.
