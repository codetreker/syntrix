import assert from 'node:assert/strict';
import { createTestLockManager } from './test-locks.mjs';

// Resolve every SDK import from this isolated installation, never from the workspace.
const remote = await import('@syntrix/client');
assert.equal(typeof remote.SyntrixClient, 'function');
assert.equal('createReplicationRuntime' in remote, false);
assert.equal('createReplicaQueryClient' in remote, false);
assert.equal('createReplicaDownstream' in remote, false);
assert.equal('createReplicaHttpSource' in remote, false);
assert.equal('createReplicaHttpUpstream' in remote, false);
assert.equal('resolveReplica' in remote, false);
const unsupportedClient = new remote.SyntrixClient('https://example.test', { database: 'app' });
assert.equal(typeof unsupportedClient.replicate('users').where('active', '==', true).limit(10), 'object');
await assert.rejects(unsupportedClient.openReplica({ name: 'unsupported', collections: { users: unsupportedClient.replicate('users') } }),
  error => error.code === 'ReplicaUnsupportedEnvironment');
await import('fake-indexeddb/auto');
const sdkEntry = import.meta.resolve('@syntrix/client');
const { loadReplicaRuntime } = await import(new URL('./internal/replica/loader.js', sdkEntry));
const replica = await loadReplicaRuntime();
const until = async (condition) => {
  const deadline = Date.now() + 5000;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error('Packed runtime did not reach the expected state');
    await new Promise((resolve) => setTimeout(resolve, 2));
  }
};

for (const fault of ['document', 'metadata', 'checkpoint']) {
  const schema = replica.fillWithDefaultSettings({
    version: 0, primaryKey: 'id', type: 'object',
    properties: { id: { type: 'string', maxLength: 100 }, value: { type: 'number' } },
    required: ['id', 'value'],
  });
  const storage = replica.getRxStorageDexie();
  const params = {
    databaseName: `packed-${fault}-${crypto.randomUUID()}`, databaseInstanceToken: 'packed-test',
    multiInstance: false, devMode: false, options: {},
  };
  const fork = await storage.createStorageInstance({ ...params, collectionName: 'fork', schema });
  const meta = await storage.createStorageInstance({
    ...params, collectionName: 'meta', schema: replica.getRxReplicationMetaInstanceSchema(schema, false),
  });
  const wrappedFork = Object.create(fork);
  const wrappedMeta = Object.create(meta);
  let failure = true;
  let fetched = 0;
  const diagnostics = [];
  const committed = [];
  const checkpoints = [
    { sequence: 1, phase: 'scan', after: 'alice' },
    { sequence: 2, phase: 'live', token: 'resume-2' },
    { sequence: 3 },
  ];
  const received = [];
  const reject = (rows) => ({ error: [{ status: 500, documentId: rows[0].document.id, writeRow: rows[0] }] });
  wrappedFork.bulkWrite = async (rows, context) => {
    if (failure && fetched === 2 && fault === 'document') return reject(rows);
    return fork.bulkWrite(rows, context);
  };
  wrappedMeta.bulkWrite = async (rows, context) => {
    const matching = fault === 'metadata' && context === 'replication-down-write-meta'
      || fault === 'checkpoint' && context === 'replication-set-checkpoint' && rows[0].document.itemId === 'down';
    if (failure && fetched === 2 && matching) return reject(rows);
    return meta.bulkWrite(rows, context);
  };
  const start = () => replica.createReplicationRuntime({
    identifier: 'packed', forkInstance: wrappedFork, metaInstance: wrappedMeta,
    conflictHandler: replica.defaultConflictHandler, hashFunction: replica.defaultHashSha256,
    pullBatchSize: 1, pushBatchSize: 1,
    readSource: async (checkpoint) => {
      received.push(checkpoint);
      assert.deepEqual(checkpoint, checkpoint === undefined ? undefined : checkpoints[checkpoint.sequence - 1]);
      fetched = (checkpoint?.sequence ?? 0) + 1;
      if (fetched > 3) return { documents: [], checkpoint, complete: true };
      return { documents: [{ id: `remote-${fetched}`, value: fetched, _deleted: false }],
        checkpoint: checkpoints[fetched - 1], complete: fetched === 3 };
    },
    writeRemote: async () => [],
    onCheckpoint: async (checkpoint) => {
      const stored = await meta.findDocumentsById(['down|1'], true);
      assert.deepEqual(stored[0].checkpointData, { source: checkpoint });
      committed.push(checkpoint.sequence);
    },
    onError: (error) => { diagnostics.push({ error, stopped: runtime.stopped }); },
  });
  let runtime = start();
  try {
    await until(() => diagnostics.length === 1);
    assert.equal(diagnostics[0].stopped, true);
    assert.equal(runtime.ready, false);
    await assert.rejects(runtime.close());
    assert.deepEqual(committed, [1]);
    failure = false;
    runtime = start();
    await until(() => runtime.ready);
    await runtime.waitForIdle();
    assert.equal(diagnostics.length, 1);
    const docs = await fork.findDocumentsById(['remote-1', 'remote-2', 'remote-3'], false);
    assert.equal(docs.length, 3);
    assert.equal(committed.at(-1), 3);
    await runtime.close();
    runtime = start();
    await until(() => runtime.ready || runtime.stopped);
    await runtime.waitForIdle();
    assert.equal(runtime.ready, true);
    assert.deepEqual(received, [undefined, checkpoints[0], checkpoints[0], checkpoints[1], checkpoints[2]]);
    assert.deepEqual(committed, [1, 2, 3, 3]);
    assert.equal(diagnostics.length, 1);
    console.log(`Packed Dexie ${fault} failure: durable prefix, checkpoint replacement and fresh-instance replay passed`);
  } finally {
    await runtime.close().catch((error) => {
      if (error !== runtime.error) throw error;
    });
    await fork.remove();
    await meta.remove();
  }
}

