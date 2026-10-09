import { expect, test } from 'bun:test';
import axios from 'axios';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import type { RxStorage } from 'rxdb';
import { SyntrixError } from '../../api/errors.js';
import type { ReplicaOptionsSnapshot } from '../../api/replica-reference.js';
import type { ReplicaDiagnostic, ReplicaSyncStatus } from '../../api/replica-types.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { createNamespace } from './identity.js';
import { openReplicaDatabase, type ReplicaDatabaseEnvironment } from './database.js';

const token = (subject = 'alice') => `${btoa('{}')}.${btoa(JSON.stringify({ sub: subject }))}.sig`.replace(/=/g, '');
const definition = { collection: 'users', filters: [], orderBy: [] };
const until = async (predicate: () => boolean | Promise<boolean>) => {
  const deadline = Date.now() + 5_000;
  while (!await predicate()) { if (Date.now() > deadline) throw new Error('Facade condition timed out'); await Bun.sleep(5); }
};
const fixture = (override: Partial<ReplicaDatabaseEnvironment> = {}) => {
  const provider = new DefaultTokenProvider({ token: token() });
  const http = axios.create({ baseURL: 'https://example.test', adapter: async () => { throw new SyntrixError('UNAVAILABLE', 'offline', 503); } });
  const environment: ReplicaDatabaseEnvironment = { storage: getRxStorageDexie({ indexedDB, IDBKeyRange }), lockManager: createTestLockManager(),
    sourceTransport: { socket: () => { throw new TypeError('WebSocket unavailable in the offline fixture'); } },
    coordinator: { leadership: () => ({ wait: async signal => signal.throwIfAborted(), close: async () => {} }), now: Date.now,
      random: () => 1, set: (callback, delay) => setTimeout(callback, delay), clear: timer => clearTimeout(timer) }, ...override };
  const open = async (options: ReplicaOptionsSnapshot) => {
    const session = await createReplicaSession(provider);
    try { return await session.track(() => openReplicaDatabase({ session, axios: http, provider, endpoint: 'https://example.test', database: 'app', options }, environment)); }
    catch (error) { await session.close(); throw error; }
  };
  return { provider, http, environment, open };
};

test('offline facade supports immutable queries, conditional CRUD, typed watch, and isolated aliases', async () => {
  const f = fixture();
  const db = await f.open({ name: crypto.randomUUID(), collections: { left: definition, right: definition }, sync: { hintDelayMs: 10 } });
  try {
    await db.sync.pause();
    const left = db.collection<{ value: bigint | number; status: string }>('left');
    const right = db.collection('right');
    const payload = { value: 9007199254740993n, status: 'open' };
    const writing = left.doc('alice').set(payload); payload.status = 'changed'; await writing;
    expect(await right.doc('alice').get()).toBeNull();
    expect(await left.doc('alice').get()).toMatchObject({ id: 'alice', collection: 'users', value: 9007199254740993n, status: 'open' });
    const values = ['open']; const open = left.where('status', 'in', values); values[0] = 'closed';
    expect(await open.get()).toHaveLength(1);
    await expect(left.doc('alice').ifMatch('status', '==', 'wrong').update({ status: 'closed' })).rejects.toBeInstanceOf(Error);
    await left.doc('alice').ifMatch('status', '==', 'open').update({ value: 1n });
    const observed: (bigint | number)[] = [];
    const stop = left.limit(1).watch(documents => { const row = documents[0]; if (row && !row.deleted) observed.push(row.value); });
    await until(() => observed.length === 1);
    await left.doc('alice').update({ value: 1 });
    await until(() => observed.length === 2);
    expect(observed).toEqual([1n, 1]); stop();
    const added = await left.add({ value: 2n, status: 'open' });
    expect(added.path).toBe(`users/${added.id}`);
    const first = await left.orderBy('id').limit(1).getPage();
    expect(first.documents).toHaveLength(1); expect(first.nextCursor).not.toBeNull();
    expect(await left.orderBy('id').limit(1).startAfter(first.nextCursor!).get()).toHaveLength(1);
    await left.doc('alice').delete(); expect(await left.doc('alice').get()).toBeNull();
    expect(await left.doc('alice').get({ showDeleted: true })).toMatchObject({ id: 'alice', deleted: true });
    await left.doc('alice').set({ value: 3n, status: 'recreated' });
    expect(await left.doc('alice').get()).toMatchObject({ value: 3n });
  } finally { await db.close(); }
});

