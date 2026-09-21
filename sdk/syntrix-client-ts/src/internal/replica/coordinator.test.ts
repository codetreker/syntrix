import { expect, spyOn, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { EMPTY } from 'rxjs';
import { DefaultTokenProvider } from '../auth/provider.js';
import { SyntrixError } from '../../api/errors.js';
import { createReplicaSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage } from './storage.js';
import { compactAlias } from './compaction.js';
import { createReplicaDownstream, type CoordinatorEnvironment, type ReplicaDownstream } from './coordinator.js';
import { encodeBusinessPayload, freezeSourceDefinition } from './records.js';
import { ReplicaStorageError } from './storage-types.js';
import type { ReplicaSourceAdapter, SourceEventsPage, SourceLeaseContext } from './source-types.js';

const until = async (predicate: () => boolean | Promise<boolean>) => {
  const end = Date.now() + 4_000;
  while (!await predicate()) { if (Date.now() > end) throw new Error('Coordinator deadline exceeded'); await Bun.sleep(5); }
};
const fixture = async () => {
  const provider = new DefaultTokenProvider({ token: `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'alice', exp: 0 }))}.sig`.replace(/=/g, '') });
  const session = await createReplicaSession(provider);
  const options = { session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage: getRxStorageDexie({ indexedDB, IDBKeyRange }) };
  const storage = await openAliasStorage(options);
  return { storage, session, provider, options };
};
const page = (cursor = 'c1'): SourceEventsPage => ({ protocolVersion: 1, mode: 'events', databaseIdentity: 'db1', sourceHash: 'hash1',
  checkpoint: cursor, generationId: 'g1', phase: 'live', caughtUp: true, bootstrapComplete: true, events: [] });
const source = (read: ReplicaSourceAdapter['read']): ReplicaSourceAdapter => ({ definition: freezeSourceDefinition({ collection: 'users', filters: [] }), mode: 'events', read });
const immediate: CoordinatorEnvironment = { leadership: () => ({ wait: async signal => { signal.throwIfAborted(); }, close: async () => {} }),
  now: Date.now, random: () => 1, set: (callback, delay) => setTimeout(callback, delay), clear: clearTimeout };

test('source leases are leader-only and external maintenance releases a page only after native drain', async () => {
  const env = await fixture();
  const leases: SourceLeaseContext[] = [];
  const events: string[] = [];
  let enter!: () => void, release!: () => void, grant!: () => void;
  const entered = new Promise<void>(resolve => { enter = resolve; });
  const gate = new Promise<void>(resolve => { release = resolve; });
  const elected = new Promise<void>(resolve => { grant = resolve; });
  const native = env.storage.native;
  let arm = true;
  const spy = spyOn(env.storage, 'native').mockImplementation(async scope => {
    const handles = await native(scope);
    return { ...handles, fork: new Proxy(handles.fork, { get(target, key) {
      if (key === 'bulkWrite') return async (...args: Parameters<typeof target.bulkWrite>) => {
        if (arm && args[1].startsWith('replication-downstream-')) { arm = false; enter(); await gate; }
        return target.bulkWrite(...args);
      };
      const value = Reflect.get(target, key, target); return typeof value === 'function' ? value.bind(target) : value;
    } }) };
  });
  const adapter = source(async () => { throw new Error('An acquired source lease must own reads'); });
  adapter.resume = () => { events.push('resumed'); };
  adapter.acquire = context => {
    const index = leases.push(context);
    return { ...adapter, read: async () => { events.push(`read:${index}`); return page(); },
      committed: () => { events.push(`committed:${index}`); }, released: () => { events.push(`released:${index}`); },
      invalidate: () => { events.push(`invalidated:${index}`); }, close: async () => { events.push(`closed:${index}`); } };
  };
  const coordinator = createReplicaDownstream({ storage: env.storage, source: adapter, options: { hintDelayMs: 5 } }, { ...immediate,
    leadership: () => ({ wait: async () => elected, close: async () => {} }) });
  try {
    await until(() => coordinator.snapshot.state === 'waiting');
    expect(leases).toEqual([]);
    grant(); await entered;
    expect(events).toEqual(['read:1']);
    const maintenance = env.storage.withMaintenance(async () => {
      expect(events).toEqual(['read:1', 'invalidated:1', 'released:1', 'closed:1']);
    });
    await until(() => events.includes('invalidated:1'));
    expect(events).not.toContain('released:1'); expect(events).not.toContain('closed:1');
    const paused = coordinator.pause();
    release(); await Promise.all([maintenance, paused]);
    expect(events).not.toContain('committed:1');
    await coordinator.resume(); await until(() => coordinator.snapshot.state === 'idle');
    expect(events.filter(event => event === 'resumed')).toHaveLength(1);
    expect(events.indexOf('closed:1')).toBeLessThan(events.indexOf('resumed'));
    expect(events.indexOf('resumed')).toBeLessThan(events.indexOf('read:2'));
    await coordinator.resume();
    expect(events.filter(event => event === 'resumed')).toHaveLength(1);
    expect(leases).toHaveLength(2);
    expect(leases[1].scope.nativeInstanceId).not.toBe(leases[0].scope.nativeInstanceId);
    expect(events).toContain('committed:2');
    leases[0].hint(); await Bun.sleep(20);
    expect(events.filter(event => event === 'read:2')).toHaveLength(1);
    leases[1].hint(); await until(() => events.filter(event => event === 'read:2').length === 2 && coordinator.snapshot.state === 'idle');
  } finally { grant(); release(); spy.mockRestore(); await coordinator.close(); await env.storage.close(); }
  expect(events.filter(event => event === 'closed:1')).toHaveLength(1);
  expect(events.filter(event => event === 'closed:2')).toHaveLength(1);
});

test('accepted page cleanup preserves persistence and lease-close failures while releasing its receipt', async () => {
  const env = await fixture();
  const database = await env.storage.withMaintenance(async access => access.backend.database);
  const persistence = new Error('binding persistence failed'), cleanup = new Error('source lease close failed');
  const events: string[] = [];
  const bind = spyOn(env.storage, 'bind').mockImplementation(async () => { throw persistence; });
  const adapter = source(async () => { throw new Error('Use the elected lease'); });
  adapter.acquire = () => ({ ...adapter, read: async () => { events.push('accepted'); return page(); },
    committed: () => { events.push('committed'); }, released: () => { events.push('released'); },
    invalidate: () => { events.push('invalidated'); }, close: async () => { events.push('closed'); throw cleanup; } });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: adapter }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'blocked');
    expect(events).toEqual(['accepted', 'invalidated', 'released', 'closed']);
    const failure = coordinator.snapshot.error as { errors: unknown[] };
    expect(failure.errors[0]).toBe(persistence);
    expect((failure.errors[1] as { errors: unknown[] }).errors).toEqual([persistence, cleanup]);
    await expect(coordinator.close()).rejects.toBeDefined();
    expect(events.filter(event => event === 'closed')).toHaveLength(1);
  } finally { bind.mockRestore(); await Promise.allSettled([coordinator.close(), env.storage.close()]); await database.close(); }
});

