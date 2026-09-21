import { afterEach, describe, expect, it, mock } from 'bun:test';
import axios, { AxiosError, type InternalAxiosRequestConfig } from 'axios';
import { AuthSessionChangedError } from '../../api/errors.js';
import { encodeQueryValue } from '../../api/value.js';
import type { TokenProvider } from '../auth/types.js';
import { createReplicaSourceTransport } from './source-transport.js';
import type { SourceReadContext } from './source-types.js';
import type { FrozenSourceDefinition } from './storage-types.js';
import type { RequestScope } from './storage.js';

type Frame = { id?: string; type: string; payload?: any };
const definition: FrozenSourceDefinition = { collection: 'users', filters: [], orderBy: [], limit: null };
const identity = { protocolVersion: 1, databaseIdentity: '0123456789abcdef', sourceHash: 'a'.repeat(64), generationId: '01234567-89ab-cdef-0123-456789abcdef' };
const live = { id: 'alice', collection: 'users', version: 9007199254740993n, createdAt: 1n, updatedAt: 2n, name: 'Alice' };
const page = () => ({ ...identity, mode: 'events', events: [{ type: 'upsert', document: encodeQueryValue(live) }],
  checkpoint: 'opaque', phase: 'live', caughtUp: true, bootstrapComplete: true });
const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve(); };
const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const scope = (nativeInstanceId = 'native-1'): RequestScope => ({ subject: 'alice', sessionVersion: 0,
  definitionHash: 'b'.repeat(64), physicalEpoch: 'epoch-1', requestId: 'owner-1', nativeInstanceId });
const readContext = (requestId: string, signal: AbortSignal, changes: Partial<SourceReadContext> = {}): SourceReadContext => ({
  requestId, signal, sessionVersion: 0, checkpoint: null, expectedDatabaseIdentity: null, expectedSourceHash: null, ...changes,
});

class Socket {
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;
  readyState = 0;
  onopen: ((event: any) => void) | null = null;
  onclose: ((event: any) => void) | null = null;
  onerror: ((event: any) => void) | null = null;
  onmessage: ((event: any) => void) | null = null;
  sent: Frame[] = [];
  failSend: string | undefined;
  constructor(readonly url: string) {}
  open() { this.readyState = 1; this.onopen?.({}); }
  close() { this.readyState = 3; this.onclose?.({}); }
  send(text: string) {
    if (this.readyState !== 1) throw new Error('Socket closed');
    const message = JSON.parse(text) as Frame;
    if (message.type === this.failSend) throw new Error('Simulated send failure');
    this.sent.push(message);
  }
  receive(frame: Frame) { this.onmessage?.({ data: JSON.stringify(frame) }); }
  messages(type: string) { return this.sent.filter(frame => frame.type === type); }
  auth() {
    const auth = this.messages('auth').slice(-1)[0]!;
    expect(auth).toBeDefined();
    this.receive({ id: auth.id, type: 'auth_ack', payload: { mode: 'replica-data' } });
  }
  registered(subId: string) {
    this.receive({ id: subId, type: 'subscribe_ack', payload: { subId, databaseIdentity: identity.databaseIdentity } });
  }
  respond(read: Frame, value: unknown = page()) {
    this.receive({ id: read.id, type: 'replica_page', payload: { subId: read.payload.subId,
      requestId: read.id, page: value } });
  }
}

class Clock {
  now = 0;
  private serial = 0;
  timers = new Map<number, { at: number; callback: () => void }>();
  setTimeout = (callback: () => void, delay: number) => {
    const id = ++this.serial;
    this.timers.set(id, { at: this.now + Math.max(1, delay), callback });
    return id as unknown as ReturnType<typeof setTimeout>;
  };
  clearTimeout = (id: ReturnType<typeof setTimeout>) => { this.timers.delete(id as unknown as number); };
  async advance(ms: number) {
    const end = this.now + ms;
    for (;;) {
      const entry = [...this.timers].filter(([, timer]) => timer.at <= end).sort((a, b) => a[1].at - b[1].at)[0];
      if (!entry) break;
      const [id, timer] = entry;
      this.now = timer.at;
      this.timers.delete(id);
      timer.callback();
      await flush();
    }
    this.now = end;
    await flush();
  }
}

