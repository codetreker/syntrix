import type { AxiosInstance } from 'axios';
import { AuthSessionChangedError, SyntrixError } from '../../api/errors.js';
import type { TokenProvider } from '../auth/types.js';
import { createReplicaHttpSource } from './source.js';
import { copySourceDefinition } from './source-codec.js';
import { connectReplicaSocket, ReplicaSocketError, type ReplicaSocket } from './replica-socket.js';
import type { ReplicaSourceAdapter, SourceLease, SourceLeaseContext, SourceReadContext } from './source-types.js';
import { ReplicaStorageError, type FrozenSourceDefinition } from './storage-types.js';

export type ReplicaSourceTransportEnvironment = {
  socket(url: string): WebSocket;
  now(): number;
  random(): number;
  setTimeout(callback: () => void, ms: number): ReturnType<typeof setTimeout>;
  clearTimeout(timer: ReturnType<typeof setTimeout>): void;
};
export type ReplicaSourceTransportDiagnostic = {
  subId?: string; requestId?: string; transportEpoch: number; mode: 'ws' | 'http'; physicalEpoch?: string;
};
type Registration = { socket: ReplicaSocket; subId: string; expected: string | null; epoch: number };
type Receipt = { alias: string; requestId: string; accepted: boolean; registration?: Registration; release(): void };
type Waiter = { alias: string; signal: AbortSignal; accept(receipt: Receipt): void; reject(error: unknown): void; requestId: string; cleanup(): void };
type AliasState = { retryAt: number; backoff?: unknown };
type LeaseState = { active: boolean; context: SourceLeaseContext; alias: string; invalidate(): void; onSocketError(error: unknown): void };
const transportFailure = (error: unknown): boolean => error instanceof ReplicaSocketError
  && (error.kind === 'transport' || error.kind === 'protocol');
const retryableSource = (error: unknown): boolean => {
  if (transportFailure(error)) return false;
  const status = field(error, 'status') ?? field(field(error, 'response'), 'status');
  if (status === 429 || typeof status === 'number' && status >= 500 && status <= 599) return true;
  return status === undefined && ['ERR_NETWORK', 'ECONNABORTED', 'ETIMEDOUT', 'ECONNRESET'].includes(String(field(error, 'code')));
};
const unavailable = (message: string) => new ReplicaSocketError('REPLICATION_TRANSPORT_UNAVAILABLE', message, 'transport');
const field = (error: unknown, key: string): unknown => typeof error === 'object' && error !== null ? Reflect.get(error, key) : undefined;