for (const closeFails of [false, true]) test(`observer blocking drains an accepted source page automatically (cleanup failure: ${closeFails})`, async () => {
  const env = await fixture();
  const database = await env.storage.withMaintenance(async access => access.backend.database);
  const events: string[] = [], cleanupError = new Error('lease cleanup failed after observer block');
  let entered!: () => void, release!: () => void;
  const applying = new Promise<void>(resolve => { entered = resolve; });
  const gate = new Promise<void>(resolve => { release = resolve; });
  const native = env.storage.native;
  const spy = spyOn(env.storage, 'native').mockImplementation(async scope => {
    const handles = await native(scope);
    return { ...handles, fork: new Proxy(handles.fork, { get(target, key) {
      if (key === 'bulkWrite') return async (...args: Parameters<typeof target.bulkWrite>) => {
        if (args[1].startsWith('replication-downstream-')) { entered(); await gate; }
        return target.bulkWrite(...args);
      };
      const value = Reflect.get(target, key, target); return typeof value === 'function' ? value.bind(target) : value;
    } }) };
  });
  const adapter = source(async () => { throw new Error('Use the elected lease'); });
  adapter.acquire = () => ({ ...adapter, read: async () => { events.push('accepted'); return page(); },
    committed: () => { events.push('committed'); }, released: () => { events.push('released'); },
    invalidate: () => { events.push('invalidated'); }, close: async () => { events.push('closed'); if (closeFails) throw cleanupError; } });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: adapter, options: { pollIntervalMs: 10 } }, immediate);
  try {
    await applying;
    const scope = await env.storage.captureScope();
    await env.storage.withReplicationAccess(scope, access => access.writeManifest({ ...access.manifest,
      issues: [{ id: 'observer-issue', logicalId: null, code: 'ReplicaWriteConflict' }] }));
    await until(() => coordinator.snapshot.state === 'blocked');
    expect(events).toEqual(['accepted', 'invalidated']);
    release(); await until(() => events.includes('closed'));
    expect(events).toEqual(['accepted', 'invalidated', 'released', 'closed']);
    if (closeFails) {
      await until(() => (coordinator.snapshot.error as { code?: string })?.code === 'ReplicaCleanupFailed');
      const failure = coordinator.snapshot.error as { errors: unknown[] };
      expect(failure.errors[0]).toMatchObject({ code: 'ReplicaRecoveryPending' });
      expect(failure.errors[1]).toBe(cleanupError);
      await expect(coordinator.close()).rejects.toBe(cleanupError);
    } else await coordinator.close();
  } finally { release(); spy.mockRestore(); await Promise.allSettled([coordinator.close(), env.storage.close()]); await database.close(); }
});

