# Syntrix Service Deployment

The package-owned [Dockerfile](../Dockerfile) builds the Syntrix instance binary.
[kubernetes.yml](kubernetes.yml) deploys only that standalone service in
`syntrix-instance`, connecting to existing MongoDB and PostgreSQL. The
[platform architecture](../../../docs/architecture.md) owns the distinct Console
developer and Management employee services. Console's API continues to provision
predefined HTTP test workloads.

## Requirements and resources

Use an existing Kubernetes cluster with working DNS, image access, and a default
StorageClass supporting `ReadWriteOnce` claims. Configure an existing MongoDB
replica set and private instance PostgreSQL database. All MongoDB advertised
replica-member addresses must resolve and be reachable from the runtime Pod, not
just the initial URI host. PostgreSQL credentials need the DDL permissions required
to initialize instance user and catalog tables. Database installation, source-data
persistence, backups, and cluster lifecycle remain operator-owned.

| Resource | Responsibility |
|---|---|
| `Namespace/syntrix-instance` | Runtime deployment namespace |
| `Deployment/syntrix`, `Service/syntrix` | One standalone runtime and ClusterIP HTTP access on port 8080 |
| `PersistentVolumeClaim/syntrix-data` | 10 GiB for signing keys, Pebble derived stores, and Puller buffers |
| `ConfigMap/syntrix-config` | Nonsecret configuration and authorization/index templates |
| `Secret/syntrix-config-secret` | Existing backend connections and administrator bootstrap credentials |

Runtime `/data` retains the RSA signing key, Pebble indexes and their embedded
progress/catalog, and Puller event buffers. Ordinary process replacement reuses
those stores and preserves token signatures. Standalone Trigger transport is
in-memory and requires no NATS service; the runtime PVC does not make that queue
durable.

The runtime uses UID/GID 65532, a read-only root, writable `/data` and `/tmp`,
stdout JSON logs, and no ServiceAccount token. Recreate and one replica preserve
exclusive ownership of its local derived stores. The image contains only the
binary and runtime base, with no configuration, credentials, signing keys, or
shell. Configuration must be mounted at `/etc/syntrix`; its default command is
`--standalone --config-dir /etc/syntrix`. The manifest supplies no database
workloads, public TLS ingress, or distributed-service orchestration.

## Build and configure

Run from the repository root:

```bash
docker build -t syntrix:development packages/syntrix

# For an already prepared k3d cluster named default:
k3d image import syntrix:development --cluster default

mkdir -p .tmp
cp packages/syntrix/deployment/kubernetes.yml .tmp/syntrix-kubernetes.yml
```

Select an explicit context for the following commands:

```bash
SYNTRIX_KUBE_CONTEXT=k3d-default
SYNTRIX_MANIFEST=.tmp/syntrix-kubernetes.yml
```

Edit the local manifest before applying it. The Secret's `config.local.yml` must
select the actual MongoDB URI/database and PostgreSQL DSN; sample DNS names and
credentials are placeholders. Choose the MongoDB replica-set name and transport
settings that match the existing deployment. URL-encode reserved characters in
connection credentials. Set the administrator bootstrap password. If its username
changes, also update `identity.authn.admin_username` in the ConfigMap.

Set `gateway.realtime.allowed_origins` to the exact browser origin; the example
allows `http://localhost:8080`. Use an isolated namespace per runtime and update
all namespaced resources together if its name changes. Make the image available
to the selected cluster; for a registry, publish an immutable tag and set it in
the local manifest.

Configuration loads defaults, `/etc/syntrix/config.yml`, then the Secret's
`config.local.yml`. The example rules cover only the default database and allow
authenticated `admin`/`system` roles. Its index template covers `deployment_checks`
ordered by `createdAt` descending. Define additional database policies and index
templates before using their APIs.