export const createReplicaSourceTransport = (options: {
  axios: AxiosInstance; provider: TokenProvider; endpoint: string; database: string;
  sessionVersion: number; signal: AbortSignal;
  onDiagnostic?(alias: string, phase: string, details: ReplicaSourceTransportDiagnostic, error?: unknown): void;
}, environment: Partial<ReplicaSourceTransportEnvironment> = {}) => {
  const env: ReplicaSourceTransportEnvironment = {
    socket: url => new WebSocket(url), now: Date.now, random: Math.random,
    setTimeout: (callback, ms) => setTimeout(callback, ms), clearTimeout: timer => clearTimeout(timer), ...environment,
  };
  const leases = new Set<LeaseState>();
  const aliasStates = new Map<string, AliasState>();
  const credits = new Map<string, Receipt>();
  const waiters: Waiter[] = [];
  let closed = false, epoch = 0, retry = 0;
  let socket: ReplicaSocket | undefined;
  let attempt: { controller: AbortController; epoch: number; promise: Promise<ReplicaSocket> } | undefined;
  let connectionError: unknown;
  let connectionRetryAt = 0;
  let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  const assert = () => {
    options.signal.throwIfAborted();
    if (options.provider.getSessionVersion() !== options.sessionVersion) throw new AuthSessionChangedError();
    if (closed) throw new ReplicaStorageError('ReplicaDatabaseClosed', 'Replica source transport is closed');
  };
  const live = () => !closed && !options.signal.aborted && options.provider.getSessionVersion() === options.sessionVersion;
  const report = (owner: LeaseState, phase: string, details: Partial<ReplicaSourceTransportDiagnostic> = {}, error?: unknown) => {
    if (!live() || !owner.active || !leases.has(owner) || owner.context.signal.aborted) return;
    options.onDiagnostic?.(owner.alias, phase, { transportEpoch: epoch, mode: socket ? 'ws' : 'http',
      physicalEpoch: owner.context.scope.physicalEpoch, ...details }, error);
  };
  const cancelRetry = () => { if (reconnectTimer !== undefined) env.clearTimeout(reconnectTimer); reconnectTimer = undefined; };
  const stopSocket = () => {
    cancelRetry();
    const old = attempt; attempt = undefined;
    const connected = socket; socket = undefined;
    ++epoch;
    old?.controller.abort(unavailable('Replica connection owner retired'));
    connected?.close();
  };
  const scheduleReconnect = () => {
    if (!live() || !leases.size || reconnectTimer !== undefined || socket || attempt) return;
    if (connectionError !== undefined && !transportFailure(connectionError) && !retryableSource(connectionError)) return;
    const delay = Math.min(30_000, 1_000 * 2 ** Math.min(retry++, 5)) * (0.5 + env.random() * 0.5);
    reconnectTimer = env.setTimeout(() => { reconnectTimer = undefined; if (live() && leases.size) startSocket(); }, Math.max(delay, connectionRetryAt - env.now()));
  };
  const connectionFailed = (error: unknown) => {
    connectionError = error;
    if (retryableSource(error)) {
      const seconds = field(error, 'retryAfter');
      connectionRetryAt = env.now() + Math.max(1_000, typeof seconds === 'number' && Number.isFinite(seconds) ? seconds * 1_000 : 0);
    } else connectionRetryAt = 0;
    for (const owner of [...leases]) owner.onSocketError(error);
    scheduleReconnect();
  };
  const startSocket = (): void => {
    if (!live() || !leases.size || socket || attempt) return;
    if (connectionError !== undefined && !transportFailure(connectionError)
      && (!retryableSource(connectionError) || env.now() < connectionRetryAt)) return;
    cancelRetry();
    const controller = new AbortController();
    const currentEpoch = ++epoch;
    const current = { controller, epoch: currentEpoch, promise: undefined as unknown as Promise<ReplicaSocket> };
    attempt = current;
    current.promise = connectReplicaSocket({ ...options, signal: AbortSignal.any([options.signal, controller.signal]) }, env);
    for (const owner of [...leases]) report(owner, 'connect', { transportEpoch: currentEpoch });
    void current.promise.then(connected => {
      if (!live() || attempt !== current || !leases.size) { connected.close(); return; }
      attempt = undefined;
      if (connected.closed) { connectionFailed(connected.closeError ?? unavailable('Replica connection closed')); return; }
      socket = connected; connectionError = undefined; connectionRetryAt = 0; retry = 0;
      connected.onClose(error => {
        if (socket !== connected) return;
        socket = undefined;
        connectionFailed(error);
      });
      for (const owner of [...leases]) {
        report(owner, 'recovered', { transportEpoch: currentEpoch, mode: 'ws' });
        if (live() && owner.active && !owner.context.signal.aborted) owner.context.hint();
      }
    }, error => {
      if (attempt !== current) return;
      attempt = undefined;
      connectionFailed(error);
    });
  };
  const pump = () => {
    if (!live()) return;
    for (let index = 0; index < waiters.length && credits.size < 4;) {
      const waiter = waiters[index]!;
      if (credits.has(waiter.alias)) { index++; continue; }
      waiters.splice(index, 1); waiter.cleanup();
      if (waiter.signal.aborted) { waiter.reject(waiter.signal.reason); continue; }
      let released = false;
      const receipt: Receipt = { alias: waiter.alias, requestId: waiter.requestId, accepted: false, release: () => {
        if (released) return;
        released = true;
        if (credits.get(waiter.alias) === receipt) credits.delete(waiter.alias);
        pump();
      } };
      credits.set(waiter.alias, receipt);
      waiter.accept(receipt);
    }
  };
  const reserve = (alias: string, requestId: string, signal: AbortSignal): Promise<Receipt> => new Promise((resolve, reject) => {
    try { assert(); signal.throwIfAborted(); } catch (error) { reject(error); return; }
    if (waiters.some(waiter => waiter.alias === alias)) { reject(new ReplicaStorageError('ReplicaRoundBusy', 'An alias already has a queued source read')); return; }
    const cancel = () => {
      const index = waiters.indexOf(waiter);
      if (index >= 0) waiters.splice(index, 1);
      waiter.cleanup(); reject(signal.reason); pump();
    };
    const timeout = env.setTimeout(() => {
      const index = waiters.indexOf(waiter);
      if (index >= 0) waiters.splice(index, 1);
      waiter.cleanup();
      reject(new SyntrixError('REPLICA_PAGE_CAPACITY', 'Replica page admission timed out', 429, undefined, 1));
      pump();
    }, 45_000);
    const waiter: Waiter = { alias, requestId, signal, accept: resolve, reject, cleanup: () => {
      signal.removeEventListener('abort', cancel); env.clearTimeout(timeout);
    } };
    signal.addEventListener('abort', cancel, { once: true });
    if (signal.aborted) { cancel(); return; }
    waiters.push(waiter); pump();
  });
  const wait = <T>(operation: () => Promise<T>, signal: AbortSignal): Promise<T> => new Promise((resolve, reject) => {
    let settled = false;
    const finish = (error?: unknown, value?: T) => {
      if (settled) return;
      settled = true; signal.removeEventListener('abort', abort);
      if (error !== undefined) reject(error); else resolve(value!);
    };
    const abort = () => finish(signal.reason);
    signal.addEventListener('abort', abort, { once: true });
    if (signal.aborted) { abort(); return; }
    try { void operation().then(value => finish(undefined, value), error => finish(error)); }
    catch (error) { finish(error); }
  });
  const close = async (): Promise<void> => {
    if (closed) return;
    closed = true; options.signal.removeEventListener('abort', abort);
    for (const owner of [...leases]) owner.invalidate();
    stopSocket();
    for (const waiter of waiters.splice(0)) { waiter.cleanup(); waiter.reject(options.signal.reason ?? unavailable('Replica transport closed')); }
  };
  const abort = () => { void close(); };
  options.signal.addEventListener('abort', abort, { once: true });
  if (options.signal.aborted) abort();

  return {
    close,
    source(alias: string, sourceDefinition: FrozenSourceDefinition): ReplicaSourceAdapter {
      const definition = copySourceDefinition(sourceDefinition);
      const mode = definition.limit === null ? 'events' as const : 'replace' as const;
      const http = createReplicaHttpSource({ ...options, definition });
      let state = aliasStates.get(alias);
      if (!state) { state = { retryAt: 0 }; aliasStates.set(alias, state); }
      const aliasState = state;
      return {
        definition, mode,
        read: async () => { throw new ReplicaStorageError('ReplicaScopeChanged', 'Source reads require an elected owner lease'); },
        acquire(context): SourceLease {
          assert(); context.signal.throwIfAborted();
          if (context.scope.sessionVersion !== options.sessionVersion) throw new AuthSessionChangedError();
          const controller = new AbortController();
          const signal = AbortSignal.any([context.signal, options.signal, controller.signal]);
          let registration: Registration | undefined;
          let receipt: Receipt | undefined;
          let reading = false, firstRead = true, closedLease = false;
          let sourceError: unknown;
          const owner: LeaseState = { active: true, context, alias,
            invalidate: () => invalidate(),
            onSocketError: error => {
              registration = undefined;
              if (!transportFailure(error) && !retryableSource(error)) sourceError = error;
              report(owner, transportFailure(error) ? 'fallback' : 'closed', {}, error);
            },
          };
          const active = () => { assert(); signal.throwIfAborted(); if (!owner.active) throw new ReplicaStorageError('ReplicaScopeChanged', 'Replica source lease retired'); };
          const remember = (error: unknown) => {
            if (retryableSource(error)) {
              const seconds = field(error, 'retryAfter');
              const delay = typeof seconds === 'number' && Number.isFinite(seconds) && seconds >= 0 ? seconds * 1_000 : 1_000;
              aliasState.retryAt = Math.max(aliasState.retryAt, env.now() + delay);
              aliasState.backoff = error;
            }
          };
          const assertBackoff = () => {
            if (aliasState.backoff && aliasState.retryAt > env.now()) {
              if (aliasState.backoff instanceof SyntrixError) {
                throw new SyntrixError(aliasState.backoff.code, aliasState.backoff.message, aliasState.backoff.status, undefined, (aliasState.retryAt - env.now()) / 1_000);
              }
              throw aliasState.backoff;
            }
          };
          const invalidate = () => {
            if (!owner.active) return;
            owner.active = false; leases.delete(owner);
            context.signal.removeEventListener('abort', invalidate);
            const old = registration; registration = undefined;
            controller.abort(signal.aborted ? signal.reason : new ReplicaStorageError('ReplicaScopeChanged', 'Replica source lease retired'));
            old?.socket.unsubscribe(old.subId);
            if (!leases.size) stopSocket();
          };
          const release = (requestId: string, committed: boolean) => {
            if (!receipt || receipt.requestId !== requestId) return;
            const current = receipt; receipt = undefined;
            try {
              if (committed && current.accepted && live() && owner.active) {
                current.registration?.socket.ack(current.registration.subId, requestId);
                report(owner, 'committed', { requestId, subId: current.registration?.subId,
                  mode: current.registration ? 'ws' : 'http', transportEpoch: current.registration?.epoch ?? epoch });
              }
            } finally { current.release(); }
          };
          context.signal.addEventListener('abort', invalidate, { once: true });
          leases.add(owner);
          if (signal.aborted) invalidate();
          startSocket();
          scheduleReconnect();
          return {
            definition, mode, invalidate,
            committed: requestId => { try { release(requestId, true); } catch (error) { console.error('Replica receipt diagnostic failed', error); } },
            released: requestId => release(requestId, false),
            close: async () => { if (closedLease) return; closedLease = true; invalidate(); if (receipt) release(receipt.requestId, false); },
            read: async (request: SourceReadContext) => {
              active(); request.signal.throwIfAborted(); assertBackoff();
              if (sourceError !== undefined) {
                if (retryableSource(sourceError) && env.now() >= aliasState.retryAt) sourceError = undefined;
                else throw sourceError;
              }
              if (reading || receipt) throw new ReplicaStorageError('ReplicaRoundBusy', 'An alias already has an outstanding source page');
              if (request.sessionVersion !== options.sessionVersion) throw new AuthSessionChangedError();
              reading = true;
              const readSignal = AbortSignal.any([signal, request.signal]);
              try {
                receipt = await reserve(alias, request.requestId, readSignal);
                active(); readSignal.throwIfAborted(); assertBackoff();
                const opening = attempt;
                if (firstRead && opening) {
                  firstRead = false;
                  try { await wait(() => opening.promise, readSignal); }
                  catch (error) { if (!transportFailure(error)) throw error; }
                }
                firstRead = false;
                active(); readSignal.throwIfAborted(); assertBackoff();
                if (connectionError !== undefined && !transportFailure(connectionError)) {
                  if (!retryableSource(connectionError)) throw connectionError;
                  if (env.now() < connectionRetryAt) throw connectionError;
                  startSocket();
                  const retrying = attempt;
                  if (retrying) await wait(() => retrying.promise, readSignal);
                  active(); readSignal.throwIfAborted();
                  if (connectionError !== undefined && !transportFailure(connectionError)) throw connectionError;
                }
                let page;
                const connected = socket;
                if (connected && !connected.closed) {
                  try {
                    if (!registration || registration.socket !== connected || registration.expected !== request.expectedDatabaseIdentity) {
                      const old = registration; registration = undefined;
                      old?.socket.unsubscribe(old.subId);
                      const current: Registration = { socket: connected, subId: crypto.randomUUID(), expected: request.expectedDatabaseIdentity, epoch };
                      registration = current;
                      await connected.register(current.subId, definition, current.expected, signal,
                        () => { if (owner.active && registration === current && live()) owner.context.hint(); },
                        error => {
                          if (registration !== current || !owner.active) return;
                          registration = undefined; remember(error);
                          if (!transportFailure(error)) sourceError = error;
                          report(owner, transportFailure(error) ? 'fallback' : 'closed', { subId: current.subId }, error);
                          if (owner.active && live()) owner.context.hint();
                        });
                      active(); readSignal.throwIfAborted();
                      report(owner, 'subscribed', { subId: current.subId }); active();
                      owner.context.hint(); active();
                    }
                    const current = registration!;
                    report(owner, 'read', { requestId: request.requestId, subId: current.subId, mode: 'ws' });
                    active(); readSignal.throwIfAborted();
                    page = await connected.read(current.subId, { ...request, signal: readSignal }, definition);
                    active(); readSignal.throwIfAborted(); receipt.registration = current;
                  } catch (error) {
                    active(); readSignal.throwIfAborted(); remember(error);
                    if (!transportFailure(error)) throw error;
                    const old = registration; registration = undefined;
                    old?.socket.unsubscribe(old.subId);
                    report(owner, 'fallback', { requestId: request.requestId, mode: 'http' }, error); active();
                  }
                }
                if (!page) {
                  active(); readSignal.throwIfAborted(); assertBackoff();
                  const httpAbort = new AbortController();
                  const httpSignal = AbortSignal.any([readSignal, httpAbort.signal]);
                  const deadline = env.setTimeout(() => httpAbort.abort(unavailable('Replica HTTP read timed out')), 45_000);
                  report(owner, 'read', { requestId: request.requestId, mode: 'http' });
                  try { active(); page = await wait(() => http.read({ ...request, signal: httpSignal }), httpSignal); }
                  finally { env.clearTimeout(deadline); }
                }
                active(); readSignal.throwIfAborted(); receipt.accepted = true;
                report(owner, 'accepted', { requestId: request.requestId, subId: receipt.registration?.subId, mode: receipt.registration ? 'ws' : 'http' });
                active(); readSignal.throwIfAborted(); return page;
              } catch (error) {
                remember(error);
                if (receipt) release(request.requestId, false);
                throw error;
              } finally { reading = false; }
            },
          };
        },
      };
    },
  };
};