test('pause drains requests and preserves leadership and CRUD until explicit resume', async () => {
  const env = await fixture(); let calls = 0, leaderCloses = 0;
  let pendingSignal: AbortSignal | undefined;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(context => {
    if (++calls === 1) return Promise.resolve(page());
    if (calls > 2) return Promise.resolve(page(`c${calls}`));
    pendingSignal = context.signal;
    return new Promise((_resolve, reject) => context.signal.addEventListener('abort', () => reject(context.signal.reason), { once: true }));
  }) }, { ...immediate, leadership: () => ({ wait: async () => {}, close: async () => { leaderCloses++; } }) });
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    coordinator.refresh(); await until(() => !!pendingSignal);
    await coordinator.pause();
    expect(pendingSignal!.aborted).toBe(true); expect(coordinator.snapshot.state).toBe('paused');
    expect(coordinator.snapshot.leader).toBe(true); expect(leaderCloses).toBe(0);
    await env.storage.set('offline', { value: 1 });
    coordinator.refresh(); coordinator.hint(); await Bun.sleep(30); expect(calls).toBe(2);
    const inspection = await coordinator.inspect({ logicalId: 'offline' });
    expect(inspection.document?.desired?.logicalId).toBe('offline');
    await coordinator.resume(); await until(() => calls === 3 && coordinator.snapshot.state === 'idle');
    expect((await env.storage.get('offline'))?.id).toBe('offline');
  } finally { await coordinator.close(); await env.storage.close(); }
  expect(leaderCloses).toBe(1);
});

test('resume refuses unresolved durable markers and followers cannot authorize recovery', async () => {
  const env = await fixture(); let reads = 0;
  const scope = await env.storage.captureScope();
  await env.storage.withReplicationAccess(scope, async access => { await access.writeManifest({ ...access.manifest,
    dirtyUpstream: { id: 'crashed-phase', session: scope.sessionVersion, physicalEpoch: scope.physicalEpoch, mayHaveDispatched: true, targets: [] } }); });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { reads++; return page(); }) }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'blocked');
    await coordinator.pause();
    await expect(coordinator.resume()).rejects.toMatchObject({ code: 'ReplicaRecoveryPending' });
    expect(reads).toBe(0); expect((await coordinator.inspect()).issueId).toBe('crashed-phase');
  } finally { await coordinator.close(); await env.storage.close(); }
  const followerEnv = await fixture();
  const follower = createReplicaDownstream({ storage: followerEnv.storage, source: source(async () => page()) }, {
    ...immediate, leadership: () => ({ wait: signal => new Promise((_resolve, reject) => signal.addEventListener('abort', () => reject(signal.reason), { once: true })), close: async () => {} }),
  });
  try {
    await follower.pause();
    await expect(follower.resolve({ kind: 'retry-uncertain', issueId: 'anything', acknowledgeRepeatedEffects: true })).rejects.toMatchObject({ code: 'ReplicaNotLeader' });
  } finally { await follower.close(); await followerEnv.storage.close(); }
});

test('explicit resume restarts a blocked read after authorization recovers', async () => {
  const env = await fixture(); let allowed = false, calls = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => {
    calls++;
    if (!allowed) throw new SyntrixError('FORBIDDEN', 'Access revoked', 403);
    return page();
  }) }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'blocked');
    expect(calls).toBe(1);
    allowed = true;
    await coordinator.resume();
    await until(() => coordinator.snapshot.state === 'idle');
    expect(calls).toBe(2); expect(coordinator.snapshot.ready).toBe(true);
  } finally { await coordinator.close(); await env.storage.close(); }
});

