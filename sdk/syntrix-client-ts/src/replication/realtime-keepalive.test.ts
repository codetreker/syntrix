import { afterEach, beforeEach, describe, expect, it, mock, spyOn } from 'bun:test';
import axios, { AxiosError, InternalAxiosRequestConfig } from 'axios';
import { DefaultTokenProvider } from '../internal/auth/provider';
import { setupAuthInterceptor } from '../internal/auth/interceptor';
import { SyntrixClient } from '../clients/syntrix-client';
import { AuthSessionChangedError } from '../api/errors';
import { TokenProvider } from '../internal/auth/types';
import { BaseMessage, RealtimeClient, RealtimeClientOptions } from './realtime';

class ControlledWebSocket {
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSING = 2;
  static CLOSED = 3;
  static instances: ControlledWebSocket[] = [];
  readyState = ControlledWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onclose: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  sent: BaseMessage[] = [];

  constructor(readonly url: string) {
    ControlledWebSocket.instances.push(this);
  }

  open() {
    this.readyState = ControlledWebSocket.OPEN;
    this.onopen?.();
  }

  send(data: string) {
    if (this.readyState !== ControlledWebSocket.OPEN) throw new Error('Socket is not open');
    this.sent.push(JSON.parse(data));
  }

  close() {
    this.readyState = ControlledWebSocket.CLOSED;
    this.onclose?.();
  }

  receive(message: BaseMessage) {
    this.onmessage?.({ data: JSON.stringify(message) });
  }

  messages(type: string) {
    return this.sent.filter(message => message.type === type);
  }

  authenticate() {
    const request = this.messages('auth').slice(-1)[0]!;
    expect(request).toBeDefined();
    this.receive({ id: request.id, type: 'auth_ack' });
  }

  ready(subId: string) {
    const request = this.messages('subscribe').find(message => message.id === subId)!;
    expect(request).toBeDefined();
    this.receive({ id: request.id, type: 'subscribe_ack', payload: { subId } });
  }
}

const flush = async () => {
  for (let i = 0; i < 12; i++) await Promise.resolve();
};

const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};

const query = { query: { collection: 'orders' } };
const event = (subId: string) => ({
  type: 'event', payload: { subId, delta: { type: 'update', id: 'one', timestamp: 1 } },
});

// Explicit timer advancement keeps reconnect and authentication deadline races reproducible.
class Clock {
  now = 1_000;
  private nextId = 0;
  timers = new Map<number, { at: number; repeat: number; callback: () => void }>();
  timeout = (callback: () => void, delay = 0) => this.add(callback, delay, false);
  interval = (callback: () => void, delay = 0) => this.add(callback, delay, true);
  clear = (id: number) => { this.timers.delete(id); };
  private add(callback: () => void, delay: number, repeat: boolean) {
    const id = ++this.nextId;
    this.timers.set(id, { at: this.now + Math.max(1, delay), repeat: repeat ? Math.max(1, delay) : 0, callback });
    return id;
  }
  async advance(ms: number) {
    const end = this.now + ms;
    for (;;) {
      const next = [...this.timers].filter(([, timer]) => timer.at <= end)
        .sort((a, b) => a[1].at - b[1].at)[0];
      if (!next) break;
      const [id, timer] = next;
      this.now = timer.at;
      if (timer.repeat) timer.at += timer.repeat;
      else this.timers.delete(id);
      timer.callback();
      await flush();
    }
    this.now = end;
    await flush();
  }
}