test('same namespace handles reopen offline and one close leaves the other handle usable', async () => {
  const f = fixture(); const options = { name: crypto.randomUUID(), collections: { users: definition } };
  const first = await f.open(options); const second = await f.open(options);
  let watches = 0;
  try {
    await first.sync.pause(); await second.sync.pause();
    first.collection('users').watch(() => { watches++; });
    await first.collection('users').doc('alice').set({ value: 1n });
    await until(() => watches > 0);
    await first.close(); const closedCount = watches;
    await second.collection('users').doc('alice').update({ value: 2n });
    expect(await second.collection('users').doc('alice').get()).toMatchObject({ value: 2n });
    await Bun.sleep(20); expect(watches).toBe(closedCount);
    expect(() => first.collection('users')).toThrow('closed');
  } finally { await first.close(); await second.close(); }
  const reopened = await f.open(options);
  try { await reopened.sync.pause(); expect(await reopened.collection('users').doc('alice').get()).toMatchObject({ value: 2n }); }
  finally { await reopened.close(); }
});

test('status reports durable pending and inspection exposes public documents without native records', async () => {
  const f = fixture(); const events: ReplicaDiagnostic[] = [];
  const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition }, sync: { hintDelayMs: 10 }, onDiagnostic: event => events.push(event) });
  let status!: ReplicaSyncStatus;
  const stop = db.sync.subscribe(value => { status = value; });
  try {
    await db.sync.pause();
    await db.collection('users').doc('alice').set({ secret: 'payload-must-not-leak', value: 1n });
    await until(() => status.aliases.users.pending === 1);
    expect(status.aliases.users.pins).toBe(1); expect(status.aliases.users.ready).toBe(false);
    const inspected = await db.sync.inspect('users', { id: 'alice' });
    expect(inspected.document).toMatchObject({ id: 'alice', desired: { existence: 'live', document: { value: 1n } }, assumed: null });
    expect(JSON.stringify(inspected, (_key, value) => typeof value === 'bigint' ? value.toString() : value)).not.toContain('payload":');
    const setEvents = events.filter(event => event.operation === 'set');
    expect(setEvents.map(event => event.phase)).toEqual(['start', 'complete']);
    expect(setEvents[0].operationId).toBe(setEvents[1].operationId);
    expect(setEvents[0].replicaId).toBe(setEvents[1].replicaId);
    expect(setEvents[0].sessionVersion).toBe(f.provider.getSessionVersion());
    const diagnosticText = JSON.stringify(events);
    expect(diagnosticText).not.toContain('payload-must-not-leak'); expect(diagnosticText).not.toContain(token());
    expect(diagnosticText).not.toContain('filters'); expect(diagnosticText).not.toContain('cause');
  } finally { stop(); await db.close(); }
});

test('rejected removal leaves pending data usable and omitted historical aliases can be removed', async () => {
  const f = fixture(); const name = crypto.randomUUID();
  const old = await f.open({ name, collections: { users: definition, historical: definition } });
  try {
    await old.sync.pause(); await old.collection('users').doc('pending').set({ value: 1 });
    await expect(old.removeCollection('users')).rejects.toMatchObject({ code: 'ReplicaRemovalBlocked' });
    expect(await old.collection('users').doc('pending').get()).toMatchObject({ value: 1 });
  } finally { await old.close(); }
  const current = await f.open({ name, collections: { users: definition } });
  try { await current.sync.pause(); await current.removeCollection('historical'); }
  finally { await current.close(); }
  const recreated = await f.open({ name, collections: { historical: definition } });
  try { await recreated.sync.pause(); expect(await recreated.collection('historical').get()).toEqual([]); await recreated.removeCollection('historical'); expect(() => recreated.collection('historical')).toThrow(); }
  finally { await recreated.close(); }
});

test('failed tracked open closes every partial alias without recursively closing its session', async () => {
  const f = fixture(); const name = crypto.randomUUID();
  const first = await f.open({ name, collections: { b: definition } }); await first.sync.pause(); await first.close();
  await expect(f.open({ name, collections: { a: definition, b: { ...definition, collection: 'other' } } })).rejects.toBeInstanceOf(Error);
  const valid = await f.open({ name, collections: { a: definition, b: definition } });
  try { await valid.sync.pause(); await valid.collection('a').doc('still-works').set({ value: 1 }); }
  finally { await valid.close(); }
});