test('paused authority inspection uses the lifetime signal and close drains its canceled read', async () => {
  const env = await fixture(); let reads = 0, delay = false, pendingSignal: AbortSignal | undefined;
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => page()), upstream: {
    prepare: () => { throw new Error('Inspection must not prepare Push'); },
    push: async () => { throw new Error('Inspection must not Push'); },
    readCurrent: async (id, context) => {
      reads++;
      expect(context.expectedDatabaseIdentity).toBe('db1');
      expect(context.signal.aborted).toBe(false);
      if (delay) {
        pendingSignal = context.signal;
        await new Promise<void>((_resolve, reject) => context.signal.addEventListener('abort', () => {
          void gate.then(() => reject(context.signal.reason));
        }, { once: true }));
      }
      return { id, collection: 'users', value: 'server', version: 2n, createdAt: 1n, updatedAt: 2n };
    },
  } }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    await coordinator.pause();
    await env.storage.set('alice', { value: 'offline' });
    const before = await env.storage.captureScope();
    const offline = await coordinator.inspect({ logicalId: 'alice' });
    expect(reads).toBe(0); expect(offline.document?.current).toBeUndefined();
    const current = await coordinator.inspect({ logicalId: 'alice', readCurrent: true });
    expect(current.document?.current).toMatchObject({ source: 'authoritative-read', document: { value: 'server', version: 2n } });
    expect((await env.storage.captureScope()).nativeInstanceId).toBe(before.nativeInstanceId);
    expect(coordinator.snapshot.state).toBe('paused');
    delay = true;
    const inspecting = coordinator.inspect({ logicalId: 'alice', readCurrent: true }).catch(error => error);
    await until(() => !!pendingSignal);
    await env.storage.set('sibling', { value: 'still editable' });
    let drained = false;
    const closing = coordinator.close().then(() => { drained = true; });
    await until(() => pendingSignal!.aborted);
    await Bun.sleep(10); expect(drained).toBe(false);
    release(); await closing;
    expect(await inspecting).toMatchObject({ code: 'ReplicaCoordinatorClosed' });
    expect(await env.storage.get('alice')).toMatchObject({ value: 'offline' });
  } finally { release(); await coordinator.close(); await env.storage.close(); }
});

test('native downstream becomes durably ready, pending writes remain pending, and close permits recreation', async () => {
  const env = await fixture(); let coordinator: ReplicaDownstream | undefined;
  const contexts: Parameters<ReplicaSourceAdapter['read']>[0][] = [];
  const remote = source(async context => { contexts.push(context); return page(`c${contexts.length}`); });
  try {
    await env.storage.set('offline', { value: 1 });
    coordinator = createReplicaDownstream({ storage: env.storage, source: remote }, immediate);
    await until(() => coordinator!.snapshot.state === 'idle');
    expect(coordinator.snapshot.ready).toBe(true);
    expect(contexts[0].expectedDatabaseIdentity).toBeNull();
    expect((await env.storage.get('offline'))?.id).toBe('offline');
    await coordinator.close();
    expect(coordinator.snapshot.state).toBe('closed');
    await env.storage.set('after-close', { value: 2 });
    coordinator = createReplicaDownstream({ storage: env.storage, source: remote }, immediate);
    await until(() => coordinator!.snapshot.state === 'idle');
    expect(contexts[contexts.length - 1]?.expectedDatabaseIdentity).toBe('db1');
    expect(contexts[contexts.length - 1]?.checkpoint).toBe('c1');
  } finally { await coordinator?.close(); await env.storage.close(); }
});

test('transient reads back off despite hints, reset after success, and permanent source errors block', async () => {
  const env = await fixture(); let calls = 0, fail = true;
  const moments: number[] = [];
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => {
    calls++; moments.push(Date.now());
    if (fail) throw new SyntrixError('UNAVAILABLE', 'Busy', 503);
    return page();
  }), options: { retryBaseMs: 100, retryMaxMs: 200, hintDelayMs: 10 } }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'retrying');
    for (let i = 0; i < 100; i++) { coordinator.hint(); coordinator.refresh(); }
    await Bun.sleep(30); expect(calls).toBe(1);
    fail = false;
    await until(() => coordinator.snapshot.state === 'idle');
    expect(calls).toBe(2); expect(moments[1] - moments[0]).toBeGreaterThanOrEqual(90);
    expect(coordinator.snapshot.error).toBeUndefined();
  } finally { await coordinator.close(); await env.storage.close(); }
  const second = await fixture();
  const blocked = createReplicaDownstream({ storage: second.storage, source: source(async () => { throw new SyntrixError('FORBIDDEN', 'Denied', 403); }) }, immediate);
  try { await until(() => blocked.snapshot.state === 'blocked'); expect((blocked.snapshot.error as SyntrixError).status).toBe(403); }
  finally { await blocked.close(); await second.storage.close(); }
});

test('hint storms coalesce to one round and poll uses the same source path', async () => {
  const env = await fixture(); let calls = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { calls++; return page(`c${calls}`); }),
    options: { hintDelayMs: 40, pollIntervalMs: 200 } }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    for (let i = 0; i < 100; i++) coordinator.hint();
    await Bun.sleep(15); expect(calls).toBe(1);
    await until(() => calls === 2 && coordinator.snapshot.state === 'idle');
    await Bun.sleep(60); expect(calls).toBe(2);
    await until(() => calls === 3);
  } finally { await coordinator.close(); await env.storage.close(); }
});

