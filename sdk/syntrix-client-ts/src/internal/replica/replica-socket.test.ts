import { afterEach, describe, expect, it } from 'bun:test';
import { DefaultTokenProvider } from '../auth/provider.js';
import { connectReplicaSocket, ReplicaSocketError } from './replica-socket.js';
import type { SourceReadContext } from './source-types.js';
import type { FrozenSourceDefinition } from './storage-types.js';

type Frame = { id: string; type: string; payload: Record<string, any> };
const definition: FrozenSourceDefinition = { collection: 'users', filters: [], orderBy: [], limit: null };
const databaseIdentity = '0123456789abcdef';
const sourceHash = 'a'.repeat(64);
const page = { protocolVersion: 1, mode: 'events', databaseIdentity, sourceHash,
  generationId: '01234567-89ab-cdef-0123-456789abcdef', events: [], checkpoint: 'opaque',
  phase: 'live', caughtUp: true, bootstrapComplete: true };
const resources: Array<() => Promise<void>> = [];
afterEach(async () => { for (const close of resources.splice(0)) await close(); });

const fixture = async () => {
  const registrations = new Set<string>();
  const frames: Frame[] = [];
  const waiters = new Set<() => void>();
  let peer: Bun.ServerWebSocket<undefined> | undefined;
  let holdRead = false;
  let failUnsubscribe = false;
  let peerClosed = false;
  const notify = () => { for (const check of [...waiters]) check(); };
  const wait = (condition: () => boolean): Promise<void> => new Promise((resolve, reject) => {
    const timer = setTimeout(() => { waiters.delete(check); reject(new Error('Expected socket activity did not arrive')); }, 2_000);
    const check = () => { if (condition()) { clearTimeout(timer); waiters.delete(check); resolve(); } };
    waiters.add(check); check();
  });
  const send = (frame: Frame) => peer!.send(JSON.stringify(frame));
  const server = Bun.serve<undefined>({
    hostname: '127.0.0.1', port: 0,
    fetch(request, server) { return server.upgrade(request) ? undefined : new Response('Upgrade required', { status: 400 }); },
    websocket: {
      open(socket) { peer = socket; },
      close() { peerClosed = true; registrations.clear(); notify(); },
      message(_socket, data) {
        const frame = JSON.parse(String(data)) as Frame;
        frames.push(frame);
        if (frame.type === 'auth') send({ id: frame.id, type: 'auth_ack', payload: { mode: 'replica-data' } });
        if (frame.type === 'subscribe') {
          registrations.add(frame.id);
          send({ id: frame.id, type: 'subscribe_ack', payload: { subId: frame.id, databaseIdentity } });
        }
        if (frame.type === 'unsubscribe') {
          registrations.delete(frame.payload.subId);
          send({ id: frame.id, type: 'unsubscribe_ack', payload: { subId: frame.payload.subId } });
        }
        if (frame.type === 'replica_read' && !holdRead) send({ id: frame.id, type: 'replica_page', payload: {
          subId: frame.payload.subId, requestId: frame.id, page,
        } });
        notify();
      },
    },
  });
  const owner = new AbortController();
  const socket = await connectReplicaSocket({ endpoint: server.url.origin, database: 'db',
    provider: new DefaultTokenProvider({ token: 'access-token' }), sessionVersion: 0, signal: owner.signal,
  }, { socket: url => {
    const socket = new WebSocket(url);
    const send = socket.send.bind(socket);
    socket.send = data => {
      if (failUnsubscribe && typeof data === 'string' && JSON.parse(data).type === 'unsubscribe') throw new Error('Send failed');
      send(data);
    };
    return socket;
  } });
  resources.push(async () => { socket.close(); await wait(() => peerClosed); server.stop(true); });
  const register = async (id: string) => {
    const controller = new AbortController();
    const errors: Error[] = [];
    let hints = 0;
    await socket.register(id, definition, databaseIdentity, controller.signal,
      () => { hints++; notify(); }, error => { errors.push(error); notify(); });
    return { controller, errors, get hints() { return hints; } };
  };
  const context = (requestId: string): SourceReadContext => ({ requestId, checkpoint: null,
    signal: owner.signal, sessionVersion: 0, expectedDatabaseIdentity: databaseIdentity, expectedSourceHash: sourceHash });
  const busy = (subId: string, requestId?: string) => send({ id: requestId ?? subId, type: 'error', payload: {
    subId, ...(requestId ? { requestId } : {}), code: 'REPLICATION_SOURCE_BUSY', message: 'Authorization queue full', retryAfter: 2,
  } });
  return { socket, frames, registrations, wait, register, context, send, busy,
    holdReads() { holdRead = true; }, resumeReads() { holdRead = false; }, failCleanupSend() { failUnsubscribe = true; } };
};