The Deployment initially has zero replicas, allowing configuration and bootstrap
to be selected deliberately:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" apply -f "$SYNTRIX_MANIFEST"
```

If `/data` already contains a complete, compatible Indexer catalog and valid
Puller lineage/history for these sources, keep the normal arguments, set
`Deployment/syntrix` to `replicas: 1` in the local manifest, and apply it. A fresh
runtime PVC needs the explicit bootstrap procedure below, even when the existing
source databases are empty. Invalid or incomplete derived state requires
maintenance recovery; ordinary startup does not silently rebuild it.

## Bootstrap fresh derived state

Pause every application/direct-source writer, metadata change, and physical
cleanup process, including MongoDB tombstone TTL deletion, using the existing
database and application administration. Keep them paused through bootstrap and
persistent-startup acceptance. `--writes-quiesced` records that attestation; it
does not block writers. The
[Indexer storage contract](../../../docs/design/server/indexer/04.storage.md#offline-bootstrap-and-startup)
owns source-boundary checks, replay readiness, and explicit derived reset.

After confirming quiescence, start the single maintenance runtime:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n syntrix-instance patch deployment syntrix \
  --type=strategic --patch '{"spec":{"replicas":1,"template":{"spec":{"containers":[{"name":"syntrix","args":["--standalone","--config-dir","/etc/syntrix","--bootstrap-indexes","--writes-quiesced","--bootstrap-timeout=30m"]}]}}}}'
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n syntrix-instance \
  rollout status deployment/syntrix --timeout=1980s
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n syntrix-instance logs deployment/syntrix
```

Confirm the log message `Index bootstrap and subscription application are ready;
external writers and MongoDB tombstone TTL deletion may resume`. Bootstrap keeps
the same process serving after the scan and applied subscription barrier complete.
The startup probe budget is 31 minutes and the rollout progress deadline is
33 minutes, covering the 30-minute bootstrap operation plus startup margin.
Its HTTP listener starts after successful explicit bootstrap. On normal restart,
`/health` checks only the listener and does not prove dependency or Indexer replay
readiness.

Keep writers paused while selecting normal persistent startup. Set
`Deployment/syntrix` to `replicas: 1` in the local manifest and keep its original
normal arguments, then apply it and wait:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" apply -f "$SYNTRIX_MANIFEST"
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n syntrix-instance \
  rollout status deployment/syntrix --timeout=120s
```

The replacement process validates the existing Pebble catalog and Puller lineage.
In a separate terminal, forward the Service:

```bash
kubectl --context "$SYNTRIX_KUBE_CONTEXT" -n syntrix-instance \
  port-forward service/syntrix 8080:8080
```

Use the [API reference](../../../docs/reference/api.md) to log in and execute an
authenticated query of `deployment_checks` with `orderBy` set to
`[{"field":"createdAt","direction":"desc"}]`. A successful empty result is valid
for a fresh instance. Confirm indexed-query behavior before enabling MongoDB TTL
cleanup and admitting writers through their existing administration.

A failed bootstrap must leave writers fenced. Stop its Deployment, inspect the
failure, and use the Indexer procedure's explicit `--reset-derived` option for a
complete rebuild. Do not blindly retry maintenance against partially built stores.
If the database or cleanup process is replaced during maintenance, verify that
write and TTL quiescence still holds before continuing.

## Updates and storage lifetime

Keep `replicas: 1` and normal arguments in the local manifest for subsequent
applications. Reapplying the untouched example scales the runtime back to zero.
Change mounted configuration and image tags deliberately, then restart the
Deployment after configuration changes; the process does not reload YAML.
Administrator bootstrap preserves existing accounts, so changing its configured
password does not rotate the persisted credential. Coordinate connection-secret
updates with actual database credential changes.

Do not scale this Deployment above one replica or share its PVC among processes.
Distributed placement and consumer/shard coordination require their own deployment
design. The Service exposes HTTP only. Persistent startup rejects stale/incomplete
catalogs or invalid replay history, requiring write-quiesced maintenance recovery.

Deleting the Deployment stops the runtime without deleting its explicit PVC.
Deleting the namespace deletes its namespaced claims; actual underlying-volume
retention also depends on the StorageClass reclaim policy. PVC deletion destroys
signing material and derived stores if the volume is reclaimed. Existing database
resources are managed outside this manifest. Coordinate source-database and signing
key backups before destructive resets.