test('closing a waiting follower does not await the raw election promise or read the source', async () => {
  const env = await fixture(); let calls = 0, released = false;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { calls++; return page(); }) }, {
    ...immediate, leadership: () => ({ wait: signal => new Promise((_resolve, reject) => {
      if (signal.aborted) reject(signal.reason); else signal.addEventListener('abort', () => reject(signal.reason), { once: true });
    }), close: async () => { released = true; } }),
  });
  try { await Bun.sleep(20); await coordinator.close(); expect(calls).toBe(0); expect(released).toBe(true); await env.storage.set('still-open', { value: 1 }); }
  finally { await coordinator.close(); await env.storage.close(); }
});

test('alias close aborts a pending source request and drains the coordinator', async () => {
  const env = await fixture(); let signal: AbortSignal | undefined;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(context => {
    signal = context.signal;
    return new Promise((_resolve, reject) => context.signal.addEventListener('abort', () => reject(context.signal.reason), { once: true }));
  }) }, immediate);
  await until(() => !!signal);
  await env.storage.close();
  expect(signal!.aborted).toBe(true); expect(coordinator.snapshot.state).toBe('closed');
  await coordinator.close();
});

test('RESYNC_REQUIRED restarts from an empty cursor with the original database binding', async () => {
  const env = await fixture();
  const contexts: Parameters<ReplicaSourceAdapter['read']>[0][] = [];
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async context => {
    contexts.push(context);
    if (contexts.length === 2) throw new SyntrixError('RESYNC_REQUIRED', 'History expired', 409);
    return { ...page(`c${contexts.length}`), generationId: contexts.length > 2 ? 'g2' : 'g1' };
  }), options: { retryBaseMs: 20 } }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    const initialGeneration = coordinator.snapshot.generation;
    coordinator.refresh();
    await until(() => contexts.length === 3 && coordinator.snapshot.state === 'idle');
    expect(coordinator.snapshot.generation).not.toBe(initialGeneration);
    expect(contexts).toHaveLength(3);
    expect(contexts[1].checkpoint).toBe('c1');
    expect(contexts[2].checkpoint).toBeNull();
    expect(contexts[2].expectedDatabaseIdentity).toBe('db1');
    expect(contexts[2].expectedSourceHash).toBe('hash1');
  } finally { await coordinator.close(); await env.storage.close(); }
});

test('followers reconcile durable readiness after missed notifications without any source reads', async () => {
  const env = await fixture(); const followerStorage = await openAliasStorage(env.options);
  let followerReads = 0;
  const disconnectedFeed = new Proxy(followerStorage, { get(target, key) { return key === 'changes' ? EMPTY : Reflect.get(target, key); } });
  const follower = createReplicaDownstream({ storage: disconnectedFeed, source: source(async () => { followerReads++; return page(); }),
    options: { pollIntervalMs: 30 } }, { ...immediate, leadership: () => ({
      wait: signal => new Promise((_resolve, reject) => { signal.addEventListener('abort', () => reject(signal.reason), { once: true }); }), close: async () => {},
    }) });
  const leader = createReplicaDownstream({ storage: env.storage, source: source(async () => page()) }, immediate);
  try {
    await until(() => leader.snapshot.ready && follower.snapshot.ready);
    expect(follower.snapshot.leader).toBe(false); expect(follower.snapshot.generation).toBe(leader.snapshot.generation); expect(followerReads).toBe(0);
  } finally { await follower.close(); await leader.close(); await followerStorage.close(); await env.storage.close(); }
});

test('not-clean maintenance recaptures native scope and uses capped retry scheduling', async () => {
  const env = await fixture(); await env.storage.set('pending', { value: 1 });
  const oldScope = await env.storage.captureScope();
  const stats = env.storage.stats;
  const spy = spyOn(env.storage, 'stats').mockImplementation(async () => ({ ...await stats(), shouldCompact: true }));
  let calls = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { calls++; return page(); }),
    options: { maintenanceBackoffMs: 1000 } }, immediate);
  try {
    await until(() => calls === 2 && coordinator.snapshot.state === 'idle');
    expect((await env.storage.captureScope()).nativeInstanceId).not.toBe(oldScope.nativeInstanceId);
    expect((await env.storage.get('pending'))?.id).toBe('pending');
    await Bun.sleep(50); expect(calls).toBe(2);
  } finally { spy.mockRestore(); await coordinator.close(); await env.storage.close(); }
});

