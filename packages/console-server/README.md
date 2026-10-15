# Console Server

An independent developer-facing service with PostgreSQL accounts, sessions and
deployment records. The Kubernetes executor creates predefined HTTP test
Deployments and ClusterIP Services. The [platform architecture](../../docs/architecture.md)
separates Console developers, Management employees and instance end users.
The [API reference](../../docs/reference/console_api.md) owns requests and responses.

## Requirements

Use Go 1.25 or later, a dedicated PostgreSQL database, and a Kubernetes cluster
with a working pod network and image registry access. The maintainer prepares
the local k3d cluster. This package does not create, stop or delete clusters.
Its physical database and privileges are separate from every instance's private
system PostgreSQL database; they may use the same PostgreSQL server.

```bash
make -C packages/console-server build
CONSOLE_TEST_POSTGRES_DSN='postgres://console:console@localhost:5432/console_test?sslmode=disable' \
  make -C packages/console-server test
```

Real PostgreSQL tests use temporary schemas in the explicitly selected test
database. Both `make test` and `make coverage` require
`CONSOLE_TEST_POSTGRES_DSN` and run the pinned `go-cov@v0.1.0` through
[scripts/coverage.sh](scripts/coverage.sh). The module-owned
[.go-cov.toml](.go-cov.toml) requires total coverage of 90%, each package 85%,
and each function 80%, with race detection and a three-minute test timeout.
Critical uncovered blocks also fail the command. No packages, files, or functions
are excluded, including the command entrypoint. Enforcement is explicit and does
not depend on the ambient `CI` variable. The entrypoint clears go-cov environment
overrides so they cannot weaken the module policy. Coverage is written to
`coverage.out`. The concurrent idempotency test uses a PostgreSQL lock barrier
to make every request reach insertion before the winning transaction commits,
so conflict replay is exercised on every run. Tests release their locks and
join requests before dropping the dedicated fixture schema.

CI and the pre-push hook use this same gate; CI supplies PostgreSQL and also
builds the container. Direct `go test` is available for focused development, but
integration tests skip without their database environment and it does not enforce
coverage. Build assets are independent of instance Go builds and frontend
dependencies.

## Configuration and Local Run

Copy [configs/config.yml](configs/config.yml) into an ignored local configuration,
replace the bootstrap password, and configure PostgreSQL plus Kubernetes:

```yaml
server:
  address: "127.0.0.1:8090"
  public_origin: "http://localhost:8090"
database:
  dsn: "postgres://console:console@localhost:5432/console?sslmode=disable"
auth:
  session_ttl: 24h
  developers:
    - id: "development"
      username: "developer"
      password: "replace-this-password"
provisioning:
  mode: "kubeconfig"
  kubeconfig: "../../../.tmp/k3d/kubeconfig"
  context: "k3d-default"
  namespace: "syntrix-tests"
  installation_id: "console-development"
workload:
  reconcile_interval: 2s
  operation_timeout: 20s
  templates:
    - id: "http-test"
      image: "nginxinc/nginx-unprivileged:1.27.5-alpine"
      port: 8080
      env: {}
      cpu_request: "25m"
      cpu_limit: "100m"
      memory_request: "32Mi"
      memory_limit: "64Mi"
      probe_path: "/"
```

Relative kubeconfig paths resolve against the configuration file's directory.
In-cluster mode uses the service's ServiceAccount and does not fall back to a
workstation kubeconfig. `CONSOLE_POSTGRES_DSN` overrides `database.dsn`; other
settings come from the selected file. Unknown YAML fields or invalid values
prevent startup. Module configuration types own their validation/defaults.

```bash
(cd packages/console-server && ./bin/console-server --config configs/config.local.yml)
```

Startup initializes module-owned tables without deleting existing data.
Configuration provisions missing developer IDs and password hashes. Existing
credentials are preserved; changing the configured password does not reset a
persisted account. Conflicting ID/username assignments fail startup. Removing an
entry does not remove an existing account. Runtime sessions and authorization
use PostgreSQL. The configured ID is the resource owner; project Identity roles
do not authorize these operations.

The installation ID identifies the Console installation and its assigned target
cluster; it and the accepted namespace are part of persisted workload identity.
Keep them stable, and use a new installation ID for a different cluster. Existing
requests preserve their frozen template and target even when configuration changes.
Public template responses include only
IDs, never environment secrets. The configured HTTP image must support port
8080 and unprivileged execution for the example template.

