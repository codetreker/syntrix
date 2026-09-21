import { expect, test } from 'bun:test';
import { createHash } from 'node:crypto';
import type { ServerWebSocket } from 'bun';
import axios from 'axios';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import type { RxStorage } from 'rxdb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { SyntrixError } from '../../api/errors.js';
import type { ReplicaOptionsSnapshot } from '../../api/replica-reference.js';
import type { ReplicaDatabase, ReplicaDiagnostic, ReplicaSyncStatus } from '../../api/replica-types.js';
import { encodeQueryValue } from '../../api/value.js';
import { setupAuthInterceptor } from '../auth/interceptor.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { openReplicaDatabase, type ReplicaDatabaseEnvironment } from './database.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { canonicalJson } from './records.js';
import { createReplicaSession } from './session.js';

const jwt = (subject = 'alice') => `${btoa('{}')}.${btoa(JSON.stringify({ sub: subject }))}.sig`.replace(/=/g, '');
const definition = { collection: 'users', filters: [], orderBy: [] };
const databaseIdentity = '0123456789abcdef';
const generationId = '01234567-89ab-4def-8123-456789abcdef';
const deferred = () => {
  let resolve!: () => void;
  let resolved = false;
  const promise = new Promise<void>(done => { resolve = () => { resolved = true; done(); }; });
  return { promise, resolve, get resolved() { return resolved; } };
};
const until = async (predicate: () => boolean | Promise<boolean>, label: string) => {
  const deadline = Date.now() + 12_000;
  while (!await predicate()) {
    if (Date.now() > deadline) throw new Error(`Composed transport deadline: ${label}`);
    await Bun.sleep(5);
  }
};
type Frame = { id: string; type: string; payload: Record<string, any> };
type Read = { transport: 'ws' | 'http'; body: Record<string, any>; requestId: string; expectedIdentity: unknown;
  socket?: ServerWebSocket<{ serial: number }>; frame?: Frame };
const sourceHash = (read: Read) => createHash('sha256').update(canonicalJson({ collection: read.body.collection, source: read.body.source })).digest('hex');
const document = (id: string, value: unknown, version = 1n) => encodeQueryValue({ id, collection: 'users', version, createdAt: 1n, updatedAt: version, value });
const events = (read: Read, checkpoint: string, entries: unknown[] = []) => ({ protocolVersion: 1, mode: 'events', databaseIdentity,
  sourceHash: sourceHash(read), generationId, checkpoint, phase: 'live', caughtUp: true, bootstrapComplete: true, events: entries });
const windowPage = (read: Read, count: number) => ({ protocolVersion: 1, mode: 'replace', databaseIdentity,
  sourceHash: sourceHash(read), generationId: crypto.randomUUID(), requestId: read.requestId, complete: true,
  effectiveOrder: [{ field: 'id', direction: 'asc' }], documents: Array.from({ length: count }, (_, i) => document(`item-${String(i).padStart(3, '0')}`, BigInt(i))) });

