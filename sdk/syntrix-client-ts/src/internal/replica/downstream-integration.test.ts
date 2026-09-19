import { describe, expect, test } from 'bun:test';
import axios from 'axios';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { firstValueFrom, filter, timeout } from 'rxjs';
import { encodeQueryValue } from '../../api/value.js';
import { setupAuthInterceptor } from '../auth/interceptor.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaDownstream, type CoordinatorEnvironment, type ReplicaDownstream } from './coordinator.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { createReplicaQueryClient } from './query.js';
import { freezeSourceDefinition } from './records.js';
import { createReplicaSession } from './session.js';
import { createReplicaHttpSource } from './source.js';
import { openAliasStorage } from './storage.js';
import type { ReplicaDocument } from './storage-types.js';

const identity = { protocolVersion: 1, databaseIdentity: '0123456789abcdef', sourceHash: 'a'.repeat(64),
  generationId: '01234567-89ab-cdef-0123-456789abcdef' };
const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub }))}.sig`.replace(/=/g, '');
const upsert = (id: string, value: unknown, version = 1n) => ({ type: 'upsert', document: encodeQueryValue({
  id, collection: 'users', version, createdAt: 1n, updatedAt: version, value,
} as Parameters<typeof encodeQueryValue>[0]) });
const events = (checkpoint: string, entries: unknown[] = [], extra = {}) => ({ ...identity, mode: 'events', events: entries,
  checkpoint, phase: 'live', caughtUp: true, bootstrapComplete: true, ...extra });
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};
const immediate: CoordinatorEnvironment = {
  leadership: () => ({ wait: async signal => signal.throwIfAborted(), close: async () => {} }),
  now: Date.now, random: () => 1, set: (callback, delay) => setTimeout(callback, delay), clear: clearTimeout,
};
const waitState = (owner: ReplicaDownstream, state: 'idle' | 'blocked' | 'closed') => firstValueFrom(owner.status$.pipe(
  filter(status => status.state === state), timeout(10_000),
));
type RequestRecord = { body: Record<string, any>; identity: string | null; authorization: string | null };
type Handler = (request: RequestRecord) => Response | Promise<Response>;
const fixture = async (limit?: number) => {
  const requests: RequestRecord[] = [];
  const handlers: Handler[] = [];
  const server = Bun.serve({ hostname: '127.0.0.1', port: 0, async fetch(request) {
    const record = { body: await request.json() as Record<string, any>,
      identity: request.headers.get('x-syntrix-expected-database-identity'), authorization: request.headers.get('authorization') };
    requests.push(record);
    const handler = handlers.shift();
    return handler ? handler(record) : Response.json({ code: 'UNEXPECTED_READ', message: 'Unexpected source request' }, { status: 400 });
  } });
  const provider = new DefaultTokenProvider({ token: jwt('alice') });
  const session = await createReplicaSession(provider);
  const sourceDefinition = { collection: 'users', filters: [], ...(limit === undefined ? {} : { limit }) };
  const definition = freezeSourceDefinition(sourceDefinition);
  const options = { session, endpoint: server.url.origin, database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: sourceDefinition, lockManager: createTestLockManager(), storage: getRxStorageDexie({ indexedDB, IDBKeyRange }) };
  const storage = await openAliasStorage(options);
  const database = await storage.withMaintenance(async access => access.backend.database);
  const http = axios.create({ baseURL: server.url.origin });
  setupAuthInterceptor(http, provider);
  const source = createReplicaHttpSource({ axios: http, provider, database: 'app', definition });
  const query = createReplicaQueryClient(storage);
  let owner: ReplicaDownstream | undefined;
  const start = () => owner = createReplicaDownstream({ storage, source,
    options: { pollIntervalMs: 60_000, retryBaseMs: 10, retryMaxMs: 10 } }, immediate);
  const refresh = async () => {
    const settled = firstValueFrom(owner!.status$.pipe(filter(status => status.state === 'syncing'), timeout(10_000)))
      .then(() => waitState(owner!, 'idle'));
    owner!.refresh();
    return settled;
  };
  const close = async (expectedFailure?: unknown) => {
    if (expectedFailure) {
      const collections = Object.values(database.collections);
      try {
        await expect(owner!.close()).rejects.toBe(expectedFailure);
        await query.close();
        await expect(storage.close()).rejects.toBe(expectedFailure);
      } finally {
        try {
          await database.close();
          for (const collection of collections) expect(collection.closed).toBe(true);
        } finally { server.stop(true); }
      }
      return;
    }
    try { await owner?.close(); }
    finally { try { await query.close(); } finally { try { await storage.close(); } finally { server.stop(true); } } }
  };
  return { requests, handlers, storage, options, query, provider, source, start, refresh, close };
};
const window = (request: RequestRecord, ids: string[], extra = {}) => ({ ...identity, mode: 'replace',
  generationId: crypto.randomUUID(), requestId: request.body.requestId, complete: true,
  effectiveOrder: [{ field: 'id', direction: 'asc' }], documents: ids.map(id => upsert(id, id).document), ...extra });
const watchResults = (query: ReturnType<typeof createReplicaQueryClient>) => {
  const values: string[][] = [];
  const listeners = new Set<() => void>();
  let failure: unknown;
  const stop = query.watch({}, (rows: ReplicaDocument[]) => { values.push(rows.map(row => row.id)); for (const notify of listeners) notify(); },
    error => { failure = error; for (const notify of listeners) notify(); });
  const wait = (matches: (ids: string[]) => boolean) => new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => { listeners.delete(check); reject(new Error('Query result deadline exceeded')); }, 10_000);
    const check = () => {
      if (failure || matches(values[values.length - 1] ?? [])) {
        clearTimeout(timer); listeners.delete(check);
        if (failure) reject(failure); else resolve();
      }
    };
    listeners.add(check); check();
  });
  return { values, wait, stop };
};

describe('HTTP source through native Dexie replication and query views', () => {
  test('empty progress, retry and reopen preserve cursor, ordered same-ID events and exact values', async () => {
    const env = await fixture();
    try {
      env.handlers.push(() => Response.json(events('prefix', [], { phase: 'scan', caughtUp: false, bootstrapComplete: false })),
        () => Response.json({ code: 'REPLICATION_UNAVAILABLE', message: 'Busy' }, { status: 503 }),
        () => Response.json(events('head', [upsert('alice', 'old', 99n), { type: 'delete', id: 'alice' },
          upsert('alice', 9007199254740993n, 2n), { type: 'leave', id: 'unknown' }])));
      const owner = env.start();
      await waitState(owner, 'idle');
      expect(env.requests.map(request => request.body.checkpoint)).toEqual([null, 'prefix', 'prefix']);
      expect(env.requests.map(request => request.identity)).toEqual([null, identity.databaseIdentity, identity.databaseIdentity]);
      expect(env.requests.every(request => request.authorization === `Bearer ${jwt('alice')}`)).toBe(true);
      expect(await env.query.get()).toMatchObject([{ id: 'alice', value: 9007199254740993n, version: 2n }]);
      expect(owner.snapshot.ready).toBe(true);
      await owner.close();
      env.handlers.push(() => Response.json(events('resumed', [])));
      await waitState(env.start(), 'idle');
      expect(env.requests[env.requests.length - 1]?.body.checkpoint).toBe('head');
      expect(await env.query.get()).toMatchObject([{ id: 'alice', value: 9007199254740993n }]);
    } finally { await env.close(); }
  }, 20_000);

  test('multi-batch window activates atomically and manifest-only activation updates a live query', async () => {
    const env = await fixture(240);
    let watcher: ReturnType<typeof watchResults> | undefined;
    try {
      env.handlers.push(request => Response.json(window(request, ['old'])));
      await waitState(env.start(), 'idle');
      watcher = watchResults(env.query); await watcher.wait(ids => ids.join() === 'old');
      const generation = (await env.storage.readManifest()).activeSourceGeneration;
      const observed: (string | null)[] = [];
      let activated = false;
      const original = env.storage.withReplicationAccess;
      env.storage.withReplicationAccess = (scope, consume) => original(scope, access => consume(new Proxy(access, {
        get(target, key) {
          if (key === 'withDocument') return async (...args: Parameters<typeof target.withDocument>) => {
            if (!activated) observed.push(target.manifest.activeSourceGeneration);
            return target.withDocument(...args);
          };
          if (key === 'writeManifest') return async (...args: Parameters<typeof target.writeManifest>) => {
            if (args[0].activeSourceGeneration !== generation) activated = true;
            return target.writeManifest(...args);
          };
          return Reflect.get(target, key, target);
        },
      })));
      const next = Array.from({ length: 205 }, (_, i) => `new-${String(i).padStart(3, '0')}`);
      env.handlers.push(request => Response.json(window(request, next)));
      await env.refresh(); await watcher.wait(ids => ids.length === next.length);
      expect(watcher.values.every(ids => ids.join() === 'old' || ids.join() === next.join())).toBe(true);
      expect(observed.length).toBeGreaterThan(200);
      expect(observed.every(value => value === generation)).toBe(true);
      expect((await env.storage.readManifest()).activeSourceGeneration).not.toBe(generation);
      expect(env.requests).toHaveLength(2);
      env.handlers.push(request => Response.json(window(request, [])));
      await env.refresh(); await watcher.wait(ids => ids.length === 0);
      expect(await env.query.get()).toEqual([]);
    } finally { watcher?.stop(); await env.close(); }
  }, 30_000);

  test('leave and delete retain pending desired content while source metadata advances', async () => {
    const env = await fixture();
    try {
      await env.storage.set('alice', { value: 'offline' });
      env.handlers.push(() => Response.json(events('one', [upsert('alice', 'remote', 9n), { type: 'leave', id: 'alice' }])));
      await waitState(env.start(), 'idle');
      expect(await env.query.get()).toMatchObject([{ id: 'alice', value: 'offline', version: 9n }]);
      env.handlers.push(() => Response.json(events('two', [{ type: 'delete', id: 'alice' }])));
      await env.refresh();
      expect(await env.query.get()).toMatchObject([{ id: 'alice', value: 'offline', version: 9n }]);
    } finally { await env.close(); }
  }, 20_000);

  test('resync preserves identity header and rejects a reassigned database before applying data', async () => {
    const env = await fixture();
    try {
      env.handlers.push(() => Response.json(events('saved', [upsert('alice', 'stable')])));
      const owner = env.start(); await waitState(owner, 'idle');
      const manifest = await env.storage.readManifest();
      env.handlers.push(() => Response.json({ code: 'RESYNC_REQUIRED', message: 'Expired' }, { status: 409 }),
        () => Response.json(events('wrong', [upsert('alice', 'wrong')], { databaseIdentity: 'fedcba9876543210', generationId: crypto.randomUUID() })));
      const blocked = waitState(owner, 'blocked'); owner.refresh(); await blocked;
      expect(env.requests.map(request => request.body.checkpoint)).toEqual([null, 'saved', null]);
      expect(env.requests.slice(1).map(request => request.identity)).toEqual([identity.databaseIdentity, identity.databaseIdentity]);
      expect(await env.query.get()).toMatchObject([{ id: 'alice', value: 'stable' }]);
      expect((await env.storage.readManifest()).boundDatabaseId).toBe(manifest.boundDatabaseId);
      expect((await env.storage.readManifest()).activeSourceGeneration).toBe(manifest.activeSourceGeneration);
    } finally { await env.close(); }
  }, 20_000);

  test('a malformed later window member cannot partially replace active membership', async () => {
    const env = await fixture(240);
    try {
      env.handlers.push(request => Response.json(window(request, ['old'])));
      const owner = env.start(); await waitState(owner, 'idle');
      const generation = (await env.storage.readManifest()).activeSourceGeneration;
      env.handlers.push(request => {
        const response = window(request, Array.from({ length: 205 }, (_, i) => `member-${i}`));
        response.documents[204] = upsert('bad', 'value').document;
        (response.documents[204] as any).value.version = { type: 'float64', value: 1 };
        return Response.json(response);
      });
      const blocked = waitState(owner, 'blocked'); owner.refresh(); await blocked;
      expect((await env.storage.readManifest()).activeSourceGeneration).toBe(generation);
      expect(await env.query.get()).toMatchObject([{ id: 'old' }]);
      expect((await env.storage.stats()).knownIds).toBe(1);
    } finally { await env.close(); }
  }, 20_000);

  test('a valid HTTP envelope from another generation cannot continue the saved cursor', async () => {
    const env = await fixture();
    try {
      env.handlers.push(() => Response.json(events('saved', [upsert('alice', 'stable')])));
      const owner = env.start(); await waitState(owner, 'idle');
      const before = await env.storage.readManifest();
      const scope = await env.storage.captureScope();
      const checkpoint = await env.storage.withReplicationAccess(scope, access => access.withDownCheckpoint(async value => structuredClone(value)));
      env.handlers.push(() => Response.json(events('wrong', [upsert('alice', 'wrong')], { generationId: crypto.randomUUID() })));
      const blocked = waitState(owner, 'blocked'); owner.refresh(); await blocked;
      expect(env.requests[1].body.checkpoint).toBe('saved');
      expect(owner.snapshot.error).toMatchObject({ message: 'Source generation changed while continuing its cursor' });
      expect(await env.storage.readManifest()).toEqual(before);
      expect(await env.query.get()).toMatchObject([{ id: 'alice', value: 'stable' }]);
      expect(await env.storage.withReplicationAccess(scope, access => access.withDownCheckpoint(async value => structuredClone(value)))).toEqual(checkpoint);
      await owner.close();
      env.provider.setToken(jwt('bob'));
      expect(await env.provider.getToken()).toBe(jwt('bob'));
      expect(env.storage.signal.aborted).toBe(true);
      env.provider.setToken(jwt('alice'));
      const session = await createReplicaSession(env.provider);
      const reopened = await openAliasStorage({ ...env.options, session });
      try {
        expect((await reopened.readManifest()).boundDatabaseId).toBe(before.boundDatabaseId);
        expect(await reopened.get('alice')).toMatchObject({ value: 'stable' });
      } finally { await reopened.close(); }
    } finally { await env.close(); }
  }, 20_000);

  test('durable native checkpoint recovers an interrupted manifest activation before resuming HTTP', async () => {
    const env = await fixture();
    const original = env.storage.withReplicationAccess;
    const failure = new Error('activation disk failure');
    let failActivation = true;
    env.storage.withReplicationAccess = (scope, consume) => original(scope, access => consume(new Proxy(access, {
      get(target, key) {
        if (key === 'writeManifest') return async (...args: Parameters<typeof target.writeManifest>) => {
          if (failActivation && args[0].sourceReady) throw failure;
          return target.writeManifest(...args);
        };
        return Reflect.get(target, key, target);
      },
    })));
    try {
      env.handlers.push(() => Response.json(events('durable', [upsert('alice', 9007199254740993n)])));
      const owner = env.start(); await waitState(owner, 'blocked');
      expect(owner.snapshot.error).toMatchObject({ message: 'activation disk failure' });
      expect((await env.storage.readManifest()).activeSourceGeneration).toBeNull();
      const scope = await env.storage.captureScope();
      const checkpoint = await original(scope, access => access.withDownCheckpoint(async value => structuredClone(value)));
      expect(checkpoint).toMatchObject({ source: { sourceCursor: 'durable', complete: true } });
      await expect(owner.close()).rejects.toBe(failure); failActivation = false;
      const session = await createReplicaSession(new DefaultTokenProvider({ token: jwt('alice') }));
      const recoveredStorage = await openAliasStorage({ ...env.options, session });
      const recoveredQuery = createReplicaQueryClient(recoveredStorage);
      env.handlers.push(async request => {
        expect((await recoveredStorage.readManifest()).sourceReady).toBe(true);
        expect(request.body.checkpoint).toBe('durable');
        return Response.json(events('resumed'));
      });
      const recoveredOwner = createReplicaDownstream({ storage: recoveredStorage, source: env.source,
        options: { pollIntervalMs: 60_000 } }, immediate);
      try {
        await waitState(recoveredOwner, 'idle');
        expect(await recoveredQuery.get()).toMatchObject([{ id: 'alice', value: 9007199254740993n }]);
        expect(env.requests).toHaveLength(2);
      } finally { await recoveredOwner.close(); await recoveredQuery.close(); await recoveredStorage.close(); }
    } finally { failActivation = false; env.storage.withReplicationAccess = original; await env.close(failure); }
  }, 20_000);

  for (const end of ['owner', 'account'] as const) test(`late HTTP response after ${end} cancellation cannot apply`, async () => {
    const env = await fixture();
    const admitted = deferred(); const release = deferred();
    try {
      env.handlers.push(async () => { admitted.resolve(); await release.promise; return Response.json(events('late', [upsert('late', 'stale')])); });
      const owner = env.start(); await admitted.promise;
      if (end === 'owner') await owner.close();
      else { env.provider.setToken(jwt('bob')); await env.provider.getToken(); }
      release.resolve();
      expect(owner.snapshot.state).toBe('closed');
      expect(env.requests).toHaveLength(1);
      if (end === 'owner') {
        expect(await env.storage.get('late')).toBeNull();
        expect((await env.storage.readManifest()).boundDatabaseId).toBeNull();
      } else {
        expect(env.storage.signal.aborted).toBe(true);
        env.provider.setToken(jwt('alice'));
        const session = await createReplicaSession(env.provider);
        const reopened = await openAliasStorage({ ...env.options, session });
        try {
          expect(await reopened.get('late')).toBeNull();
          expect((await reopened.readManifest()).boundDatabaseId).toBeNull();
        } finally { await reopened.close(); }
      }
    } finally { release.resolve(); await env.close(); }
  }, 15_000);
});