for (const mode of ['same-handle', 'pending', 'other-handle'] as const) test(`external ${mode} compaction resumes source reads under the existing leader`, async () => {
  const env = await fixture();
  const maintenanceStorage = mode === 'other-handle' ? await openAliasStorage(env.options) : env.storage;
  const contexts: Parameters<ReplicaSourceAdapter['read']>[0][] = [];
  let elections = 0, releases = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async context => {
    contexts.push(context);
    return { ...page(`c${contexts.length}`), events: [{ type: 'upsert', document: {
      id: 'remote', collection: 'users', version: BigInt(contexts.length), createdAt: 1n, updatedAt: BigInt(contexts.length), value: contexts.length,
    } }] };
  }) }, { ...immediate, leadership: () => ({ wait: async () => { elections++; }, close: async () => { releases++; } }) });
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    if (mode === 'pending') await env.storage.set('pending', { value: 'keep' });
    const before = await env.storage.captureScope();
    const result = await compactAlias(maintenanceStorage);
    expect(result.status).toBe(mode === 'pending' ? 'not-clean' : 'compacted');
    coordinator.refresh();
    await until(() => contexts.length === 2 && coordinator.snapshot.state === 'idle');
    expect(contexts[1].checkpoint).toBe('c1');
    expect(contexts[1].expectedDatabaseIdentity).toBe('db1');
    expect(await env.storage.get('remote')).toMatchObject({ value: 2, version: 2n });
    expect((await env.storage.captureScope()).nativeInstanceId).not.toBe(before.nativeInstanceId);
    if (mode === 'pending') expect(await env.storage.get('pending')).toMatchObject({ value: 'keep' });
    expect(coordinator.snapshot).toMatchObject({ state: 'idle', ready: true, leader: true, error: undefined });
    expect(elections).toBe(1); expect(releases).toBe(0);
    coordinator.refresh();
    await until(() => contexts.length === 3 && coordinator.snapshot.state === 'idle');
    expect(contexts[2].checkpoint).toBe('c2');
  } finally {
    await coordinator.close();
    if (maintenanceStorage !== env.storage) await maintenanceStorage.close();
    await env.storage.close();
  }
});

for (const mode of ['same-handle', 'other-handle'] as const) test(`external ${mode} maintenance during a source round resumes its durable cursor`, async () => {
  const env = await fixture();
  const maintenanceStorage = mode === 'other-handle' ? await openAliasStorage(env.options) : env.storage;
  const contexts: Parameters<ReplicaSourceAdapter['read']>[0][] = [];
  let releaseRead!: () => void;
  let elections = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async context => {
    contexts.push(context);
    if (contexts.length === 2) await new Promise<void>((resolve, reject) => {
      releaseRead = resolve;
      context.signal.addEventListener('abort', () => reject(context.signal.reason), { once: true });
    });
    return page(`c${contexts.length}`);
  }) }, { ...immediate, leadership: () => ({ wait: async () => { elections++; }, close: async () => {} }) });
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    coordinator.refresh();
    await until(() => contexts.length === 2);
    expect((await compactAlias(maintenanceStorage)).status).toBe('compacted');
    releaseRead();
    await until(() => contexts.length === 3 && coordinator.snapshot.state === 'idle');
    expect(contexts[2].checkpoint).toBe('c1');
    expect(coordinator.snapshot).toMatchObject({ state: 'idle', leader: true, error: undefined });
    expect(elections).toBe(1);
  } finally {
    releaseRead?.(); await coordinator.close();
    if (maintenanceStorage !== env.storage) await maintenanceStorage.close();
    await env.storage.close();
  }
});

test('retired coordinator waits for maintenance drain before capturing replacement handles', async () => {
  const env = await fixture(); let calls = 0, capturing = false, captured = false;
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { calls++; return page(`c${calls}`); }) }, immediate);
  let maintenance: ReturnType<typeof compactAlias> | undefined;
  let scopeSpy: ReturnType<typeof spyOn> | undefined;
  let unregister = () => {};
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    unregister = env.storage.registerNative({ invalidate() {}, close: () => gate });
    const capture = env.storage.captureScope;
    scopeSpy = spyOn(env.storage, 'captureScope').mockImplementation(async requestId => {
      capturing = true;
      const result = await capture(requestId);
      captured = true;
      return result;
    });
    maintenance = compactAlias(env.storage);
    coordinator.refresh();
    await until(() => capturing);
    expect(captured).toBe(false); expect(calls).toBe(1);
    expect(coordinator.snapshot.state).toBe('syncing');
    release();
    expect((await maintenance).status).toBe('compacted');
    await until(() => calls === 2 && coordinator.snapshot.state === 'idle');
    expect(captured).toBe(true); expect(coordinator.snapshot.leader).toBe(true);
  } finally {
    release(); unregister(); scopeSpy?.mockRestore(); await maintenance;
    await coordinator.close(); await env.storage.close();
  }
});