test('account change during actual storage open drains the captured session without deadlock', async () => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange }); let entered = false, release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const storage: RxStorage<any, any> = { ...original, createStorageInstance: async parameters => {
    entered = true; await gate; return original.createStorageInstance(parameters);
  } };
  const f = fixture({ storage });
  const opening = f.open({ name: crypto.randomUUID(), collections: { users: definition } }).catch(error => error);
  await until(() => entered);
  f.provider.setToken(token('bob'));
  const installed = f.provider.getToken();
  release();
  expect(await opening).toMatchObject({ code: 'AUTH_SESSION_CHANGED' });
  expect(await installed).toBe(token('bob'));
});

test('one alias cleanup failure preserves its cause while every other alias is closed', async () => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const failure = new Error('durable close failed');
  const opened = new Set<string>(), closed = new Set<string>(); let injected = false;
  const storage: RxStorage<any, any> = { ...original, createStorageInstance: async parameters => {
    const raw = await original.createStorageInstance(parameters); opened.add(parameters.databaseName);
    return new Proxy(raw, { get(target, property) {
      if (property === 'close') return async () => {
        await target.close(); closed.add(parameters.databaseName);
        if (!injected && parameters.collectionName === 'manifest') { injected = true; throw failure; }
      };
      const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const f = fixture({ storage });
  const db = await f.open({ name: crypto.randomUUID(), collections: { a: definition, b: definition } });
  await db.sync.pause();
  const first = db.close(); const error = await first.catch(value => value);
  const includes = (value: unknown, seen = new Set<unknown>()): boolean => {
    if (value === failure) return true;
    if (!value || typeof value !== 'object' || seen.has(value)) return false;
    seen.add(value); return Object.values(value).some(child => includes(child, seen));
  };
  expect(injected).toBe(true); expect(includes(error)).toBe(true);
  expect(closed).toEqual(opened); expect(db.close()).toBe(first);
});

test('close cancels admitted writes waiting for another owner without waiting for its lock', async () => {
  const f = fixture(); const name = crypto.randomUUID();
  const db = await f.open({ name, collections: { users: definition } });
  await db.sync.pause();
  const identity = await createNamespace('https://example.test', 'app', name, 'users', 'alice');
  let release!: () => void, held = false, closing: Promise<void> | undefined;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const lock = f.environment.lockManager!.request(`syntrix:${identity.hash}:alias`, { mode: 'exclusive' }, async () => { held = true; await gate; });
  let writing: Promise<unknown> | undefined;
  try {
    await until(() => held);
    writing = db.collection('users').doc('queued').set({ value: 1 }).catch(error => error);
    await Bun.sleep(10);
    let drained = false;
    closing = db.close().then(() => { drained = true; });
    await until(() => drained);
    expect(await writing).toBeInstanceOf(Error);
  } finally { release(); await lock; await closing; await writing; await db.close(); }
}, 10_000);

test('an empty database handle can cancel historical removal waiting for a peer lock', async () => {
  const f = fixture(); const name = crypto.randomUUID();
  const db = await f.open({ name, collections: {} });
  const identity = await createNamespace('https://example.test', 'app', name, 'historical', 'alice');
  let release!: () => void, held = false;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const lock = f.environment.lockManager!.request(`syntrix:${identity.hash}:alias`, { mode: 'exclusive' }, async () => { held = true; await gate; });
  try {
    await until(() => held);
    const removal = db.removeCollection('historical').catch(error => error);
    await Bun.sleep(10);
    await db.close();
    expect(await removal).toMatchObject({ code: 'ReplicaDatabaseClosed' });
  } finally { release(); await lock; await db.close(); }
});

for (const action of ['close', 'account', 'unsubscribe'] as const) {
  test(`watch result diagnostics cannot deliver data after ${action}`, async () => {
    const f = fixture(); let diagnosticAction = (_event: ReplicaDiagnostic) => {};
    const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition }, onDiagnostic: event => diagnosticAction(event) });
    let stop = () => {}, triggered = false, cleanup: Promise<unknown> | undefined;
    const results: unknown[] = [], errors: unknown[] = [];
    try {
      await db.sync.pause(); await db.collection('users').doc('alice').set({ secret: 'old-account-data' });
      diagnosticAction = event => {
        if (triggered || event.operation !== 'watch' || event.phase !== 'result') return;
        triggered = true;
        if (action === 'close') cleanup = db.close();
        else if (action === 'account') { f.provider.setToken(token('bob')); cleanup = f.provider.getToken(); }
        else stop();
      };
      stop = db.collection('users').watch(value => results.push(value), error => errors.push(error));
      await until(() => triggered); await cleanup; await Bun.sleep(10);
      expect(results).toEqual([]); expect(errors).toEqual([]);
      if (action === 'unsubscribe') {
        const again: unknown[] = [];
        const stopAgain = db.collection('users').watch(value => again.push(value));
        try { await until(() => again.length === 1); } finally { stopAgain(); }
      }
    } finally { stop(); await cleanup; await db.close(); }
  });
}

for (const [operation, action] of [['get-document', 'close'], ['get-document', 'account'], ['inspect', 'account']] as const) {
  test(`${operation} completion diagnostics reject obsolete results after ${action}`, async () => {
    const f = fixture(); let diagnosticAction = (_event: ReplicaDiagnostic) => {};
    const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition }, onDiagnostic: event => diagnosticAction(event) });
    let triggered = false, cleanup: Promise<unknown> | undefined;
    try {
      await db.sync.pause(); await db.collection('users').doc('alice').set({ secret: 'old-account-data' });
      diagnosticAction = event => {
        if (triggered || event.operation !== operation || event.phase !== 'complete') return;
        triggered = true;
        if (action === 'close') cleanup = db.close();
        else { f.provider.setToken(token('bob')); cleanup = f.provider.getToken(); }
      };
      const result = operation === 'inspect' ? db.sync.inspect('users', { id: 'alice' }) : db.collection('users').doc('alice').get();
      await expect(result).rejects.toMatchObject({ code: 'ReplicaDatabaseClosed' });
      expect(triggered).toBe(true); await cleanup;
    } finally { await cleanup; await db.close(); }
  });
}

