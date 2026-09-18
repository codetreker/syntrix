import { describe, expect, test } from 'bun:test';
import axios from 'axios';
import { getRxStorageMemory } from 'rxdb/plugins/storage-memory';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getUnderlyingPersistentStorage, now, type RxStorage } from 'rxdb';
import { AuthSessionChangedError } from '../../api/errors.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { setupAuthInterceptor } from '../auth/interceptor.js';
import { createLocalSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage } from './storage.js';
import type { LocalRecord } from './storage-types.js';
import {
  createLocalReplicationRuntime,
  defaultConflictHandler,
  defaultHashSha256,
  fillWithDefaultSettings,
  getRxReplicationMetaInstanceSchema,
  type LocalReplicationOptions,
  type LocalReplicationRuntime,
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
  const runtimes: LocalReplicationRuntime[] = [];
  const config: LocalReplicationOptions<Row, Cursor> = {
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
    const runtime = createLocalReplicationRuntime(config);
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

describe('private local replication runtime', () => {
  for (const outcome of ['success', 'storage-error', 'unrelated-session-error', 'cleanup-error'] as const) {
    test(`session cancellation drains a native alias read while preserving ${outcome}`, async () => {
      const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
      const provider = new DefaultTokenProvider({ token: jwt('A') });
      const session = await createLocalSession(provider);
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
      const native = await alias.native(await alias.captureScope());
      const errors: unknown[] = [];
      let sourceCalls = 0;
      armed = true;
      const runtime = createLocalReplicationRuntime<LocalRecord, Cursor>({
        identifier: native.identifier, forkInstance: native.fork, metaInstance: native.meta, ownerSignal: native.ownerSignal,
        hashFunction: defaultHashSha256, conflictHandler: defaultConflictHandler,
        readSource: async () => { sourceCalls++; throw new Error('Canceled startup must not reach source'); },
        writeRemote: async () => { throw new Error('Canceled startup must not push'); },
        onError: error => { errors.push(error); },
      });
      alias.registerNative({ invalidate() {}, close: () => runtime.close() });
      try {
        await until(() => entered);
        provider.setToken(jwt('B'));
        expect(runtime.stopped).toBe(true);
        let tokenSettled = false;
        const token = provider.getToken().then(value => { tokenSettled = true; return value; }, error => { tokenSettled = true; throw error; });
        const tokenResult = token.then(value => ({ value }), error => ({ error }));
        await tick();
        expect(tokenSettled).toBe(false);
        gate.resolve();
        if (outcome === 'success') {
          await runtime.close();
          await alias.close();
          expect(await tokenResult).toEqual({ value: jwt('B') });
          expect(runtime.error).toBeUndefined();
          expect(errors).toEqual([]);
          await expect(alias.get('alice')).rejects.toBe(session.signal.reason);
        } else {
          if (outcome !== 'cleanup-error') await expect(runtime.close()).rejects.toBe(fault);
          const closeError = await alias.close().catch(error => error);
          if (outcome === 'cleanup-error') expect(closeError).toMatchObject({ code: 'LocalStorageCleanupFailed', cause: fault, cleanupErrors: [fault] });
          else expect(closeError).toBe(fault);
          expect(await tokenResult).toEqual({ error: closeError });
          expect(provider.isAuthenticated()).toBe(false);
        }
        expect(sourceCalls).toBe(0);
      } finally {
        gate.resolve();
        await Promise.allSettled([runtime.close(), alias.close(), session.close()]);
        await Promise.allSettled(raw.map(instance => instance.close()));
      }
    }, 5000);
  }

  test('an already canceled owner cannot create a native runtime', async () => {
    const f = await fixture();
    const owner = new AbortController();
    const reason = new AuthSessionChangedError();
    owner.abort(reason);
    try {
      expect(() => createLocalReplicationRuntime({ ...f.config, ownerSignal: owner.signal })).toThrow(reason);
      expect(await f.meta.findDocumentsById(['down|1', 'up|1'], true)).toEqual([]);
    } finally { await f.close(); }
  });

  test('a canceled owner drains its checkpoint queue without admitting a later checkpoint hook', async () => {
    const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
    const provider = new DefaultTokenProvider({ token: jwt('A') });
    const session = await createLocalSession(provider);
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
      const session = await createLocalSession(provider);
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
      const runtimes: LocalReplicationRuntime[] = [];
      const config: LocalReplicationOptions<Row, SourceCursor> = {
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
        const first = createLocalReplicationRuntime(config);
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
        const restarted = createLocalReplicationRuntime(config);
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
      let runtime: LocalReplicationRuntime;
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