test('maintenance between scope capture and handle acquisition recaptures the retired instance', async () => {
  const env = await fixture();
  const capture = env.storage.captureScope;
  let captures = 0, reads = 0;
  const spy = spyOn(env.storage, 'captureScope').mockImplementation(async requestId => {
    const scope = await capture(requestId);
    if (++captures === 1) expect((await compactAlias(env.storage)).status).toBe('not-clean');
    return scope;
  });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { reads++; return page(); }) }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'idle');
    expect(captures).toBe(2); expect(reads).toBe(1);
    expect(coordinator.snapshot.leader).toBe(true);
  } finally { spy.mockRestore(); await coordinator.close(); await env.storage.close(); }
});

test('maintenance drain between scope capture and handle acquisition waits for a replacement', async () => {
  const env = await fixture();
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const unregister = env.storage.registerNative({ invalidate() {}, close: () => gate });
  const capture = env.storage.captureScope;
  let captures = 0, reads = 0, elections = 0;
  let maintenance: ReturnType<typeof compactAlias> | undefined;
  const spy = spyOn(env.storage, 'captureScope').mockImplementation(async requestId => {
    const attempt = ++captures;
    const scope = await capture(requestId);
    if (attempt === 1) maintenance = compactAlias(env.storage);
    return scope;
  });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { reads++; return page(); }) }, {
    ...immediate, leadership: () => ({ wait: async () => { elections++; }, close: async () => {} }),
  });
  try {
    await until(() => captures === 2 || coordinator.snapshot.state === 'blocked');
    expect(coordinator.snapshot.state).toBe('syncing');
    expect(captures).toBe(2); expect(reads).toBe(0);
    release();
    expect((await maintenance!).status).toBe('not-clean');
    await until(() => coordinator.snapshot.state === 'idle');
    expect(captures).toBe(2); expect(reads).toBe(1); expect(elections).toBe(1);
  } finally {
    release(); unregister(); spy.mockRestore(); await maintenance;
    await coordinator.close(); await env.storage.close();
  }
});

for (const code of ['ReplicaScopeChanged', 'ReplicaMaintenance']) test(`${code} without a retired instance blocks instead of retrying`, async () => {
  const env = await fixture();
  const failure = new ReplicaStorageError(code, 'Native instance admission was revoked');
  const spy = spyOn(env.storage, 'native').mockRejectedValue(failure);
  let reads = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => { reads++; return page(); }) }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'blocked');
    expect(coordinator.snapshot.error).toBe(failure);
    expect(spy).toHaveBeenCalledTimes(1); expect(reads).toBe(0);
  } finally { spy.mockRestore(); await coordinator.close(); await env.storage.close(); }
});

test('a real storage failure during external retirement blocks and remains observable on drain', async () => {
  const env = await fixture();
  const database = await env.storage.withMaintenance(async access => access.backend.database);
  const failure = new Error('durable metadata failed during retirement');
  let entered = false, release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const native = env.storage.native;
  const spy = spyOn(env.storage, 'native').mockImplementation(async scope => {
    const handles = await native(scope);
    return { ...handles, meta: new Proxy(handles.meta, { get(target, key) {
      if (key === 'findDocumentsById') return async () => { entered = true; await gate; throw failure; };
      const value = Reflect.get(target, key); return typeof value === 'function' ? value.bind(target) : value;
    } }) };
  });
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => page()) }, immediate);
  try {
    await until(() => entered);
    const maintenance = compactAlias(env.storage).catch(error => error);
    release();
    expect(await maintenance).toBe(failure);
    await until(() => coordinator.snapshot.state === 'blocked');
    expect(coordinator.snapshot.error).toBe(failure);
    await expect(coordinator.close()).rejects.toBe(failure);
    await expect(env.storage.close()).rejects.toBe(failure);
  } finally {
    release(); spy.mockRestore(); await Promise.allSettled([coordinator.close(), env.storage.close()]); await database.close();
  }
});

test('unexpected controlled upstream conflicts block without overwriting pending content', async () => {
  const env = await fixture(); await env.storage.set('alice', { value: 'local' });
  let pushes = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => page()), writeRemote: async rows => {
    pushes++;
    return rows.map(row => ({ ...row.newDocumentState, payload: encodeBusinessPayload({ value: 'remote' }) }));
  } }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'blocked');
    expect(pushes).toBe(1);
    expect(coordinator.snapshot.error).toMatchObject({ code: 'ReplicaConflictUnresolved' });
    expect(await env.storage.get('alice')).toMatchObject({ value: 'local' });
  } finally { await coordinator.close(); await env.storage.close(); }
});