## Browser Console

The independent [Console client](../console-client/README.md) supplies bilingual
developer login, restored sessions, logout, and an authenticated Overview. This
Go service remains API-only; its image does not embed or read frontend assets.
The frontend uses the existing cookie/`me`/CSRF endpoints without changing
developer account persistence.

For Vite development, keep the backend listener at `127.0.0.1:8090` and set
`server.public_origin: "http://localhost:5173"`. The client fixes that port and
proxies relative auth/API requests without rewriting Host or Origin. Use
`localhost` consistently in the browser; direct cross-origin requests are not
covered by the backend's CORS contract.

Production serves a separate static web image and same-origin API reverse proxy.
Set `server.public_origin` to the exact externally visible HTTPS origin and
configure TLS termination for that origin. The frontend's
[deployment](../console-client/deployment/) owns static routing; the existing
backend manifest below stays unchanged. The static container has no provisioner
credentials or ServiceAccount token.

## Kubernetes Deployment

Build the image and make it available to your prepared cluster:

```bash
docker build -t console-server:development packages/console-server
k3d image import console-server:development --cluster default
```

[deployment/kubernetes.yml](deployment/kubernetes.yml) creates `syntrix-platform`
for platform services and `syntrix-tests` for requested test workloads, one Console
ServiceAccount with the workload permissions, one Console
Deployment using Recreate, and a ClusterIP Service. Provide the configuration
and PostgreSQL secrets before deploying. For the in-cluster configuration, use
`server.address: "0.0.0.0:8090"`, an explicit public origin, and
`provisioning.mode: "in_cluster"`.

Console currently executes deployment requests directly. Future Management
services can run in `syntrix-platform`; their service permissions are decided
when those services are implemented. `provisioning.namespace` selects the test
workload target and remains `syntrix-tests`.

```bash
kubectl --context k3d-default create namespace syntrix-platform
kubectl --context k3d-default -n syntrix-platform create secret generic console-config \
  --from-file=config.yml=packages/console-server/configs/config.local.yml
kubectl --context k3d-default -n syntrix-platform create secret generic console-postgres \
  --from-literal=dsn='postgres://console:console@postgres-host:5432/console?sslmode=require'
kubectl --context k3d-default apply -f packages/console-server/deployment/kubernetes.yml
kubectl --context k3d-default -n syntrix-platform rollout status deployment/console-server --timeout=120s
```

Use a production secret mechanism for credentials outside local testing. RBAC
grants fixed workload CRUD and pod/endpoint observation only. Workloads disable
ServiceAccount token mounting and privilege escalation. The provisioner verifies
resource identity and UID before adoption/deletion and does not accept arbitrary
images, manifests, namespaces or credentials through the API.

One active executor is enforced with a PostgreSQL advisory lock. Same-resource
reconciliation and deletion hold a bounded row transaction; PostgreSQL serializes
their accepted state changes. Resource creation and database receipt can fail
independently, so reconciliation verifies and resumes accepted operations.
Shutdown cancels execution and preserves launched workloads.

## Test Workload Access

Use the API to log in, select `http-test`, and create a workload with an
Idempotency-Key and session CSRF token. Wait for `ready`, then forward its Service:

```bash
kubectl --context k3d-default -n syntrix-tests port-forward service/w-WORKLOAD_ID 18080:8080
curl --fail http://localhost:18080/
```

ClusterIP DNS is internal to Kubernetes. Deleting a workload through Console
persists cleanup intent; poll until `deleted`. Deleted records and idempotency
keys remain in PostgreSQL and continue to be reconciled for late remote effects.
The service does not provision managed Syntrix instances, OAuth, projects, or
employee Management yet. The planned
[managed-instance lifecycle](../../docs/design/server/console/03.instance_lifecycle.md)
owns administrator registration of shared MongoDB/PostgreSQL/NATS servers,
private database and scoped credential allocation, runtime lifecycle, revisioned
configuration, and the five-PR implementation sequence. It allocates database
namespaces and trigger artifacts, without deploying database or NATS servers.
Current configuration and API examples above describe only the delivered
test-workload service.