describe('Realtime subscription lifecycle', () => {
  const originals = {
    WebSocket: globalThis.WebSocket, setTimeout, clearTimeout, setInterval, clearInterval,
    now: Date.now, random: Math.random, error: console.error, warn: console.warn,
  };
  let clock: Clock;
  let clients: RealtimeClient[];
  let logs: ReturnType<typeof mock>;

  beforeEach(() => {
    clock = new Clock();
    clients = [];
    ControlledWebSocket.instances = [];
    globalThis.WebSocket = ControlledWebSocket as unknown as typeof WebSocket;
    globalThis.setTimeout = clock.timeout as unknown as typeof setTimeout;
    globalThis.setInterval = clock.interval as unknown as typeof setInterval;
    globalThis.clearTimeout = clock.clear as unknown as typeof clearTimeout;
    globalThis.clearInterval = clock.clear as unknown as typeof clearInterval;
    Date.now = () => clock.now;
    Math.random = () => 0.5;
    logs = mock(() => {});
    console.error = logs;
    console.warn = mock(() => {});
  });

  afterEach(() => {
    for (const client of clients) client.dispose();
    const timerCount = clock.timers.size;
    const openSockets = ControlledWebSocket.instances.filter(ws => ws.readyState !== ControlledWebSocket.CLOSED).length;
    Object.assign(globalThis, {
      WebSocket: originals.WebSocket, setTimeout: originals.setTimeout, clearTimeout: originals.clearTimeout,
      setInterval: originals.setInterval, clearInterval: originals.clearInterval,
    });
    Date.now = originals.now;
    Math.random = originals.random;
    console.error = originals.error;
    console.warn = originals.warn;
    expect(timerCount).toBe(0);
    expect(openSockets).toBe(0);
  });

  const create = (options?: RealtimeClientOptions, provider?: Partial<TokenProvider>) => {
    const client = new RealtimeClient('ws://localhost/realtime/ws', {
      getSessionVersion: () => 0, getToken: async () => 'test-token', refreshToken: async () => 'fresh-token', ...provider,
    } as TokenProvider, 'test-db', options);
    clients.push(client);
    return client;
  };

  const connect = async (client: RealtimeClient) => {
    const pending = client.connect();
    const ws = ControlledWebSocket.instances.slice(-1)[0]!;
    ws.open();
    await flush();
    ws.authenticate();
    await pending;
    return ws;
  };

  for (const phase of ['before open', 'token pending', 'auth acknowledgment'] as const) {
    it(`rejects an authentication session changed during ${phase}`, async () => {
      let session = 1;
      const token = deferred<string>();
      const getToken = mock(() => phase === 'token pending' ? token.promise : Promise.resolve('old-token'));
      const client = create({ reconnectDelayMs: 10 }, { getSessionVersion: () => session, getToken });
      client.subscribe(query);
      const pending = client.connect();
      const outcome = pending.then(() => undefined, error => error);
      const ws = ControlledWebSocket.instances[0];
      if (phase !== 'before open') {
        ws.open();
        await flush();
      }
      session++;
      if (phase === 'before open') ws.open();
      else if (phase === 'token pending') token.resolve('old-token');
      else ws.authenticate();
      await flush();
      const error = await outcome;
      expect(error).toBeInstanceOf(AuthSessionChangedError);
      expect(error.code).toBe('AUTH_SESSION_CHANGED');
      expect(ws.messages('subscribe')).toHaveLength(0);
      if (phase === 'before open') expect(getToken).not.toHaveBeenCalled();
      if (phase === 'token pending') expect(ws.messages('auth')).toHaveLength(0);
      expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
      await clock.advance(100);
      expect(ControlledWebSocket.instances).toHaveLength(1);
      expect(clock.timers.size).toBe(0);
    });
  }

  it('does not refresh another session after an old authentication rejection', async () => {
    let session = 1;
    const refreshToken = mock(async () => 'new-session-token');
    const client = create(undefined, { getSessionVersion: () => session, refreshToken });
    const pending = client.connect();
    const outcome = pending.then(() => undefined, error => error);
    const ws = ControlledWebSocket.instances[0];
    ws.open();
    await flush();
    session++;
    ws.receive({ id: ws.messages('auth')[0].id, type: 'error',
      payload: { code: 'unauthorized', message: 'invalid token' } });
    expect(await outcome).toBeInstanceOf(AuthSessionChangedError);
    expect(refreshToken).not.toHaveBeenCalled();
    expect(ws.messages('auth')).toHaveLength(1);
    expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
  });

  for (const result of ['resolve', 'reject'] as const) {
    it(`rejects a session replaced while refresh is pending before its ${result}`, async () => {
      let session = 1;
      const refresh = deferred<string>();
      const client = create(undefined, { getSessionVersion: () => session, refreshToken: () => refresh.promise });
      const pending = client.connect();
      const outcome = pending.then(() => undefined, error => error);
      const ws = ControlledWebSocket.instances[0];
      ws.open();
      await flush();
      ws.receive({ id: ws.messages('auth')[0].id, type: 'error',
        payload: { code: 'unauthorized', message: 'invalid token' } });
      session++;
      if (result === 'resolve') refresh.resolve('old-session-refreshed-token');
      else refresh.reject(new Error('old refresh failed'));
      expect(await outcome).toBeInstanceOf(AuthSessionChangedError);
      expect(ws.messages('auth')).toHaveLength(1);
      expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
      expect(clock.timers.size).toBe(0);
    });
  }

  it('invalidates one shared HTTP and WebSocket refresh when the real provider logs out', async () => {
    const onTokenRefresh = mock(() => {});
    const onAuthError = mock(() => {});
    const provider = new DefaultTokenProvider({
      token: 'A', refreshToken: 'R1', onTokenRefresh, onAuthError,
    }, 'https://localhost');
    const refresh = deferred<{ data: { access_token: string; refresh_token: string } }>();
    const originalPost = axios.post;
    const post = mock(async (url: string, body: unknown) => {
      if (url.endsWith('/refresh')) return refresh.promise;
      expect(url).toBe('https://localhost/auth/v1/logout');
      expect(body).toEqual({ refresh_token: 'R1' });
      return { data: {} };
    });
    axios.post = post as typeof axios.post;
    const refreshCalls = spyOn(provider, 'refreshToken');
    try {
      const client = new RealtimeClient('ws://localhost/realtime/ws', provider, 'test-db');
      clients.push(client);
      const wsResult = client.connect().then(() => undefined, error => error);
      const ws = ControlledWebSocket.instances[0];
      ws.open();
      await flush();
      expect(ws.messages('auth')[0].payload.token).toBe('A');
      ws.receive({ id: ws.messages('auth')[0].id, type: 'error',
        payload: { code: 'unauthorized', message: 'invalid token' } });
      await flush();
      expect(refreshCalls).toHaveBeenCalledTimes(1);

      const adapter = mock(async (config: InternalAxiosRequestConfig) => {
        expect(config.headers.get('Authorization')).toBe('Bearer A');
        throw new AxiosError('Authentication failed', 'ERR_BAD_REQUEST', config, undefined, {
          config, data: {}, headers: {}, status: 401, statusText: 'Unauthorized',
        });
      });
      const http = axios.create({ adapter });
      setupAuthInterceptor(http, provider);
      const httpResult = http.get('/document').then(() => undefined, error => error);
      await flush();
      expect(refreshCalls).toHaveBeenCalledTimes(2);
      expect(post).toHaveBeenCalledTimes(1);
      expect(post).toHaveBeenCalledWith('https://localhost/auth/v1/refresh', { refresh_token: 'R1' });

      await provider.logout();
      refresh.resolve({ data: { access_token: 'obsolete-access', refresh_token: 'obsolete-refresh' } });
      expect(await httpResult).toBeInstanceOf(AuthSessionChangedError);
      expect(await wsResult).toBeInstanceOf(AuthSessionChangedError);
      expect(await provider.getToken()).toBeNull();
      expect(provider.isAuthenticated()).toBe(false);
      expect(onTokenRefresh).not.toHaveBeenCalled();
      expect(onAuthError).not.toHaveBeenCalled();
      expect(adapter).toHaveBeenCalledTimes(1);
      expect(ws.messages('auth')).toHaveLength(1);
      expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
      expect(clock.timers.size).toBe(0);
      expect(post).toHaveBeenCalledTimes(2);
      await expect(provider.refreshToken()).rejects.toThrow('No refresh token available');
    } finally {
      refreshCalls.mockRestore();
      axios.post = originalPost;
    }
  });

  it('does not run a queued reconnect after its authentication session changes', async () => {
    let session = 1;
    const client = create({ reconnectDelayMs: 10 }, { getSessionVersion: () => session });
    const first = await connect(client);
    first.close();
    session++;
    await clock.advance(100);
    expect(ControlledWebSocket.instances).toHaveLength(1);
    expect(clock.timers.size).toBe(0);
    expect(client.getState()).toBe('disconnected');
  });

  for (const authenticated of [false, true]) {
    it(`explicit connect replaces a changed session with a ${authenticated ? 'previously authenticated' : 'pending'} attempt`, async () => {
      let session = 1;
      const client = create(undefined, {
        getSessionVersion: () => session,
        getToken: async () => `token-${session}`,
      });
      const oldPromise = client.connect();
      const oldOutcome = oldPromise.then(() => undefined, error => error);
      const oldSocket = ControlledWebSocket.instances[0];
      oldSocket.open();
      await flush();
      if (authenticated) {
        oldSocket.authenticate();
        await oldPromise;
      }
      session++;
      const replacement = client.connect();
      expect(replacement).not.toBe(oldPromise);
      expect(oldSocket.readyState).toBe(ControlledWebSocket.CLOSED);
      expect(ControlledWebSocket.instances).toHaveLength(2);
      const newSocket = ControlledWebSocket.instances[1];
      newSocket.open();
      await flush();
      expect(newSocket.messages('auth')[0].payload.token).toBe('token-2');
      newSocket.authenticate();
      await replacement;
      if (!authenticated) expect(await oldOutcome).toBeInstanceOf(AuthSessionChangedError);
      expect(client.getState()).toBe('connected');
    });
  }

  it('stops event dispatch when a subscription callback replaces the authentication session', async () => {
    let session = 1;
    const client = create(undefined, { getSessionVersion: () => session });
    const global = mock(() => {});
    const onEvent = mock(() => { session++; });
    const id = client.subscribe(query, { onEvent });
    client.on('onEvent', global);
    const ws = await connect(client);
    ws.receive(event(id));
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(global).not.toHaveBeenCalled();
    ws.receive(event(id));
    expect(onEvent).toHaveBeenCalledTimes(1);
    expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
    expect(clock.timers.size).toBe(0);
  });

  it('shares a handshake and gates all registrations on matching authentication acknowledgment', async () => {
    const client = create();
    expect(client.getState()).toBe('disconnected');
    expect(client.getLastMessageTime()).toBe(0);
    const onConnect = mock(() => {});
    client.on('onConnect', onConnect);
    const a = client.subscribe(query);
    const pending = client.connect();
    expect(client.connect()).toBe(pending);
    expect(ControlledWebSocket.instances).toHaveLength(1);
    const ws = ControlledWebSocket.instances[0];
    const b = client.subscribe(query);
    ws.open();
    await flush();
    expect(ws.messages('auth')[0].payload).toEqual({ token: 'test-token', database: 'test-db' });
    expect(ws.messages('subscribe')).toHaveLength(0);
    expect(client.getState()).toBe('connecting');
    let settled = false;
    void pending.then(() => { settled = true; });
    ws.receive({ type: 'auth_ack', id: 'another-request' });
    await flush();
    expect(settled).toBe(false);
    ws.authenticate();
    await pending;
    expect(client.getLastMessageTime()).toBe(clock.now);
    expect(ws.messages('subscribe').map(message => message.id)).toEqual([a, b]);
    ws.authenticate();
    expect(ws.messages('subscribe')).toHaveLength(2);
    expect(onConnect).toHaveBeenCalledTimes(1);
    await client.connect();
    expect(ControlledWebSocket.instances).toHaveLength(1);
  });

  it('isolates subscription events, snapshots, readiness and errors while preserving global observers', async () => {
    const client = create();
    const a = { onEvent: mock(() => {}), onSnapshot: mock(() => {}), onReady: mock(() => {}), onError: mock(() => {}) };
    const b = { onEvent: mock(() => {}), onSnapshot: mock(() => {}), onReady: mock(() => {}), onError: mock(() => {}) };
    const globalEvent = mock(() => {});
    const globalError = mock(() => {});
    client.on('onEvent', globalEvent).on('onError', globalError);
    const aId = client.subscribe(query, a);
    const bId = client.subscribe(query, b);
    const ws = await connect(client);
    ws.receive(event(aId));
    ws.receive({ type: 'snapshot', payload: JSON.stringify({ subId: bId, documents: [{ id: 'one' }] }) });
    expect(a.onEvent).toHaveBeenCalledTimes(1);
    expect(b.onEvent).not.toHaveBeenCalled();
    expect(a.onSnapshot).not.toHaveBeenCalled();
    expect(b.onSnapshot).toHaveBeenCalledTimes(1);
    expect(globalEvent).toHaveBeenCalledTimes(1);
    ws.ready(aId);
    ws.ready(aId);
    expect(a.onReady).toHaveBeenCalledTimes(1);
    expect(b.onReady).not.toHaveBeenCalled();
    ws.receive({ type: 'error', id: bId, payload: { message: 'Invalid collection' } });
    expect(b.onError).toHaveBeenCalledTimes(1);
    expect(a.onError).not.toHaveBeenCalled();
    expect(globalError).toHaveBeenCalledTimes(1);
    ws.ready(bId);
    expect(b.onReady).not.toHaveBeenCalled();
    client.unsubscribe(aId);
    client.unsubscribe(aId);
    ws.receive(event(aId));
    ws.ready(aId);
    expect(a.onEvent).toHaveBeenCalledTimes(1);
    expect(globalEvent).toHaveBeenCalledTimes(1);
    expect(ws.messages('unsubscribe')).toHaveLength(1);
    expect(ws.readyState).toBe(ControlledWebSocket.OPEN);
  });

  for (const code of ['snapshot_failed', 'snapshot_limit']) {
    it(`preserves acknowledged live delivery after ${code}`, async () => {
      const client = create();
      const a = { onReady: mock(() => {}), onEvent: mock(() => {}), onError: mock(() => {}) };
      const b = { onReady: mock(() => {}), onEvent: mock(() => {}), onError: mock(() => {}) };
      const globalEvent = mock(() => {});
      const globalError = mock(() => {});
      client.on('onEvent', globalEvent).on('onError', globalError);
      const aId = client.subscribe({ ...query, sendSnapshot: true }, a);
      const bId = client.subscribe(query, b);
      const ws = await connect(client);
      ws.ready(aId);
      ws.ready(bId);
      ws.receive({ type: 'error', id: aId, payload: { code, message: 'Initial snapshot unavailable' } });
      expect(a.onError).toHaveBeenCalledTimes(1);
      expect(globalError).toHaveBeenCalledTimes(1);
      expect(b.onError).not.toHaveBeenCalled();
      ws.receive(event(aId));
      ws.receive(event(bId));
      expect(a.onEvent).toHaveBeenCalledTimes(1);
      expect(b.onEvent).toHaveBeenCalledTimes(1);
      expect(globalEvent).toHaveBeenCalledTimes(2);
      ws.ready(aId);
      expect(a.onReady).toHaveBeenCalledTimes(1);
      expect(b.onReady).toHaveBeenCalledTimes(1);
      expect(client.getState()).toBe('connected');
      expect(ws.messages('subscribe')).toHaveLength(2);
      expect(ws.messages('unsubscribe')).toHaveLength(0);
      expect(ControlledWebSocket.instances).toHaveLength(1);

      client.unsubscribe(aId);
      ws.receive(event(aId));
      ws.receive({ type: 'error', id: aId, payload: { code, message: 'Late snapshot failure' } });
      expect(a.onEvent).toHaveBeenCalledTimes(1);
      expect(globalEvent).toHaveBeenCalledTimes(2);
      expect(a.onError).toHaveBeenCalledTimes(1);
      expect(globalError).toHaveBeenCalledTimes(1);
      expect(ws.messages('unsubscribe')).toHaveLength(1);
    });
  }

  for (const { acknowledged, code } of [
    { acknowledged: false, code: 'snapshot_failed' },
    { acknowledged: false, code: 'snapshot_limit' },
    { acknowledged: true, code: 'permission_denied' },
  ]) {
    it(`retains terminal subscription errors: acknowledged=${acknowledged}, code=${code}`, async () => {
      const client = create();
      const onReady = mock(() => {});
      const onEvent = mock(() => {});
      const onError = mock(() => {});
      const id = client.subscribe({ ...query, sendSnapshot: true }, { onReady, onEvent, onError });
      const ws = await connect(client);
      if (acknowledged) ws.ready(id);
      ws.receive({ type: 'error', id, payload: { code, message: 'Subscription unavailable' } });
      ws.ready(id);
      ws.receive(event(id));
      expect(onReady).toHaveBeenCalledTimes(acknowledged ? 1 : 0);
      expect(onEvent).not.toHaveBeenCalled();
      expect(onError).toHaveBeenCalledTimes(1);

      // A late snapshot error must not revive a registration already rejected.
      ws.receive({ type: 'error', id, payload: { code: 'snapshot_failed', message: 'Late snapshot failure' } });
      ws.receive(event(id));
      expect(onEvent).not.toHaveBeenCalled();
    });
  }

  it('does not send or restore a subscription removed before authentication', async () => {
    const client = create();
    const ready = mock(() => {});
    const id = client.subscribe(query, { onReady: ready });
    expect(ControlledWebSocket.instances).toHaveLength(0);
    client.unsubscribe(id);
    const ws = await connect(client);
    expect(ws.messages('subscribe')).toHaveLength(0);
    expect(ws.messages('unsubscribe')).toHaveLength(0);
    ws.receive({ id, type: 'subscribe_ack', payload: { subId: id } });
    expect(ready).not.toHaveBeenCalled();
  });

  it('restores a failed registration only on a later connection', async () => {
    const client = create();
    const ready = mock(() => {});
    const error = mock(() => {});
    const id = client.subscribe(query, { onReady: ready, onError: error });
    const first = await connect(client);
    first.receive({ type: 'error', id, payload: { message: 'Collection unavailable' } });
    expect(error).toHaveBeenCalledTimes(1);
    first.ready(id);
    expect(ready).not.toHaveBeenCalled();
    expect(first.messages('subscribe')).toHaveLength(1);
    client.disconnect();
    const second = await connect(client);
    second.ready(id);
    expect(ready).toHaveBeenCalledTimes(1);
  });

  it('resubscribes retained registrations once per connection and ignores old socket callbacks', async () => {
    const client = create({ reconnectDelayMs: 10 });
    const ready = mock(() => {});
    const callback = mock(() => {});
    const id = client.subscribe(query, { onReady: ready, onEvent: callback });
    const first = await connect(client);
    first.ready(id);
    const staleMessage = first.onmessage!;
    const staleClose = first.onclose!;
    first.close();
    await clock.advance(10);
    const second = ControlledWebSocket.instances[1];
    second.open();
    await flush();
    second.authenticate();
    second.ready(id);
    second.ready(id);
    staleMessage({ data: JSON.stringify(event(id)) });
    staleClose();
    expect(callback).not.toHaveBeenCalled();
    expect(ready).toHaveBeenCalledTimes(2);
    expect(client.getState()).toBe('connected');
    expect(second.messages('subscribe')).toHaveLength(1);
  });

  it('manual connect cancels a queued retry and disconnect preserves subscriptions without reconnecting', async () => {
    const client = create({ reconnectDelayMs: 10 });
    const id = client.subscribe(query);
    const first = await connect(client);
    first.close();
    const second = await connect(client);
    await clock.advance(20);
    expect(ControlledWebSocket.instances).toHaveLength(2);
    expect(second.messages('subscribe')[0].id).toBe(id);
    client.disconnect();
    client.disconnect();
    await clock.advance(10_000);
    expect(ControlledWebSocket.instances).toHaveLength(2);
    const third = await connect(client);
    expect(third.messages('subscribe')[0].id).toBe(id);
  });

  for (const failure of ['close', 'error', 'disconnect', 'dispose'] as const) {
    it(`settles a pending handshake on ${failure}`, async () => {
      const client = create({ maxReconnectAttempts: 0 });
      const pending = client.connect();
      const rejected = pending.then(() => { throw new Error('Expected handshake rejection'); }, error => error);
      const ws = ControlledWebSocket.instances[0];
      if (failure === 'close') ws.close();
      else if (failure === 'error') ws.onerror?.();
      else client[failure]();
      expect(await rejected).toBeInstanceOf(Error);
      expect(clock.timers.size).toBe(0);
    });
  }

  it('uses a hard authentication deadline even when heartbeats keep arriving', async () => {
    const client = create({ activityTimeoutMs: 90, maxReconnectAttempts: 0 });
    const pending = client.connect();
    const rejected = pending.then(() => { throw new Error('Expected handshake rejection'); }, error => error);
    const ws = ControlledWebSocket.instances[0];
    ws.open();
    await flush();
    for (let i = 0; i < 3; i++) {
      await clock.advance(25);
      ws.receive({ type: 'heartbeat' });
    }
    await clock.advance(16);
    expect(await rejected).toBeInstanceOf(Error);
    expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
  });

  it('keeps authenticated connections alive on heartbeat and reconnects after activity stops', async () => {
    const client = create({ activityTimeoutMs: 90, reconnectDelayMs: 10 });
    const disconnected = mock(() => {});
    const callback = mock(() => {});
    client.on('onDisconnect', disconnected).on('onEvent', callback);
    const ws = await connect(client);
    for (let i = 0; i < 6; i++) {
      await clock.advance(30);
      ws.receive({ type: 'heartbeat' });
      expect(client.getLastMessageTime()).toBe(clock.now);
    }
    expect(disconnected).not.toHaveBeenCalled();
    expect(callback).not.toHaveBeenCalled();
    await clock.advance(140);
    expect(disconnected).toHaveBeenCalledTimes(1);
    expect(ControlledWebSocket.instances).toHaveLength(2);
  });

  it('refreshes exactly once for correlated unauthorized authentication errors', async () => {
    const refreshToken = mock(async () => 'fresh-token');
    const client = create({ maxReconnectAttempts: 0 }, { refreshToken });
    const pending = client.connect();
    const rejected = pending.then(() => { throw new Error('Expected handshake rejection'); }, error => error);
    const ws = ControlledWebSocket.instances[0];
    ws.open();
    await flush();
    const first = ws.messages('auth')[0];
    ws.receive({ type: 'error', id: 'unrelated', payload: { code: 'unauthorized', message: 'invalid token' } });
    expect(refreshToken).not.toHaveBeenCalled();
    ws.receive({ type: 'error', id: first.id, payload: { code: 'unauthorized', message: 'invalid token' } });
    await flush();
    expect(refreshToken).toHaveBeenCalledTimes(1);
    const retry = ws.messages('auth')[1];
    expect(retry.payload.token).toBe('fresh-token');
    expect(retry.id).not.toBe(first.id);
    ws.receive({ type: 'auth_ack', id: first.id });
    expect(client.getState()).not.toBe('connected');
    ws.receive({ type: 'error', id: retry.id, payload: { code: 'unauthorized', message: 'invalid token' } });
    expect(await rejected).toBeInstanceOf(Error);
    expect(refreshToken).toHaveBeenCalledTimes(1);
  });

  for (const mode of ['missing token', 'token rejection', 'refresh rejection', 'invalid auth'] as const) {
    it(`terminates authentication on ${mode}`, async () => {
      const original = new Error('provider failed');
      const provider = {
        getToken: mock(async () => {
          if (mode === 'token rejection') throw original;
          return mode === 'missing token' ? null : 'token';
        }),
        refreshToken: mock(async () => { throw original; }),
      };
      const client = create({ maxReconnectAttempts: 0 }, provider);
      const reported = mock(() => {});
      client.subscribe(query, { onError: reported });
      const pending = client.connect();
      const rejected = pending.then(() => { throw new Error('Expected handshake rejection'); }, error => error);
      const ws = ControlledWebSocket.instances[0];
      ws.open();
      await flush();
      if (mode === 'refresh rejection' || mode === 'invalid auth') {
        ws.receive({ type: 'error', id: ws.messages('auth')[0].id, payload: {
          code: mode === 'invalid auth' ? 'invalid_auth' : 'unauthorized', message: 'invalid token',
        } });
      }
      expect(await rejected).toBeInstanceOf(Error);
      expect(reported).toHaveBeenCalledTimes(1);
      expect(ws.messages('subscribe')).toHaveLength(0);
      expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
    });
  }

  for (const phase of ['token', 'refresh'] as const) {
    for (const outcome of ['resolve', 'reject'] as const) {
      it(`ignores a late ${phase} ${outcome} after disconnect and replacement`, async () => {
        const late = deferred<string>();
        let calls = 0;
        const client = create({ maxReconnectAttempts: 0 }, {
          getToken: async () => ++calls === 1 && phase === 'token' ? late.promise : 'token',
          refreshToken: () => late.promise,
        });
        const pending = client.connect();
        const rejected = pending.then(() => { throw new Error('Expected handshake rejection'); }, error => error);
        const old = ControlledWebSocket.instances[0];
        old.open();
        await flush();
        if (phase === 'refresh') old.receive({ type: 'error', id: old.messages('auth')[0].id,
          payload: { code: 'unauthorized', message: 'invalid token' } });
        client.disconnect();
        expect(await rejected).toBeInstanceOf(Error);
        const replacement = await connect(client);
        const sent = replacement.sent.length;
        if (outcome === 'resolve') late.resolve('late-token');
        else late.reject(new Error('late provider failure'));
        await flush();
        expect(replacement.sent).toHaveLength(sent);
        expect(old.messages('auth')).toHaveLength(phase === 'token' ? 0 : 1);
        expect(client.getState()).toBe('connected');
      });
    }
  }

  it('exhausts failed handshake retries despite each socket opening', async () => {
    const client = create({ maxReconnectAttempts: 2, reconnectDelayMs: 10 });
    const pending = client.connect();
    const rejected = pending.then(() => { throw new Error('Expected handshake rejection'); }, error => error);
    for (let attempt = 0; attempt < 3; attempt++) {
      const ws = ControlledWebSocket.instances[attempt];
      ws.open();
      await flush();
      ws.close();
      await clock.advance(10 * 2 ** attempt);
    }
    expect(await rejected).toBeInstanceOf(Error);
    await clock.advance(1_000);
    expect(ControlledWebSocket.instances).toHaveLength(3);
    expect(clock.timers.size).toBe(0);
  });

  it('isolates callback exceptions from protocol parsing and other observers', async () => {
    const client = create();
    const global = mock(() => {});
    const error = new Error('consumer failure');
    const reported = mock(() => {});
    const id = client.subscribe(query, { onEvent: () => { throw error; } });
    client.on('onEvent', global).on('onError', reported);
    const ws = await connect(client);
    ws.receive(event(id));
    expect(global).toHaveBeenCalledTimes(1);
    expect(reported).toHaveBeenCalledWith(error);
    expect(logs).not.toHaveBeenCalled();
  });

  it('reports connection failure once to every active subscription even when one error callback throws', async () => {
    const client = create({ maxReconnectAttempts: 0 });
    const firstError = mock(() => { throw new Error('error callback failed'); });
    const secondError = mock(() => {});
    const globalError = mock(() => {});
    client.subscribe(query, { onError: firstError });
    client.subscribe(query, { onError: secondError });
    client.on('onError', globalError);
    const pending = client.connect();
    const rejected = pending.then(() => { throw new Error('Expected handshake rejection'); }, error => error);
    const ws = ControlledWebSocket.instances[0];
    ws.onerror?.();
    ws.close();
    expect(await rejected).toBeInstanceOf(Error);
    expect(firstError).toHaveBeenCalledTimes(1);
    expect(secondError).toHaveBeenCalledTimes(1);
    expect(globalError).toHaveBeenCalledTimes(1);
  });

  it('does not report an old connection failure to subscriptions created by a state callback', async () => {
    const client = create({ maxReconnectAttempts: 0 });
    const newError = mock(() => {});
    const ws = await connect(client);
    let replacement: Promise<void> | undefined;
    client.on('onStateChange', state => {
      if (state !== 'disconnected' || replacement) return;
      client.subscribe(query, { onError: newError });
      replacement = client.connect();
      void replacement.catch(() => {});
    });
    ws.close();
    expect(newError).not.toHaveBeenCalled();
    const next = ControlledWebSocket.instances[1];
    next.open();
    await flush();
    next.authenticate();
    await replacement;
    expect(newError).not.toHaveBeenCalled();
  });

  it('stops an error broadcast when a callback explicitly disconnects', async () => {
    const client = create({ maxReconnectAttempts: 0 });
    const firstError = mock(() => client.disconnect());
    const secondError = mock(() => {});
    const globalError = mock(() => {});
    client.subscribe(query, { onError: firstError });
    client.subscribe(query, { onError: secondError });
    client.on('onError', globalError);
    const ws = await connect(client);
    ws.close();
    expect(firstError).toHaveBeenCalledTimes(1);
    expect(secondError).not.toHaveBeenCalled();
    expect(globalError).not.toHaveBeenCalled();
    expect(clock.timers.size).toBe(0);
  });

  it('suppresses remaining error reporting after an error callback disposes and throws', async () => {
    const client = create({ maxReconnectAttempts: 0 });
    const globalError = mock(() => {});
    client.subscribe(query, { onError: () => {
      client.dispose();
      throw new Error('callback failed after disposal');
    } });
    client.on('onError', globalError);
    const ws = await connect(client);
    ws.close();
    expect(globalError).not.toHaveBeenCalled();
    expect(logs).not.toHaveBeenCalled();
    expect(clock.timers.size).toBe(0);
  });

  it('allows disposal inside callbacks without continuing registration or dispatch', async () => {
    const client = create();
    const global = mock(() => {});
    const id = client.subscribe(query, { onEvent: () => client.dispose() });
    client.on('onEvent', global);
    const ws = await connect(client);
    ws.receive(event(id));
    expect(global).not.toHaveBeenCalled();
    expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
    expect(() => client.subscribe(query)).toThrow();
    expect(() => client.on('onEvent', global)).toThrow();
    await expect(client.connect()).rejects.toBeInstanceOf(Error);
    client.unsubscribe(id);
    client.disconnect();
    client.dispose();
  });

  it('stops connecting when a state callback disposes the client', async () => {
    const client = create();
    client.on('onStateChange', state => { if (state === 'connecting') client.dispose(); });
    await expect(client.connect()).rejects.toBeInstanceOf(Error);
    expect(ControlledWebSocket.instances.every(ws => ws.readyState === ControlledWebSocket.CLOSED)).toBe(true);
    expect(clock.timers.size).toBe(0);
  });

  it('a readiness callback can remove another subscription before its acknowledgment', async () => {
    const client = create();
    const secondReady = mock(() => {});
    let second = '';
    const first = client.subscribe(query, { onReady: () => client.unsubscribe(second) });
    second = client.subscribe(query, { onReady: secondReady });
    const ws = await connect(client);
    ws.ready(first);
    ws.ready(second);
    expect(secondReady).not.toHaveBeenCalled();
    expect(ws.messages('unsubscribe')).toHaveLength(1);
    expect(client.getState()).toBe('connected');
  });

  it('convenience subscriptions each receive one shared connection failure', async () => {
    const sdk = new SyntrixClient('https://localhost', { database: 'test-db' });
    const rt = sdk.realtime();
    clients.push(rt);
    const firstError = mock(() => {});
    const secondError = mock(() => {});
    sdk.subscribe('orders', { onError: firstError });
    sdk.subscribe('users', { onError: secondError });
    const ws = ControlledWebSocket.instances[0];
    ws.open();
    await flush();
    expect(firstError).toHaveBeenCalledTimes(1);
    expect(secondError).toHaveBeenCalledTimes(1);
    expect(ws.readyState).toBe(ControlledWebSocket.CLOSED);
    expect(clock.timers.size).toBe(0);
  });

  it('convenience subscriptions share automatic connection, callbacks and logout cleanup', async () => {
    const sdk = new SyntrixClient('https://localhost', { database: 'test-db', auth: { token: 'token' } });
    const rt = sdk.realtime();
    clients.push(rt);
    const global = mock(() => {});
    const a = { onEvent: mock(() => {}), onReady: mock(() => {}) };
    const b = { onEvent: mock(() => {}), onReady: mock(() => {}) };
    rt.on('onEvent', global);
    const first = sdk.subscribe('orders', a);
    const second = sdk.subscribe('users', b);
    expect(ControlledWebSocket.instances).toHaveLength(1);
    const pending = rt.connect();
    const ws = ControlledWebSocket.instances[0];
    expect(ws.url).toBe('wss://localhost/realtime/ws');
    ws.open();
    await flush();
    ws.authenticate();
    await pending;
    expect(ws.messages('subscribe').map(message => message.payload.query.collection)).toEqual(['orders', 'users']);
    ws.ready(first.subId);
    ws.ready(second.subId);
    expect(a.onReady).toHaveBeenCalledTimes(1);
    expect(b.onReady).toHaveBeenCalledTimes(1);
    ws.receive(event(first.subId));
    expect(a.onEvent).toHaveBeenCalledTimes(1);
    expect(b.onEvent).not.toHaveBeenCalled();
    expect(global).toHaveBeenCalledTimes(1);
    first.unsubscribe();
    ws.receive(event(second.subId));
    expect(b.onEvent).toHaveBeenCalledTimes(1);
    const staleMessage = ws.onmessage!;
    await sdk.logout();
    staleMessage({ data: JSON.stringify(event(second.subId)) });
    expect(b.onEvent).toHaveBeenCalledTimes(1);
    await expect(rt.connect()).rejects.toBeInstanceOf(Error);
    expect(clock.timers.size).toBe(0);
    const replacement = sdk.realtime();
    clients.push(replacement);
    expect(replacement).not.toBe(rt);
  });
});