const resources: Array<{ close(): Promise<void>; clock: Clock; sockets: Socket[] }> = [];
afterEach(async () => {
  for (const resource of resources.splice(0)) {
    await resource.close();
    expect(resource.clock.timers.size).toBe(0);
    expect(resource.sockets.every(socket => socket.readyState === 3)).toBe(true);
  }
});

const fixture = (providerChanges: Partial<TokenProvider> = {}, socketFailure?: Error) => {
  const clock = new Clock();
  const sockets: Socket[] = [];
  const owner = new AbortController();
  const requests: InternalAxiosRequestConfig[] = [];
  const diagnostic = mock((..._args: unknown[]) => {});
  const provider: TokenProvider = {
    getSessionVersion: () => 0, getToken: async () => 'access-token', refreshToken: async () => 'refreshed-token',
    setToken: () => {}, setRefreshToken: () => {}, ...providerChanges,
  };
  const http = axios.create({ adapter: async config => {
    requests.push(config);
    return { config, status: 200, statusText: 'OK', headers: {}, data: JSON.stringify(page()) };
  } });
  const transport = createReplicaSourceTransport({ axios: http, provider, endpoint: 'https://localhost/prefix',
    database: 'db slug', sessionVersion: 0, signal: owner.signal, onDiagnostic: diagnostic }, {
    socket: (url: string) => { if (socketFailure) throw socketFailure; const socket = new Socket(url); sockets.push(socket); return socket as unknown as WebSocket; },
    setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout, now: () => clock.now, random: () => 0.5,
  });
  resources.push({ close: () => transport.close(), clock, sockets });
  const acquire = (alias = 'users', native = 'native-1', def = definition) => {
    const hint = mock(() => {});
    const controller = new AbortController();
    const source = transport.source(alias, def);
    const lease = source.acquire!({ scope: scope(native), signal: controller.signal, hint });
    return { lease, source, hint, controller, read: (id: string, changes: Partial<SourceReadContext> = {}) =>
      lease.read(readContext(id, controller.signal, changes)) };
  };
  const authenticate = async (socket = sockets[sockets.length - 1]!) => {
    socket.open();
    await flush();
    socket.auth();
    await flush();
    return socket;
  };
  const register = async (socket: Socket, index = socket.messages('subscribe').length - 1) => {
    const registration = socket.messages('subscribe')[index]!;
    expect(registration).toBeDefined();
    socket.registered(registration.id!);
    await flush();
    return registration.id!;
  };
  return { transport, provider, owner, clock, sockets, requests, diagnostic, acquire, authenticate, register };
};

