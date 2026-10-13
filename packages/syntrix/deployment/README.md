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
provision MongoDB, PostgreSQL, or NATS server Pods. The implemented
[managed runtime contract](../../../docs/design/server/console/04.managed_runtime_contract.md)
provides public embedded rendering, explicit broker attachment, staged cleanup
initialization and component readiness. Console allocation and complete-instance
management remain future work.

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
NATS with JetStream enabled. Distributed values declare one complete broker
binding, including ownership, stream/subject/consumer names and bounded storage
policy. Instances may share an account with distinct subject namespaces; workers
of one instance share its durable consumer. Shared credentials retain account-wide
authority. `namespace` and `name_prefix` only rename Kubernetes resources; source
database isolation and broker binding selection are separate obligations. Database
installation, source persistence, backups and cluster lifecycle remain
operator-owned.

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
| `resource_mode` | Required `operator` or `managed`; selects runtime initialization and broker authority |
| `broker` | Distributed-only complete binding; its `resource_mode` must match the root value |
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

In distributed mode, `broker` requires positive `spec_revision`, explicit
`ownership.installation_id`, 32 lowercase hexadecimal `instance_id` and
`allocation_id`, `stream_name`, `subject_prefix`, `consumer_name`, and `policy`
with `storage` (`file` or `memory`), `replicas` (1..5), positive `max_bytes`,
`max_messages` and `max_age`. Operator mode creates missing objects from this
specification, refetches them and verifies the full supported policy; foreign or
conflicting objects fail without updates. Managed mode only attaches/verifies
objects provisioned beforehand. Managed names derive from the instance ID:
`SYNTRIX_<id>_TRIGGERS`, the same subject prefix, and
`SYNTRIX_<id>_TRIGGERS_DELIVERY`. All publisher/consumer settings use that binding.
The renderer validates values without connecting to NATS.

The public `github.com/codetreker/syntrix/deployment` package exposes
`Render(config, Options)` with embedded templates and `RenderSource(source,
config, Options)` for trusted operator templates. Both return complete validated
manifest bytes or an error without effects. Returned bytes contain Secrets.
The CLI file adapter owns atomic private publication; the runtime configuration
uses module-owned `initialization.mode` and `trigger.broker`.

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

