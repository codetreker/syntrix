import { describe, expect, test } from 'bun:test';
import axios from 'axios';
import { getRxStorageMemory } from 'rxdb/plugins/storage-memory';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getUnderlyingPersistentStorage, now, type RxStorage } from 'rxdb';
import { AuthSessionChangedError } from '../../api/errors.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { setupAuthInterceptor } from '../auth/interceptor.js';
import { createReplicaSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage } from './storage.js';
import type { ReplicaRecord } from './storage-types.js';
import {
  createReplicationRuntime,
  defaultConflictHandler,
  defaultHashSha256,
  fillWithDefaultSettings,
  getRxReplicationMetaInstanceSchema,
  type ReplicationOptions,
  type ReplicationRuntime,
} from './runtime.js';

type Row = { id: string; value: number };
type Cursor = { sequence: number };
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};
const tick = () => new Promise<void>(resolve => setTimeout(resolve, 1));
const until = async (condition: () => boolean) => {
  const deadline = Date.now() + 3000;
  while (!condition()) {
    if (Date.now() > deadline) throw new Error('Runtime did not reach the expected state');
    await tick();
  }
};

const fixture = async () => {
  const storage = getRxStorageMemory();
  const schema = fillWithDefaultSettings<Row>({
    version: 0, primaryKey: 'id', type: 'object',
    properties: { id: { type: 'string', maxLength: 100 }, value: { type: 'number' } },
    required: ['id', 'value'],
  });
  const options = { databaseName: crypto.randomUUID(), databaseInstanceToken: 'runtime', options: {}, devMode: false, multiInstance: false };
  const fork = await storage.createStorageInstance({ ...options, collectionName: 'fork', schema });
  const meta = await storage.createStorageInstance({ ...options, collectionName: 'meta', schema: getRxReplicationMetaInstanceSchema<Row, { source: object }>(schema, false) });
  const errors: unknown[] = [];
  const committed: number[] = [];
  const pushed: string[] = [];
  const runtimes: ReplicationRuntime[] = [];
  const config: ReplicationOptions<Row, Cursor> = {
    identifier: 'runtime', forkInstance: fork, metaInstance: meta,
    conflictHandler: defaultConflictHandler, hashFunction: defaultHashSha256,
    pullBatchSize: 10,
    readSource: async checkpoint => ({ documents: [], checkpoint: checkpoint ?? { sequence: 1 }, complete: true }),
    createProgressDocument: page => ({ id: '$progress', value: page.checkpoint.sequence, _deleted: false }),
    isControlDocument: row => row.id.startsWith('$'),
    writeRemote: async rows => { pushed.push(...rows.map(row => row.newDocumentState.id)); return []; },
    onCheckpoint: async checkpoint => { committed.push(checkpoint.sequence); },
    onError: error => { errors.push(error); },
  };
  const seed = async (ids: string[]) => {
    const result = await fork.bulkWrite(ids.map(id => ({ document: {
      id, value: 1, _deleted: false, _attachments: {}, _rev: '1-local', _meta: { lwt: now() },
    } })), 'local');
    expect(result.error).toHaveLength(0);
  };
  const start = () => {
    const runtime = createReplicationRuntime(config);
    runtimes.push(runtime);
    return runtime;
  };
  const close = async () => {
    await Promise.allSettled(runtimes.map(runtime => runtime.close()));
    await fork.close();
    await meta.close();
  };
  return { fork, meta, config, errors, committed, pushed, seed, start, close };
};