test('retry-after remains a lower bound and a mismatched source never starts network reads', async () => {
  const env = await fixture(); let calls = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => {
    calls++;
    if (calls === 1) throw new SyntrixError('RATE_LIMITED', 'Wait', 429, undefined, 0.12);
    return page();
  }), options: { retryBaseMs: 10 } }, immediate);
  try {
    await until(() => coordinator.snapshot.state === 'retrying');
    coordinator.refresh(); await Bun.sleep(50); expect(calls).toBe(1);
    await until(() => coordinator.snapshot.state === 'idle'); expect(calls).toBe(2);
  } finally { await coordinator.close(); await env.storage.close(); }
  const other = await fixture(); let unexpectedReads = 0;
  const mismatch = createReplicaDownstream({ storage: other.storage, source: {
    ...source(async () => { unexpectedReads++; return page(); }), definition: freezeSourceDefinition({ collection: 'other', filters: [] }),
  } }, immediate);
  try { await until(() => mismatch.snapshot.state === 'blocked'); expect(unexpectedReads).toBe(0); expect(mismatch.snapshot.error).toMatchObject({ code: 'ReplicaSourceMismatch' }); }
  finally { await mismatch.close(); await other.storage.close(); }
});

for (const status of [500, 502]) test(`HTTP ${status} uses bounded read retry and recovers`, async () => {
  const env = await fixture(); let calls = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => {
    if (++calls === 1) throw new SyntrixError('INTERNAL_ERROR', 'Temporary upstream failure', status);
    return page();
  }), options: { retryBaseMs: 30 } }, immediate);
  try { await until(() => coordinator.snapshot.state === 'idle'); expect(calls).toBe(2); expect(coordinator.snapshot.ready).toBe(true); }
  finally { await coordinator.close(); await env.storage.close(); }
});

test('native I/O and election cleanup failures remain observable and failed close retains ownership', async () => {
  const env = await fixture();
  const database = await env.storage.withMaintenance(async access => access.backend.database);
  const ioFailure = new Error('metadata I/O failed'), electionFailure = new Error('leadership cleanup failed');
  let entered = false, release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const native = env.storage.native;
  const spy = spyOn(env.storage, 'native').mockImplementation(async scope => {
    const handles = await native(scope);
    return { ...handles, meta: new Proxy(handles.meta, { get(target, key) {
      if (key === 'findDocumentsById') return async () => { entered = true; await gate; throw ioFailure; };
      const value = Reflect.get(target, key); return typeof value === 'function' ? value.bind(target) : value;
    } }) };
  });
  let electionCloses = 0;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(async () => page()) }, {
    ...immediate, leadership: () => ({ wait: async () => {}, close: async () => { electionCloses++; throw electionFailure; } }),
  });
  try {
    await until(() => entered);
    const first = coordinator.close();
    release();
    const error = await first.catch(value => value);
    expect(error.cause).toBe(ioFailure); expect(error.errors).toEqual([ioFailure, electionFailure]);
    expect(coordinator.close()).toBe(first); expect(await coordinator.close().catch(value => value)).toBe(error);
    expect(electionCloses).toBe(1);
    await expect(env.storage.close()).rejects.toBe(ioFailure);
  } finally {
    release(); spy.mockRestore();
    await Promise.allSettled([coordinator.close(), env.storage.close()]);
    await database.close();
  }
});

for (const stop of ['alias', 'account'] as const) test(`handled source rejection racing ${stop} close does not poison cleanup`, async () => {
  const env = await fixture();
  const database = await env.storage.withMaintenance(async access => access.backend.database);
  let rejectRead!: (error: unknown) => void;
  const coordinator = createReplicaDownstream({ storage: env.storage, source: source(() => new Promise((_resolve, reject) => { rejectRead = reject; })) }, immediate);
  try {
    await until(() => !!rejectRead);
    rejectRead(new SyntrixError('FORBIDDEN', 'Source authorization changed', 403));
    await Promise.resolve();
    if (stop === 'alias') await env.storage.close();
    const replacement = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'bob', exp: 0 }))}.sig`.replace(/=/g, '');
    env.provider.setToken(replacement);
    expect(await env.provider.getToken()).toBe(replacement);
    await coordinator.close();
    expect(coordinator.snapshot.state).toBe('closed');
  } finally {
    await Promise.allSettled([coordinator.close(), env.storage.close()]);
    await database.close();
  }
});
