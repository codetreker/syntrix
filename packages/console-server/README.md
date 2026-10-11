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
Its database is separate from every instance's private system PostgreSQL.

```bash
make -C packages/console-server build
make -C packages/console-server test
CONSOLE_TEST_POSTGRES_DSN='postgres://console:console@localhost:5432/console_test?sslmode=disable' \
  CI=true make -C packages/console-server coverage
```

Real PostgreSQL tests use temporary schemas in the explicitly selected test
database. Without `CONSOLE_TEST_POSTGRES_DSN`, integration tests skip. Coverage
uses the repository's pinned go-cov tool. CI supplies PostgreSQL, checks race and
coverage, and builds the container. Build assets are independent of instance Go
builds and frontend dependencies.

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

## Kubernetes Deployment

Build the image and make it available to your prepared cluster:

```bash
docker build -t console-server:development packages/console-server
k3d image import console-server:development --cluster default
```

[deployment/kubernetes.yml](deployment/kubernetes.yml) creates separate Console
and test namespaces, a dedicated ServiceAccount, namespaced RBAC, one Console
Deployment using Recreate, and a ClusterIP Service. Provide the configuration
and PostgreSQL secrets before deploying. For the in-cluster configuration, use
`server.address: "0.0.0.0:8090"`, an explicit public origin, and
`provisioning.mode: "in_cluster"`.

```bash
kubectl --context k3d-default create namespace syntrix-console
kubectl --context k3d-default -n syntrix-console create secret generic console-config \
  --from-file=config.yml=packages/console-server/configs/config.local.yml
kubectl --context k3d-default -n syntrix-console create secret generic console-postgres \
  --from-literal=dsn='postgres://console:console@postgres-host:5432/console?sslmode=require'
kubectl --context k3d-default apply -f packages/console-server/deployment/kubernetes.yml
kubectl --context k3d-default -n syntrix-console rollout status deployment/console-server --timeout=120s
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
The service does not provision full Syntrix instances,
OAuth, projects or employee Management yet.
