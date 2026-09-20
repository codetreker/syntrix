import { defaultHashSha256, type RxReplicationWriteToMasterRow, type WithDeleted } from 'rxdb';
import { BehaviorSubject, type Observable } from 'rxjs';
import type { AliasStorage, RequestScope } from './storage.js';
import { ReplicaStorageError, type ReplicaRecord } from './storage-types.js';
import type { ReplicaSourceAdapter } from './source-types.js';
import { createDownstreamAdapter } from './downstream.js';
import { createReplicationRuntime, type ReplicationRuntime } from './runtime.js';
import { businessEqual, canonicalJson } from './records.js';
import { compactAlias } from './compaction.js';
import { createReplicaLeadership, ReplicaCleanupError, throwCleanupFailures, type ReplicaLeadership } from './leadership.js';
import type { UpstreamTransport } from './upstream-types.js';
import { inspectReplica, resolveReplica, replayReplicaRecovery, type RecoveryDecision, type ReplicaInspection } from './recovery.js';
import { createUpstreamAdapter, UpstreamFailure } from './upstream.js';

export type ReplicaDownstreamStatus = Readonly<{
  state: 'waiting' | 'syncing' | 'idle' | 'retrying' | 'paused' | 'blocked' | 'closed';
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
  pause(): Promise<void>;
  resume(): Promise<void>;
  inspect(options?: { logicalId?: string; readCurrent?: boolean }): Promise<ReplicaInspection>;
  resolve(decision: RecoveryDecision): Promise<void>;
  close(): Promise<void>;
}
export type ReplicaDownstreamOptions = {
  storage: AliasStorage;
  source: ReplicaSourceAdapter;
  upstream?: UpstreamTransport;
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
type NativeOwner = {
  scope: RequestScope;
  signal: AbortSignal;
  adapter: ReturnType<typeof createDownstreamAdapter>;
  upstream?: ReturnType<typeof createUpstreamAdapter>;
  runtime?: ReplicationRuntime;
  unregister?: () => void;
};
class NativeRetired extends Error {
  constructor(readonly owner: NativeOwner) { super('Replica native owner was retired'); }
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
  if (input.upstream && input.writeRemote) throw new TypeError('Configure one upstream adapter');
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
  let activity = new AbortController();
  const closedReason = new ReplicaStorageError('ReplicaCoordinatorClosed', 'Replica coordinator is closed');
  const statuses = new BehaviorSubject<ReplicaDownstreamStatus>(Object.freeze({ state: 'waiting', leader: false, ready: false, generation: null }));
  const leadership = clock.leadership(storage.namespace);
  let leader = false, leaderReady = false, closed = false, paused = false, blocked = false, dirty = true, reset = false;
  let controls = Promise.resolve();
  const inspections = new Set<Promise<ReplicaInspection>>();
  const control = <T>(operation: () => Promise<T>): Promise<T> => {
    const result = controls.then(operation);
    controls = result.then(() => undefined, () => undefined);
    return result;
  };
  let retryAttempt = 0, retryAt = 0, maintenanceAt = 0;
  let dirtyAt = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let timerAt = Infinity;
  let reconcileTimer: ReturnType<typeof setTimeout> | undefined;
  let running: Promise<void> | undefined;
  let closing: Promise<void> | undefined;
  let native: NativeOwner | undefined;
  let unregister = () => {};
  const publish = (patch: Partial<ReplicaDownstreamStatus>) => {
    if (closed && patch.state !== 'closed') return;
    statuses.next(Object.freeze({ ...statuses.value, ...patch }));
  };
  const active = () => { abort.signal.throwIfAborted(); };
  const activeRun = () => { active(); activity.signal.throwIfAborted(); };
  const reconcile = async (enforce?: boolean) => {
    const manifest = await storage.readManifest();
    active();
    if (canonicalJson(manifest.definition) !== canonicalJson(input.source.definition) ||
        input.source.mode !== (manifest.definition.limit === null ? 'events' : 'replace')) {
      throw new ReplicaStorageError('ReplicaSourceMismatch', 'Source adapter differs from the frozen alias definition');
    }
    publish({ ready: manifest.sourceReady && manifest.activeSourceGeneration !== null && !manifest.partialDelivery, generation: manifest.activeSourceGeneration });
    if ((manifest.dirtyUpstream && !native?.upstream?.ownsPhase(manifest.dirtyUpstream)) || manifest.issues.length || manifest.recoveryIntent) {
      const error = new ReplicaStorageError('ReplicaRecoveryPending', 'Replica requires upstream recovery before applying source changes');
      if (enforce ?? (leaderReady && native?.runtime?.stopped !== true)) throw error;
      if (!paused) publish({ state: 'blocked', error });
    } else if (!leader && !paused && statuses.value.state === 'blocked') {
      publish({ state: 'waiting', error: undefined });
    }
  };
  const closeNative = async (owned: ReplicationRuntime) => {
    try { await owned.close(); }
    catch (error) {
      const handled = error instanceof SourceReadFailure || error instanceof UpstreamFailure || ['ReplicaRecoveryRequired', 'ReplicaRecoveryPending', 'ReplicaConflictUnresolved', 'ReplicaSourceInvalid',
        'ReplicaSourceMismatch', 'ReplicaIdentityMismatch', 'ReplicaSourceResponseTooLarge'].includes(String(field(error, 'code')));
      if (error !== owned.error || !handled) throw error;
    }
  };
  const drainNative = async (owned = native) => {
    if (!owned) return;
    if (owned.runtime) await closeNative(owned.runtime);
    owned.unregister?.();
    if (native === owned) native = undefined;
  };
  const assertOwner = (owned: NativeOwner) => {
    activeRun();
    if (owned.runtime?.error !== undefined) throw owned.runtime.error;
    if (owned.signal.aborted) throw new NativeRetired(owned);
  };
  const block = (error: unknown) => {
    blocked = true; dirty = false;
    activity.abort(error);
    if (timer !== undefined) clock.clear(timer);
    timer = undefined;
    publish({ state: 'blocked', error, retryAt: undefined });
  };
  const schedule = (delay: number) => {
    if (closed || paused || blocked || !leader || !leaderReady || running) return;
    const at = Math.max(clock.now() + delay, retryAt);
    if (timer !== undefined && timerAt <= at) return;
    if (timer !== undefined) clock.clear(timer);
    timerAt = at;
    timer = clock.set(() => { timer = undefined; timerAt = Infinity; launch(); }, Math.max(at - clock.now(), 0));
  };
  const request = (delay: number) => {
    if (closed || paused || blocked) return;
    dirtyAt = dirty ? Math.min(dirtyAt, clock.now() + delay) : clock.now() + delay;
    dirty = true;
    schedule(Math.max(dirtyAt - clock.now(), 0));
  };
  const createNative = async () => {
    activeRun();
    await reconcile(true);
    let scope = await storage.captureScope();
    let handles: Awaited<ReturnType<AliasStorage['native']>>;
    while (true) {
      activeRun();
      try { handles = await storage.native(scope); break; }
      catch (error) {
        activeRun();
        if (!['ReplicaScopeChanged', 'ReplicaMaintenance'].includes(String(field(error, 'code')))) throw error;
        const current = await storage.captureScope();
        // There is no captured owner signal until native() returns. Retry this
        // handoff only when a fresh scope proves that its instance was retired.
        if (current.subject !== scope.subject || current.sessionVersion !== scope.sessionVersion || current.definitionHash !== scope.definitionHash ||
            current.physicalEpoch === scope.physicalEpoch && current.nativeInstanceId === scope.nativeInstanceId) throw error;
        scope = current;
      }
    }
    activeRun();
    const guardedSource: ReplicaSourceAdapter = { ...input.source, read: async context => {
      try { return await input.source.read(context); }
      catch (error) {
        if (context.signal.aborted && (error === context.signal.reason || field(error, 'code') === 'ERR_CANCELED')) throw context.signal.reason;
        throw new SourceReadFailure(error);
      }
    } };
    const owned: NativeOwner = {
      scope, signal: handles.ownerSignal,
      adapter: createDownstreamAdapter({ storage, scope, source: guardedSource, requestRefresh: () => request(0),
        ownsPhase: marker => !activity.signal.aborted && owned.upstream?.ownsPhase(marker) === true }),
    };
    if (input.upstream) owned.upstream = createUpstreamAdapter({ storage, scope, transport: input.upstream,
      onSettlement: (checkpoint, signal) => owned.adapter.onUpCheckpoint(checkpoint, signal) });
    native = owned;
    assertOwner(owned);
    await owned.adapter.recover(AbortSignal.any([owned.signal, abort.signal, activity.signal]));
    assertOwner(owned);
    await owned.adapter.beginRound({ reset });
    assertOwner(owned);
    reset = false;
    owned.runtime = createReplicationRuntime({
      identifier: handles.identifier, forkInstance: handles.fork, metaInstance: handles.meta,
      hashFunction: defaultHashSha256, conflictHandler: { isEqual: businessEqual, resolve: async () => {
        throw new ReplicaStorageError('ReplicaConflictUnresolved', 'Replica upstream conflicts require recovery before further replication');
      } },
      ownerSignal: AbortSignal.any([handles.ownerSignal, abort.signal, activity.signal]), pullBatchSize: 201,
      upstreamEnabled: input.upstream !== undefined || input.writeRemote !== undefined,
      writeRemote: owned.upstream?.writeRemote ?? input.writeRemote,
      upstreamPersistence: owned.upstream?.upstreamPersistence,
      isControlDocument: row => row.kind !== 'd',
      readSource: owned.adapter.readSource, onCheckpoint: owned.adapter.onCheckpoint,
      onUpCheckpoint: input.upstream ? undefined : owned.adapter.onUpCheckpoint,
      onError: () => request(0),
    });
    owned.unregister = storage.registerNative({ invalidate() {}, close: () => closeNative(owned.runtime!) });
    return owned;
  };
  const run = async () => {
    let owned = native;
    try {
      dirty = false; dirtyAt = Infinity;
      publish({ state: 'syncing', error: undefined, retryAt: undefined });
      if (!owned) owned = await createNative();
      else {
        assertOwner(owned);
        await owned.runtime!.waitForIdle();
        assertOwner(owned);
        // A different alias handle can replace the epoch without notifying this
        // owner until its next guarded storage operation.
        await storage.withReplicationAccess(owned.scope, async () => {});
        assertOwner(owned);
        await owned.adapter.beginRound();
        assertOwner(owned);
        owned.runtime!.requestResync();
      }
      await owned.runtime!.waitForIdle();
      assertOwner(owned);
      await reconcile(true);
      assertOwner(owned);
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
        activeRun();
        dirty = true; dirtyAt = clock.now();
      }
    } catch (error) {
      if (closed || paused || blocked || abort.signal.aborted) return;
      owned ??= native;
      const retired = owned?.signal.aborted && (error instanceof NativeRetired && error.owner === owned || error === owned.signal.reason);
      try { await drainNative(owned); }
      catch (drainError) { block(drainError === error ? error : new ReplicaCleanupError([error, drainError])); return; }
      let outcome = owned?.upstream?.outcome;
      if (outcome?.kind === 'blocked' && outcome.error === error && (error instanceof SourceReadFailure || retired)) {
        try {
          const manifest = await storage.readManifest();
          if (!manifest.dirtyUpstream && !manifest.issues.length && !manifest.recoveryIntent) outcome = undefined;
        } catch (readError) { block(readError); return; }
      }
      if (outcome && outcome.kind !== 'retry') {
        block(outcome.error);
      } else if (outcome?.kind === 'retry') {
        const exponential = Math.min(retryMax, retryBase * 2 ** Math.min(retryAttempt++, 30));
        retryAt = clock.now() + Math.max(exponential * (0.5 + clock.random() * 0.5), outcome.retryAfterMs ?? 0);
        dirty = true; dirtyAt = retryAt;
        publish({ state: 'retrying', error: outcome.error, retryAt });
      } else if (retired) {
        dirty = true; dirtyAt = clock.now();
      } else if (error instanceof SourceReadFailure) {
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
    if (closed || paused || blocked || !leader || !leaderReady || running) return;
    running = run().finally(() => {
      running = undefined;
      if (!closed && !paused && !blocked) schedule(dirty ? Math.max(dirtyAt - clock.now(), 0) : poll);
    });
  };
  let reconciling: Promise<void> | undefined;
  const reconcileTick = () => {
    if (closed || blocked || paused) return;
    // The upstream owner classifies a failed phase only after every sibling
    // drains. An observer must not turn that intermediate marker into a
    // permanent block before the owner can prove a safe retry.
    reconciling = reconcile(native?.upstream ? false : undefined).catch(error => { if (!closed && !paused) block(error); }).finally(() => {
      reconciling = undefined;
      if (!closed && !blocked && !paused) reconcileTimer = clock.set(reconcileTick, poll);
    });
  };
  const invalidate = () => {
    if (closed) return;
    closed = true;
    abort.abort(storage.signal.aborted ? storage.signal.reason : closedReason);
    activity.abort(abort.signal.reason);
    if (timer !== undefined) clock.clear(timer);
    if (reconcileTimer !== undefined) clock.clear(reconcileTimer);
    publish({ state: 'closed', leader: false });
  };
  const close = () => closing ??= (async () => {
    invalidate();
    subscription.unsubscribe();
    storage.signal.removeEventListener('abort', onStorageAbort);
    const failures: unknown[] = [];
    const tasks = await Promise.allSettled([startup, controls, running, reconciling]);
    for (const result of tasks) if (result.status === 'rejected') failures.push(result.reason);
    // Inspection errors belong to their callers; closing still owns the read
    // cancellation and must wait for its HTTP and storage work to exit.
    await Promise.allSettled([...inspections]);
    try { await drainNative(); } catch (error) { failures.push(error); }
    try { await leadership.close(); } catch (error) { failures.push(error); }
    statuses.complete();
    throwCleanupFailures(failures);
    unregister();
  })();
  const onStorageAbort = () => { invalidate(); void close().catch(() => {}); };
  const subscription = storage.changes.subscribe({ next: event => {
    if (event.type === 'view' && !reconciling && !closed && !blocked && !paused) {
      if (reconcileTimer !== undefined) clock.clear(reconcileTimer);
      reconcileTick();
    }
  }, error: block });
  unregister = storage.registerResource({ invalidate, close });
  storage.signal.addEventListener('abort', onStorageAbort, { once: true });
  const initializeLeader = async () => {
    activeRun();
    if ((await storage.readManifest()).recoveryIntent) await replayReplicaRecovery(storage);
    activeRun();
    await reconcile(true);
    activeRun();
    leaderReady = true;
  };
  const startup = (async () => {
    try {
      await reconcile(false);
      await leadership.wait(abort.signal);
      active(); leader = true; publish({ leader: true });
      await control(async () => {
        if (paused || closed) return;
        await initializeLeader();
        launch();
      });
    } catch (error) { if (!closed && !paused) block(error); }
  })();
  reconcileTimer = clock.set(reconcileTick, poll);
  const stopAdmission = () => {
    active();
    paused = true;
    activity.abort(new ReplicaStorageError('ReplicaPaused', 'Replica synchronization is paused'));
    if (timer !== undefined) clock.clear(timer);
    timer = undefined; timerAt = Infinity;
    if (reconcileTimer !== undefined) clock.clear(reconcileTimer);
  };
  const drainPaused = async () => {
    await Promise.all([running, reconciling]);
    const owned = native;
    await drainNative();
    active();
    const outcome = owned?.upstream?.outcome;
    if (outcome && outcome.kind !== 'retry') blocked = true;
    publish({ state: 'paused', retryAt: undefined, ...(outcome ? { error: outcome.error } : {}) });
  };
  const pause = (): Promise<void> => { stopAdmission(); return control(drainPaused); };
  const resume = (): Promise<void> => control(async () => {
    active();
    if (!paused && !blocked) { await reconcile(true); return; }
    await Promise.all([running, reconciling]);
    await drainNative();
    activity = new AbortController();
    try {
      if (leader) await initializeLeader();
      else await reconcile(true);
    } catch (error) { block(error); throw error; }
    activeRun();
    paused = false; blocked = false; dirty = true; dirtyAt = clock.now(); retryAt = 0; retryAttempt = 0;
    publish({ state: leader ? 'syncing' : 'waiting', error: undefined, retryAt: undefined });
    if (reconcileTimer !== undefined) clock.clear(reconcileTimer);
    reconcileTimer = clock.set(reconcileTick, poll);
    schedule(0);
  });
  const resolve = async (decision: RecoveryDecision): Promise<void> => {
    active();
    if (!leader) throw new ReplicaStorageError('ReplicaNotLeader', 'Recovery requires the elected coordinator');
    if (!input.upstream) throw new ReplicaStorageError('ReplicaUpstreamUnavailable', 'Recovery requires the upstream transport');
    stopAdmission();
    await control(async () => {
      await drainPaused();
      active();
      await resolveReplica(storage, input.upstream!, decision, { signal: abort.signal });
      active();
      blocked = false;
      await reconcile(false);
      publish({ state: 'paused', error: undefined });
    });
  };
  const inspect = (options: { logicalId?: string; readCurrent?: boolean } = {}): Promise<ReplicaInspection> => {
    active();
    const operation = inspectReplica(storage, { ...options, signal: abort.signal }, input.upstream).then(result => { active(); return result; });
    inspections.add(operation);
    void operation.then(() => inspections.delete(operation), () => inspections.delete(operation));
    return operation;
  };
  return { get snapshot() { return statuses.value; }, status$: statuses.asObservable(), refresh: () => request(0), hint: () => request(hintDelay),
    pause, resume, inspect, resolve, close };
};
