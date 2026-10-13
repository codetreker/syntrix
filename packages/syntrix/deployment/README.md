# Syntrix Kubernetes Deployment

The package-owned [Dockerfile](../Dockerfile) builds the instance runtime.
The [release guide](RELEASE.md) describes the public amd64/arm64 image and the
archive containing matching Linux CLIs. Standalone and distributed templates
connect to existing databases; the
`syntrix-cli render-kubernetes` command renders either template from deployment
values. The [platform architecture](../../../docs/architecture.md) keeps these
operator-managed runtime resources separate from developer Console and employee
Management. Console's API continues to provision predefined HTTP test workloads.
The planned [managed-instance lifecycle](../../../docs/design/server/console/03.instance_lifecycle.md)
will select the latest complete successfully published runtime package, freeze
its archive identity and image version tag, and use its matching CLI/templates to reconcile
complete runtime bundles with private databases allocated on registered shared
infrastructure. Bootstrap, acceptance, finalization and normal roles retain one
release and version tag with `IfNotPresent`; published version tags must not be
overwritten. Metadata digests remain verification/diagnostic evidence.
Retries/start/restart reuse the accepted release. It does not
provision MongoDB, PostgreSQL, or NATS server Pods. Its proposed
[PR 1 runtime contract](../../../docs/design/server/console/04.managed_runtime_contract.md)
defines public embedded rendering, managed broker attachment, staged cleanup
initialization, and component readiness. Those interfaces are not implemented by
the current operator commands below.

## Templates and requirements

| Mode | Template and values | Normal runtime placement |
|---|---|---|
| Standalone | [Template](standalone/kubernetes.template.yml), [values](standalone/values.example.yml) | One process with direct local calls; one data claim |
| Distributed | [Template](distributed/kubernetes.template.yml), [values](distributed/values.example.yml) | API, Query, Puller, Indexer, Streamer, Trigger Evaluator, and Trigger Worker Deployments; separate Puller/Indexer claims |

Both modes require an operator-prepared cluster, image access, persistent storage,
and operator-selected instance-private MongoDB and PostgreSQL databases. MongoDB
must belong to an existing replica set.
Every advertised MongoDB replica-member address must resolve and be reachable
from Pods, not just the initial URI host. PostgreSQL credentials need permission
to initialize instance user/catalog tables. Distributed mode requires existing
NATS with JetStream enabled in a server or account dedicated to the instance.
Generated configurations use the runtime-default `TRIGGERS` stream and subject
prefix with durable consumer `consumer`. `namespace` and `name_prefix` rename
Kubernetes resources; they do not isolate broker delivery or source data. Database
installation, source persistence, backups, and cluster lifecycle remain
operator-owned. The planned managed mode uses per-instance stream, subject,
and consumer names in a shared NATS account, with all workers of one instance
sharing one consumer. It requires runtime and renderer changes; the current
operator templates retain the isolated-account requirement above.

Claims use `ReadWriteOnce`. An empty `storage_class` selects the cluster default;
`storage_gib` sets each claim's requested capacity. Distributed bootstrap must
be able to mount both Puller and Indexer claims in one Pod. Use storage whose
placement constraints permit that shared maintenance placement.

The image contains the runtime binary without configuration, credentials,
signing material, or a shell. Templates mount configuration at `/etc/syntrix`,
use UID/GID 65532, a read-only root, writable `/data` and `/tmp`, stdout JSON logs,
and no ServiceAccount token. Stateful roles use Recreate. Services are ClusterIP:
HTTP 8080 for standalone/API and internal gRPC 9000 for applicable distributed
roles. Public TLS ingress is configured separately.

## Select artifacts and prepare values