describe('replica hybrid source transport', () => {
  it('uses replica-data authentication and fresh registration acknowledgments as hints, without reading eagerly', async () => {
    const test = fixture();
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    expect(socket.url).toBe('wss://localhost/prefix/realtime/ws?mode=replica-data');
    expect(socket.messages('auth')[0]!.payload).toEqual({ token: 'access-token', database: 'db slug', mode: 'replica-data' });
    expect(socket.messages('subscribe')).toHaveLength(0);
    expect(socket.messages('replica_read')).toHaveLength(0);
    const initialHints = source.hint.mock.calls.length;
    const result = source.read('read-1');
    await flush();
    const subId = await test.register(socket);
    expect(source.hint).toHaveBeenCalledTimes(initialHints + 1);
    socket.registered(subId);
    socket.receive({ type: 'replica_changed', id: subId, payload: { subId } });
    expect(source.hint).toHaveBeenCalledTimes(initialHints + 2);
    expect(socket.messages('replica_read')).toHaveLength(1);
    socket.respond(socket.messages('replica_read')[0]!);
    await result;
    source.lease.committed('read-1');
    expect(test.requests).toHaveLength(0);
    await expect(source.source.read(readContext('unleased', source.controller.signal))).rejects.toThrow();
  });

  it('decodes exact typed WS pages and acknowledges only an explicit whole-page commit', async () => {
    const test = fixture();
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    const result = source.read('read-1', { checkpoint: 'stored-cursor', expectedDatabaseIdentity: identity.databaseIdentity,
      expectedSourceHash: identity.sourceHash });
    await flush();
    const subId = await test.register(socket);
    const request = socket.messages('replica_read')[0]!;
    expect(request).toEqual({ id: 'read-1', type: 'replica_read', payload: {
      subId, requestId: 'read-1', expectedDatabaseIdentity: identity.databaseIdentity, expectedSourceHash: identity.sourceHash,
      request: { collection: 'users', source: { version: 1, filters: [], orderBy: [] }, checkpoint: 'stored-cursor', limit: 100 },
    } });
    socket.respond(request);
    const decoded = await result;
    expect(decoded).toMatchObject({ mode: 'events', events: [{ type: 'upsert', document: live }], checkpoint: 'opaque' });
    expect(socket.messages('replica_ack')).toHaveLength(0);
    source.lease.committed('read-1');
    expect(socket.messages('replica_ack')).toEqual([{ id: 'read-1', type: 'replica_ack', payload: { subId, requestId: 'read-1' } }]);
    source.lease.released('read-1');
    expect(test.requests).toHaveLength(0);
  });

  it('sends complete window requests without a cursor or transfer limit', async () => {
    const test = fixture();
    const source = test.acquire('top-users', 'native-1', { ...definition, limit: 2,
      orderBy: [{ field: 'name', direction: 'asc' }] });
    await flush();
    const socket = await test.authenticate();
    const result = source.read('window-1', { checkpoint: 'not-a-window-cursor' });
    await flush();
    await test.register(socket);
    const read = socket.messages('replica_read')[0]!;
    expect(read.payload.request).toEqual({ collection: 'users', requestId: 'window-1',
      source: { version: 1, filters: [], orderBy: [{ field: 'name', direction: 'asc' }], limit: 2 } });
    socket.respond(read, { ...identity, mode: 'replace', requestId: 'window-1', complete: true,
      documents: [encodeQueryValue(live)], effectiveOrder: [{ field: 'name', direction: 'asc' }, { field: 'id', direction: 'asc' }] });
    expect(await result).toMatchObject({ mode: 'replace', complete: true, documents: [live] });
    source.lease.committed('window-1');
    expect(test.requests).toHaveLength(0);
  });

  it('keeps an accepted page commit successful when its acknowledgment cannot be sent', async () => {
    const test = fixture();
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    const result = source.read('read-1');
    await flush();
    await test.register(socket);
    socket.respond(socket.messages('replica_read')[0]!);
    expect((await result).mode).toBe('events');
    socket.failSend = 'replica_ack';
    expect(() => source.lease.committed('read-1')).not.toThrow();
    source.lease.released('read-1');
    expect(test.diagnostic).toHaveBeenCalled();
  });

  it('cancels an authentication token wait without allowing its late token to reopen transport', async () => {
    const token = deferred<string>();
    const test = fixture({ getToken: () => token.promise });
    const source = test.acquire();
    await flush();
    expect(test.sockets).toHaveLength(0);
    const result = source.read('waiting-token').catch(error => error);
    await flush();
    const reason = new Error('replica owner stopped');
    test.owner.abort(reason);
    await test.transport.close();
    token.resolve('late-token');
    await flush();
    expect(await result).toBeInstanceOf(Error);
    expect(test.sockets).toHaveLength(0);
    expect(test.requests).toHaveLength(0);
  });

  for (const reason of [new DOMException('Blocked by connection policy', 'SecurityError'), new ReferenceError('WebSocket unavailable')]) {
    it(`falls back to HTTP when the socket constructor throws ${reason.name}`, async () => {
      const test = fixture({}, reason);
      const source = test.acquire();
      expect((await source.read('constructor-fallback')).mode).toBe('events');
      expect(test.requests).toHaveLength(1);
      expect(test.sockets).toHaveLength(0);
      expect(test.diagnostic.mock.calls.some(call => (call[3] as { cause?: unknown } | undefined)?.cause === reason)).toBe(true);
      source.lease.committed('constructor-fallback');
    });
  }

  it('preserves a credential provider failure without HTTP fallback', async () => {
    const reason = new Error('Credential provider unavailable');
    const test = fixture({ getToken: async () => { throw reason; } });
    const source = test.acquire();
    expect(await source.read('provider-failed').catch(error => error)).toBe(reason);
    expect(test.requests).toHaveLength(0);
    expect(test.sockets).toHaveLength(0);
  });

  it('falls back to the same typed HTTP request after a transport failure', async () => {
    const test = fixture();
    const source = test.acquire();
    const result = source.read('read-1', { checkpoint: 'last-committed', expectedDatabaseIdentity: identity.databaseIdentity,
      expectedSourceHash: identity.sourceHash });
    await flush();
    test.sockets[0]!.close();
    const decoded = await result;
    expect(decoded).toMatchObject({ mode: 'events', checkpoint: 'opaque', events: [{ type: 'upsert', document: live }] });
    expect(test.requests).toHaveLength(1);
    expect(test.requests[0]!.url).toBe('/replication/v1/databases/db%20slug/pull');
    expect(JSON.parse(test.requests[0]!.data)).toEqual({ collection: 'users', source: { version: 1, filters: [], orderBy: [] },
      checkpoint: 'last-committed', limit: 100 });
    expect(test.requests[0]!.headers.get('X-Syntrix-Expected-Database-Identity')).toBe(identity.databaseIdentity);
    source.lease.committed('read-1');
  });

  it('retains four accepted WS page credits across socket loss before admitting HTTP fallback', async () => {
    const test = fixture();
    const owners = Array.from({ length: 5 }, (_, index) => test.acquire(`alias-${index}`));
    await flush();
    const socket = await test.authenticate();
    for (let index = 0; index < 4; index++) {
      const result = owners[index]!.read(`page-${index}`);
      await flush();
      await test.register(socket);
      socket.respond(socket.messages('replica_read')[index]!);
      await result;
    }
    socket.close();
    let completed = false;
    const fifth = owners[4]!.read('page-4').then(value => { completed = true; return value; });
    await flush();
    expect(completed).toBe(false);
    expect(test.requests).toHaveLength(0);
    owners[0]!.lease.committed('page-0');
    await fifth;
    expect(test.requests).toHaveLength(1);
    for (let index = 1; index < 5; index++) owners[index]!.lease.committed(`page-${index}`);
  });

  it('uses the same four-page credit pool for HTTP pages awaiting local commit', async () => {
    const test = fixture();
    const owners = Array.from({ length: 5 }, (_, index) => test.acquire(`alias-${index}`));
    await flush();
    test.sockets[0]!.close();
    for (let index = 0; index < 4; index++) await owners[index]!.read(`page-${index}`);
    expect(test.requests).toHaveLength(4);
    let completed = false;
    const fifth = owners[4]!.read('page-4').then(value => { completed = true; return value; });
    await flush();
    expect(completed).toBe(false);
    expect(test.requests).toHaveLength(4);
    owners[0]!.lease.released('page-0');
    await fifth;
    expect(test.requests).toHaveLength(5);
    for (let index = 1; index < 5; index++) owners[index]!.lease.committed(`page-${index}`);
  });

  it('keeps probing beyond five failed connections and authenticates without eager source reads', async () => {
    const test = fixture();
    test.acquire();
    await flush();
    for (let index = 0; index < 7; index++) {
      expect(test.sockets).toHaveLength(index + 1);
      test.sockets[index]!.close();
      await flush();
      const next = Math.min(...[...test.clock.timers.values()].map(timer => timer.at));
      expect(Number.isFinite(next)).toBe(true);
      await test.clock.advance(next - test.clock.now);
    }
    expect(test.sockets).toHaveLength(8);
    const recovered = await test.authenticate();
    expect(recovered.messages('subscribe')).toHaveLength(0);
    expect(recovered.messages('replica_read')).toHaveLength(0);
    expect(test.requests).toHaveLength(0);
  });

  it('propagates source busy and its retry window without bypassing it over HTTP', async () => {
    const test = fixture();
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    const first = source.read('busy-1').catch(error => error);
    await flush();
    const subId = await test.register(socket);
    socket.receive({ id: 'busy-1', type: 'error', payload: { subId, requestId: 'busy-1',
      code: 'REPLICATION_SOURCE_BUSY', message: 'Read capacity exhausted', retryAfter: 3 } });
    expect(await first).toMatchObject({ code: 'REPLICATION_SOURCE_BUSY', retryAfter: 3 });
    expect(test.requests).toHaveLength(0);
    const second = source.read('busy-2').catch(error => error);
    await flush();
    expect(test.requests).toHaveLength(0);
    expect(socket.messages('replica_read')).toHaveLength(1);
    expect(await second).toMatchObject({ code: 'REPLICATION_SOURCE_BUSY' });
    await test.clock.advance(3_000);
    const retry = source.read('busy-3');
    await flush();
    expect(socket.messages('replica_read')).toHaveLength(2);
    socket.respond(socket.messages('replica_read')[1]!);
    await retry;
    source.lease.committed('busy-3');
  });

  it('re-registers after an asynchronous source-busy notification backoff expires', async () => {
    const test = fixture();
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    const first = source.read('page-before-busy');
    await flush();
    const subId = await test.register(socket);
    socket.respond(socket.messages('replica_read')[0]!);
    await first;
    source.lease.committed('page-before-busy');
    socket.receive({ id: subId, type: 'error', payload: { subId, code: 'REPLICATION_SOURCE_BUSY', message: 'busy', retryAfter: 2 } });
    expect(await source.read('during-backoff').catch(error => error)).toMatchObject({ code: 'REPLICATION_SOURCE_BUSY' });
    await test.clock.advance(2_000);
    const retry = source.read('after-backoff').catch(error => error);
    await flush();
    expect(socket.messages('subscribe')).toHaveLength(2);
    const nextSubId = await test.register(socket);
    expect(nextSubId).not.toBe(subId);
    socket.respond(socket.messages('replica_read')[1]!);
    expect((await retry).mode).toBe('events');
    source.lease.committed('after-backoff');
    expect(test.requests).toHaveLength(0);
  });

  it('retires old native leases and ignores old subIds after a fresh lease is registered', async () => {
    const test = fixture();
    const first = test.acquire('users', 'native-old');
    await flush();
    const socket = await test.authenticate();
    const result = first.read('old-read');
    await flush();
    const oldId = await test.register(socket);
    socket.respond(socket.messages('replica_read')[0]!);
    await result;
    first.lease.committed('old-read');
    first.lease.invalidate();
    await first.lease.close();
    const next = test.acquire('users', 'native-new');
    const nextResult = next.read('new-read');
    await flush();
    const replacement = await test.authenticate();
    const newId = await test.register(replacement);
    expect(newId).not.toBe(oldId);
    const readyCount = next.hint.mock.calls.length;
    socket.receive({ id: oldId, type: 'replica_changed', payload: { subId: oldId } });
    socket.registered(oldId);
    expect(next.hint).toHaveBeenCalledTimes(readyCount);
    replacement.receive({ id: oldId, type: 'replica_changed', payload: { subId: oldId } });
    replacement.registered(oldId);
    expect(next.hint).toHaveBeenCalledTimes(readyCount);
    replacement.respond(replacement.messages('replica_read').slice(-1)[0]!);
    await nextResult;
    next.lease.committed('new-read');
    await expect(first.read('obsolete')).rejects.toThrow();
  });

  it('retains an accepted alias page credit until its retired native lease releases it', async () => {
    const test = fixture();
    const old = test.acquire('users', 'native-old');
    await flush();
    const socket = await test.authenticate();
    const accepted = old.read('old-page');
    await flush();
    await test.register(socket);
    socket.respond(socket.messages('replica_read')[0]!);
    await accepted;
    old.lease.invalidate();
    const next = test.acquire('users', 'native-new');
    const pending = next.read('new-page');
    await flush();
    const replacement = await test.authenticate();
    expect(replacement.messages('replica_read')).toHaveLength(0);
    expect(socket.messages('replica_read')).toHaveLength(1);
    expect(test.requests).toHaveLength(0);
    old.lease.released('old-page');
    await flush();
    await test.register(replacement);
    expect(replacement.messages('replica_read')).toHaveLength(1);
    replacement.respond(replacement.messages('replica_read')[0]!);
    await pending;
    next.lease.committed('new-page');
    await old.lease.close();
  });

  it('retires a mismatched WS page and independently validates its HTTP fallback', async () => {
    const test = fixture();
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    const result = source.read('expected-read').catch(error => error);
    await flush();
    const subId = await test.register(socket);
    socket.receive({ id: 'wrong-read', type: 'replica_page', payload: { subId, requestId: 'wrong-read', page: page() } });
    expect(await result).toMatchObject({ mode: 'events', checkpoint: 'opaque' });
    expect(test.requests).toHaveLength(1);
    expect(socket.readyState).toBe(3);
  });

  it('rejects session replacement before delivering source results', async () => {
    let version = 0;
    const test = fixture({ getSessionVersion: () => version });
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    const result = source.read('old-session').catch(error => error);
    await flush();
    await test.register(socket);
    version++;
    socket.respond(socket.messages('replica_read')[0]!);
    expect(await result).toBeInstanceOf(AuthSessionChangedError);
    expect(test.requests).toHaveLength(0);
  });


  for (const [code, status] of [
    ['RESYNC_REQUIRED', 409], ['DATABASE_IDENTITY_MISMATCH', 409], ['REPLICATION_BUDGET_EXCEEDED', 422],
    ['REPLICATION_UNAVAILABLE', 503], ['INDEX_UNAVAILABLE', 503], ['REPLICATION_WINDOW_INCOMPLETE', 503],
    ['INTERNAL_ERROR', 500], ['REPLICATION_UNSUPPORTED', 501], ['DEADLINE_EXCEEDED', 504],
  ] as const) {
    it(`preserves ${code} from a WS source read without HTTP fallback`, async () => {
      const test = fixture();
      const source = test.acquire();
      await flush();
      const socket = await test.authenticate();
      const result = source.read('read-error').catch(error => error);
      await flush();
      const subId = await test.register(socket);
      socket.receive({ id: 'read-error', type: 'error', payload: { subId, requestId: 'read-error', code, message: code } });
      expect(await result).toMatchObject({ code, status });
      expect(test.requests).toHaveLength(0);
    });
  }

  it('preserves a registration error correlated only by its outer request ID', async () => {
    const test = fixture();
    const source = test.acquire();
    await flush();
    const socket = await test.authenticate();
    const result = source.read('register-error').catch(error => error);
    await flush();
    const registration = socket.messages('subscribe')[0]!;
    expect(registration).toBeDefined();
    socket.receive({ id: registration.id, type: 'error', payload: { code: 'REPLICATION_UNAVAILABLE', message: 'Backend unavailable' } });
    expect(await result).toMatchObject({ code: 'REPLICATION_UNAVAILABLE', status: 503 });
    expect(test.requests).toHaveLength(0);
  });

  it('honors source busy during authentication before probing and reading again', async () => {
    const test = fixture();
    const source = test.acquire();
    const first = source.read('auth-busy').catch(error => error);
    await flush();
    const socket = test.sockets[0]!;
    socket.open();
    socket.receive({ id: socket.messages('auth')[0]!.id, type: 'error', payload: {
      code: 'REPLICATION_SOURCE_BUSY', message: 'Authorization capacity exhausted', retryAfter: 2,
    } });
    expect(await first).toMatchObject({ code: 'REPLICATION_SOURCE_BUSY', retryAfter: 2 });
    expect(await source.read('too-early').catch(error => error)).toMatchObject({ code: 'REPLICATION_SOURCE_BUSY' });
    await test.clock.advance(1_999);
    expect(test.sockets).toHaveLength(1);
    expect(test.requests).toHaveLength(0);
    await test.clock.advance(1);
    expect(test.sockets).toHaveLength(2);
    const recovered = await test.authenticate();
    expect(recovered.messages('subscribe')).toHaveLength(0);
    const retry = source.read('after-auth-busy');
    await flush();
    await test.register(recovered);
    recovered.respond(recovered.messages('replica_read')[0]!);
    expect((await retry).mode).toBe('events');
    source.lease.committed('after-auth-busy');
    expect(test.requests).toHaveLength(0);
  });

  it('does not downgrade a forbidden replica authentication to HTTP', async () => {
    const test = fixture();
    const source = test.acquire();
    const result = source.read('forbidden').catch(error => error);
    await flush();
    const socket = test.sockets[0]!;
    socket.open();
    await flush();
    socket.receive({ id: socket.messages('auth')[0]!.id, type: 'error', payload: { code: 'FORBIDDEN', message: 'Not a database owner' } });
    expect(await result).toMatchObject({ code: 'FORBIDDEN' });
    expect(test.requests).toHaveLength(0);
    await test.clock.advance(100_000);
    expect(test.sockets).toHaveLength(1);
  });


  it('bounds page admission to 45 seconds without sending beyond four retained pages', async () => {
    const test = fixture();
    const owners = Array.from({ length: 5 }, (_, index) => test.acquire(`capacity-${index}`));
    await flush();
    const socket = await test.authenticate();
    for (let index = 0; index < 4; index++) {
      const result = owners[index]!.read(`held-${index}`);
      await flush();
      await test.register(socket);
      socket.respond(socket.messages('replica_read')[index]!);
      await result;
    }
    const rejected = owners[4]!.read('capacity-expired').catch(error => error);
    await flush();
    await test.clock.advance(45_000);
    expect(await rejected).toMatchObject({ code: 'REPLICA_PAGE_CAPACITY', status: 429, retryAfter: 1 });
    expect(socket.messages('replica_read')).toHaveLength(4);
    expect(test.requests).toHaveLength(0);
    owners[0]!.lease.committed('held-0');
    await test.clock.advance(1_000);
    const retry = owners[4]!.read('capacity-recovered');
    await flush();
    await test.register(socket);
    socket.respond(socket.messages('replica_read')[4]!);
    await retry;
    owners[4]!.lease.committed('capacity-recovered');
    for (let index = 1; index < 4; index++) owners[index]!.lease.committed(`held-${index}`);
  });

  for (const ending of ['abort', 'deadline'] as const) {
    for (const completion of ['resolve', 'reject'] as const) {
      it(`retires a pending refresh on ${ending} and ignores its late ${completion}`, async () => {
        const refresh = deferred<string>();
        const test = fixture({ refreshToken: () => refresh.promise });
        const source = test.acquire();
        const result = source.read('refresh-pending').catch(error => error);
        await flush();
        const socket = test.sockets[0]!;
        socket.open();
        socket.receive({ id: socket.messages('auth')[0]!.id, type: 'error', payload: { code: 'UNAUTHORIZED', message: 'expired' } });
        await flush();
        if (ending === 'abort') test.owner.abort(new Error('owner ended'));
        else await test.clock.advance(10_000);
        const outcome = await result;
        if (ending === 'abort') expect(outcome).toBeInstanceOf(Error);
        else { expect(outcome.mode).toBe('events'); source.lease.committed('refresh-pending'); }
        await test.transport.close();
        if (completion === 'resolve') refresh.resolve('obsolete-token');
        else refresh.reject(new Error('obsolete refresh failed'));
        await flush();
        expect(socket.messages('auth')).toHaveLength(1);
        expect(socket.readyState).toBe(3);
        expect(test.sockets).toHaveLength(1);
        expect(test.clock.timers.size).toBe(0);
      });
    }
  }

  it('cancels when the provider synchronously aborts before returning a pending credential', async () => {
    const token = deferred<string>();
    let test!: ReturnType<typeof fixture>;
    test = fixture({ getToken: () => { test.owner.abort(new Error('closed from provider')); return token.promise; } });
    const source = test.acquire();
    expect(await source.read('synchronous-abort').catch(error => error)).toBeInstanceOf(Error);
    await test.transport.close();
    token.resolve('late');
    await flush();
    expect(test.sockets).toHaveLength(0);
    expect(test.requests).toHaveLength(0);
    expect(test.clock.timers.size).toBe(0);
  });

  for (const binding of ['sourceHash', 'databaseIdentity'] as const) {
    it(`isolates a correlated ${binding} mismatch from another alias on the same socket`, async () => {
      const test = fixture();
      const bad = test.acquire('bad');
      const good = test.acquire('good');
      await flush();
      const socket = await test.authenticate();
      const rejected = bad.read('bad-binding', { expectedSourceHash: identity.sourceHash,
        expectedDatabaseIdentity: identity.databaseIdentity }).catch(error => error);
      await flush();
      await test.register(socket);
      socket.respond(socket.messages('replica_read')[0]!, { ...page(), [binding]: 'b'.repeat(binding === 'sourceHash' ? 64 : 16) });
      expect(await rejected).toMatchObject({ code: binding === 'sourceHash' ? 'ReplicaSourceMismatch' : 'ReplicaIdentityMismatch' });
      expect(socket.readyState).toBe(1);
      const result = good.read('good-binding');
      await flush();
      await test.register(socket);
      socket.respond(socket.messages('replica_read')[1]!);
      expect((await result).mode).toBe('events');
      good.lease.committed('good-binding');
      expect(test.requests).toHaveLength(0);
      expect(test.sockets).toHaveLength(1);
    });
  }

  it('retries a transient Axios refresh failure with authentication only and retains its original error', async () => {
    const reason = new AxiosError('Refresh unavailable', 'ERR_BAD_RESPONSE', undefined, undefined, {
      status: 503, statusText: 'Unavailable', headers: {}, config: {} as InternalAxiosRequestConfig, data: {},
    });
    let calls = 0;
    const test = fixture({ refreshToken: async () => { if (++calls === 1) throw reason; return 'refreshed-token'; } });
    const source = test.acquire();
    const first = source.read('refresh-failed').catch(error => error);
    await flush();
    const socket = test.sockets[0]!;
    socket.open();
    socket.receive({ id: socket.messages('auth')[0]!.id, type: 'error', payload: { code: 'UNAUTHORIZED', message: 'expired' } });
    expect(await first).toBe(reason);
    expect(test.requests).toHaveLength(0);
    await test.clock.advance(1_000);
    expect(test.sockets).toHaveLength(2);
    const recovered = test.sockets[1]!;
    recovered.open();
    recovered.receive({ id: recovered.messages('auth')[0]!.id, type: 'error', payload: { code: 'UNAUTHORIZED', message: 'expired' } });
    await flush();
    expect(recovered.messages('auth')[1]!.payload.token).toBe('refreshed-token');
    recovered.auth();
    await flush();
    expect(recovered.messages('subscribe')).toHaveLength(0);
    const retry = source.read('refresh-recovered');
    await flush();
    await test.register(recovered);
    recovered.respond(recovered.messages('replica_read')[0]!);
    expect((await retry).mode).toBe('events');
    source.lease.committed('refresh-recovered');
    expect(calls).toBe(2);
    expect(test.requests).toHaveLength(0);
  });

});