for (const phase of ['start', 'failed'] as const) {
  for (const action of ['close', 'account'] as const) {
    test(`watch ${phase} diagnostic ${action} prevents later admission or error delivery`, async () => {
      const f = fixture(); let diagnosticAction = (_event: ReplicaDiagnostic) => {};
      const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition }, onDiagnostic: event => diagnosticAction(event) });
      let triggered = false, cleanup: Promise<unknown> | undefined, stop = () => {};
      const results: unknown[] = [], errors: unknown[] = [];
      try {
        await db.sync.pause();
        diagnosticAction = event => {
          if (triggered || event.operation !== 'watch' || event.phase !== phase) return;
          triggered = true;
          if (action === 'close') cleanup = db.close();
          else { f.provider.setToken(token('bob')); cleanup = f.provider.getToken(); }
        };
        const query = phase === 'failed' ? db.collection('users').startAfter('not-allowed-for-watch') : db.collection('users');
        stop = query.watch(value => results.push(value), error => errors.push(error));
        await until(() => triggered); await cleanup; await Bun.sleep(10);
        expect(results).toEqual([]); expect(errors).toEqual([]);
      } finally { stop(); await cleanup; await db.close(); }
    });
  }
}

test('thrown diagnostics are isolated while valid reads, watches, and watch errors remain observable', async () => {
  const f = fixture();
  const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition }, onDiagnostic: () => { throw new Error('observer failed'); } });
  let stop = () => {}, stopInvalid = () => {};
  try {
    await db.sync.pause(); await db.collection('users').doc('alice').set({ value: 1n });
    expect(await db.collection('users').doc('alice').get()).toMatchObject({ value: 1n });
    expect((await db.sync.inspect('users', { id: 'alice' })).document?.desired).toMatchObject({ existence: 'live' });
    const results: unknown[] = [], errors: unknown[] = [];
    stop = db.collection('users').watch(value => results.push(value));
    stopInvalid = db.collection('users').startAfter('invalid-for-watch').watch(() => { throw new Error('Invalid query returned data'); }, error => errors.push(error));
    await until(() => results.length === 1 && errors.length === 1);
    expect(errors[0]).toBeInstanceOf(TypeError);
  } finally { stop(); stopInvalid(); await db.close(); }
});