describe('replica socket registration retirement', () => {
  it('releases every busy subscription while another alias keeps the real socket active', async () => {
    const test = await fixture();
    const healthy = await test.register('healthy');
    for (let attempt = 0; attempt < 6; attempt++) {
      const id = `busy-${attempt}`;
      const failed = await test.register(id);
      expect(test.registrations.size).toBe(2);
      test.busy(id);
      await test.wait(() => failed.errors.length === 1);
      expect(failed.errors[0]).toBeInstanceOf(ReplicaSocketError);
      expect(failed.errors[0]).toMatchObject({ kind: 'source', status: 429, retryAfter: 2 });
      const requestId = `healthy-read-${attempt}`;
      expect(await test.socket.read('healthy', test.context(requestId), definition)).toMatchObject({ checkpoint: 'opaque' });
      test.socket.ack('healthy', requestId);
      await test.wait(() => test.frames.some(frame => frame.type === 'replica_ack' && frame.id === requestId));
      expect([...test.registrations]).toEqual(['healthy']);
      expect(test.frames.filter(frame => frame.type === 'unsubscribe' && frame.id === id)).toHaveLength(1);
      failed.controller.abort(new Error('Late owner cleanup'));
      expect(test.socket.closed).toBe(false);
      expect(healthy.errors).toEqual([]);
    }
  });

  it('ignores late old-subscription frames and aborts after a replacement registration is active', async () => {
    const test = await fixture();
    const old = await test.register('old');
    test.busy('old');
    await test.wait(() => old.errors.length === 1 && !test.registrations.has('old'));
    const replacement = await test.register('replacement');
    old.controller.abort();
    test.send({ id: 'old', type: 'subscribe_ack', payload: { subId: 'old', databaseIdentity } });
    test.send({ id: 'old', type: 'replica_changed', payload: { subId: 'old' } });
    test.send({ id: 'obsolete-read', type: 'replica_page', payload: { subId: 'old', requestId: 'obsolete-read', page } });
    test.send({ id: 'replacement', type: 'replica_changed', payload: { subId: 'replacement' } });
    await test.wait(() => replacement.hints === 1);
    expect(old.hints).toBe(0);
    expect([...test.registrations]).toEqual(['replacement']);
    expect(await test.socket.read('replacement', test.context('current'), definition)).toMatchObject({ caughtUp: true });
    test.socket.ack('replacement', 'current');
    expect(test.socket.closed).toBe(false);
  });

  it('retains a registration after a request-scoped busy error and permits the next authorized read', async () => {
    const test = await fixture();
    const subscription = await test.register('source');
    test.holdReads();
    const rejected = test.socket.read('source', test.context('busy-read'), definition).catch(error => error);
    await test.wait(() => test.frames.some(frame => frame.id === 'busy-read'));
    test.busy('source', 'busy-read');
    expect(await rejected).toMatchObject({ code: 'REPLICATION_SOURCE_BUSY', retryAfter: 2 });
    expect(test.registrations.has('source')).toBe(true);
    expect(subscription.errors).toEqual([]);
    expect(test.frames.filter(frame => frame.type === 'unsubscribe')).toEqual([]);
    test.resumeReads();
    expect(await test.socket.read('source', test.context('retry-read'), definition)).toMatchObject({ checkpoint: 'opaque' });
    test.socket.ack('source', 'retry-read');
  });

  it('retires a delivered page owner on subscription error without invalidating another alias', async () => {
    const test = await fixture();
    await test.register('healthy');
    const failing = await test.register('delivered');
    await test.socket.read('delivered', test.context('delivered-read'), definition);
    test.busy('delivered');
    await test.wait(() => failing.errors.length === 1 && !test.registrations.has('delivered'));
    test.socket.ack('delivered', 'delivered-read');
    expect(test.socket.closed).toBe(false);
    expect([...test.registrations]).toEqual(['healthy']);
    expect(test.frames.filter(frame => frame.type === 'replica_ack')).toEqual([]);
  });

  it('closes the shared socket if retirement cannot deliver unsubscribe', async () => {
    const test = await fixture();
    await test.register('healthy');
    const failing = await test.register('failing');
    test.failCleanupSend();
    test.busy('failing');
    await test.wait(() => failing.errors.length === 1 && test.registrations.size === 0);
    expect(test.socket.closed).toBe(true);
    expect(test.socket.closeError).toMatchObject({ code: 'REPLICATION_TRANSPORT_UNAVAILABLE' });
  });
});