const fixture = (storage: RxStorage<any, any> = getRxStorageDexie({ indexedDB, IDBKeyRange })) => {
  const provider = new DefaultTokenProvider({ token: jwt() });
  const sockets = new Set<ServerWebSocket<{ serial: number }>>();
  const clients: WebSocket[] = [];
  const frames: { serial: number; frame: Frame }[] = [];
  const reads: Read[] = [];
  const httpCalls: string[] = [];
  const paths: string[] = [];
  const failures: unknown[] = [];
  const subscriptions = new Map<string, Record<string, any>>();
  const timers = new Set<ReturnType<typeof setTimeout>>();
  const databases = new Set<ReplicaDatabase>();
  let serial = 0, offline = false;
  let read = async (request: Read): Promise<unknown> => events(request, 'head');
  let ack = (_frame: Frame) => {};
  const send = (socket: ServerWebSocket<{ serial: number }>, frame: Frame) => {
    if (socket.readyState === WebSocket.OPEN) socket.send(JSON.stringify(frame));
  };
  const server = Bun.serve<{ serial: number }>({ hostname: '127.0.0.1', port: 0,
    fetch(request, upgrade) {
      paths.push(request.url);
      if (upgrade.upgrade(request, { data: { serial: ++serial } })) return;
      return new Response('WebSocket required', { status: 400 });
    }, websocket: {
      open(socket) { sockets.add(socket); },
      close(socket) { sockets.delete(socket); },
      async message(socket, data) {
        try {
          const frame = JSON.parse(String(data)) as Frame;
          frames.push({ serial: socket.data.serial, frame });
          if (frame.type === 'auth') send(socket, { type: 'auth_ack', id: frame.id, payload: { mode: 'replica-data' } });
          else if (frame.type === 'subscribe') {
            subscriptions.set(frame.id, frame.payload);
            send(socket, { type: 'subscribe_ack', id: frame.id, payload: { subId: frame.id, databaseIdentity } });
          } else if (frame.type === 'replica_read') {
            const request: Read = { transport: 'ws', body: frame.payload.request, requestId: frame.id,
              expectedIdentity: frame.payload.expectedDatabaseIdentity, socket, frame };
            reads.push(request);
            const page = await read(request);
            send(socket, { type: 'replica_page', id: frame.id, payload: { subId: frame.payload.subId, requestId: frame.id, page } });
          } else if (frame.type === 'replica_ack') ack(frame);
          else if (frame.type === 'unsubscribe') send(socket, { type: 'unsubscribe_ack', id: frame.id, payload: { subId: frame.payload.subId } });
        } catch (error) { failures.push(error); socket.close(1011, 'Fixture failed'); }
      },
    },
  });
  const endpoint = `${server.url.origin}/prefix`;
  const http = axios.create({ baseURL: endpoint, adapter: async config => {
    httpCalls.push(config.url!);
    if (offline) throw new SyntrixError('UNAVAILABLE', 'Fixture offline', 503);
    if (!config.url?.endsWith('/pull')) throw new Error(`Unexpected composed HTTP request: ${config.url}`);
    const body = JSON.parse(config.data as string) as Record<string, any>;
    const request: Read = { transport: 'http', body, requestId: body.requestId ?? '', expectedIdentity: config.headers.get('X-Syntrix-Expected-Database-Identity') };
    reads.push(request);
    return { config, status: 200, statusText: 'OK', headers: {}, data: JSON.stringify(await read(request)) };
  } });
  setupAuthInterceptor(http, provider);
  const environment: ReplicaDatabaseEnvironment = {
    storage, lockManager: createTestLockManager(),
    sourceTransport: {
      socket: url => { if (offline) throw new Error('Fixture socket unavailable'); const socket = new WebSocket(url); clients.push(socket); return socket; },
      now: Date.now, random: () => 1,
      setTimeout: (callback, delay) => { const timer = setTimeout(() => { timers.delete(timer); callback(); }, delay); timers.add(timer); return timer; },
      clearTimeout: timer => { timers.delete(timer); clearTimeout(timer); },
    },
    coordinator: { leadership: () => ({ wait: async signal => { signal.throwIfAborted(); }, close: async () => {} }),
      now: Date.now, random: () => 1, set: (callback, delay) => setTimeout(callback, delay), clear: timer => clearTimeout(timer) },
  };
  const open = async (options: ReplicaOptionsSnapshot) => {
    const session = await createReplicaSession(provider);
    try {
      const db = await session.track(() => openReplicaDatabase({ session, provider, axios: http, endpoint, database: 'app',
        options: { ...options, sync: { pollIntervalMs: 60_000, hintDelayMs: 5, retryBaseMs: 50, retryMaxMs: 100, ...options.sync } } }, environment));
      databases.add(db); return db;
    } catch (error) { await session.close(); throw error; }
  };
  const close = async () => {
    try {
      await Promise.all([...databases].map(db => db.close()));
      await until(() => clients.every(socket => socket.readyState === WebSocket.CLOSED), 'client sockets drain');
      expect(timers.size).toBe(0); expect(failures).toEqual([]);
    } finally { server.stop(true); }
  };
  return { provider, reads, frames, sockets, clients, subscriptions, httpCalls, paths, environment, open, close,
    reply(handler: typeof read) { read = handler; }, acknowledged(handler: typeof ack) { ack = handler; },
    offline() { offline = true; }, messages(type: string) { return frames.filter(entry => entry.frame.type === type); },
  };
};