test('a diagnostic-triggered close preserves the original storage read failure', async () => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const failure = new Error('actual storage read failed'); let inject = false;
  const storage: RxStorage<any, any> = { ...original, createStorageInstance: async parameters => {
    const raw = await original.createStorageInstance(parameters);
    return new Proxy(raw, { get(target, property) {
      if (property === 'findDocumentsById' && parameters.collectionName.startsWith('records_')) return (...args: Parameters<typeof target.findDocumentsById>) => {
        if (inject) throw failure;
        return target.findDocumentsById(...args);
      };
      const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const f = fixture({ storage }); let diagnosticAction = (_event: ReplicaDiagnostic) => {};
  const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition }, onDiagnostic: event => diagnosticAction(event) });
  let cleanup: Promise<void> | undefined;
  try {
    await db.sync.pause(); await db.collection('users').doc('alice').set({ value: 1 });
    diagnosticAction = event => { if (event.operation === 'get-document' && event.phase === 'failed') { inject = false; cleanup = db.close(); } };
    inject = true;
    await expect(db.collection('users').doc('alice').get()).rejects.toBe(failure);
    expect(cleanup).toBeDefined(); await cleanup;
  } finally { inject = false; await cleanup; await db.close(); }
});


test('query contention diagnostics correlate recovery without exposing the query or document', async () => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  let armed = false;
  let entered!: () => void, release!: () => void;
  const waiting = new Promise<void>(resolve => { entered = resolve; });
  const gate = new Promise<void>(resolve => { release = resolve; });
  const storage: RxStorage<any, any> = { ...original, createStorageInstance: async parameters => {
    const raw = await original.createStorageInstance(parameters);
    return new Proxy(raw, { get(target, property) {
      if (property === 'query' && parameters.collectionName.startsWith('records_')) return async (...args: Parameters<typeof target.query>) => {
        const result = await target.query(...args);
        if (armed) { armed = false; entered(); await gate; }
        return result;
      };
      const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const manager = createTestLockManager();
  let queueWrite = false, writeQueued!: () => void;
  const queued = new Promise<void>(resolve => { writeQueued = resolve; });
  const lockManager = { request: (name: string, options: LockOptions, callback: LockGrantedCallback<unknown>) => {
    const request = manager.request(name, options, callback);
    if (queueWrite && name.endsWith(':view') && options.mode === 'exclusive') { queueWrite = false; writeQueued(); }
    return request;
  } } as LockManager;
  const f = fixture({ storage, lockManager }); const events: ReplicaDiagnostic[] = [];
  const options = { name: crypto.randomUUID(), collections: { users: definition }, queryLimits: { scanCandidates: 1 },
    onDiagnostic: (event: ReplicaDiagnostic) => events.push(event) };
  const db = await f.open(options), writer = await f.open(options);
  let stop = () => {};
  const results: unknown[] = [], errors: unknown[] = [];
  try {
    await db.sync.pause(); await writer.sync.pause();
    await db.collection('users').doc('private-document-id').set({ secret: 'private-query-value', versioned: 1 });
    armed = true;
    stop = db.collection('users').where('secret', '==', 'private-query-value').watch(value => results.push(value), error => errors.push(error));
    await waiting;
    queueWrite = true;
    const writing = writer.collection('users').doc('private-document-id').update({ versioned: 2 });
    await queued;
    release(); await writing;
    await until(() => events.some(event => event.phase === 'recovered'));
    const activity = events.filter(event => ['contended', 'recovered'].includes(event.phase));
    expect(activity.map(event => event.phase)).toEqual(['contended', 'recovered']);
    for (const event of activity) {
      expect(event).toMatchObject({ operation: 'query', alias: 'users', sessionVersion: f.provider.getSessionVersion(), code: 'ReplicaQueryWorkContention' });
      expect(event.durationMs).toBeGreaterThanOrEqual(0);
      expect(Object.isFrozen(event)).toBe(true);
    }
    expect(activity[0].operationId).toBe(activity[1].operationId);
    expect(activity[0].replicaId).toBe(activity[1].replicaId);
    const diagnosticText = JSON.stringify(activity);
    for (const value of ['private-document-id', 'private-query-value', 'secret', token(), 'filters', 'cause']) expect(diagnosticText).not.toContain(value);
    expect(errors).toEqual([]);
    expect(results).toHaveLength(1);
    expect(results[0]).toMatchObject([{ id: 'private-document-id', versioned: 2 }]);
  } finally { armed = false; release(); stop(); await writer.close(); await db.close(); }
}, 10_000);