describe('native upstream persistence phases', () => {
  test('each bounded unit finishes its durable settlement before the next unit begins', async () => {
    const f = await fixture();
    f.config.pushBatchSize = 1;
    await f.seed(Array.from({ length: 9 }, (_, index) => `pending-${index}`));
    const wireGate = deferred(), completionGate = deferred();
    let firstWire = true, completing = false, phaseActive = false;
    const phases: string[][] = [];
    f.config.writeRemote = async rows => {
      f.pushed.push(...rows.map(row => row.newDocumentState.id));
      if (firstWire) { firstWire = false; await wireGate.promise; }
      return [];
    };
    f.config.upstreamPersistence = {
      begin: async documents => {
        expect(phaseActive).toBe(false); phaseActive = true;
        expect(documents.length).toBeLessThanOrEqual(4);
        phases.push(documents.map(document => document.id));
      },
      complete: async checkpoint => {
        expect((await f.meta.findDocumentsById(['up|1'], false))[0].checkpointData as unknown).toEqual(checkpoint);
        if (!completing) { completing = true; await completionGate.promise; }
        phaseActive = false;
      },
      failed: async error => { throw error; },
    };
    const runtime = f.start();
    try {
      await until(() => !firstWire);
      await tick(); wireGate.resolve();
      await until(() => completing);
      const before = phases.length;
      await tick(); expect(phases).toHaveLength(before);
      expect(phaseActive).toBe(true);
      completionGate.resolve(); await runtime.waitForIdle();
      expect(phases.length).toBeGreaterThan(1);
      expect(phaseActive).toBe(false);
      expect(f.pushed).toHaveLength(9);
      expect(f.errors).toEqual([]);
    } finally { wireGate.resolve(); completionGate.resolve(); await f.close(); }
  });

  test('no-op desired states pass through phase completion without a wire callback', async () => {
    const f = await fixture();
    f.config.readSource = async checkpoint => ({ documents: checkpoint ? [] : [{ id: 'alice', value: 1, _deleted: false }], checkpoint: { sequence: 1 }, complete: true });
    const first = f.start(); await first.waitForIdle(); await first.close();
    let row = (await f.fork.findDocumentsById(['alice'], false))[0];
    for (const value of [2, 1]) {
      const next = { ...row, value, _meta: { lwt: now() }, _rev: `${Number(row._rev.split('-')[0]) + 1}-edit` };
      expect((await f.fork.bulkWrite([{ previous: row, document: next }], 'local')).error).toEqual([]); row = next;
    }
    let completed = 0;
    f.config.upstreamPersistence = {
      begin: async () => {},
      complete: async checkpoint => { expect(checkpoint.lwt).toBeGreaterThanOrEqual(row._meta.lwt); completed++; },
      failed: async error => { throw error; },
    };
    try {
      const runtime = f.start(); await runtime.waitForIdle();
      expect(completed).toBeGreaterThan(0); expect(f.pushed).toEqual([]); expect(f.errors).toEqual([]);
    } finally { await f.close(); }
  });

  for (const point of ['begin', 'wire', 'metadata', 'checkpoint', 'complete'] as const) {
    test(`phase ${point} failure drains cleanup and retains the original error`, async () => {
      const f = await fixture(); await f.seed(['pending']);
      const fault = new Error(`phase-${point}`), gate = deferred();
      const failed: unknown[] = [];
      let completed = 0, cleanupEntered = false;
      const original = f.meta.bulkWrite.bind(f.meta);
      f.meta.bulkWrite = async (rows, context) => {
        if ((point === 'metadata' && rows.some(row => row.document.itemId === 'pending')) ||
          (point === 'checkpoint' && rows.some(row => row.document.id === 'up|1'))) throw fault;
        return original(rows, context);
      };
      f.config.writeRemote = async () => { if (point === 'wire') throw fault; return []; };
      f.config.upstreamPersistence = {
        begin: async () => { if (point === 'begin') throw fault; },
        complete: async () => { completed++; if (point === 'complete') throw fault; },
        failed: async error => { failed.push(error); cleanupEntered = true; await gate.promise; },
      };
      const runtime = f.start();
      try {
        await until(() => cleanupEntered);
        let drained = false;
        const close = runtime.close().catch(error => { expect(error).toBe(fault); }).then(() => { drained = true; });
        await tick(); expect(drained).toBe(false);
        gate.resolve(); await close;
        await expect(runtime.waitForIdle()).rejects.toBe(fault);
        expect(failed).toEqual([fault]); expect(f.errors).toEqual([fault]);
        expect(completed).toBe(point === 'complete' ? 1 : 0);
      } finally { gate.resolve(); await f.close(); }
    });
  }

  for (const distinctCleanup of [false, true]) {
    test(`phase failure bookkeeping ${distinctCleanup ? 'retains a distinct cleanup error' : 'deduplicates the original failure'}`, async () => {
      const f = await fixture(); await f.seed(['pending']);
      const original = new Error('wire failed');
      const cleanup = distinctCleanup ? new Error('failure bookkeeping failed') : original;
      f.config.writeRemote = async () => { throw original; };
      f.config.upstreamPersistence = {
        begin: async () => {}, complete: async () => {},
        failed: async error => { expect(error).toBe(original); throw cleanup; },
      };
      const runtime = f.start();
      try {
        await until(() => runtime.stopped);
        const error = await runtime.waitForIdle().catch(error => error);
        if (distinctCleanup) {
          expect(error).toMatchObject({ cause: original, cleanupErrors: [cleanup] });
          expect(f.errors).toEqual([original, error]);
        } else {
          expect(error).toBe(original); expect(f.errors).toEqual([original]);
        }
        expect(runtime.error).toBe(error);
        await expect(runtime.close()).rejects.toBe(error);
      } finally { await f.close(); }
    });
  }

  test('a successful sibling followed by failure leaves the whole unit incomplete and stops queued dispatch', async () => {
    const f = await fixture(); f.config.pushBatchSize = 1;
    await f.seed(Array.from({ length: 8 }, (_, index) => `pending-${index}`));
    const firstGate = deferred(); let calls = 0, phase = 0;
    const fault = new Error('second unit sibling failed');
    const dispatched: { phase: number; id: string }[] = [], failed: unknown[] = [], completed: number[] = [];
    f.config.upstreamPersistence = {
      begin: async () => { phase++; },
      complete: async () => { completed.push(phase); },
      failed: async error => { failed.push(error); },
    };
    f.config.writeRemote = async rows => {
      calls++; dispatched.push({ phase, id: rows[0].newDocumentState.id });
      if (calls === 1) await firstGate.promise;
      if (calls === 3) throw fault;
      return [];
    };
    const runtime = f.start();
    try {
      await until(() => calls === 1); await tick(); firstGate.resolve();
      await until(() => runtime.stopped); await expect(runtime.waitForIdle()).rejects.toBe(fault);
      expect(dispatched).toHaveLength(3);
      expect(dispatched[1].phase).toBe(dispatched[2].phase);
      expect(completed).not.toContain(dispatched[1].phase);
      expect(await f.meta.findDocumentsById([`${dispatched[1].id}|0`], false)).toEqual([]);
      expect(failed).toEqual([fault]); expect(f.errors).toEqual([fault]);
    } finally { firstGate.resolve(); await f.close(); }
  });

  test('cancellation after dispatch drains failed bookkeeping without claiming a durable completion', async () => {
    const f = await fixture(); await f.seed(['pending']);
    const owner = new AbortController(), wireGate = deferred(), cleanupGate = deferred();
    const canceled = new Error('owner retired'); f.config.ownerSignal = owner.signal;
    let sent = false, cleanup = false, completed = 0;
    const failures: unknown[] = [];
    f.config.writeRemote = async () => { sent = true; await wireGate.promise; return []; };
    f.config.upstreamPersistence = {
      begin: async () => {}, complete: async () => { completed++; },
      failed: async error => { cleanup = true; failures.push(error); await cleanupGate.promise; },
    };
    const runtime = f.start();
    try {
      await until(() => sent); owner.abort(canceled); wireGate.resolve();
      await until(() => cleanup);
      let closed = false; const close = runtime.close().then(() => { closed = true; });
      await tick(); expect(closed).toBe(false);
      cleanupGate.resolve(); await close;
      expect(completed).toBe(0); expect(failures).toHaveLength(1);
      expect(await f.meta.findDocumentsById(['up|1', 'pending|0'], false)).toEqual([]);
      expect(f.errors).toEqual([]);
    } finally { wireGate.resolve(); cleanupGate.resolve(); await f.close(); }
  });
});

