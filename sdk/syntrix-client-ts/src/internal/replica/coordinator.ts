import { defaultHashSha256, type RxReplicationWriteToMasterRow, type WithDeleted } from 'rxdb';
import { BehaviorSubject, type Observable } from 'rxjs';
import type { AliasStorage } from './storage.js';
import { ReplicaStorageError, type ReplicaRecord } from './storage-types.js';
import type { ReplicaSourceAdapter } from './source-types.js';
import { createDownstreamAdapter } from './downstream.js';
import { createReplicationRuntime, type ReplicationRuntime } from './runtime.js';
import { businessEqual, canonicalJson } from './records.js';
import { compactAlias } from './compaction.js';
import { createReplicaLeadership, ReplicaCleanupError, throwCleanupFailures, type ReplicaLeadership } from './leadership.js';

export type ReplicaDownstreamStatus = Readonly<{
  state: 'waiting' | 'syncing' | 'idle' | 'retrying' | 'blocked' | 'closed';
  leader: boolean;
  ready: boolean;
  generation: string | null;
  error?: unknown;
  retryAt?: number;
}>;
export interface ReplicaDownstream {
  readonly snapshot: ReplicaDownstreamStatus;
  readonly status$: Observable<ReplicaDownstreamStatus>;
  refresh(): void;
  hint(): void;
  close(): Promise<void>;
}
export type ReplicaDownstreamOptions = {
  storage: AliasStorage;
  source: ReplicaSourceAdapter;
  writeRemote?(rows: RxReplicationWriteToMasterRow<ReplicaRecord>[], signal: AbortSignal): Promise<WithDeleted<ReplicaRecord>[]>;
  options?: { pollIntervalMs?: number; hintDelayMs?: number; retryBaseMs?: number; retryMaxMs?: number; maintenanceBackoffMs?: number };
};
export type CoordinatorEnvironment = {
  leadership(namespace: string): ReplicaLeadership;
  now(): number;
  random(): number;
  set(callback: () => void, milliseconds: number): ReturnType<typeof setTimeout>;
  clear(timer: ReturnType<typeof setTimeout>): void;
};
const environment: CoordinatorEnvironment = {
  leadership: createReplicaLeadership, now: Date.now, random: Math.random,
  set: (callback, milliseconds) => setTimeout(callback, milliseconds), clear: timer => clearTimeout(timer),
};
class SourceReadFailure extends Error {
  constructor(readonly original: unknown) { super('Replica source read failed'); }
}
const field = (error: unknown, key: string): unknown => typeof error === 'object' && error !== null ? Reflect.get(error, key) : undefined;
const transientRead = (error: unknown): boolean => {
  if (['UNAUTHORIZED', 'FORBIDDEN', 'AUTH_SESSION_CHANGED', 'DATABASE_IDENTITY_MISMATCH',
    'REQUEST_TOO_LARGE', 'REPLICATION_BUDGET_EXCEEDED'].includes(String(field(error, 'code')))) return false;
  const status = field(error, 'status') ?? field(field(error, 'response'), 'status');
  if (status === 429 || (typeof status === 'number' && status >= 500 && status <= 599)) return true;
  return status === undefined && ['ERR_NETWORK', 'ECONNABORTED', 'ETIMEDOUT', 'ECONNRESET'].includes(String(field(error, 'code')));
};

