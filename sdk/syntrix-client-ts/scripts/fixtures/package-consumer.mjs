import assert from 'node:assert/strict';
import { createTestLockManager } from './test-locks.mjs';

// Resolve every SDK import from this isolated installation, never from the workspace.
const remote = await import('@syntrix/client');
assert.equal(typeof remote.SyntrixClient, 'function');
assert.equal('createLocalReplicationRuntime' in remote, false);
await import('fake-indexeddb/auto');
const sdkEntry = import.meta.resolve('@syntrix/client');
const { loadLocalRuntime } = await import(new URL('./internal/local/loader.js', sdkEntry));
const local = await loadLocalRuntime();
const until = async (condition) => {
  const deadline = Date.now() + 5000;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error('Packed runtime did not reach the expected state');
    await new Promise((resolve) => setTimeout(resolve, 2));
  }
};

for (const fault of ['document', 'metadata', 'checkpoint']) {
  const schema = local.fillWithDefaultSettings({
    version: 0, primaryKey: 'id', type: 'object',
    properties: { id: { type: 'string', maxLength: 100 }, value: { type: 'number' } },
    required: ['id', 'value'],
  });
  const storage = local.getRxStorageDexie();
  const params = {
    databaseName: `packed-${fault}-${crypto.randomUUID()}`, databaseInstanceToken: 'packed-test',
    multiInstance: false, devMode: false, options: {},
  };
  const fork = await storage.createStorageInstance({ ...params, collectionName: 'fork', schema });
  const meta = await storage.createStorageInstance({
    ...params, collectionName: 'meta', schema: local.getRxReplicationMetaInstanceSchema(schema, false),
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
  const start = () => local.createLocalReplicationRuntime({
    identifier: 'packed', forkInstance: wrappedFork, metaInstance: wrappedMeta,
    conflictHandler: local.defaultConflictHandler, hashFunction: local.defaultHashSha256,
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
const client = new remote.SyntrixClient('https://packed.invalid/base', {
  database: 'app', auth: { token: token('alice') },
});
// The installed REST provider and the bundled local session load distinct
// module graphs. Their ownership fence must still be shared on this provider.
const provider = client.tokenProvider;
let session = await local.createLocalSession(provider);
const lockManager = createTestLockManager();
const options = {
  endpoint: 'https://packed.invalid/base/', database: 'app', name: crypto.randomUUID(), alias: 'activeUsers',
  source: { collection: 'users', filters: [] }, lockManager,
};
let alias = await local.openAliasStorage({ ...options, session });
await alias.set('specified-id', { counter: 9007199254740993n, type: 'business', _deleted: 'business' });
assert.equal((await alias.get('specified-id')).counter, 9007199254740993n);
await alias.close();
await session.close();
session = await local.createLocalSession(provider);
alias = await local.openAliasStorage({ ...options, session });
assert.equal((await alias.get('specified-id')).counter, 9007199254740993n);
await alias.delete('specified-id');
assert.equal(await alias.get('specified-id'), null);
await alias.set('specified-id', { counter: 9007199254740994n });
assert.equal((await alias.get('specified-id')).counter, 9007199254740994n);
provider.setToken(token('bob'));
await provider.getToken();
await assert.rejects(alias.get('specified-id'));
const bob = await local.createLocalSession(provider);
const isolated = await local.openAliasStorage({ ...options, session: bob });
assert.equal(await isolated.get('specified-id'), null);
await isolated.close();
await bob.close();
console.log('Packed local storage: exact values, offline reopen, same-ID recreation and cross-bundle account isolation passed');
