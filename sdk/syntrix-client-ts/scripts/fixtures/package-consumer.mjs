import assert from 'node:assert/strict';
import { createTestLockManager } from './test-locks.mjs';

// Resolve every SDK import from this isolated installation, never from the workspace.
const remote = await import('@syntrix/client');
assert.equal(typeof remote.SyntrixClient, 'function');
assert.equal('createReplicationRuntime' in remote, false);
assert.equal('createReplicaQueryClient' in remote, false);
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