For an extracted release archive, select `SYNTRIX_CLI` and
`SYNTRIX_DEPLOYMENT` using the [release guide](RELEASE.md#use-an-extracted-archive),
then continue with the selected mode and values. To build from a repository
checkout, run from its root:

```bash
make -C packages/syntrix build
docker build -t syntrix:development packages/syntrix

# For an already prepared k3d cluster named default:
k3d image import syntrix:development --cluster default

SYNTRIX_CLI=packages/syntrix/bin/syntrix-cli
SYNTRIX_DEPLOYMENT=packages/syntrix/deployment
mkdir -p .tmp
SYNTRIX_MODE=standalone
SYNTRIX_TEMPLATE="$SYNTRIX_DEPLOYMENT/$SYNTRIX_MODE/kubernetes.template.yml"
SYNTRIX_VALUES=.tmp/syntrix-values.yml
cp "$SYNTRIX_DEPLOYMENT/$SYNTRIX_MODE/values.example.yml" "$SYNTRIX_VALUES"
chmod 600 "$SYNTRIX_VALUES"
```

Choose `distributed` before copying for separate service Pods. Edit the values
with actual connections and credentials; example DNS names/passwords are
placeholders. Choose transport, replica-set, and credential settings matching the
existing databases, with URI-reserved credential characters encoded. Set
`allowed_origins` to exact browser origins and select an image available in the
cluster. The renderer does not contact databases or Kubernetes to verify them.

| Values | Meaning and validation |
|---|---|
| `mode` | `standalone` or `distributed`, matching the selected template |
| `namespace`, `name_prefix` | Lowercase Kubernetes DNS labels; prefix names shared resources and the temporary bootstrap Pod |
| `image` | Runtime image reference |
| `storage_class`, `storage_gib` | Existing StorageClass or empty for default; positive GiB per claim |
| `connections.mongodb_uri`, `mongodb_database`, `postgres_dsn` | Existing source/system database connections |
| `connections.nats_url` | Required existing NATS URI in distributed mode |
| `signing_key_secret` | Existing immutable PKCS#1 RSA key Secret; required in distributed mode, optional in standalone |
| `identity.admin_username`, `admin_password` | Current runtime administrator bootstrap; existing accounts are preserved |
| `allowed_origins` | Nonempty list of HTTP/HTTPS origins with no path, query, fragment, or credentials |
| `bootstrap_resources` | Explicit CPU/memory requests and limits for the maintenance Pod |
| `workloads.<role>.name` | Deployment/Service name and Pod-name prefix, at most 52 characters |
| `workloads.<role>.replicas` | Explicit desired normal replica count, including zero |
| `workloads.<role>.resources` | Explicit CPU/memory requests and limits for that role |

Standalone requires exactly the `standalone` role. Distributed requires exactly
`api`, `query`, `puller`, `indexer`, `streamer`, `trigger_evaluator`, and
`trigger_worker`. Standalone, Puller, Indexer, and Trigger Evaluator replicas must
be zero or one. API, Query, Streamer, and Trigger Worker permit zero through
2147483647; dependencies must still be running for their consumers to operate.
Names must be unique and must not collide with generated resources. The example
namespace is `syntrix-instance` and its resource prefix is `instance`; both are
configurable.

CPU accepts positive integer millicores such as `250m`, or positive cores with at
most three decimal places such as `0.25` or `2`. Memory accepts positive integer
bytes or integer decimal `k/M/G/T/P/E` and binary `Ki/Mi/Gi/Ti/Pi/Ei` quantities,
such as `512Mi` or `2Gi`. Fractional memory and CPU exponent notation are rejected.
For each resource, requests must not exceed limits. Unknown fields, duplicate
keys, multiple values documents, and invalid/missing required values fail
rendering. This values format is separate from the runtime's composed YAML.

The templates mount base configuration and a generated connection/administrator
Secret. Runtime defaults load first, then `config.yml`, then `config.local.yml`.
Example authorization allows authenticated `admin`/`system` on the default
database; the index template covers `deployment_checks` by descending `createdAt`.
Add actual application policy and index templates before using other APIs.

## Shared signing key

Distributed API, Trigger Worker, and bootstrap must use the same signing material.
Provision the Secret named by `signing_key_secret` in the selected namespace with
an `auth_private.pem` entry containing a PKCS#1 RSA private key. Keep it immutable
and operator-managed; the renderer references it without creating or replacing
it. For the distributed example, starting from an existing protected key file:

```bash
SYNTRIX_KUBE_CONTEXT=k3d-default
SYNTRIX_NAMESPACE=syntrix-instance
SYNTRIX_PREFIX=instance
kubectl --context "$SYNTRIX_KUBE_CONTEXT" create namespace "$SYNTRIX_NAMESPACE"
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  create secret generic instance-signing-key --from-file=auth_private.pem=.tmp/auth_private.pem
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  patch secret instance-signing-key --type=merge --patch '{"immutable":true}'
```

Use the existing namespace/Secret when already provisioned, and adjust names to
the selected values. Standalone may use the same mechanism; with an empty
`signing_key_secret`, it generates `/data/keys/auth_private.pem` and retains it in
its data claim. Changing keys changes token authority and requires coordinated
replacement, not editing a running replica's key independently. Separate Pods
still compose local Identity capabilities; no independent Identity service is
added.

## Render and apply normal state

Set the context, namespace, and prefix to match the selected values:

```bash
SYNTRIX_KUBE_CONTEXT=k3d-default
SYNTRIX_NAMESPACE=syntrix-instance
SYNTRIX_PREFIX=instance
"$SYNTRIX_CLI" render-kubernetes \
  --template "$SYNTRIX_TEMPLATE" --config "$SYNTRIX_VALUES" \
  --output .tmp/syntrix-kubernetes.yml
```

Normal rendering honors configured replicas and emits no maintenance Pod. Use
it directly only when the derived stores contain a complete, compatible Indexer
catalog and valid Puller lineage/history. Fresh claims require bootstrap even
when source databases are empty. Invalid state fails rather than rebuilding.

Synchronous Puller/Indexer startup failures propagate through `services.Manager`.
The runtime CLI cancels owned work, invokes shutdown, and exits nonzero. Normal
Kubernetes workloads restart the failed container and revalidate persisted state,
so an Indexer that initially cannot reach a delayed Puller can start on a later
attempt. Invalid catalogs or replay history continue to fail without implicit
bootstrap or reset; initial restart does not replace the Indexer's bounded
transport recovery after successful startup.

File output is written atomically with mode 0600; its parent directory must
already exist. Inputs are not overwritten. Generated output contains credentials,
so keep it outside Git and ordinary logs. Stdout is available only with explicit
`--output -`. Template parsing, execution, and rendered-YAML validation complete
before output emission. Their failures leave stdout empty and the previous private
output file unchanged. Displayed template diagnostics omit template contents and
runtime values while retaining the raw cause through `Unwrap` for callers. The
renderer does not apply resources.

For compatible initialized state, apply the normal render and wait:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" apply -f .tmp/syntrix-kubernetes.yml
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  rollout status deployment -l "app.kubernetes.io/instance=$SYNTRIX_PREFIX" --timeout=120s
```

Confirm a real indexed query before admitting traffic as described below.

## Bootstrap fresh derived state

Pause every application/direct-source writer, metadata change, and physical
cleanup process including MongoDB tombstone TTL deletion, using existing database
and application administration. Keep them paused through normal-runtime query
acceptance. The [Indexer storage contract](../../../docs/design/server/indexer/04.storage.md#offline-bootstrap-and-startup)
owns source-boundary checks, replay readiness, and explicit derived reset.
`--writes-quiesced` records that attestation; it does not block writers.

Before replacing existing runtime state, stop all Deployments for this prefix and
wait until their Pods are deleted. Remove an old bootstrap Pod gracefully first
if one exists. For existing normal Deployments:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  scale deployment -l "app.kubernetes.io/instance=$SYNTRIX_PREFIX" --replicas=0
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  wait --for=delete pod -l "app.kubernetes.io/instance=$SYNTRIX_PREFIX" --timeout=120s
```

All state-owning processes must have stopped before the maintenance Pod starts.
A `ReadWriteOnce` claim can still be mounted by multiple Pods on the same node;
it does not enforce exclusive Pebble ownership by itself.

Render bootstrap with both attestation flags, apply it, and confirm readiness:

```bash
"$SYNTRIX_CLI" render-kubernetes \
  --template "$SYNTRIX_TEMPLATE" --config "$SYNTRIX_VALUES" \
  --output .tmp/syntrix-bootstrap.yml --bootstrap --writes-quiesced
kubectl --context "$SYNTRIX_KUBE_CONTEXT" apply -f .tmp/syntrix-bootstrap.yml
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  wait --for=condition=Ready "pod/$SYNTRIX_PREFIX-bootstrap" --timeout=1980s
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  logs "pod/$SYNTRIX_PREFIX-bootstrap"
```

Bootstrap rendering forces every normal Deployment to zero replicas and creates
one `restartPolicy: Never` standalone maintenance Pod. Standalone mounts its data
claim; distributed bootstrap mounts the normal Puller and Indexer claims at
`/data/puller` and `/data/indexer`, using the shared signing key and the same
source/index configuration. The maintenance process keeps serving after its scan
and applied subscription barrier complete. Confirm the log prefix `Index bootstrap
and subscription application are ready`. Keep external writers and MongoDB
tombstone TTL paused through handoff and the normal indexed-query acceptance below;
the current success log's resume wording does not establish that later fence.

The HTTP listener starts after successful explicit bootstrap. Startup probes allow
31 minutes for the 30-minute maintenance operation; Deployment progress deadlines
allow 33 minutes. The bootstrap Pod never automatically reruns maintenance after
failure. Leave writers fenced, inspect the failure, and follow the Indexer recovery
procedure with explicit derived reset rather than blindly retrying partial state.

Keep writers paused, gracefully delete the successful maintenance Pod, and wait
for deletion before allowing normal processes to open its derived stores:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  delete pod "$SYNTRIX_PREFIX-bootstrap" --wait=true --timeout=120s
"$SYNTRIX_CLI" render-kubernetes \
  --template "$SYNTRIX_TEMPLATE" --config "$SYNTRIX_VALUES" \
  --output .tmp/syntrix-kubernetes.yml
kubectl --context "$SYNTRIX_KUBE_CONTEXT" apply -f .tmp/syntrix-kubernetes.yml
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  rollout status deployment -l "app.kubernetes.io/instance=$SYNTRIX_PREFIX" --timeout=120s
```

Do not force-delete the bootstrap Pod. Normal rendering omits its definition but
`kubectl apply` does not delete it automatically. Reapplying the bootstrap render
scales normal roles back to zero and starts maintenance again; retain the normal
render for subsequent updates.

## Runtime acceptance and operational limits

For the unmodified examples, forward Service `instance` in standalone mode or
`instance-api` in distributed mode:

```bash
# Set instance-api for the distributed example.
SYNTRIX_API_SERVICE=instance
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  port-forward "service/$SYNTRIX_API_SERVICE" 8080:8080
```

Use the [API reference](../../../docs/reference/api.md) to log in and query
`deployment_checks` with `orderBy` set to
`[{"field":"createdAt","direction":"desc"}]`. An empty result is valid for a fresh
instance. Confirm indexed-query behavior before resuming writers and MongoDB TTL
cleanup through their existing administration. If a source or cleanup process was
replaced during maintenance, recheck quiescence before continuing.

API/standalone probes use HTTP `/health` on port 8080. Exact `GET`/`HEAD` requests
to that path bypass the general application limiter without consuming its budget;
other paths/methods and authentication limits keep their existing behavior.
Other distributed roles use TCP 9000, not a gRPC health RPC or an HTTP route.
These probes show listener availability, not dependency, replay, or consumer
readiness. TCP probes can briefly pass while startup validation or replay is still
running; a synchronous startup failure then terminates the process. Configuration
is loaded at startup; restart affected Deployments after updating rendered values
or policy/templates.

Replica validation preserves current singleton ownership; it does not implement
sharding or fleet coordination. API rule pushes and authorization caches are
process-local, so pushing rules to one API replica does not update the others.
Current distributed Trigger composition uses a MEMORY JetStream stream and the
durable consumer `consumer`. Pending tasks do not become durable across NATS
restart by mounting a NATS volume. Standalone trigger pubsub is also in-memory.

Deleting Deployments does not delete their claims. Deleting a namespace deletes
its claims and operator signing-key Secret; underlying-volume retention depends
on the StorageClass reclaim policy. Existing source databases are outside these
manifests. Back up source data and signing material before destructive resets.