test('facade applies a split WebSocket window and ACKs only after the final native and manifest commits', async () => {
  const gate = deferred(), entered = deferred();
  const committed: string[] = [];
  let armed = true;
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const storage: RxStorage<any, any> = { ...original, createStorageInstance: async parameters => {
    const raw = await original.createStorageInstance(parameters);
    return new Proxy(raw, { get(target, key) {
      if (key === 'bulkWrite') return async (...args: Parameters<typeof target.bulkWrite>) => {
        const documents = args[0].map(row => row.document as { id?: string; checkpointData?: { source?: { final?: boolean } };
          sourceReady?: boolean; activeSourceGeneration?: string | null });
        const final = documents.some(row => row.id === 'down|1' && row.checkpointData?.source?.final === true);
        if (armed && final) { armed = false; entered.resolve(); await gate.promise; }
        const result = await target.bulkWrite(...args);
        if (!result.error.length) {
          if (final) committed.push('native-final');
          if (documents.some(row => row.id === 'down|1' && row.checkpointData?.source?.final === false)) committed.push('native-part');
          if (parameters.collectionName === 'manifest' && documents.some(row => row.sourceReady && row.activeSourceGeneration)) committed.push('manifest-ready');
        }
        return result;
      };
      const value = Reflect.get(target, key, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const f = fixture(storage); const receipts: string[][] = [];
  f.reply(async request => windowPage(request, 101));
  f.acknowledged(() => receipts.push([...committed]));
  const db = await f.open({ name: crypto.randomUUID(), collections: { window: { ...definition, limit: 101 } } });
  const results: string[][] = []; const errors: unknown[] = [];
  const stop = db.collection('window').watch(rows => results.push(rows.map(row => row.id)), error => errors.push(error));
  try {
    await until(() => entered.resolved, 'final native checkpoint reached');
    expect(committed).toContain('native-part');
    expect(f.messages('replica_ack')).toEqual([]);
    expect(results.every(ids => ids.length === 0)).toBe(true);
    gate.resolve();
    await until(() => receipts.length > 0 && results.some(ids => ids.length === 101), 'complete window receipt and local watch');
    expect(receipts[0]).toContain('native-final'); expect(receipts[0]).toContain('manifest-ready');
    expect(results.every(ids => ids.length === 0 || ids.length === 101)).toBe(true);
    expect(await db.collection('window').doc('item-100').get()).toMatchObject({ value: 100n });
    expect(errors).toEqual([]); expect(f.httpCalls).toEqual([]);
    expect(f.clients).toHaveLength(1);
    expect(f.paths.every(path => new URL(path).pathname === '/prefix/realtime/ws' && new URL(path).searchParams.get('mode') === 'replica-data')).toBe(true);
  } finally { gate.resolve(); stop(); await f.close(); }
}, 30_000);

test('disconnect after a committed WS page resumes HTTP from its durable cursor and fixed database identity', async () => {
  const f = fixture();
  f.reply(async request => request.transport === 'ws'
    ? events(request, 'ws-committed', request.body.checkpoint === null ? [{ type: 'upsert', document: document('alice', 9007199254740993n) }] : [])
    : events(request, 'http-committed', [{ type: 'upsert', document: document('bob', 9007199254740994n) }]));
  const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition }, sync: { pollIntervalMs: 50 } });
  const changes: string[][] = [];
  const stop = db.collection('users').watch(rows => changes.push(rows.map(row => row.id)));
  try {
    await until(() => f.messages('replica_ack').length > 0, 'WS page committed');
    expect(await db.collection('users').doc('alice').get()).toMatchObject({ value: 9007199254740993n });
    const current = [...f.sockets][0]; expect(current).toBeDefined(); current.close(1012, 'Backend disconnected');
    await until(() => f.reads.some(read => read.transport === 'http') && changes.some(ids => ids.includes('bob')), 'HTTP fallback applied');
    const firstHTTP = f.reads.find(read => read.transport === 'http')!;
    expect(firstHTTP.body.checkpoint).toBe('ws-committed');
    expect(firstHTTP.expectedIdentity).toBe(databaseIdentity);
    expect(await db.collection('users').doc('bob').get()).toMatchObject({ value: 9007199254740994n });
  } finally { stop(); await f.close(); }
}, 15_000);

test('only elected alias owners acquire transport leases, sharing one socket and retiring it after the last pause', async () => {
  const f = fixture();
  const owners = new Set<string>();
  f.environment.coordinator!.leadership = namespace => ({
    wait: async signal => {
      if (!owners.has(namespace)) { owners.add(namespace); return; }
      await new Promise<void>((_resolve, reject) => { if (signal.aborted) reject(signal.reason); else signal.addEventListener('abort', () => reject(signal.reason), { once: true }); });
    }, close: async () => {},
  });
  const collections = { left: { ...definition, filters: [{ field: 'lane', op: '==' as const, value: 'left' }] },
    right: { ...definition, filters: [{ field: 'lane', op: '==' as const, value: 'right' }] } };
  f.reply(async request => {
    const lane = request.body.source.filters[0].value.value as string;
    return events(request, `${lane}-head`, request.body.checkpoint === null ? [{ type: 'upsert', document: document(lane, lane) }] : []);
  });
  const options = { name: crypto.randomUUID(), collections };
  const leader = await f.open(options), follower = await f.open(options);
  let followerStatus!: ReplicaSyncStatus;
  const stop = follower.sync.subscribe(status => { followerStatus = status; });
  try {
    await until(() => f.messages('replica_ack').length >= 2, 'both elected aliases committed');
    expect(f.clients).toHaveLength(1);
    expect(new Set(f.messages('subscribe').map(entry => entry.frame.payload.source.filters[0].value.value))).toEqual(new Set(['left', 'right']));
    expect(await follower.collection('left').get()).toMatchObject([{ id: 'left', value: 'left' }]);
    expect(await follower.collection('right').get()).toMatchObject([{ id: 'right', value: 'right' }]);
    expect(Object.values(followerStatus.aliases).every(alias => !alias.leader)).toBe(true);
    const leftSubIDs = new Set(f.messages('subscribe').filter(entry => entry.frame.payload.source.filters[0].value.value === 'left').map(entry => entry.frame.id));
    await leader.sync.pause('left');
    await until(() => f.messages('unsubscribe').some(entry => leftSubIDs.has(entry.frame.id)), 'left lease retired');
    expect([...f.sockets]).toHaveLength(1);
    await leader.sync.pause('right');
    await until(() => f.clients.every(socket => socket.readyState === WebSocket.CLOSED), 'last active lease closes shared socket');
    const oldSubIDs = new Set(f.messages('subscribe').map(entry => entry.frame.id));
    await leader.sync.resume('left');
    await until(() => f.messages('subscribe').some(entry => entry.serial === 2), 'resumed alias registers fresh source');
    expect(f.clients).toHaveLength(2);
    expect(oldSubIDs.has(f.messages('subscribe').find(entry => entry.serial === 2)!.frame.id)).toBe(false);
    await until(() => f.reads.some(read => read.socket?.data.serial === 2), 'resumed source read');
    expect(f.reads.find(read => read.socket?.data.serial === 2)?.body.checkpoint).toBe('left-head');
    expect(f.httpCalls).toEqual([]);
  } finally { stop(); await f.close(); }
}, 20_000);

test('reentrant page-accepted diagnostic close prevents old data from reaching facade watches or persistence', async () => {
  const f = fixture(), gate = deferred();
  f.reply(async request => { await gate.promise; return events(request, 'late', [{ type: 'upsert', document: document('late', 'old owner') }]); });
  let act = (_event: ReplicaDiagnostic) => {};
  const options = { name: crypto.randomUUID(), collections: { users: definition }, onDiagnostic: (event: ReplicaDiagnostic) => act(event) };
  const db = await f.open(options);
  let closing: Promise<void> | undefined;
  const results: string[][] = [], errors: unknown[] = [];
  const stop = db.collection('users').watch(rows => results.push(rows.map(row => row.id)), error => errors.push(error));
  act = event => { if (!closing && event.operation === 'transport' && event.phase === 'accepted') closing = db.close(); };
  try {
    await until(() => f.reads.length > 0, 'pending WS response'); gate.resolve();
    await until(() => closing !== undefined, 'diagnostic retired the facade'); await closing;
    expect(results.every(ids => ids.length === 0)).toBe(true); expect(errors).toEqual([]);
    expect(f.messages('replica_ack')).toEqual([]);
    act = () => {}; f.offline();
    const reopened = await f.open(options); await reopened.sync.pause();
    expect(await reopened.collection('users').doc('late').get()).toBeNull();
  } finally { gate.resolve(); stop(); await closing; await f.close(); }
}, 15_000);

test('facade close cancels a WS credential wait without awaiting the shared token promise', async () => {
  const f = fixture();
  const tokenRead = f.provider.getToken.bind(f.provider);
  let release!: (token: string) => void, reads = 0;
  const credentials = new Promise<string>(resolve => { release = resolve; });
  f.provider.getToken = () => ++reads === 1 ? tokenRead() : credentials;
  const db = await f.open({ name: crypto.randomUUID(), collections: { users: definition } });
  let closed = false;
  try {
    await until(() => reads > 1, 'transport waiting for shared credentials');
    const closing = db.close().then(() => { closed = true; });
    await until(() => closed, 'close drains independently of unresolved credentials');
    await closing;
    expect(f.messages('auth')).toEqual([]);
    release(jwt());
  } finally { release(jwt()); await f.close(); }
}, 15_000);