const token = (subject) => `eyJhbGciOiJub25lIn0.${Buffer.from(JSON.stringify({ sub: subject, oid: subject, exp: 1 })).toString('base64url')}.signature`;
for (const fault of ['assumed', 'fork-chunk']) {
  const owner = new remote.SyntrixClient('https://packed.invalid/base', { database: 'app', auth: { token: token('owner') } });
  const session = await replica.createReplicaSession(owner.tokenProvider);
  const underlying = replica.getRxStorageDexie();
  const injected = new Error(`packed wrapped ${fault} failure`);
  let failure = true;
  let chunks = 0;
  const storage = { ...underlying, async createStorageInstance(params) {
    const raw = await underlying.createStorageInstance(params);
    return new Proxy(raw, { get(target, property) {
      if (property === 'bulkWrite') return async (rows, context) => {
        if (params.collectionName.startsWith('records_') && failure && ++chunks === 2 && fault === 'fork-chunk') throw injected;
        if (params.collectionName.startsWith('meta_') && failure && fault === 'assumed' && context === 'replication-down-write-meta') throw injected;
        return target.bulkWrite(rows, context);
      };
      const value = Reflect.get(target, property, target);
      return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const options = { endpoint: 'https://packed.invalid/base/', database: 'app', name: crypto.randomUUID(),
    alias: 'users', source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage, session };
  let alias = await replica.openAliasStorage(options);
  let native = await alias.native(await alias.captureScope());
  const documents = await Promise.all(Array.from({ length: 6 }, async (_, index) => {
    const logicalId = `remote-${index}`;
    return { key: `d:${await replica.defaultHashSha256(logicalId)}`, kind: 'd', logicalId, existence: 'live',
      payload: JSON.stringify({ type: 'object', value: { value: { type: 'string', value: 'remote' } } }),
      editToken: null, pin: null, wire: {}, _deleted: false };
  }));
  const pushed = [];
  const errors = [];
  const start = () => replica.createReplicationRuntime({
    ...native, forkInstance: native.fork, metaInstance: native.meta,
    hashFunction: replica.defaultHashSha256, conflictHandler: replica.defaultConflictHandler,
    pullBatchSize: 6, pushBatchSize: 4,
    readSource: async () => ({ documents, checkpoint: { sequence: 1 }, complete: true }),
    writeRemote: async rows => { pushed.push(...rows); return []; }, onError: error => errors.push(error),
  });
  let runtime = start();
  try {
    await until(() => runtime.stopped);
    await assert.rejects(runtime.close(), error => error === injected);
    assert.deepEqual(errors, [injected]);
    const stored = await native.fork.findDocumentsById(documents.slice(0, 4).map(doc => doc.key), false);
    assert.equal(stored.length, 4);
    assert.deepEqual(stored[0]._meta.o, { _rev: 1, hash: await replica.defaultHashSha256(native.identifier) });
    failure = false;
    await alias.close();
    alias = await replica.openAliasStorage(options);
    native = await alias.native(await alias.captureScope());
    runtime = start();
    await runtime.waitForIdle();
    assert.equal(runtime.ready, true);
    assert.deepEqual(pushed, []);
    await alias.set('remote-0', { value: 'edited' });
    await runtime.waitForIdle();
    assert.equal(pushed.length, 1);
    assert.equal(pushed[0].newDocumentState.logicalId, 'remote-0');
    assert.equal(JSON.parse(pushed[0].newDocumentState.payload).value.value.value, 'edited');
    assert.equal(JSON.parse(pushed[0].assumedMasterState.payload).value.value.value, 'remote');
    assert.deepEqual(errors, [injected]);
    console.log(`Packed wrapped Dexie ${fault}: durable provenance prevents replay echo and preserves genuine edits`);
  } finally {
    await runtime.close().catch(error => { if (error !== injected) throw error; });
    await alias.close();
    await session.close();
  }
}

const client = new remote.SyntrixClient('https://packed.invalid/base', {
  database: 'app', auth: { token: token('alice') },
});
// The installed REST provider and the bundled replica session load distinct
// module graphs. Their ownership fence must still be shared on this provider.
const provider = client.tokenProvider;
let session = await replica.createReplicaSession(provider);
const lockManager = createTestLockManager();
const options = {
  endpoint: 'https://packed.invalid/base/', database: 'app', name: crypto.randomUUID(), alias: 'activeUsers',
  source: { collection: 'users', filters: [] }, lockManager,
};
let alias = await replica.openAliasStorage({ ...options, session });
await alias.set('specified-id', { counter: 9007199254740993n, type: 'business', _deleted: 'business' });
assert.equal((await alias.get('specified-id')).counter, 9007199254740993n);
await alias.close();
await session.close();
session = await replica.createReplicaSession(provider);
alias = await replica.openAliasStorage({ ...options, session });
assert.equal((await alias.get('specified-id')).counter, 9007199254740993n);
await alias.delete('specified-id');
assert.equal(await alias.get('specified-id'), null);
await alias.set('specified-id', { counter: 9007199254740994n });
assert.equal((await alias.get('specified-id')).counter, 9007199254740994n);
await alias.set('earlier', { counter: 9007199254740993n });
const queries = replica.createReplicaQueryClient(alias);
try {
  const spec = { orderBy: [{ field: 'counter', direction: 'asc' }], limit: 1 };
  const first = await queries.getPage(spec);
  assert.deepEqual(first.documents.map(doc => doc.id), ['earlier']);
  assert.equal(first.documents[0].counter, 9007199254740993n);
  const second = await queries.getPage({ ...spec, startAfter: first.nextCursor });
  assert.deepEqual(second.documents.map(doc => doc.id), ['specified-id']);
  assert.equal(second.nextCursor, null);
  const snapshots = [];
  const failures = [];
  const stop = queries.watch(spec, documents => snapshots.push(documents), error => failures.push(error));
  try {
    await until(() => snapshots.length === 1 || failures.length);
    await alias.update('specified-id', { counter: 0n });
    await until(() => snapshots.length === 2 || failures.length);
    assert.equal(snapshots[1][0].id, 'specified-id');
    assert.equal(snapshots[1][0].counter, 0n);
    await alias.update('specified-id', { counter: 0 });
    await until(() => snapshots.length === 3 || failures.length);
    assert.equal(snapshots[2][0].counter, 0);
    assert.deepEqual(failures, []);
  } finally { stop(); }
} finally { await queries.close(); }
assert.equal(queries.debugStats().payloadBytes, 0);
assert.equal(queries.debugStats().nodes, 0);
console.log('Packed replica queries: exact keyset pages, dynamic window refill, typed changes and resource release passed');
provider.setToken(token('bob'));
await provider.getToken();
await assert.rejects(alias.get('specified-id'));
const bob = await replica.createReplicaSession(provider);
const isolated = await replica.openAliasStorage({ ...options, session: bob });
assert.equal(await isolated.get('specified-id'), null);
await isolated.close();
await bob.close();
console.log('Packed replica storage: exact values, offline reopen, same-ID recreation and cross-bundle account isolation passed');

const sourceGeneration = crypto.randomUUID();
const databaseIdentity = '0123456789abcdef';
const sourceHash = 'a'.repeat(64);
let sourceReads = 0;
const sourceServer = Bun.serve({ hostname: '127.0.0.1', port: 0, async fetch(request) {
  assert.equal(new URL(request.url).pathname, '/base/replication/v1/databases/app/pull');
  const body = await request.json();
  assert.equal(body.limit, 100);
  assert.equal(body.source.version, 1);
  assert.equal(request.headers.get('X-Syntrix-Expected-Database-Identity'), sourceReads === 0 ? null : databaseIdentity);
  assert.equal(body.checkpoint, sourceReads === 0 ? null : 'packed-source-1');
  sourceReads++;
  const events = sourceReads === 1 ? [{ type: 'upsert', document: { type: 'object', value: {
    id: { type: 'string', value: 'remote-user' }, collection: { type: 'string', value: 'users' },
    version: { type: 'int64', value: '1' }, createdAt: { type: 'int64', value: '1' }, updatedAt: { type: 'int64', value: '1' },
    score: { type: 'int64', value: '9007199254740993' },
  } } }] : [{ type: 'leave', id: 'remote-user' }];
  return Response.json({ protocolVersion: 1, mode: 'events', databaseIdentity, sourceHash, events,
    checkpoint: `packed-source-${sourceReads}`, generationId: sourceGeneration,
    phase: 'live', caughtUp: true, bootstrapComplete: true });
} });
let sourceSession, sourceAlias, downstream, sourceQueries;
try {
  const sourceClient = new remote.SyntrixClient(`${sourceServer.url.href}base`, { database: 'app', auth: { token: token('source-user') } });
  sourceSession = await replica.createReplicaSession(sourceClient.tokenProvider);
  sourceAlias = await replica.openAliasStorage({ session: sourceSession, endpoint: `${sourceServer.url.href}base`,
    database: 'app', name: crypto.randomUUID(), alias: 'users', source: { collection: 'users', filters: [] },
    lockManager: createTestLockManager() });
  const source = replica.createReplicaHttpSource({ axios: sourceClient.pullTransport.axios,
    provider: sourceClient.tokenProvider, database: 'app', definition: (await sourceAlias.readManifest()).definition });
  downstream = replica.createReplicaDownstream({ storage: sourceAlias, source, options: { pollIntervalMs: 60_000 } }, {
    leadership: () => ({ wait: async signal => signal.throwIfAborted(), close: async () => {} }),
    now: Date.now, random: () => 0.5, set: (callback, delay) => setTimeout(callback, delay), clear: timer => clearTimeout(timer),
  });
  await until(() => downstream.snapshot.state === 'idle' || downstream.snapshot.state === 'blocked');
  assert.equal(downstream.snapshot.state, 'idle');
  assert.equal(downstream.snapshot.ready, true);
  sourceQueries = replica.createReplicaQueryClient(sourceAlias);
  assert.equal((await sourceQueries.get())[0].score, 9007199254740993n);
  await sourceAlias.update('remote-user', { score: 9n });
  downstream.refresh();
  await until(() => sourceReads === 2 && downstream.snapshot.state === 'idle');
  assert.equal((await sourceQueries.get())[0].score, 9n);
  assert.equal((await sourceAlias.readManifest()).boundDatabaseId, databaseIdentity);
  console.log('Packed downstream: typed HTTP source, bound resume and query visibility preserve pending edits on leave');
} finally {
  const cleanup = await Promise.allSettled([sourceQueries?.close(), downstream?.close(), sourceAlias?.close(), sourceSession?.close()]);
  sourceServer.stop(true);
  for (const result of cleanup) if (result.status === 'rejected') throw result.reason;
}

const upstreamGeneration = crypto.randomUUID();
const pushRequests = [];
let currentRemote = null, upstreamReads = 0, currentReads = 0, losePushResponse = false;
const upstreamServer = Bun.serve({ hostname: '127.0.0.1', port: 0, async fetch(request) {
  const path = new URL(request.url).pathname;
  const body = await request.json();
  const identity = request.headers.get('X-Syntrix-Expected-Database-Identity');
  if (path.endsWith('/pull')) {
    assert.equal(identity, upstreamReads++ === 0 ? null : databaseIdentity);
    return Response.json({ protocolVersion: 1, mode: 'events', databaseIdentity, sourceHash,
      events: [], checkpoint: `upstream-source-${upstreamReads}`, generationId: upstreamGeneration,
      phase: 'live', caughtUp: true, bootstrapComplete: true });
  }
  assert.equal(identity, databaseIdentity);
  if (path.endsWith('/query')) {
    currentReads++;
    assert.deepEqual(body.filters, [{ field: 'id', op: '==', value: { type: 'string', value: 'alice' } }]);
    assert.equal(body.orderBy, undefined);
    assert.equal(body.showDeleted, true);
    return Response.json({ documents: currentRemote ? [remote.encodeQueryValue(currentRemote)] : [],
      nextCursor: null, effectiveOrder: [{ field: 'id', direction: 'asc' }] });
  }
  assert.ok(path.endsWith('/push'));
  assert.equal(body.changes.length, 1);
  const change = body.changes[0], desired = remote.decodeQueryValue(change.document);
  pushRequests.push({ action: change.action, desired });
  if (change.action === 'create') assert.equal(desired.version, undefined);
  else assert.equal(desired.version, currentRemote.version);
  currentRemote = { ...desired, collection: 'users', version: (currentRemote?.version ?? 0n) + 1n,
    createdAt: 1n, updatedAt: 2n };
  return losePushResponse ? Response.json({ code: 'INTERNAL', message: 'Reply lost after write' }, { status: 500 })
    : Response.json({ conflicts: [] });
} });
let upstreamSession, upstreamAlias, synchronization;
try {
  const client = new remote.SyntrixClient(upstreamServer.url.href, { database: 'app', auth: { token: token('upstream-user') } });
  upstreamSession = await replica.createReplicaSession(client.tokenProvider);
  upstreamAlias = await replica.openAliasStorage({ session: upstreamSession, endpoint: upstreamServer.url.href,
    database: 'app', name: crypto.randomUUID(), alias: 'users', source: { collection: 'users', filters: [] },
    lockManager: createTestLockManager() });
  await upstreamAlias.set('alice', { exact: 9007199254740993n });
  const source = replica.createReplicaHttpSource({ axios: client.pullTransport.axios, provider: client.tokenProvider,
    database: 'app', definition: (await upstreamAlias.readManifest()).definition });
  const upstream = replica.createReplicaHttpUpstream({ axios: client.pullTransport.axios, provider: client.tokenProvider,
    database: 'app', collection: 'users' });
  synchronization = replica.createReplicaDownstream({ storage: upstreamAlias, source, upstream,
    options: { pollIntervalMs: 60_000 } }, {
    leadership: () => ({ wait: async signal => signal.throwIfAborted(), close: async () => {} }),
    now: Date.now, random: () => 0.5, set: (callback, delay) => setTimeout(callback, delay), clear: timer => clearTimeout(timer),
  });
  await until(() => pushRequests.length === 1 && synchronization.snapshot.state === 'idle');
  assert.equal(currentRemote.exact, 9007199254740993n);
  assert.equal((await upstreamAlias.readManifest()).dirtyUpstream, null);
  await synchronization.pause();
  await upstreamAlias.set('alice', { exact: 9007199254740994n });
  await synchronization.resume();
  await until(() => pushRequests.length === 2 && synchronization.snapshot.state === 'idle');
  assert.equal(pushRequests[1].action, 'update');
  assert.equal(currentReads, 1);
  await synchronization.pause();
  losePushResponse = true;
  await upstreamAlias.set('alice', { exact: 9007199254740995n });
  await synchronization.resume();
  await until(() => synchronization.snapshot.state === 'blocked');
  assert.equal(pushRequests.length, 3);
  assert.ok((await upstreamAlias.readManifest()).dirtyUpstream);
  await assert.rejects(synchronization.resume());
  const inspected = await synchronization.inspect({ logicalId: 'alice', readCurrent: true });
  assert.equal(inspected.document.current.source, 'authoritative-read');
  assert.equal(inspected.document.current.document.exact, 9007199254740995n);
  await synchronization.resolve({ kind: 'adopt-server', issueId: inspected.issueId, logicalId: 'alice',
    physicalEpoch: inspected.physicalEpoch, editToken: inspected.document.desired.editToken });
  assert.equal((await upstreamAlias.readManifest()).dirtyUpstream, null);
  await synchronization.resume();
  await until(() => synchronization.snapshot.state === 'idle');
  assert.equal(pushRequests.length, 3);
  console.log('Packed upstream: typed create/update, preflight, uncertain response, authority inspection and explicit adopt passed');
} finally {
  const cleanup = await Promise.allSettled([synchronization?.close(), upstreamAlias?.close(), upstreamSession?.close()]);
  upstreamServer.stop(true);
  for (const result of cleanup) if (result.status === 'rejected') throw result.reason;
}

const browserProperties = [
  [globalThis, 'window', Object.getOwnPropertyDescriptor(globalThis, 'window')],
  [globalThis, 'document', Object.getOwnPropertyDescriptor(globalThis, 'document')],
  [navigator, 'locks', Object.getOwnPropertyDescriptor(navigator, 'locks')],
  [navigator, 'onLine', Object.getOwnPropertyDescriptor(navigator, 'onLine')],
];
Object.defineProperty(globalThis, 'window', { configurable: true, value: globalThis });
Object.defineProperty(globalThis, 'document', { configurable: true, value: {
  visibilityState: 'visible', addEventListener() {}, removeEventListener() {},
} });
Object.defineProperty(navigator, 'locks', { configurable: true, value: createTestLockManager() });
Object.defineProperty(navigator, 'onLine', { configurable: true, value: false });
const publicServer = Bun.serve({ hostname: '127.0.0.1', port: 0, fetch() {
  return Response.json({ code: 'UNAVAILABLE', message: 'Offline consumer fixture' }, { status: 503 });
} });
let publicReplica, historyReplica, reopenedReplica;
let stopPublicWatch = () => {};
try {
  const client = new remote.SyntrixClient(publicServer.url.href, { database: 'app', auth: { token: token('public-user') } });
  const name = `packed-public-${crypto.randomUUID()}`;
  const diagnostics = [];
  publicReplica = await client.openReplica({ name, collections: {
    tasks: client.replicate('users').where('active', '==', true),
    scratch: client.replicate('users').where('active', '==', false),
  }, onDiagnostic: event => diagnostics.push(event) });
  await publicReplica.sync.pause();
  const tasks = publicReplica.collection('tasks');
  await tasks.doc('specified-id').set({ active: true, exact: 9007199254740993n, score: 1 });
  await tasks.doc('next').set({ active: true, score: 2 });
  const page = await tasks.orderBy('score').limit(1).getPage();
  assert.deepEqual(page.documents.map(row => row.id), ['specified-id']);
  assert.ok(page.nextCursor);
  assert.deepEqual((await tasks.orderBy('score').limit(1).startAfter(page.nextCursor).getPage()).documents.map(row => row.id), ['next']);
  const windows = [];
  stopPublicWatch = tasks.orderBy('score').limit(1).watch(rows => windows.push(rows.map(row => row.id)));
  await until(() => windows.length === 1);
  await tasks.doc('next').update({ score: 0 });
  await until(() => windows.at(-1)?.[0] === 'next');
  assert.equal((await tasks.doc('specified-id').get()).exact, 9007199254740993n);
  assert.equal(await publicReplica.collection('scratch').doc('specified-id').get(), null);
  await assert.rejects(publicReplica.removeCollection('tasks'), error => error.code === 'ReplicaRemovalBlocked');
  const inspected = await publicReplica.sync.inspect('tasks', { id: 'specified-id' });
  assert.equal(inspected.document.desired.document.exact, 9007199254740993n);
  assert.equal('payload' in inspected.document.desired, false);
  assert.ok(diagnostics.some(event => event.operationId && event.replicaId && Number.isInteger(event.sessionVersion)));
  assert.ok(diagnostics.every(event => !JSON.stringify(event).includes('9007199254740993')));
  stopPublicWatch();
  await publicReplica.close();
  historyReplica = await client.openReplica({ name, collections: {} });
  await historyReplica.removeCollection('scratch');
  await assert.rejects(historyReplica.removeCollection('tasks'), error => error.code === 'ReplicaRemovalBlocked');
  await historyReplica.close();
  reopenedReplica = await client.openReplica({ name, collections: { tasks: client.replicate('users').where('active', '==', true) } });
  await reopenedReplica.sync.pause();
  assert.equal((await reopenedReplica.collection('tasks').doc('specified-id').get()).exact, 9007199254740993n);
  console.log('Packed public replica: local CRUD, dynamic watch, exact cursor pages, alias isolation, offline reopen and historical removal passed');
} finally {
  stopPublicWatch();
  const cleanup = await Promise.allSettled([publicReplica?.close(), historyReplica?.close(), reopenedReplica?.close()]);
  publicServer.stop(true);
  for (const [target, key, descriptor] of browserProperties) {
    if (descriptor) Object.defineProperty(target, key, descriptor);
    else delete target[key];
  }
  for (const result of cleanup) if (result.status === 'rejected') throw result.reason;
}