export const createReplicaDownstream = (input: ReplicaDownstreamOptions, clock: CoordinatorEnvironment = environment): ReplicaDownstream => {
  const { storage } = input;
  storage.signal.throwIfAborted();
  const poll = input.options?.pollIntervalMs ?? 10_000;
  const hintDelay = input.options?.hintDelayMs ?? 200;
  const retryBase = input.options?.retryBaseMs ?? 1_000;
  const retryMax = input.options?.retryMaxMs ?? 30_000;
  const maintenanceBackoff = input.options?.maintenanceBackoffMs ?? 30_000;
  for (const value of [poll, hintDelay, retryBase, retryMax, maintenanceBackoff]) {
    if (!Number.isSafeInteger(value) || value <= 0) throw new RangeError('Coordinator intervals must be positive safe integers');
  }
  if (retryBase > retryMax) throw new RangeError('Retry base exceeds maximum');
  const abort = new AbortController();
  const closedReason = new ReplicaStorageError('ReplicaCoordinatorClosed', 'Replica coordinator is closed');
  const statuses = new BehaviorSubject<ReplicaDownstreamStatus>(Object.freeze({ state: 'waiting', leader: false, ready: false, generation: null }));
  const leadership = clock.leadership(storage.namespace);
  let leader = false, closed = false, blocked = false, dirty = true, reset = false;
  let retryAttempt = 0, retryAt = 0, maintenanceAt = 0;
  let dirtyAt = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let timerAt = Infinity;
  let reconcileTimer: ReturnType<typeof setTimeout> | undefined;
  let running: Promise<void> | undefined;
  let closing: Promise<void> | undefined;
  let native: ReplicationRuntime | undefined;
  let adapter: ReturnType<typeof createDownstreamAdapter> | undefined;
  let unregisterNative: (() => void) | undefined;
  let unregister = () => {};
  const publish = (patch: Partial<ReplicaDownstreamStatus>) => {
    if (closed && patch.state !== 'closed') return;
    statuses.next(Object.freeze({ ...statuses.value, ...patch }));
  };
  const active = () => { abort.signal.throwIfAborted(); };
  const reconcile = async () => {
    const manifest = await storage.readManifest();
    active();
    if (canonicalJson(manifest.definition) !== canonicalJson(input.source.definition) ||
        input.source.mode !== (manifest.definition.limit === null ? 'events' : 'replace')) {
      throw new ReplicaStorageError('ReplicaSourceMismatch', 'Source adapter differs from the frozen alias definition');
    }
    publish({ ready: manifest.sourceReady && manifest.activeSourceGeneration !== null && !manifest.partialDelivery, generation: manifest.activeSourceGeneration });
    if (manifest.dirtyUpstream || manifest.issues.length || manifest.recoveryIntent) {
      throw new ReplicaStorageError('ReplicaRecoveryPending', 'Replica requires upstream recovery before applying source changes');
    }
  };
  const closeNative = async (owned: ReplicationRuntime) => {
    try { await owned.close(); }
    catch (error) {
      const handled = error instanceof SourceReadFailure || ['ReplicaConflictUnresolved', 'ReplicaSourceInvalid',
        'ReplicaSourceMismatch', 'ReplicaIdentityMismatch', 'ReplicaSourceResponseTooLarge'].includes(String(field(error, 'code')));
      if (error !== owned.error || !handled) throw error;
    }
  };
  const drainNative = async () => {
    if (native) await closeNative(native);
    native = undefined; adapter = undefined;
    unregisterNative?.(); unregisterNative = undefined;
  };
  const block = (error: unknown) => {
    blocked = true; dirty = false;
    abort.abort(error);
    if (timer !== undefined) clock.clear(timer);
    timer = undefined;
    publish({ state: 'blocked', error, retryAt: undefined });
  };
  const schedule = (delay: number) => {
    if (closed || blocked || !leader || running) return;
    const at = Math.max(clock.now() + delay, retryAt);
    if (timer !== undefined && timerAt <= at) return;
    if (timer !== undefined) clock.clear(timer);
    timerAt = at;
    timer = clock.set(() => { timer = undefined; timerAt = Infinity; launch(); }, Math.max(at - clock.now(), 0));
  };
  const request = (delay: number) => {
    if (closed || blocked) return;
    dirtyAt = dirty ? Math.min(dirtyAt, clock.now() + delay) : clock.now() + delay;
    dirty = true;
    schedule(Math.max(dirtyAt - clock.now(), 0));
  };
  const createNative = async () => {
    await reconcile();
    const scope = await storage.captureScope();
    active();
    const guardedSource: ReplicaSourceAdapter = { ...input.source, read: async context => {
      try { return await input.source.read(context); }
      catch (error) {
        if (context.signal.aborted && (error === context.signal.reason || field(error, 'code') === 'ERR_CANCELED')) throw context.signal.reason;
        throw new SourceReadFailure(error);
      }
    } };
    adapter = createDownstreamAdapter({ storage, scope, source: guardedSource, requestRefresh: () => request(0) });
    await adapter.recover(abort.signal);
    await adapter.beginRound({ reset });
    reset = false;
    const handles = await storage.native(scope);
    active();
    const currentAdapter = adapter;
    native = createReplicationRuntime({
      identifier: handles.identifier, forkInstance: handles.fork, metaInstance: handles.meta,
      hashFunction: defaultHashSha256, conflictHandler: { isEqual: businessEqual, resolve: async () => {
        throw new ReplicaStorageError('ReplicaConflictUnresolved', 'Replica upstream conflicts require recovery before further replication');
      } },
      ownerSignal: AbortSignal.any([handles.ownerSignal, abort.signal]), pullBatchSize: 201,
      upstreamEnabled: input.writeRemote !== undefined, writeRemote: input.writeRemote,
      isControlDocument: row => row.kind !== 'd',
      readSource: currentAdapter.readSource, onCheckpoint: currentAdapter.onCheckpoint,
      onUpCheckpoint: currentAdapter.onUpCheckpoint,
      onError: () => request(0),
    });
    const owned = native;
    unregisterNative = storage.registerNative({ invalidate() {}, close: () => closeNative(owned) });
  };
  const run = async () => {
    try {
      dirty = false; dirtyAt = Infinity;
      publish({ state: 'syncing', error: undefined, retryAt: undefined });
      if (!native) await createNative();
      else {
        await native.waitForIdle();
        await adapter!.beginRound();
        native.requestResync();
      }
      await native!.waitForIdle();
      active();
      await reconcile();
      retryAttempt = 0; retryAt = 0;
      publish({ state: 'idle' });
      if (!dirty && clock.now() >= maintenanceAt && (await storage.stats()).shouldCompact) {
        maintenanceAt = clock.now() + maintenanceBackoff;
        await drainNative();
        const beforeMaintenance = await storage.readManifest();
        try { await compactAlias(storage); }
        catch (error) {
          // Compaction owns rollback. Only capacity and a competing clean check
          // are recoverable; unknown persistence failures require inspection.
          if (field(error, 'name') !== 'QuotaExceededError' &&
              !['ReplicaStorageLimit', 'ReplicaMaintenanceTimeout'].includes(String(field(error, 'code')))) throw error;
          const afterMaintenance = await storage.readManifest();
          if (afterMaintenance.activePhysicalEpoch !== beforeMaintenance.activePhysicalEpoch || afterMaintenance.maintenance !== null ||
              afterMaintenance.physicalEpochs.length !== 1 || afterMaintenance.physicalEpochs[0] !== beforeMaintenance.activePhysicalEpoch) throw error;
          publish({ error });
        }
        active();
        dirty = true; dirtyAt = clock.now();
      }
    } catch (error) {
      if (closed || abort.signal.aborted) return;
      try { await drainNative(); }
      catch (drainError) { block(drainError === error ? error : new ReplicaCleanupError([error, drainError])); return; }
      if (error instanceof SourceReadFailure) {
        const sourceError = error.original;
        if (field(sourceError, 'code') === 'RESYNC_REQUIRED') {
          reset = true; dirty = true; retryAt = clock.now() + retryBase; dirtyAt = retryAt;
          publish({ state: 'retrying', error: sourceError, retryAt });
        } else if (transientRead(sourceError)) {
          const exponential = Math.min(retryMax, retryBase * 2 ** Math.min(retryAttempt++, 30));
          const jitter = exponential * (0.5 + clock.random() * 0.5);
          const retryAfter = field(sourceError, 'retryAfter');
          retryAt = clock.now() + Math.max(jitter, typeof retryAfter === 'number' && Number.isFinite(retryAfter) ? retryAfter * 1_000 : 0);
          dirty = true; dirtyAt = retryAt;
          publish({ state: 'retrying', error: sourceError, retryAt });
        } else block(sourceError);
      } else block(error);
    }
  };
  const launch = () => {
    if (closed || blocked || !leader || running) return;
    running = run().finally(() => {
      running = undefined;
      if (!closed && !blocked) schedule(dirty ? Math.max(dirtyAt - clock.now(), 0) : poll);
    });
  };
  let reconciling: Promise<void> | undefined;
  const reconcileTick = () => {
    if (closed || blocked) return;
    reconciling = reconcile().catch(error => { if (!closed) block(error); }).finally(() => {
      reconciling = undefined;
      if (!closed && !blocked) reconcileTimer = clock.set(reconcileTick, poll);
    });
  };
  const invalidate = () => {
    if (closed) return;
    closed = true;
    abort.abort(storage.signal.aborted ? storage.signal.reason : closedReason);
    if (timer !== undefined) clock.clear(timer);
    if (reconcileTimer !== undefined) clock.clear(reconcileTimer);
    publish({ state: 'closed', leader: false });
  };
  const close = () => closing ??= (async () => {
    invalidate();
    subscription.unsubscribe();
    storage.signal.removeEventListener('abort', onStorageAbort);
    const failures: unknown[] = [];
    const tasks = await Promise.allSettled([startup, running, reconciling]);
    for (const result of tasks) if (result.status === 'rejected') failures.push(result.reason);
    try { await drainNative(); } catch (error) { failures.push(error); }
    try { await leadership.close(); } catch (error) { failures.push(error); }
    statuses.complete();
    throwCleanupFailures(failures);
    unregister();
  })();
  const onStorageAbort = () => { invalidate(); void close().catch(() => {}); };
  const subscription = storage.changes.subscribe({ next: event => {
    if (event.type === 'view' && !reconciling && !closed && !blocked) {
      if (reconcileTimer !== undefined) clock.clear(reconcileTimer);
      reconcileTick();
    }
  }, error: block });
  unregister = storage.registerResource({ invalidate, close });
  storage.signal.addEventListener('abort', onStorageAbort, { once: true });
  const startup = (async () => {
    try {
      await reconcile();
      await leadership.wait(abort.signal);
      active(); leader = true; publish({ leader: true }); launch();
    } catch (error) { if (!closed) block(error); }
  })();
  reconcileTimer = clock.set(reconcileTick, poll);
  return { get snapshot() { return statuses.value; }, status$: statuses.asObservable(), refresh: () => request(0), hint: () => request(hintDelay), close };
};