describe('private replication runtime', () => {
  test('downstream-only replication retains pending data and never advances the upstream checkpoint', async () => {
    const f = await fixture();
    f.config.upstreamEnabled = false;
    delete f.config.writeRemote;
    await f.seed(['pending']);
    let upHooks = 0;
    f.config.onUpCheckpoint = async () => { upHooks++; };
    const runtime = f.start();
    try {
      await until(() => runtime.ready || runtime.stopped); await runtime.waitForIdle();
      expect(runtime.ready).toBe(true);
      expect((await f.fork.findDocumentsById(['pending'], false))[0].value).toBe(1);
      expect(await f.meta.findDocumentsById(['up|1', 'pending|0'], false)).toEqual([]);
      expect(upHooks).toBe(0); expect(f.pushed).toEqual([]);
      runtime.requestResync(); await runtime.waitForIdle();
      expect(await f.meta.findDocumentsById(['up|1'], false)).toEqual([]);
    } finally { await f.close(); }
  });

  test('durable upstream hook observes no-op A to B to A progress without a remote write', async () => {
    const f = await fixture();
    f.config.readSource = async checkpoint => ({ documents: checkpoint ? [] : [{ id: 'alice', value: 1, _deleted: false }], checkpoint: { sequence: 1 }, complete: true });
    const observed: { id: string; lwt: number }[] = [];
    f.config.onUpCheckpoint = async checkpoint => {
      const stored = (await f.meta.findDocumentsById(['up|1'], false))[0];
      expect(stored.checkpointData as unknown).toEqual(checkpoint);
      observed.push(checkpoint);
    };
    try {
      const first = f.start(); await until(() => first.ready || first.stopped); await first.waitForIdle(); await first.close();
      const initialCount = observed.length;
      let current = (await f.fork.findDocumentsById(['alice'], false))[0];
      for (const value of [2, 1]) {
        const document = { ...current, value, _meta: { lwt: now() }, _rev: `${Number(current._rev.split('-')[0]) + 1}-edit` };
        expect((await f.fork.bulkWrite([{ previous: current, document }], 'local')).error).toEqual([]);
        current = document;
      }
      const second = f.start(); await until(() => second.ready || second.stopped); await second.waitForIdle();
      expect(observed.length).toBeGreaterThan(initialCount);
      expect(observed[observed.length - 1].lwt).toBeGreaterThanOrEqual(current._meta.lwt);
      expect(f.pushed).toEqual([]); expect(f.errors).toEqual([]);
    } finally { await f.close(); }
  });

  for (const failurePoint of ['metadata', 'checkpoint', 'hook'] as const) {
    test(`upstream ${failurePoint} failure cannot acknowledge settlement and preserves the original error`, async () => {
      const f = await fixture(); const fault = new Error(`upstream ${failurePoint}`);
      let hooks = 0;
      await f.seed(['pending']);
      const original = f.meta.bulkWrite.bind(f.meta);
      f.meta.bulkWrite = async (rows, context) => {
        if ((failurePoint === 'metadata' && rows.some(row => row.document.itemId === 'pending' && row.document.isCheckpoint === '0')) ||
          (failurePoint === 'checkpoint' && rows.some(row => row.document.id === 'up|1'))) throw fault;
        return original(rows, context);
      };
      f.config.onUpCheckpoint = async () => { hooks++; if (failurePoint === 'hook') throw fault; };
      const runtime = f.start();
      try {
        await until(() => runtime.stopped);
        await expect(runtime.waitForIdle()).rejects.toBe(fault);
        await expect(runtime.close()).rejects.toBe(fault);
        expect(hooks).toBe(failurePoint === 'hook' ? 1 : 0);
        expect(f.errors).toEqual([fault]);
        if (failurePoint !== 'hook') expect(await f.meta.findDocumentsById(['up|1'], false)).toEqual([]);
      } finally { await f.close(); }
    });
  }

  for (const stopPoint of ['session', 'alias-close', 'maintenance', 'maintenance-then-close'] as const) {
    for (const outcome of ['success', 'storage-error', 'unrelated-session-error', 'cleanup-error'] as const) {
      if (stopPoint.startsWith('maintenance') && outcome === 'cleanup-error') continue;
      test(`${stopPoint} cancellation drains active and queued native reads while preserving ${outcome}`, async () => {
        const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
        const provider = new DefaultTokenProvider({ token: jwt('A') });
        const session = await createReplicaSession(provider);
        const gate = deferred();
        let entered = false;
        let armed = false;
        const fault = outcome === 'unrelated-session-error' ? new AuthSessionChangedError() : new Error(outcome);
        const raw: { close(): Promise<unknown> }[] = [];
        const dexie = getRxStorageDexie({ indexedDB, IDBKeyRange });
        const storage: RxStorage<any, any> = { ...dexie, createStorageInstance: async params => {
          const instance = await dexie.createStorageInstance(params);
          raw.push(instance);
          return new Proxy(instance, { get(target, property) {
            if (property === 'findDocumentsById' && params.collectionName.startsWith('meta_')) {
              return async (ids: string[], deleted: boolean) => {
                const result = await target.findDocumentsById(ids, deleted);
                if (armed) {
                  entered = true;
                  await gate.promise;
                  if (outcome === 'storage-error' || outcome === 'unrelated-session-error') throw fault;
                }
                return result;
              };
            }
            if (property === 'close' && params.collectionName.startsWith('meta_') && outcome === 'cleanup-error') {
              return async () => { await target.close(); throw fault; };
            }
            const value = Reflect.get(target, property, target);
            return typeof value === 'function' ? value.bind(target) : value;
          } });
        } };
        const alias = await openAliasStorage({ session, endpoint: 'https://example.test', database: 'app',
          name: crypto.randomUUID(), alias: 'people', source: { collection: 'users', filters: [] },
          lockManager: createTestLockManager(), storage });
        const database = await alias.withMaintenance(async access => access.backend.database);
        const native = await alias.native(await alias.captureScope());
        const collections = Object.values(database.collections);
        const errors: unknown[] = [];
        let sourceCalls = 0;
        armed = true;
        const runtime = createReplicationRuntime<ReplicaRecord, Cursor>({
          identifier: native.identifier, forkInstance: native.fork, metaInstance: native.meta, ownerSignal: native.ownerSignal,
          hashFunction: defaultHashSha256, conflictHandler: defaultConflictHandler,
          readSource: async () => { sourceCalls++; throw new Error('Canceled startup must not reach source'); },
          writeRemote: async () => { throw new Error('Canceled startup must not push'); },
          onError: error => { errors.push(error); },
        });
        alias.registerNative({ invalidate() {}, close: () => runtime.close() });
        try {
          await until(() => entered);
          let maintenanceEntered = false;
          let stopped = false;
          const operation = stopPoint.startsWith('maintenance') ? alias.withMaintenance(async access => {
            maintenanceEntered = true;
            expect(access.ownerSignal.aborted).toBe(false);
            access.assertActive();
          }) : stopPoint === 'alias-close' ? alias.close() : Promise.resolve().then(() => provider.setToken(jwt('B'))).then(() => provider.getToken());
          const stopResult = operation.then(value => { stopped = true; return { value }; }, error => { stopped = true; return { error }; });
          await until(() => runtime.stopped);
          expect(native.ownerSignal.aborted).toBe(true);
          const aliasCloseResult = stopPoint === 'maintenance-then-close' ? alias.close().then(() => ({}), error => ({ error })) : undefined;
          await tick();
          expect(stopped).toBe(false);
          gate.resolve();
          if (outcome === 'success') {
            if (stopPoint === 'maintenance-then-close') {
              expect(await stopResult).toMatchObject({ error: { code: 'ReplicaStorageClosed' } });
              expect(await aliasCloseResult).toEqual({});
            } else expect(await stopResult).toEqual({ value: stopPoint === 'session' ? jwt('B') : undefined });
            await runtime.close();
            expect(runtime.error).toBeUndefined();
            expect(errors).toEqual([]);
            if (stopPoint === 'maintenance') {
              expect(maintenanceEntered).toBe(true);
              const current = await alias.native(await alias.captureScope());
              expect(current.ownerSignal).not.toBe(native.ownerSignal);
              expect(current.ownerSignal.aborted).toBe(false);
              expect(await current.meta.findDocumentsById(['down|1'], false)).toEqual([]);
              await expect(native.meta.findDocumentsById(['down|1'], false)).rejects.toBe(native.ownerSignal.reason);
              await alias.set('alice', { afterMaintenance: true });
              expect(await alias.get('alice')).toMatchObject({ afterMaintenance: true });
            }
            await alias.close();
            if (stopPoint !== 'session') provider.setToken(jwt('B'));
            expect(await provider.getToken()).toBe(jwt('B'));
            await expect(alias.get('alice')).rejects.toMatchObject({ code: stopPoint === 'session' ? 'AUTH_SESSION_CHANGED' : 'ReplicaStorageClosed' });
          } else {
            if (outcome !== 'cleanup-error') await expect(runtime.close()).rejects.toBe(fault);
            const result = await stopResult;
            const closeError = 'error' in result ? result.error : undefined;
            if (outcome === 'cleanup-error') expect(closeError).toMatchObject({ code: 'ReplicaStorageCleanupFailed', cause: fault, cleanupErrors: [fault] });
            else expect(closeError).toBe(fault);
            if (aliasCloseResult) expect(await aliasCloseResult).toEqual({ error: fault });
            expect(maintenanceEntered).toBe(false);
            if (stopPoint === 'session') expect(provider.isAuthenticated()).toBe(false);
          }
          expect(sourceCalls).toBe(0);
        } finally {
          gate.resolve();
          await Promise.allSettled([runtime.close(), alias.close(), session.close()]);
          // The injected runtime failure intentionally blocks alias cleanup. Close
          // the actual collection owner after checking that failure remains cached.
          try {
            await database.close();
            for (const collection of collections) expect(collection.closed).toBe(true);
          } finally { await Promise.allSettled(raw.map(instance => instance.close())); }
        }
      }, 5000);
    }
  }

  for (const stopPoint of ['alias-close', 'maintenance'] as const) {
    test(`${stopPoint} fences a queued native write and a replacement runtime can resume`, async () => {
      const jwt = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'A' }))}.sig`.replace(/=/g, '');
      const session = await createReplicaSession(new DefaultTokenProvider({ token: jwt }));
      const options = { session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'people',
        source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage: getRxStorageDexie({ indexedDB, IDBKeyRange }) };
      let alias = await openAliasStorage(options);
      const native = await alias.native(await alias.captureScope());
      const queued = deferred(), gate = deferred();
      const fork = new Proxy(native.fork, { get(target, property) {
        if (property === 'bulkWrite') return async (rows: any[], context: string) => {
          queued.resolve(); await gate.promise;
          return target.bulkWrite(rows, context);
        };
        const value = Reflect.get(target, property, target);
        return typeof value === 'function' ? value.bind(target) : value;
      } });
      const source: Pick<ReplicationOptions<ReplicaRecord, Cursor>, 'readSource' | 'writeRemote' | 'isControlDocument'> = {
        readSource: async () => ({ checkpoint: { sequence: 1 }, complete: true, documents: [{
          key: 'c:progress', kind: 'c', checkpoint: { sequence: 1 }, generation: 'g1', phase: 'live',
          bootstrapComplete: true, partialDelivery: false, _deleted: false, _attachments: {},
        }] }),
        writeRemote: async () => { throw new Error('Control documents must not upload'); },
        isControlDocument: document => document.kind !== 'd',
      };
      const runtime = createReplicationRuntime({ ...native, ...source, forkInstance: fork, metaInstance: native.meta,
        hashFunction: defaultHashSha256, conflictHandler: defaultConflictHandler });
      alias.registerNative({ invalidate() {}, close: () => runtime.close() });
      let restarted: ReplicationRuntime | undefined;
      try {
        await queued.promise;
        const stopping = stopPoint === 'alias-close' ? alias.close() : alias.withMaintenance(async () => {});
        await until(() => runtime.stopped);
        gate.resolve(); await stopping;
        expect(runtime.error).toBeUndefined();
        if (stopPoint === 'alias-close') alias = await openAliasStorage(options);
        const next = await alias.native(await alias.captureScope());
        expect(await next.fork.findDocumentsById(['c:progress'], false)).toEqual([]);
        restarted = createReplicationRuntime({ ...next, ...source, forkInstance: next.fork, metaInstance: next.meta,
          hashFunction: defaultHashSha256, conflictHandler: defaultConflictHandler });
        await restarted.waitForIdle();
        expect(restarted.ready).toBe(true);
        expect(await next.fork.findDocumentsById(['c:progress'], false)).toHaveLength(1);
      } finally {
        gate.resolve(); await runtime.close(); await restarted?.close(); await alias.close(); await session.close();
      }
    }, 10000);

    test(`${stopPoint} aborts native work waiting for another owner's Web Lock`, async () => {
      const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
      const provider = new DefaultTokenProvider({ token: jwt('A') });
      const session = await createReplicaSession(provider);
      const manager = createTestLockManager();
      const queued = deferred();
      let observing = false;
      const locks = new Proxy(manager, { get(target, property) {
        if (property === 'request') return (...args: any[]) => {
          if (observing && args[0].endsWith(':alias') && args[1].mode === 'shared') queued.resolve();
          return (target.request as any)(...args);
        };
        return Reflect.get(target, property, target);
      } });
      const alias = await openAliasStorage({ session, endpoint: 'https://example.test', database: 'app',
        name: crypto.randomUUID(), alias: 'people', source: { collection: 'users', filters: [] },
        lockManager: locks, storage: getRxStorageDexie({ indexedDB, IDBKeyRange }) });
      const native = await alias.native(await alias.captureScope());
      const gate = deferred();
      const acquired = deferred();
      const held = manager.request(`syntrix:${alias.namespace}:alias`, { mode: 'exclusive' }, async () => {
        acquired.resolve(); await gate.promise;
      });
      await acquired.promise;
      observing = true;
      const runtime = createReplicationRuntime<ReplicaRecord, Cursor>({ ...native, forkInstance: native.fork, metaInstance: native.meta,
        hashFunction: defaultHashSha256, conflictHandler: defaultConflictHandler,
        readSource: async () => { throw new Error('Canceled startup must not reach source'); },
        writeRemote: async () => { throw new Error('Canceled startup must not push'); },
      });
      alias.registerNative({ invalidate() {}, close: () => runtime.close() });
      let callbackEntered = false;
      try {
        await queued.promise;
        const stopping = stopPoint === 'alias-close' ? alias.close() : alias.withMaintenance(async access => {
          callbackEntered = true;
          expect(access.ownerSignal.aborted).toBe(false);
        });
        await until(() => runtime.stopped);
        await runtime.close();
        expect(runtime.error).toBeUndefined();
        expect(callbackEntered).toBe(false);
        if (stopPoint === 'alias-close') await stopping;
        gate.resolve(); await held; await stopping;
        expect(callbackEntered).toBe(stopPoint === 'maintenance');
      } finally {
        gate.resolve(); await held;
        await alias.close(); await session.close();
      }
    }, 10000);
  }

  test('an already canceled owner cannot create a native runtime', async () => {
    const f = await fixture();
    const owner = new AbortController();
    const reason = new AuthSessionChangedError();
    owner.abort(reason);
    try {
      expect(() => createReplicationRuntime({ ...f.config, ownerSignal: owner.signal })).toThrow(reason);
      expect(await f.meta.findDocumentsById(['down|1', 'up|1'], true)).toEqual([]);
    } finally { await f.close(); }
  });

  test('a canceled owner drains its checkpoint queue without admitting a later checkpoint hook', async () => {
    const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
    const provider = new DefaultTokenProvider({ token: jwt('A') });
    const session = await createReplicaSession(provider);
    const f = await fixture();
    const gate = deferred();
    let entered = false;
    const write = f.meta.bulkWrite.bind(f.meta);
    f.meta.bulkWrite = (rows, context) => session.track(async () => {
      const result = await write(rows, context);
      if (context === 'replication-set-checkpoint') { entered = true; await gate.promise; }
      return result;
    });
    f.config.ownerSignal = session.signal;
    const runtime = f.start();
    session.register({ invalidate() {}, close: () => runtime.close() });
    try {
      await until(() => entered);
      provider.setToken(jwt('B'));
      gate.resolve();
      await runtime.close();
      expect(await provider.getToken()).toBe(jwt('B'));
      expect(f.committed).toEqual([]);
      expect(runtime.error).toBeUndefined();
    } finally { gate.resolve(); await Promise.allSettled([session.close(), f.close()]); }
  });

  for (const point of ['before-interceptor', 'awaiting-token', 'in-flight'] as const) {
    test(`account replacement drains native HTTP work canceled ${point}`, async () => {
      const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
      const provider = new DefaultTokenProvider({ token: jwt('A') });
      const session = await createReplicaSession(provider);
      const f = await fixture();
      const tokenGate = deferred();
      const getToken = provider.getToken.bind(provider);
      let awaitingToken = false;
      if (point === 'awaiting-token') provider.getToken = async () => {
        awaitingToken = true;
        await tokenGate.promise;
        return getToken();
      };
      let sent = 0;
      let started = false;
      const http = axios.create({ adapter: async config => {
        sent++;
        if (point !== 'in-flight') throw new Error('Obsolete request reached transport');
        return new Promise((_resolve, reject) => {
          config.signal!.addEventListener!('abort', () => reject(new axios.CanceledError('canceled', config)), { once: true });
        });
      } });
      setupAuthInterceptor(http, provider);
      f.config.ownerSignal = session.signal;
      f.config.readSource = (_checkpoint, _limit, signal) => session.track(async () => {
        const request = http.get('/replication/pull', { signal });
        started = true;
        if (point === 'before-interceptor') provider.setToken(jwt('B'));
        await request;
        throw new Error('Obsolete response was admitted');
      });
      const runtime = f.start();
      session.register({ invalidate() {}, close: () => runtime.close() });
      try {
        await until(() => point === 'before-interceptor' ? started : point === 'awaiting-token' ? awaitingToken : sent === 1);
        if (point !== 'before-interceptor') provider.setToken(jwt('B'));
        await runtime.close();
        await session.close();
        expect(await getToken()).toBe(jwt('B'));
        expect(runtime.error).toBeUndefined();
        expect(f.errors).toEqual([]);
        expect(sent).toBe(point === 'in-flight' ? 1 : 0);
      } finally {
        tokenGate.resolve();
        await Promise.allSettled([session.close(), f.close()]);
      }
    }, 3000);
  }

  test('short and empty progress pages gate uploads until the durable terminal hook finishes', async () => {
    const f = await fixture();
    const hookGate = deferred();
    let terminalHook = false;
    f.config.readSource = async checkpoint => {
      const sequence = (checkpoint?.sequence ?? 0) + 1;
      return {
        documents: sequence === 2 ? [] : [{ id: `remote${sequence}`, value: sequence, _deleted: false }],
        checkpoint: { sequence }, complete: sequence === 3,
      };
    };
    f.config.onCheckpoint = async (checkpoint, status) => {
      const stored = await f.meta.findDocumentsById(['down|1'], true);
      expect(stored[0].checkpointData).toEqual({ source: checkpoint });
      f.committed.push(checkpoint.sequence);
      if (status.complete) { terminalHook = true; await hookGate.promise; }
    };
    await f.seed(['pending']);
    const runtime = f.start();
    try {
      await until(() => terminalHook);
      expect(runtime.ready).toBe(false);
      expect(f.pushed).toEqual([]);
      expect(await f.meta.findDocumentsById(['up|1'], true)).toEqual([]);
      hookGate.resolve();
      await until(() => f.pushed.includes('pending'));
      await runtime.waitForIdle();
      expect(f.committed).toEqual([1, 2, 3]);
      expect(runtime.ready).toBe(true);
      expect(f.errors).toEqual([]);
    } finally { hookGate.resolve(); await f.close(); }
  });

  test('restarts gate pending durable edits behind a fresh terminal confirmation even at an unchanged checkpoint', async () => {
    const f = await fixture();
    try {
      const first = f.start();
      await until(() => first.ready);
      await first.waitForIdle();
      await first.close();
      await f.seed(['offline']);
      const gate = deferred();
      let entered = false;
      delete f.config.createProgressDocument;
      f.config.readSource = async checkpoint => {
        entered = true;
        await gate.promise;
        return { documents: [], checkpoint: checkpoint!, complete: true };
      };
      const second = f.start();
      await until(() => entered);
      expect(second.ready).toBe(false);
      expect(f.pushed).toEqual([]);
      gate.resolve();
      await until(() => f.pushed.includes('offline'));
      await second.waitForIdle();
      expect(f.committed).toEqual([1, 1]);
      expect(f.errors).toEqual([]);
    } finally { await f.close(); }
  });

  for (const pullBatchSize of [1, 2]) {
    test(`source checkpoints replace removed fields across ${pullBatchSize === 1 ? 'full' : 'short'} pages and restart`, async () => {
      const f = await fixture();
      type SourceCursor = Record<string, string>;
      const checkpoints: SourceCursor[] = [
        { phase: 'scan', after: 'alice' },
        { phase: 'live', token: 'resume-2' },
        {},
      ];
      const received: (SourceCursor | undefined)[] = [];
      const committed: SourceCursor[] = [];
      const runtimes: ReplicationRuntime[] = [];
      const config: ReplicationOptions<Row, SourceCursor> = {
        ...f.config,
        pullBatchSize,
        createProgressDocument: undefined,
        readSource: async checkpoint => {
          const index = received.length;
          received.push(checkpoint);
          expect(checkpoint).toEqual(index === 0 ? undefined : checkpoints[index - 1]);
          return {
            documents: [{ id: `replacement-${index}`, value: index, _deleted: false }],
            checkpoint: checkpoints[index], complete: index === checkpoints.length - 1,
          };
        },
        onCheckpoint: async checkpoint => {
          const stored = await f.meta.findDocumentsById(['down|1'], true);
          expect(stored[0].checkpointData).toEqual({ source: checkpoint });
          committed.push(checkpoint);
        },
      };
      try {
        const first = createReplicationRuntime(config);
        runtimes.push(first);
        await until(() => first.ready || first.stopped);
        await first.waitForIdle();
        expect(received).toEqual([undefined, checkpoints[0], checkpoints[1]]);
        expect(committed).toEqual(checkpoints);
        await first.close();

        config.readSource = async checkpoint => {
          received.push(checkpoint);
          expect(checkpoint).toEqual({});
          return { documents: [], checkpoint: checkpoint!, complete: true };
        };
        const restarted = createReplicationRuntime(config);
        runtimes.push(restarted);
        await until(() => restarted.ready || restarted.stopped);
        await restarted.waitForIdle();
        expect(restarted.ready).toBe(true);
        expect(received).toEqual([undefined, checkpoints[0], checkpoints[1], {}]);
        expect(committed).toEqual([...checkpoints, {}]);
        expect(f.errors).toEqual([]);
      } finally {
        await Promise.allSettled(runtimes.map(runtime => runtime.close()));
        await f.close();
      }
    });
  }

  test('notification bursts retain a dirty hint, preserve the real feed and serialize remote writes', async () => {
    const f = await fixture();
    const gate = deferred();
    let active = 0;
    let peak = 0;
    let entered = false;
    let sourceReads = 0;
    let realEvents = 0;
    let acknowledged = 0;
    let scannedRows = 0;
    let peakOutstanding = 0;
    const realQuery = f.fork.query.bind(f.fork);
    f.fork.query = async query => {
      expect(query.query.limit).toBeLessThanOrEqual(4);
      const result = await realQuery(query);
      scannedRows += result.documents.length;
      peakOutstanding = Math.max(peakOutstanding, scannedRows - acknowledged);
      return result;
    };
    f.config.readSource = async checkpoint => { sourceReads++; return { documents: [], checkpoint: checkpoint ?? { sequence: 1 }, complete: true }; };
    f.config.writeRemote = async rows => {
      entered = true;
      peak = Math.max(peak, ++active);
      await gate.promise;
      f.pushed.push(...rows.map(row => row.newDocumentState.id));
      acknowledged += rows.length;
      active--;
      return [];
    };
    // A wrapped storage may expose an underlying instance; our protocol boundary
    // must remain in force even in that case.
    const outer = Object.create(f.fork) as typeof f.fork;
    Object.defineProperty(outer, 'underlyingPersistentStorage', { value: f.fork });
    f.config.forkInstance = outer;
    expect(getUnderlyingPersistentStorage(outer)).toBe(f.fork);
    const sub = f.fork.changeStream().subscribe(() => realEvents++);
    await f.seed(['first']);
    const runtime = f.start();
    try {
      await until(() => entered);
      for (let page = 0; page < 20; page++) {
        await f.seed(Array.from({ length: 50 }, (_, i) => `burst-${page}-${i}`));
        runtime.requestResync();
      }
      expect(realEvents).toBeGreaterThanOrEqual(21);
      expect(sourceReads).toBe(1);
      expect(peak).toBe(1);
      gate.resolve();
      await until(() => f.pushed.length === 1001);
      await runtime.waitForIdle();
      expect(new Set(f.pushed).size).toBe(1001);
      expect(peak).toBe(1);
      expect(sourceReads).toBe(2);
      expect(scannedRows).toBeGreaterThanOrEqual(1001);
      expect(peakOutstanding).toBeLessThanOrEqual(201);
      expect(f.errors).toEqual([]);
    } finally { gate.resolve(); sub.unsubscribe(); await f.close(); }
  });

  for (const point of ['document', 'metadata', 'checkpoint', 'hook', 'read', 'write', 'control-conflict'] as const) {
    test(`${point} failure cancels once, never exposes readiness, and drains`, async () => {
      const f = await fixture();
      const error = new Error(point);
      let runtime: ReplicationRuntime;
      f.config.onError = value => {
        expect(runtime.stopped).toBe(true);
        expect(runtime.ready).toBe(false);
        f.errors.push(value);
      };
      if (point === 'read') f.config.readSource = async () => { throw error; };
      if (point === 'hook') f.config.onCheckpoint = async () => { throw error; };
      if (point === 'write') {
        await f.seed(['local']);
        f.config.writeRemote = async () => { throw error; };
      }
      const writeFork = f.fork.bulkWrite.bind(f.fork);
      f.fork.bulkWrite = async (rows, context) => {
        if (point === 'document' || point === 'control-conflict') {
          return { error: [{ status: point === 'control-conflict' ? 409 : 500, documentId: rows[0].document.id, writeRow: rows[0] }] } as any;
        }
        return writeFork(rows, context);
      };
      const writeMeta = f.meta.bulkWrite.bind(f.meta);
      f.meta.bulkWrite = async (rows, context) => {
        if ((point === 'metadata' && context === 'replication-down-write-meta') || (point === 'checkpoint' && context === 'replication-set-checkpoint')) throw error;
        return writeMeta(rows, context);
      };
      runtime = f.start();
      try {
        await until(() => runtime.stopped);
        await expect(runtime.waitForIdle()).rejects.toBeDefined();
        expect(f.errors).toHaveLength(1);
        expect(() => runtime.requestResync()).toThrow();
        if (point !== 'write') expect(f.committed).toEqual([]);
        await expect(runtime.close()).rejects.toBeDefined();
      } finally { await f.close(); }
    });
  }

  test('close aborts the handler and waits for late completion before storage can be reused', async () => {
    const f = await fixture();
    const gate = deferred();
    let signal: AbortSignal | undefined;
    f.config.readSource = async (_checkpoint, _limit, requestSignal) => {
      signal = requestSignal;
      await gate.promise;
      return { documents: [{ id: 'late', value: 1, _deleted: false }], checkpoint: { sequence: 1 }, complete: true };
    };
    const runtime = f.start();
    try {
      await until(() => signal !== undefined);
      let closed = false;
      const closing = runtime.close().then(() => { closed = true; });
      await tick();
      expect(signal!.aborted).toBe(true);
      expect(closed).toBe(false);
      gate.resolve();
      await closing;
      expect(await f.fork.findDocumentsById(['late'], true)).toEqual([]);
      expect(f.errors).toEqual([]);
    } finally { gate.resolve(); await f.close(); }
  });

  test('invalid source pages fail before changing checkpoint', async () => {
    const f = await fixture();
    delete f.config.createProgressDocument;
    const runtime = f.start();
    try {
      await until(() => runtime.stopped);
      await expect(runtime.waitForIdle()).rejects.toThrow('progress document');
      expect(await f.meta.findDocumentsById(['down|1'], true)).toEqual([]);
    } finally { await f.close(); }
  });

  for (const operation of ['startup-read', 'fork-write', 'hook'] as const) {
    test(`close waits for an admitted ${operation} and fences every following write`, async () => {
      const f = await fixture();
      const gate = deferred();
      let entered = false;
      let metadataWrites = 0;
      const writeMeta = f.meta.bulkWrite.bind(f.meta);
      f.meta.bulkWrite = async (rows, context) => {
        metadataWrites++;
        return writeMeta(rows, context);
      };
      if (operation === 'startup-read') {
        const read = f.meta.findDocumentsById.bind(f.meta);
        f.meta.findDocumentsById = async (ids, deleted) => {
          entered = true;
          await gate.promise;
          return read(ids, deleted);
        };
      } else if (operation === 'fork-write') {
        const write = f.fork.bulkWrite.bind(f.fork);
        f.fork.bulkWrite = async (rows, context) => {
          entered = true;
          await gate.promise;
          return write(rows, context);
        };
      } else {
        f.config.onCheckpoint = async () => { entered = true; await gate.promise; };
      }
      const runtime = f.start();
      try {
        await until(() => entered);
        let closed = false;
        const writesBeforeClose = metadataWrites;
        const closing = runtime.close().then(() => { closed = true; });
        await tick();
        expect(closed).toBe(false);
        gate.resolve();
        await closing;
        expect(metadataWrites).toBe(writesBeforeClose);
        expect(runtime.ready).toBe(false);
        expect(f.errors).toEqual([]);
      } finally { gate.resolve(); await f.close(); }
    });
  }

  test('full terminal source page commits once and stops without another network request', async () => {
    const f = await fixture();
    f.config.pullBatchSize = 1;
    let reads = 0;
    f.config.readSource = async () => {
      reads++;
      return { documents: [{ id: 'terminal', value: 1, _deleted: false }], checkpoint: { sequence: 1 }, complete: true };
    };
    const runtime = f.start();
    try {
      await until(() => runtime.ready);
      await runtime.waitForIdle();
      expect(reads).toBe(1);
      expect(f.committed).toEqual([1]);
      expect(f.errors).toEqual([]);
    } finally { await f.close(); }
  });

  for (const fault of ['count', 'bytes', 'checkpoint', 'progress-classification'] as const) {
    test(`rejects a source ${fault} contract violation before document or checkpoint writes`, async () => {
      const f = await fixture();
      let writes = 0;
      const writeFork = f.fork.bulkWrite.bind(f.fork);
      f.fork.bulkWrite = async (rows, context) => { writes++; return writeFork(rows, context); };
      if (fault === 'count') {
        f.config.pullBatchSize = 1;
        f.config.readSource = async () => ({ documents: [{ id: 'a', value: 1, _deleted: false }, { id: 'b', value: 1, _deleted: false }], checkpoint: { sequence: 1 }, complete: true });
      } else if (fault === 'bytes') {
        f.config.readBounds = { maxDocumentBytes: 20 };
      } else if (fault === 'checkpoint') {
        f.config.readSource = async () => ({ documents: [], checkpoint: null as unknown as Cursor, complete: true });
      } else {
        f.config.createProgressDocument = () => ({ id: 'business', value: 1, _deleted: false });
      }
      const runtime = f.start();
      try {
        await until(() => runtime.stopped);
        await expect(runtime.waitForIdle()).rejects.toBeDefined();
        expect(writes).toBe(0);
        expect(await f.meta.findDocumentsById(['down|1'], true)).toEqual([]);
        expect(f.errors).toHaveLength(1);
      } finally { await f.close(); }
    });
  }

  test('a throwing diagnostic observer is retained in the failure chain without a detached rejection', async () => {
    const f = await fixture();
    const original = new Error('source failed');
    const observer = new Error('observer failed');
    f.config.readSource = async () => { throw original; };
    f.config.onError = () => { throw observer; };
    const runtime = f.start();
    try {
      await until(() => runtime.stopped);
      await expect(runtime.waitForIdle()).rejects.toMatchObject({ cause: original, observerError: observer });
      expect(runtime.error).toMatchObject({ cause: original, observerError: observer });
    } finally { await f.close(); }
  });

  test('a storage rejection during intentional close is returned after drain', async () => {
    const f = await fixture();
    const gate = deferred();
    const error = new Error('storage failed while closing');
    let entered = false;
    f.fork.bulkWrite = async () => {
      entered = true;
      await gate.promise;
      throw error;
    };
    const runtime = f.start();
    try {
      await until(() => entered);
      const closing = runtime.close();
      gate.resolve();
      await expect(closing).rejects.toBe(error);
      expect(runtime.error).toBe(error);
    } finally { gate.resolve(); await f.close(); }
  });
});
