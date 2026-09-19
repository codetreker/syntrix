import { expect, spyOn, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { EMPTY } from 'rxjs';
import { DefaultTokenProvider } from '../auth/provider.js';
import { SyntrixError } from '../../api/errors.js';
import { createReplicaSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage } from './storage.js';
import { createReplicaDownstream, type CoordinatorEnvironment, type ReplicaDownstream } from './coordinator.js';
import { encodeBusinessPayload, freezeSourceDefinition } from './records.js';
import type { ReplicaSourceAdapter, SourceEventsPage } from './source-types.js';

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
