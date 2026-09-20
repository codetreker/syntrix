import { describe, expect, test } from 'bun:test';
import axios from 'axios';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { decodeQueryValue, encodeQueryValue, type QueryValue } from '../../api/value.js';
import { setupAuthInterceptor } from '../auth/interceptor.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaDownstream, type CoordinatorEnvironment, type ReplicaDownstream } from './coordinator.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { decodeBusinessPayload, freezeSourceDefinition } from './records.js';
import { createReplicaSession } from './session.js';
import { createReplicaHttpSource } from './source.js';
import { openAliasStorage } from './storage.js';
import { createReplicaHttpUpstream } from './upstream-transport.js';
import type { RemoteDocument } from './upstream-types.js';

const identity = '0123456789abcdef';
const jwt = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'alice' }))}.sig`.replace(/=/g, '');
const immediate: CoordinatorEnvironment = {
  leadership: () => ({ wait: async signal => signal.throwIfAborted(), close: async () => {} }),
  now: Date.now, random: () => 1, set: (callback, delay) => setTimeout(callback, delay), clear: timer => clearTimeout(timer),
};
const deferred = () => {
  let resolve!: () => void;
  const promise = new Promise<void>(done => { resolve = done; });
  return { promise, resolve };
};
const until = async (predicate: () => boolean | Promise<boolean>) => {
  const deadline = Date.now() + 12_000;
  while (!await predicate()) {
    if (Date.now() > deadline) throw new Error('Upstream integration deadline exceeded');
    await Bun.sleep(5);
  }
};
type Change = { action: 'create' | 'update' | 'delete'; document: Record<string, QueryValue> & { id: string; version?: bigint } };
type RequestRecord = { kind: 'push' | 'query' | 'pull'; identity: string | null; body: Record<string, any>; changes: Change[] };
const remoteDoc = (id: string, value: QueryValue, version = 1n): RemoteDocument => ({ id, collection: 'users', value,
  version, createdAt: 1n, updatedAt: version });
const fixture = async (retryBaseMs = 10) => {
  const remote = new Map<string, RemoteDocument>();
  const requests: RequestRecord[] = [];
  const sourceEvents: unknown[][] = [];
  let currentIdentity = identity;
  let pushHandler: ((record: RequestRecord, apply: () => Response) => Response | Promise<Response>) | undefined;
  let queryHandler: ((record: RequestRecord, read: () => Response) => Response | Promise<Response>) | undefined;
  let sourceRound = 0;
  const server = Bun.serve({ hostname: '127.0.0.1', port: 0, async fetch(request) {
    const kind = new URL(request.url).pathname.endsWith('/push') ? 'push' : new URL(request.url).pathname.endsWith('/query') ? 'query' : 'pull';
    const body = await request.json() as Record<string, any>;
    const record: RequestRecord = { kind, identity: request.headers.get('x-syntrix-expected-database-identity'), body,
      changes: kind === 'push' ? body.changes.map((change: any) => ({ action: change.action, document: decodeQueryValue(change.document) })) : [] };
    requests.push(record);
    if (record.identity !== null && record.identity !== currentIdentity) {
      return Response.json({ code: 'DATABASE_IDENTITY_MISMATCH', message: 'The database was reassigned' }, { status: 409 });
    }
    if (kind === 'pull') {
      const events = sourceEvents.shift() ?? [];
      return Response.json({ protocolVersion: 1, mode: 'events', databaseIdentity: currentIdentity, sourceHash: 'a'.repeat(64),
        generationId: '01234567-89ab-cdef-0123-456789abcdef', checkpoint: `cursor-${++sourceRound}`, phase: 'live', caughtUp: true,
        bootstrapComplete: true, events });
    }
    if (kind === 'query') {
      const read = () => {
        const id = decodeQueryValue(body.filters[0].value) as string;
        const document = remote.get(id);
        return Response.json({ documents: document ? [encodeQueryValue(document)] : [], nextCursor: null,
          effectiveOrder: [{ field: 'id', direction: 'asc' }] });
      };
      return queryHandler ? queryHandler(record, read) : read();
    }
    const apply = () => {
      const conflicts: unknown[] = [];
      for (const [changeIndex, change] of record.changes.entries()) {
        const { id, version, ...payload } = change.document;
        const current = remote.get(id);
        const reason = change.action === 'create' ? current && !current.deleted ? 'already_exists' : null
          : !current ? 'missing' : current.deleted ? 'tombstoned' : current.version !== version ? 'version_mismatch' : null;
        if (reason) { conflicts.push({ changeIndex, id, reason, current: current ? encodeQueryValue(current) : null }); continue; }
        const nextVersion = (current?.version ?? 0n) + 1n;
        remote.set(id, { ...payload, id, collection: 'users', version: nextVersion, createdAt: current?.createdAt ?? 1n,
          updatedAt: nextVersion, ...(change.action === 'delete' ? { deleted: true } : {}) });
      }
      return Response.json({ conflicts });
    };
    return pushHandler ? pushHandler(record, apply) : apply();
  } });
  const provider = new DefaultTokenProvider({ token: jwt });
  const session = await createReplicaSession(provider);
  const definition = { collection: 'users', filters: [] };
  const options = { session, endpoint: server.url.origin, database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: definition, lockManager: createTestLockManager(), storage: getRxStorageDexie({ indexedDB, IDBKeyRange }) };
  const storage = await openAliasStorage(options);
  const database = await storage.withMaintenance(async access => access.backend.database);
  const http = axios.create({ baseURL: server.url.origin }); setupAuthInterceptor(http, provider);
  const source = createReplicaHttpSource({ axios: http, provider, database: 'app', definition: freezeSourceDefinition(definition) });
  const upstream = createReplicaHttpUpstream({ axios: http, provider, database: 'app', collection: 'users' });
  let owner: ReplicaDownstream | undefined;
  const start = () => owner = createReplicaDownstream({ storage, source, upstream,
    options: { pollIntervalMs: 60_000, retryBaseMs, retryMaxMs: retryBaseMs } }, immediate);
  const settle = async (id: string, value: QueryValue) => until(async () => {
    if (owner!.snapshot.state === 'blocked') throw owner!.snapshot.error;
    const inspection = await owner!.inspect({ logicalId: id });
    return inspection.document?.assumed?.existence === 'live' &&
      decodeBusinessPayload(inspection.document.assumed.payload).value === value && !(await storage.readManifest()).dirtyUpstream;
  });
  const seed = (id: string, value: QueryValue, version = 1n) => {
    const document = remoteDoc(id, value, version); remote.set(id, document);
    sourceEvents.push([{ type: 'upsert', document: encodeQueryValue(document) }]);
  };
  const close = async (expectedFailure?: unknown) => {
    try {
      if (expectedFailure) {
        const collections = Object.values(database.collections);
        try {
          await expect(owner!.close()).rejects.toBe(expectedFailure);
          await expect(storage.close()).rejects.toBe(expectedFailure);
        } finally {
          await database.close();
          for (const collection of collections) expect(collection.closed).toBe(true);
        }
      } else { await owner?.close(); await storage.close(); }
    } finally { server.stop(true); }
  };
  return { remote, requests, sourceEvents, storage, options, provider, source, upstream, start, settle, seed, close,
    onPush(handler: typeof pushHandler) { pushHandler = handler; }, onQuery(handler: typeof queryHandler) { queryHandler = handler; },
    reassign() { currentIdentity = 'fedcba9876543210'; },
    pushes: () => requests.filter(request => request.kind === 'push'), queries: () => requests.filter(request => request.kind === 'query'),
    get owner() { return owner!; } };
};

describe('native upstream through HTTP and persistent Dexie storage', () => {
  test('an L1 acknowledgement preserves L2 and conflict-driven version refresh sends the later edit', async () => {
    const env = await fixture(); const accepted = deferred(); const release = deferred();
    try {
      env.seed('alice', 'base'); env.start();
      await until(() => env.owner.snapshot.state === 'idle');
      env.onPush(async (_record, apply) => {
        const response = apply();
        if (env.pushes().length === 1) { accepted.resolve(); await release.promise; }
        return response;
      });
      await env.storage.set('alice', { value: 'L1' }); await accepted.promise;
      await env.storage.set('alice', { value: 'L2' }); release.resolve();
      await env.settle('alice', 'L2');
      expect(env.remote.get('alice')).toMatchObject({ value: 'L2', version: 3n });
      expect(env.pushes().map(request => request.changes[0].document.version)).toEqual([1n, 1n, 2n]);
      expect(env.queries()).toHaveLength(0);
      expect(await env.storage.get('alice')).toMatchObject({ value: 'L2' });
      expect(env.pushes().every(request => request.identity === identity)).toBe(true);
    } finally { release.resolve(); await env.close(); }
  }, 20_000);

  test('accepted create without a server version uses one authoritative preflight before the next update', async () => {
    const env = await fixture();
    try {
      env.start(); await until(() => env.owner.snapshot.state === 'idle');
      await env.storage.set('new', { value: 9007199254740993n }); await env.settle('new', 9007199254740993n);
      expect((await env.owner.inspect({ logicalId: 'new' })).document?.assumed?.wire.version).toBeUndefined();
      await env.storage.set('new', { value: 9007199254740994n }); await env.settle('new', 9007199254740994n);
      expect(env.pushes().map(request => request.changes[0].action)).toEqual(['create', 'update']);
      expect(env.pushes()[1].changes[0].document.version).toBe(1n);
      expect(env.queries()).toHaveLength(1);
      expect(env.queries()[0]).toMatchObject({ identity, body: { filters: [{ field: 'id', op: '==', value: { type: 'string', value: 'new' } }], showDeleted: true } });
      expect(env.remote.get('new')?.value).toBe(9007199254740994n);
    } finally { await env.close(); }
  }, 20_000);

  test('edits after leaving the source continue from the settled native assumed state', async () => {
    const env = await fixture();
    try {
      env.seed('alice', 'base'); env.start(); await until(() => env.owner.snapshot.state === 'idle');
      await env.storage.set('alice', { value: 'L1' }); await env.settle('alice', 'L1');
      env.sourceEvents.push([{ type: 'leave', id: 'alice' }]); env.owner.refresh();
      await until(async () => (await env.storage.get('alice')) === null);
      await env.storage.set('alice', { value: 'L2' }); await env.settle('alice', 'L2');
      await env.storage.set('alice', { value: 'L3' }); await env.settle('alice', 'L3');
      expect(env.pushes().every(request => request.changes.every(change => change.action === 'update'))).toBe(true);
      expect(env.remote.get('alice')?.value).toBe('L3');
      expect(env.queries()).toHaveLength(0);
    } finally { await env.close(); }
  }, 20_000);

  test('metadata-only contention backs off after three CAS attempts and retains the desired edit', async () => {
    const env = await fixture(1000);
    try {
      env.seed('alice', 'base'); env.start(); await until(() => env.owner.snapshot.state === 'idle');
      env.onPush(record => {
        const current = env.remote.get('alice')!;
        current.version++;
        return Response.json({ conflicts: [{ changeIndex: 0, id: 'alice', reason: 'version_mismatch', current: encodeQueryValue(current) }] });
      });
      await env.storage.set('alice', { value: 'desired' });
      await until(() => env.owner.snapshot.state === 'retrying');
      await env.owner.pause();
      expect(env.pushes()).toHaveLength(3);
      expect(env.pushes().map(request => request.changes[0].document.version)).toEqual([1n, 2n, 3n]);
      expect(await env.storage.get('alice')).toMatchObject({ value: 'desired' });
      expect(env.remote.get('alice')?.value).toBe('base');
    } finally { await env.close(); }
  }, 20_000);

  test('database reassignment is rejected by Push without an intervening Pull', async () => {
    const env = await fixture();
    try {
      env.seed('alice', 'base'); env.start(); await until(() => env.owner.snapshot.state === 'idle');
      const pulls = env.requests.filter(request => request.kind === 'pull').length;
      env.reassign(); await env.storage.set('alice', { value: 'must-not-write' });
      await until(() => env.owner.snapshot.state === 'blocked');
      expect(env.pushes()).toHaveLength(1);
      expect(env.pushes()[0].identity).toBe(identity);
      expect(env.requests.filter(request => request.kind === 'pull')).toHaveLength(pulls);
      expect(env.remote.get('alice')?.value).toBe('base');
      expect((await env.storage.readManifest()).boundDatabaseId).toBe(identity);
      expect(await env.storage.get('alice')).toMatchObject({ value: 'must-not-write' });
    } finally { await env.close(); }
  }, 20_000);

  test('an accepted prefix followed by a batch error stays uncertain across pause, resume and reopen', async () => {
    const env = await fixture();
    try {
      // Go's HTML escaping exceeds the protobuf budget while all three rows
      // still fit one native read phase, forcing two wire batches in that phase.
      for (let i = 0; i < 3; i++) await env.storage.set(`id-${i}`, { value: '<'.repeat(1500 * 1024) });
      env.onPush((_record, apply) => env.pushes().length === 1 ? apply()
        : Response.json({ code: 'INTERNAL_ERROR', message: 'Later batch failed' }, { status: 500 }));
      env.start(); await until(() => env.owner.snapshot.state === 'blocked');
      expect(env.pushes()).toHaveLength(2);
      expect(env.remote.size).toBeGreaterThan(0);
      const inspected = await env.owner.inspect();
      expect(inspected.issueId).not.toBeNull();
      expect((await env.storage.readManifest()).dirtyUpstream).not.toBeNull();
      const calls = env.pushes().length;
      env.remote.clear();
      const adoptedId = env.pushes()[0].changes[0].document.id;
      expect(inspected.targets.map(target => target.logicalId)).toContain(adoptedId);
      const adopted = await env.owner.inspect({ logicalId: adoptedId });
      await env.owner.resolve({ kind: 'adopt-server', issueId: adopted.issueId!, logicalId: adoptedId,
        editToken: adopted.document!.desired!.editToken, physicalEpoch: adopted.physicalEpoch });
      expect((await env.owner.inspect({ logicalId: adoptedId })).document).toMatchObject({
        desired: { existence: 'absent' }, assumed: { existence: 'absent' },
      });
      const remaining = await env.owner.inspect();
      expect(remaining.targets.length).toBeGreaterThan(0);
      expect(remaining.targets.some(target => target.logicalId === adoptedId)).toBe(false);
      const other = await env.owner.inspect({ logicalId: remaining.targets[0].logicalId });
      expect(other.document?.desired?.existence).toBe('live');
      await env.owner.pause(); await expect(env.owner.resume()).rejects.toThrow();
      expect(env.pushes()).toHaveLength(calls);
      await env.owner.close();
      const reopened = await openAliasStorage(env.options);
      const next = createReplicaDownstream({ storage: reopened, source: env.source, upstream: env.upstream,
        options: { pollIntervalMs: 60_000 } }, immediate);
      try {
        await until(() => next.snapshot.state === 'blocked');
        await expect(next.resume()).rejects.toThrow();
        expect(env.pushes()).toHaveLength(calls);
        expect(env.remote.size).toBe(0);
        expect((await next.inspect()).issueId).toBe(inspected.issueId);
      } finally { await next.close(); await reopened.close(); }
    } finally { await env.close(); }
  }, 30_000);

  test('recovery rejects stale edits and explicit merge continues with a new authoritative baseline', async () => {
    const env = await fixture(); const readStarted = deferred(); const release = deferred();
    try {
      env.seed('alice', 'base'); env.start(); await until(() => env.owner.snapshot.state === 'idle');
      env.remote.set('alice', remoteDoc('alice', 'foreign', 2n));
      await env.storage.set('alice', { value: 'local' }); await until(() => env.owner.snapshot.state === 'blocked');
      const inspected = await env.owner.inspect({ logicalId: 'alice' });
      env.onQuery(async (_record, read) => { const response = read(); readStarted.resolve(); await release.promise; return response; });
      const resolving = env.owner.resolve({ kind: 'adopt-server', issueId: inspected.issueId!, logicalId: 'alice',
        editToken: inspected.document!.desired!.editToken, physicalEpoch: inspected.physicalEpoch }).catch(error => error);
      await Promise.race([readStarted.promise, resolving.then(result => { throw new Error('Recovery finished before the authoritative read', { cause: result }); })]);
      await env.storage.set('alice', { value: 'newer-local' }); release.resolve();
      expect(await resolving).toMatchObject({ code: 'ReplicaRecoveryStale' });
      expect(await env.storage.get('alice')).toMatchObject({ value: 'newer-local' });
      env.onQuery(undefined);
      const current = await env.owner.inspect({ logicalId: 'alice' });
      await env.owner.resolve({ kind: 'merge-local', issueId: current.issueId!, logicalId: 'alice',
        editToken: current.document!.desired!.editToken, physicalEpoch: current.physicalEpoch, data: { value: 'merged' } });
      expect(env.owner.snapshot.state).toBe('paused');
      expect(env.remote.get('alice')?.value).toBe('foreign');
      await env.owner.resume(); await env.settle('alice', 'merged');
      expect(env.remote.get('alice')).toMatchObject({ value: 'merged', version: 3n });
      expect(env.pushes()[env.pushes().length - 1].changes[0].document.version).toBe(2n);
      expect(env.queries().every(request => request.identity === identity)).toBe(true);
    } finally { release.resolve(); await env.close(); }
  }, 25_000);

  for (const point of ['metadata', 'checkpoint'] as const) test(`accepted Push followed by ${point} failure retains the uncertain phase`, async () => {
    const env = await fixture();
    const fault = new Error(`${point} disk failure`);
    const original = env.storage.native;
    let injected = false;
    env.storage.native = async scope => {
      const handles = await original(scope);
      return { ...handles, meta: new Proxy(handles.meta, { get(target, key) {
        if (key === 'bulkWrite') return async (...args: Parameters<typeof target.bulkWrite>) => {
          if (env.pushes().length && args[0].some(row => point === 'checkpoint' ? row.document.id === 'up|1'
            : row.document.isCheckpoint === '0' && row.document.docData?.kind === 'd')) {
            injected = true; throw fault;
          }
          return target.bulkWrite(...args);
        };
        const value = Reflect.get(target, key, target);
        return typeof value === 'function' ? value.bind(target) : value;
      } }) };
    };
    try {
      env.start(); await until(() => env.owner.snapshot.state === 'idle');
      await env.storage.set('alice', { value: 'accepted' });
      await until(() => env.owner.snapshot.state === 'blocked');
      expect(injected).toBe(true);
      expect(env.remote.get('alice')?.value).toBe('accepted');
      expect(env.pushes()).toHaveLength(1);
      expect((await env.storage.readManifest()).dirtyUpstream).not.toBeNull();
      const inspection = await env.owner.inspect({ logicalId: 'alice' });
      expect(inspection.targets.map(target => target.logicalId)).toContain('alice');
      expect(decodeBusinessPayload(inspection.document!.desired!.payload)).toEqual({ value: 'accepted' });
    } finally { env.storage.native = original; await env.close(injected ? fault : undefined); }
  }, 20_000);
});
