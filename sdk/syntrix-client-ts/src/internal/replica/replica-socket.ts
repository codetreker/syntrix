import { AuthSessionChangedError, SyntrixError } from '../../api/errors.js';
import type { TokenProvider } from '../auth/types.js';
import { decodeSourcePage, encodeSourceDefinition, encodeSourceRequest, sourcePageBytes } from './source-codec.js';
import type { SourceEventsPage, SourceReadContext, SourceWindow } from './source-types.js';
import { ReplicaStorageError, type FrozenSourceDefinition } from './storage-types.js';

type Page = SourceEventsPage | SourceWindow;
export class ReplicaSocketError extends SyntrixError {
  constructor(
    code: string, message: string, public readonly kind: 'transport' | 'source' | 'auth' | 'protocol',
    status = 503, retryAfter?: number, public readonly cause?: unknown,
  ) { super(code, message, status, undefined, retryAfter); this.name = 'ReplicaSocketError'; }
}
export interface ReplicaSocket {
  readonly closed: boolean;
  readonly closeError?: Error;
  register(subId: string, definition: FrozenSourceDefinition, expectedDB: string | null,
    signal: AbortSignal, onChanged: () => void, onError: (error: Error) => void): Promise<void>;
  read(subId: string, context: SourceReadContext, definition: FrozenSourceDefinition): Promise<Page>;
  ack(subId: string, requestId: string): void;
  unsubscribe(subId: string): void;
  close(): void;
  onClose(callback: (error: Error) => void): () => void;
}
export type ReplicaSocketEnvironment = {
  socket(url: string): WebSocket;
  setTimeout(callback: () => void, ms: number): ReturnType<typeof setTimeout>;
  clearTimeout(timer: ReturnType<typeof setTimeout>): void;
};
type Pending<T> = { resolve(value: T): void; reject(error: unknown): void; cleanup(): void };
type Read = Pending<Page> & { context: SourceReadContext; definition: FrozenSourceDefinition; delivered: boolean };
type Subscription = {
  id: string; registered: boolean; registration?: Pending<void>; read?: Read; lastCommitted?: string;
  onChanged(): void; onError(error: Error): void; cleanup(): void; expectedDB: string | null;
};
const transportError = (message: string, cause?: unknown): ReplicaSocketError =>
  new ReplicaSocketError('REPLICATION_TRANSPORT_UNAVAILABLE', message, 'transport', 503, undefined, cause);
const protocolError = (message: string): ReplicaSocketError =>
  new ReplicaSocketError('REPLICATION_PROTOCOL_ERROR', message, 'protocol', 400);
const record = (value: unknown): Record<string, unknown> => {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) throw protocolError('Invalid replica envelope');
  return value as Record<string, unknown>;
};
const asError = (error: unknown): Error => error instanceof Error ? error : new Error(String(error));
const wireErrorStatuses: Readonly<Record<string, number>> = {
  BAD_REQUEST: 400, INVALID_REPLICATION_SOURCE: 400, NO_MATCHING_INDEX: 400,
  REPLICATION_PROTOCOL_ERROR: 400, UNAUTHORIZED: 401, FORBIDDEN: 403,
  DATABASE_SUSPENDED: 403, DATABASE_NOT_FOUND: 404, RESYNC_REQUIRED: 409,
  DATABASE_IDENTITY_MISMATCH: 409, DATABASE_DELETING: 410, REQUEST_TOO_LARGE: 413,
  QUERY_WORK_LIMIT: 422, REPLICATION_BUDGET_EXCEEDED: 422, REPLICATION_SOURCE_BUSY: 429,
  INTERNAL_ERROR: 500, REPLICATION_UNSUPPORTED: 501, REPLICATION_UNAVAILABLE: 503,
  INDEX_UNAVAILABLE: 503, REPLICATION_WINDOW_INCOMPLETE: 503,
  REPLICATION_TRANSPORT_BUSY: 503, REPLICATION_TRANSPORT_UNAVAILABLE: 503, DEADLINE_EXCEEDED: 504,
};
const errorFromWire = (payload: Record<string, unknown>): ReplicaSocketError => {
  if (typeof payload.code !== 'string' || typeof payload.message !== 'string' ||
      (payload.retryAfter !== undefined && (typeof payload.retryAfter !== 'number' || !Number.isFinite(payload.retryAfter) || payload.retryAfter < 0))) {
    throw protocolError('Invalid replica error');
  }
  const code = payload.code;
  const kind = code === 'UNAUTHORIZED' || code === 'FORBIDDEN' ? 'auth'
    : code === 'REPLICATION_PROTOCOL_ERROR' ? 'protocol'
    : code === 'REPLICATION_TRANSPORT_BUSY' || code === 'REPLICATION_TRANSPORT_UNAVAILABLE'
      ? 'transport' : 'source';
  // Unknown source failures remain failures; a new server code must not cause
  // a second request through HTTP to bypass the authoritative rejection.
  const status = Object.prototype.hasOwnProperty.call(wireErrorStatuses, code) ? wireErrorStatuses[code]! : 500;
  return new ReplicaSocketError(code, payload.message, kind, status, payload.retryAfter as number | undefined);
};