Startup errors and terminal asynchronous runtime failures propagate through
`services.Manager`. The runtime CLI cancels owned work, drains it, invokes shutdown
and exits nonzero. Normal
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
acceptance, cleanup finalization and full activation. The [Indexer storage contract](../../../docs/design/server/indexer/04.storage.md#offline-bootstrap-and-startup)
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
and applied subscription barrier complete. Bootstrap keeps internal cleanup and
Trigger writers held automatically; its runtime arguments do not include
`--initialization-acceptance`. External writers and MongoDB tombstone TTL remain paused
through handoff, normal indexed-query acceptance, cleanup finalization and full
activation. Bootstrap readiness does not release that fence.

The HTTP listener starts after successful explicit bootstrap. Startup probes allow
31 minutes for the 30-minute maintenance operation; Deployment progress deadlines
allow 33 minutes. The bootstrap Pod never automatically reruns maintenance after
failure. Leave writers fenced, inspect the failure, and follow the Indexer recovery
procedure with explicit derived reset rather than blindly retrying partial state.

Inspect the prepared definitions from the successful bootstrap container and
retain its `specification` field. Inspection is read-only and does not independently
certify bootstrap completion:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  exec "$SYNTRIX_PREFIX-bootstrap" -- /syntrix \
  --config-dir /etc/syntrix --inspect-initialization \
  --initialization-timeout 30s --output json
```

Keep writers paused, gracefully delete the successful maintenance Pod, and wait
for deletion before read-path acceptance processes open its derived stores:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  delete pod "$SYNTRIX_PREFIX-bootstrap" --wait=true --timeout=120s
"$SYNTRIX_CLI" render-kubernetes \
  --template "$SYNTRIX_TEMPLATE" --config "$SYNTRIX_VALUES" \
  --output .tmp/syntrix-acceptance.yml --acceptance-only --writes-quiesced
kubectl --context "$SYNTRIX_KUBE_CONTEXT" apply -f .tmp/syntrix-acceptance.yml
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  rollout status deployment -l "app.kubernetes.io/instance=$SYNTRIX_PREFIX" --timeout=120s
```

Acceptance holds DeletionWorker and Trigger writers in the process; distributed
writer-role Deployments also have zero replicas. Held roles report NOT_SERVING
individually and are excluded from the active read-role aggregate. This state
permits the query check but does not establish full normal instance readiness.

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
instance. For fresh bootstrap, once that authenticated indexed query succeeds,
finalize owner-defined
cleanup with the exact prepared specification. Select an observed acceptance Pod
for the standalone/API workload and preserve its result:

```bash
SYNTRIX_ACCEPTANCE_POD=OBSERVED_ACCEPTANCE_POD_NAME
SYNTRIX_PREPARATION=1:sha256:RECORDED_SPECIFICATION_DIGEST
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n "$SYNTRIX_NAMESPACE" \
  exec "$SYNTRIX_ACCEPTANCE_POD" -- /syntrix \
  --config-dir /etc/syntrix --finalize-initialization \
  --expected-preparation "$SYNTRIX_PREPARATION" --indexed-query-accepted \
  --initialization-timeout 30s --output json
```

The one-shot process opens only physical backends, verifies the expected digest,
creates/verifies cleanup definitions and closes connections before one JSON
receipt. It opens no listeners, authentication module or derived stores. Its
`--indexed-query-accepted` flag is the operator's attestation. Definition changes
are not atomic; partial failure yields no completion receipt and a repeated
matching finalizer inspects/converges the same specification. Inspect/finalize
are mutually exclusive, reject positional/runtime/maintenance flags, require
`--output json`, and accept a positive timeout of at most 1h. Acceptance runtime
requires `--writes-quiesced` and cannot combine with bootstrap. Diagnostics use stderr and omit connection credentials.

After finalization, gracefully stop acceptance processes that own derived stores
and wait for deletion before replacement. Render/apply full normal state without
`--acceptance-only`, observe all configured roles ready, and repeat the external
API/query check. Then resume external writers and cleanup through existing
administration. Recheck quiescence if a source or cleanup process changed during
maintenance. Ordinary restart uses full normal rendering and does not rerun
bootstrap or recreate missing managed cleanup indexes.

API/standalone startup/readiness probes use HTTP `/ready` on port 8080; liveness
uses `/health`. Exact GET/HEAD requests to either path bypass application request
budgets. Other distributed roles use standard gRPC health on port 9000 with
`syntrix.<role>` for readiness and `syntrix.liveness` for liveness. The empty gRPC
service name is the enabled-role aggregate. Roles initialize NOT_SERVING; remote
dependencies check their required role to avoid aggregate cycles.

Readiness requires usable storage/Identity, active source capture, Indexer
applied/flushed lineage, current Puller subscription admission, active delivery
and fresh dependency verification as appropriate to the role. Admission refresh
is progress-neutral; freshness expiry clears readiness even under dispatch
backpressure. Temporary loss clears ready and can recover; terminal ingestion or
consumer failure reaches process ownership and shutdown. This is component
readiness, distinct from bootstrap completion, initialization finalization and
external query acceptance.

Configuration loads at startup; restart affected Deployments after changing
values or policy/templates. Replica validation preserves current singleton
ownership without sharding or fleet coordination. API rule pushes and
authorization caches remain process-local. File-backed distributed Trigger
retention is explicit in the example broker policy; memory storage is available
only when explicitly selected. Standalone trigger pubsub remains in-memory.

JSON logs identify terminal task failures with runtime subsystem, role,
`phase: running` and `category: terminal_failure`. Readiness transitions use
`phase: readiness`, `category: role_observation` and a ready boolean. A configured
broker binding supplies installation, instance, allocation and specification
revision fields. Startup errors carry safe structured context; finite-command
diagnostics stay on stderr, separate from the initialization JSON receipt.

Deleting Deployments does not delete their claims. Deleting a namespace deletes
its claims and operator signing-key Secret; underlying-volume retention depends
on the StorageClass reclaim policy. Existing source databases are outside these
manifests. Back up source data and signing material before destructive resets.