export const connectReplicaSocket = (options: {
  endpoint: string; database: string; provider: TokenProvider; sessionVersion: number; signal: AbortSignal;
}, environment?: Partial<ReplicaSocketEnvironment>): Promise<ReplicaSocket> => {
  const env: ReplicaSocketEnvironment = {
    socket: url => new WebSocket(url), setTimeout: (callback, ms) => setTimeout(callback, ms),
    clearTimeout: timer => clearTimeout(timer), ...environment,
  };
  const subscriptions = new Map<string, Subscription>();
  const closeListeners = new Set<(error: Error) => void>();
  const utf8 = new TextEncoder();
  let socket: WebSocket | undefined;
  let closed = false;
  let closeError: Error | undefined;
  let authenticated = false;
  let authId = 'replica-auth-1';
  let refreshing = false;
  let refreshed = false;
  let handshake: Pending<ReplicaSocket>;
  let cancelCredentialWait: ((reason: unknown) => void) | undefined;
  const assertCurrent = (): void => {
    options.signal.throwIfAborted();
    if (options.provider.getSessionVersion() !== options.sessionVersion) throw new AuthSessionChangedError();
    if (closed) throw closeError ?? transportError('Replica socket is closed');
  };
  const notify = (callback: () => void): void => {
    try { callback(); } catch (error) { console.error('Replica socket callback failed', error); }
  };
  const retire = (sub: Subscription, error: unknown): void => {
    if (subscriptions.get(sub.id) !== sub) return;
    subscriptions.delete(sub.id); sub.cleanup();
    sub.registration?.reject(error); sub.read?.reject(error);
  };
  const stop = (error: unknown, report: boolean): void => {
    if (closed) return;
    if (options.signal.aborted) error = options.signal.reason;
    else if (options.provider.getSessionVersion() !== options.sessionVersion) error = new AuthSessionChangedError();
    closed = true; closeError = asError(error);
    options.signal.removeEventListener('abort', abort);
    cancelCredentialWait?.(error);
    handshake.reject(error);
    for (const sub of [...subscriptions.values()]) retire(sub, error);
    if (socket) {
      socket.onopen = null; socket.onmessage = null; socket.onclose = null; socket.onerror = null;
      try { socket.close(); } catch { /* Closing cannot restore ownership. */ }
    }
    if (report) for (const callback of [...closeListeners]) notify(() => callback(closeError!));
    closeListeners.clear();
  };
  const abort = (): void => stop(options.signal.reason, true);
  const send = (id: string, type: string, payload: unknown): void => {
    assertCurrent();
    if (!socket || socket.readyState !== 1) throw transportError('Replica socket is not open');
    try { socket.send(JSON.stringify({ id, type, payload })); }
    catch (error) { throw transportError(`Replica send failed: ${asError(error).message}`); }
  };
  const pending = <T>(resolve: (value: T) => void, reject: (error: unknown) => void,
    ms: number, timeout: () => void): Pending<T> => {
    let settled = false;
    const timer = env.setTimeout(timeout, ms);
    return {
      cleanup: () => env.clearTimeout(timer),
      resolve: value => { if (!settled) { settled = true; env.clearTimeout(timer); resolve(value); } },
      reject: error => { if (!settled) { settled = true; env.clearTimeout(timer); reject(error); } },
    };
  };
  const api: ReplicaSocket = {
    get closed() { return closed; }, get closeError() { return closeError; },
    register: (id, definition, expectedDB, signal, onChanged, onError) => new Promise<void>((resolve, reject) => {
      let owner: Subscription | undefined;
      try {
        assertCurrent(); signal.throwIfAborted();
        if (!authenticated || subscriptions.has(id)) throw protocolError('Invalid replica registration ownership');
        const sub: Subscription = { id, registered: false, onChanged, onError, expectedDB, cleanup: () => signal.removeEventListener('abort', cancel) };
        owner = sub;
        const cancel = (): void => {
          retire(sub, signal.reason);
          try { send(id, 'unsubscribe', { subId: id }); } catch (error) { stop(error, true); }
        };
        sub.registration = pending(resolve, reject, 10_000, () => stop(transportError('Replica registration timed out'), true));
        subscriptions.set(id, sub); signal.addEventListener('abort', cancel, { once: true });
        send(id, 'subscribe', { collection: definition.collection, source: encodeSourceDefinition(definition),
          ...(expectedDB === null ? {} : { expectedDatabaseIdentity: expectedDB }) });
      } catch (error) {
        reject(error);
        if (owner) retire(owner, error);
        if (!signal.aborted || options.signal.aborted) stop(error, true);
      }
    }),
    read: (id, context, definition) => new Promise<Page>((resolve, reject) => {
      let sub: Subscription | undefined;
      try {
        assertCurrent(); context.signal.throwIfAborted();
        if (context.sessionVersion !== options.sessionVersion) throw new AuthSessionChangedError();
        sub = subscriptions.get(id);
        if (!sub?.registered || sub.read) throw protocolError('Replica read has no available registration');
        const owner = sub;
        const cancel = (): void => { retire(owner, context.signal.reason); api.unsubscribe(id); };
        const request = encodeSourceRequest(definition, context);
        const deferred = pending<Page>(resolve, reject, 45_000, () => stop(transportError('Replica read timed out'), true));
        const cleanup = (): void => { deferred.cleanup(); context.signal.removeEventListener('abort', cancel); };
        sub.read = { ...deferred, cleanup, context, definition, delivered: false,
          resolve: value => { cleanup(); deferred.resolve(value); }, reject: error => { cleanup(); deferred.reject(error); } };
        context.signal.addEventListener('abort', cancel, { once: true });
        send(context.requestId, 'replica_read', { subId: id, requestId: context.requestId, request,
          ...(context.expectedDatabaseIdentity === null ? {} : { expectedDatabaseIdentity: context.expectedDatabaseIdentity }),
          ...(context.expectedSourceHash === null ? {} : { expectedSourceHash: context.expectedSourceHash }) });
      } catch (error) { reject(error); if (sub?.read) stop(error, true); }
    }),
    ack: (id, requestId) => {
      try {
        assertCurrent();
        const sub = subscriptions.get(id);
        if (!sub) return;
        if (sub.lastCommitted === requestId) return;
        if (!sub.read?.delivered || sub.read.context.requestId !== requestId) throw protocolError('Replica receipt does not own a delivered page');
        sub.read = undefined; sub.lastCommitted = requestId;
        send(requestId, 'replica_ack', { subId: id, requestId });
      } catch (error) { stop(error, true); }
    },
    unsubscribe: id => {
      const sub = subscriptions.get(id);
      if (sub) retire(sub, new DOMException('Replica subscription closed', 'AbortError'));
      if (closed) return;
      try { send(id, 'unsubscribe', { subId: id }); } catch (error) { stop(error, true); }
    },
    close: () => stop(transportError('Replica socket closed'), false),
    onClose: callback => { closeListeners.add(callback); return () => { closeListeners.delete(callback); }; },
  };
  const credentials = (operation: () => Promise<string | null>): Promise<string | null> => new Promise((resolve, reject) => {
    let settled = false;
    const finish = (complete: () => void): void => {
      if (settled) return;
      settled = true;
      if (cancelCredentialWait === cancel) cancelCredentialWait = undefined;
      complete();
    };
    const cancel = (reason: unknown): void => finish(() => reject(reason));
    // Install ownership before invoking the provider, which can synchronously
    // invalidate the session. Shared refresh work itself must not be canceled.
    cancelCredentialWait = cancel;
    try {
      assertCurrent();
      Promise.resolve(operation()).then(
        token => finish(() => resolve(token)),
        error => finish(() => reject(error)),
      );
    } catch (error) { cancel(error); }
  });
  const authenticate = async (refresh: boolean): Promise<void> => {
    try {
      assertCurrent();
      const token = await credentials(() => refresh ? options.provider.refreshToken() : options.provider.getToken());
      assertCurrent();
      if (!token) throw new ReplicaSocketError('UNAUTHORIZED', 'Replica authentication requires a token', 'auth', 401);
      if (refresh) { refreshing = false; authId = 'replica-auth-2'; send(authId, 'auth', { token, database: options.database, mode: 'replica-data' }); return; }
      const url = new URL(options.endpoint);
      url.protocol = url.protocol === 'https:' || url.protocol === 'wss:' ? 'wss:' : 'ws:';
      url.pathname = `${url.pathname.replace(/\/$/, '')}/realtime/ws`; url.search = '?mode=replica-data'; url.hash = '';
      try { socket = env.socket(url.toString()); }
      catch (cause) { throw transportError('Replica socket construction failed', cause); }
      socket.onopen = () => { try { send(authId, 'auth', { token, database: options.database, mode: 'replica-data' }); } catch (error) { stop(error, true); } };
      socket.onclose = () => stop(transportError('Replica socket disconnected'), true);
      socket.onerror = () => stop(transportError('Replica socket failed'), true);
      socket.onmessage = event => {
        try { assertCurrent(); receive(event.data); } catch (error) { stop(error, true); }
      };
    } catch (error) { if (!closed) stop(error, true); }
  };
  const receive = (data: unknown): void => {
    if (typeof data !== 'string' || data.length > sourcePageBytes + 1024 || utf8.encode(data).byteLength > sourcePageBytes + 1024) {
      throw protocolError('Replica frame exceeds its text or byte limit');
    }
    let envelope: Record<string, unknown>;
    try { envelope = record(JSON.parse(data)); } catch { throw protocolError('Invalid replica JSON frame'); }
    if (typeof envelope.type !== 'string') throw protocolError('Replica frame has no type');
    if (envelope.type === 'heartbeat') return;
    if (typeof envelope.id !== 'string' || envelope.id.length === 0) throw protocolError('Replica frame has no correlation ID');
    const payload = record(envelope.payload);
    if (!authenticated) {
      if (refreshed && envelope.id === 'replica-auth-1' && (refreshing || authId !== 'replica-auth-1')) return;
      if (envelope.id !== authId) throw protocolError('Unexpected replica authentication correlation');
      if (envelope.type === 'auth_ack' && payload.mode === 'replica-data') {
        authenticated = true; handshake.resolve(api); return;
      }
      if (envelope.type !== 'error') throw protocolError('Unexpected replica authentication response');
      const error = errorFromWire(payload);
      if (error.code === 'UNAUTHORIZED' && !refreshed) { refreshed = true; refreshing = true; void authenticate(true); return; }
      stop(error, true); return;
    }
    if (envelope.type === 'auth_ack' && envelope.id === authId) return;
    const id = payload.subId;
    if (typeof id !== 'string') {
      if (envelope.type === 'error' && envelope.id === authId) { stop(errorFromWire(payload), true); return; }
      if (envelope.type === 'error' && payload.subId === undefined && payload.requestId === undefined) {
        const registering = subscriptions.get(envelope.id);
        if (!registering) return;
        if (!registering.registration) throw protocolError('Replica error has no subscription identity');
        retire(registering, errorFromWire(payload)); return;
      }
      throw protocolError('Replica response has no subscription identity');
    }
    const sub = subscriptions.get(id);
    // Registration IDs are fresh per attempt. No retired-ID history is needed
    // to discard delayed frames from owners no longer present in this map.
    if (!sub) return;
    if (envelope.type === 'replica_page') {
      const requestId = payload.requestId;
      if (typeof requestId !== 'string' || envelope.id !== requestId) throw protocolError('Replica page request identity differs');
      if (requestId === sub.lastCommitted) return;
      const read = sub.read;
      if (!read || read.context.requestId !== requestId) throw protocolError('Replica page belongs to an unknown request');
      if (read.delivered) return;
      let page: Page;
      try { page = decodeSourcePage(payload.page, read.definition, read.context); }
      catch (error) {
        if (error instanceof ReplicaStorageError && (error.code === 'ReplicaIdentityMismatch' || error.code === 'ReplicaSourceMismatch')) {
          retire(sub, error);
          api.unsubscribe(sub.id);
          notify(() => sub.onError(error));
          return;
        }
        if (error instanceof ReplicaStorageError && (error.code === 'ReplicaSourceInvalid' || error.code === 'ReplicaSourceResponseTooLarge')) {
          throw protocolError(error.message);
        }
        throw error;
      }
      assertCurrent(); read.context.signal.throwIfAborted();
      read.delivered = true; read.resolve(page); return;
    }
    if (envelope.type === 'error') {
      const error = errorFromWire(payload);
      if (payload.requestId !== undefined) {
        if (envelope.id !== payload.requestId || sub.read?.context.requestId !== payload.requestId) {
          throw protocolError('Replica error belongs to an unknown request');
        }
        if (sub.read.delivered) { retire(sub, error); notify(() => sub.onError(error)); return; }
        const read = sub.read; sub.read = undefined; read.reject(error); return;
      }
      if (envelope.id !== id) throw protocolError('Replica subscription error identity differs');
      const registered = sub.registered; retire(sub, error);
      if (registered) notify(() => sub.onError(error));
      return;
    }
    if (envelope.id !== id) throw protocolError('Replica subscription correlation differs');
    if (envelope.type === 'subscribe_ack') {
      if (typeof payload.databaseIdentity !== 'string' || !/^[0-9a-f]{16}$/.test(payload.databaseIdentity)) throw protocolError('Invalid replica registration identity');
      if (sub.expectedDB !== null && payload.databaseIdentity !== sub.expectedDB) {
        const error = new ReplicaSocketError('DATABASE_IDENTITY_MISMATCH', 'Replica registration database identity changed', 'source', 409);
        retire(sub, error); return;
      }
      sub.registered = true; sub.registration?.resolve(); sub.registration = undefined; return;
    }
    if (envelope.type === 'replica_changed') { notify(() => sub.onChanged()); return; }
    throw protocolError('Unsupported replica response type');
  };
  return new Promise<ReplicaSocket>((resolve, reject) => {
    handshake = pending(resolve, reject, 10_000, () => stop(transportError('Replica authentication timed out'), true));
    options.signal.addEventListener('abort', abort, { once: true });
    if (options.signal.aborted) abort(); else void authenticate(false);
  });
};
