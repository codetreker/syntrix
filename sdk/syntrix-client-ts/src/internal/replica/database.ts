import type { AxiosInstance } from 'axios';
import type { RxStorage } from 'rxdb';
import type { Subscription } from 'rxjs';
import type { ReplicaOptionsSnapshot } from '../../api/replica-reference.js';
import type { ReplicaAliasStatus, ReplicaCollection, ReplicaDatabase, ReplicaDiagnostic, ReplicaDocument, ReplicaDocumentReference,
  ReplicaDocumentState, ReplicaInspection, ReplicaQuery, ReplicaRecoveryDecision, ReplicaSyncStatus } from '../../api/replica-types.js';
import type { QueryValue } from '../../api/value.js';
import type { TokenProvider } from '../auth/types.js';
import { createReplicaDownstream, type CoordinatorEnvironment, type ReplicaDownstreamStatus, type ReplicaDownstream } from './coordinator.js';
import { ReplicaCleanupError, throwCleanupFailures } from './leadership.js';
import { createReplicaQueryClient, type ReplicaQueryClient } from './query.js';
import type { ReplicaQuerySpec } from './query-semantics.js';
import { decodeBusinessPayload, encodeBusinessPayload, freezeSourceDefinition, frozenConditions, projectDocument, validateField, validateLogicalId } from './records.js';
import { removeAliasStorage } from './removal.js';
import type { RecoveryDecision, ReplicaInspection as StoredInspection } from './recovery.js';
import type { ReplicaSession } from './session.js';
import { createReplicaHttpSource } from './source.js';
import { openAliasStorage, type AliasStorage } from './storage.js';
import { ReplicaStorageError, type DataRecord, type FrozenSourceDefinition, type ReplicaCondition } from './storage-types.js';
import { createReplicaHttpUpstream } from './upstream-transport.js';

type OpenDatabaseOptions = {
  session: ReplicaSession; axios: AxiosInstance; provider: TokenProvider; endpoint: string; database: string; options: ReplicaOptionsSnapshot;
};
export type ReplicaDatabaseEnvironment = { storage?: RxStorage<any, any>; lockManager?: LockManager; coordinator?: CoordinatorEnvironment };
type Alias = {
  name: string; definition: FrozenSourceDefinition; storage: AliasStorage; query: ReplicaQueryClient;
  coordinator?: ReplicaDownstream; nativeStatus: ReplicaDownstreamStatus; durable: Awaited<ReturnType<AliasStorage['status']>>;
  removed: boolean; removing: boolean; watches: Set<() => void>; subscriptions: Subscription[];
  statusDirty: boolean; statusTask?: Promise<void>; statusTimer?: ReturnType<typeof setTimeout>; statusError?: unknown;
};
const freeze = <T>(value: T): T => {
  if (value !== null && typeof value === 'object') { Object.values(value).forEach(freeze); Object.freeze(value); }
  return value;
};
const code = (error: unknown): string => {
  const value = typeof error === 'object' && error !== null && 'code' in error ? error.code : undefined;
  return typeof value === 'string' && new Set(['AUTH_SESSION_CHANGED', 'UNAUTHORIZED', 'FORBIDDEN', 'RESYNC_REQUIRED',
    'DATABASE_IDENTITY_MISMATCH', 'RATE_LIMITED', 'INTERNAL_ERROR', 'UNAVAILABLE', 'ReplicaWriteConflict', 'ReplicaUpstreamUncertain',
    'ReplicaRecoveryPending', 'ReplicaRecoveryRequired', 'ReplicaStorageLimit', 'ReplicaReadBudgetExceeded', 'ReplicaRecordTooLarge',
    'ReplicaSourceInvalid', 'ReplicaSourceMismatch', 'ReplicaIdentityMismatch', 'ReplicaScopeChanged', 'ReplicaAliasRemoved',
    'ReplicaRecoveryStale', 'ReplicaRemovalBlocked', 'ReplicaAliasRemoving', 'QueryBudgetExceeded', 'ReplicaCoordinatorClosed', 'ReplicaDatabaseClosed']).has(value) ? value : 'ReplicaOperationFailed';
};
const projectedState = (collection: string, data: DataRecord | null): ReplicaDocumentState | null => {
  if (!data) return null;
  if (data.existence === 'absent') return { existence: 'absent', document: null };
  const document = projectDocument(collection, data, { key: `m:${data.key.slice(2)}`, kind: 'm', logicalId: data.logicalId,
    slots: [], observedExistence: data.existence, metadata: data.wire })!;
  return { existence: data.existence, document };
};
const projectInspection = (alias: Alias, id: string | undefined, inspected: StoredInspection): ReplicaInspection => {
  const result: ReplicaInspection = { issueId: inspected.issueId, stateToken: inspected.stateToken, physicalEpoch: inspected.physicalEpoch,
    phase: inspected.phase && { ...inspected.phase }, recovering: inspected.recovering,
    availableActions: inspected.availableActions.map(action => ({ ...action })),
    issues: inspected.issues.map(issue => ({ id: issue.id, documentId: issue.logicalId, code: issue.code })),
    targets: inspected.targets.map(target => ({ id: target.logicalId, editToken: target.token })) };
  if (inspected.document) {
    const current = inspected.document.current;
    result.document = { id: id!, editToken: inspected.document.desired?.editToken ?? null,
      desired: projectedState(alias.definition.collection, inspected.document.desired),
      assumed: projectedState(alias.definition.collection, inspected.document.assumed),
      ...(current ? { current: { source: 'authoritative-read' as const, state: current.document === null
        ? { existence: 'absent' as const, document: null }
        : { existence: current.document.deleted ? 'deleted' as const : 'live' as const, document: structuredClone(current.document) as ReplicaDocument } } } : {}) };
  }
  return freeze(result);
};

export const openReplicaDatabase = async (input: OpenDatabaseOptions, environment: ReplicaDatabaseEnvironment = {}): Promise<ReplicaDatabase> => {
  const { session, axios, provider, endpoint, database } = input;
  session.assertCurrent();
  const { onDiagnostic, ...serializable } = input.options;
  const options = freeze(structuredClone(serializable));
  const definitions = Object.entries(options.collections).map(([name, definition]) => ({ name, definition, frozen: freezeSourceDefinition(definition) }));
  const aliases = new Map<string, Alias>();
  const lifetime = new AbortController();
  const replicaId = crypto.randomUUID();
  const observers = new Set<{ result(status: ReplicaSyncStatus): void; error?(error: unknown): void }>();
  const operations = new Set<Promise<unknown>>();
  let closed = false;
  let ownedClosing: Promise<void> | undefined;
  let closing: Promise<void> | undefined;
  let finishOpening!: () => void;
  const opening = new Promise<void>(resolve => { finishOpening = resolve; });
  let unregister = () => {};
  const fail = (message = 'Replica database is closed'): never => { throw new ReplicaStorageError('ReplicaDatabaseClosed', message); };
  const assert = () => { if (closed) fail(); session.assertCurrent(); };
  const assertAlias = (alias: Alias) => {
    assert();
    if (alias.removed) throw new ReplicaStorageError('ReplicaAliasRemoved', 'Replica collection was removed');
    if (alias.removing) throw new ReplicaStorageError('ReplicaAliasRemoving', 'Replica collection is being removed');
    alias.storage.signal.throwIfAborted();
  };
  const requireAlias = (name: string) => {
    assert();
    const alias = aliases.get(name);
    if (!alias) throw new ReplicaStorageError('ReplicaAliasUnknown', `Replica collection ${name} is not configured`);
    assertAlias(alias); return alias;
  };
  const correlation = () => ({ operationId: crypto.randomUUID(), started: Date.now() });
  const diagnostic = (alias: string, operation: string, phase: string, error?: unknown,
    context = correlation(), details: { requestId?: string; count?: number; physicalEpoch?: string } = {}) => {
    if (closed || !onDiagnostic) return;
    const timestamp = Date.now();
    const event: ReplicaDiagnostic = Object.freeze({ replicaId, operationId: context.operationId, sessionVersion: session.version,
      alias, operation, phase, timestamp, ...details,
      ...(phase === 'start' ? {} : { durationMs: timestamp - context.started }),
      ...(error === undefined ? {} : { code: code(error) }) });
    try { onDiagnostic(event); } catch { /* Diagnostic observers do not own synchronization. */ }
  };
  const snapshot = (): ReplicaSyncStatus => Object.freeze({ aliases: Object.freeze(Object.fromEntries([...aliases].filter(([, alias]) => !alias.removed).map(([name, alias]) => {
    const native = alias.nativeStatus, durable = alias.durable;
    const checkpoint = durable.mode === 'events' && typeof durable.checkpoint?.sourceCursor === 'string' ? durable.checkpoint.sourceCursor : null;
    const status: ReplicaAliasStatus = Object.freeze({ alias: name, mode: durable.mode, state: alias.statusError === undefined ? native.state : 'blocked',
      leader: native.leader, ready: durable.sourceReady, sourceReady: durable.sourceReady, generation: durable.generation,
      physicalEpoch: durable.physicalEpoch, checkpoint, lastCompleteRound: durable.lastCompleteRound,
      pending: durable.pending, pins: durable.pins, issues: Object.freeze(durable.issues.map(issue => Object.freeze({ id: issue.id, documentId: issue.logicalId, code: issue.code }))),
      ...(native.retryAt === undefined ? {} : { retryAt: native.retryAt }),
      ...(alias.statusError !== undefined || native.error !== undefined ? { error: alias.statusError ?? native.error } : {}) });
    return [name, status];
  }))) });
  const publish = () => {
    if (closed) return;
    const status = snapshot();
    for (const observer of [...observers]) {
      if (closed || !observers.has(observer)) continue;
      try { observer.result(status); } catch (error) { try { observer.error?.(error); } catch { /* Consumer callbacks remain isolated. */ } }
    }
  };
  const report = (error: unknown) => {
    if (closed) return;
    for (const observer of [...observers]) { try { observer.error?.(error); } catch { /* Consumer callbacks remain isolated. */ } }
  };
  const markStatus = (alias: Alias) => {
    if (closed || alias.removed) return;
    alias.statusDirty = true;
    if (alias.statusTask || alias.statusTimer !== undefined) return;
    alias.statusTimer = setTimeout(() => {
      alias.statusTimer = undefined;
      if (closed || alias.removed) return;
      alias.statusDirty = false;
      alias.statusTask = alias.storage.status().then(value => {
        if (closed || alias.removed) return;
        alias.durable = value; alias.statusError = undefined; publish();
      }).catch(error => {
        if (closed || alias.removed || alias.removing) return;
        alias.statusError = error; publish(); report(error); diagnostic(alias.name, 'status', 'failed', error);
      }).finally(() => { alias.statusTask = undefined; if (alias.statusDirty) markStatus(alias); });
    }, options.sync?.hintDelayMs ?? 200);
  };
  const track = <T>(alias: Alias, name: string, operation: () => Promise<T>): Promise<T> => {
    assertAlias(alias);
    const context = correlation(); diagnostic(alias.name, name, 'start', undefined, context);
    const task = Promise.resolve().then(() => { assertAlias(alias); return operation(); }).then(result => {
      assertAlias(alias); diagnostic(alias.name, name, 'complete', undefined, context); return result;
    }).catch(error => { diagnostic(alias.name, name, 'failed', error, context); throw error; });
    operations.add(task);
    void task.then(() => operations.delete(task), () => operations.delete(task));
    return task;
  };
  const invalidate = () => {
    if (closed) return;
    closed = true;
    lifetime.abort(session.signal.aborted ? session.signal.reason : new ReplicaStorageError('ReplicaDatabaseClosed', 'Replica database is closed'));
    observers.clear();
    for (const alias of aliases.values()) {
      for (const stop of [...alias.watches]) stop();
      for (const subscription of alias.subscriptions) subscription.unsubscribe();
      if (alias.statusTimer !== undefined) clearTimeout(alias.statusTimer);
    }
  };
  const closeOwned = () => ownedClosing ??= (async () => {
    invalidate();
    await opening;
    // Alias lifetime owns cancellation for queued storage work and its native
    // coordinator. Start it first so every drain observes the same reason.
    const storageClosing = [...aliases.values()].map(alias => alias.storage.close());
    const results = await Promise.allSettled([...aliases.values()].flatMap(alias => [
      alias.coordinator?.close(), alias.query.close(), alias.statusTask,
    ]).concat(storageClosing));
    await Promise.allSettled([...operations]);
    const errors = [...new Set(results.flatMap(result => result.status === 'rejected' ? [result.reason] : []))];
    throwCleanupFailures(errors);
    unregister();
  })();
  const close = () => closing ??= (async () => {
    const errors: unknown[] = [];
    try { await closeOwned(); } catch (error) { errors.push(error); }
    try { await session.close(); } catch (error) { if (!errors.includes(error)) errors.push(error); }
    throwCleanupFailures(errors);
  })();
  unregister = session.register({ invalidate, close: closeOwned });

  const reference = <T>(alias: Alias, id: string, conditions: readonly ReplicaCondition[] = []): ReplicaDocumentReference<T> => {
    assertAlias(alias); validateLogicalId(id);
    return Object.freeze({ id, path: `${alias.definition.collection}/${id}`,
      get: (readOptions: { showDeleted?: boolean } = {}) => { const copied = { ...readOptions }; return track(alias, 'get-document', () => alias.storage.get(id, copied)) as Promise<ReplicaDocument<T> | null>; },
      ifMatch: (field, op, value) => reference<T>(alias, id, frozenConditions([...conditions, { field, op, value }])),
      set: (data: T) => { assertAlias(alias); const copied = decodeBusinessPayload(encodeBusinessPayload(data)); return track(alias, 'set', () => alias.storage.set(id, copied, { ifMatch: conditions })); },
      update: (data: Partial<T>) => { assertAlias(alias); const copied = decodeBusinessPayload(encodeBusinessPayload(data)); return track(alias, 'update', () => alias.storage.update(id, copied, { ifMatch: conditions })); },
      delete: () => track(alias, 'delete', () => alias.storage.delete(id, { ifMatch: conditions })),
    } satisfies ReplicaDocumentReference<T>);
  };
  const query = <T>(alias: Alias, spec: ReplicaQuerySpec = {}): ReplicaQuery<T> => Object.freeze({
    where: (field, op, value) => { assertAlias(alias); return query<T>(alias, { ...spec, filters: frozenConditions([...(spec.filters ?? []), { field, op, value }]) }); },
    orderBy: (field, direction = 'asc') => {
      assertAlias(alias); validateField(field);
      if (!['asc', 'desc'].includes(direction) || spec.orderBy?.some(order => order.field === field)) throw new TypeError('Query ordering requires unique fields and asc or desc direction');
      return query<T>(alias, { ...spec, orderBy: [...(spec.orderBy ?? []), Object.freeze({ field, direction })] });
    },
    limit: count => { assertAlias(alias); if (!Number.isInteger(count) || count < 1 || count > 1000) throw new RangeError('Query limit must be between 1 and 1000'); return query<T>(alias, { ...spec, limit: count }); },
    startAfter: cursor => { assertAlias(alias); if (typeof cursor !== 'string' || !cursor) throw new TypeError('Cursor must be a nonempty string'); return query<T>(alias, { ...spec, startAfter: cursor }); },
    showDeleted: (show = true) => { assertAlias(alias); if (typeof show !== 'boolean') throw new TypeError('showDeleted must be boolean'); return query<T>(alias, { ...spec, showDeleted: show }); },
    get: () => track(alias, 'query', () => alias.query.get(spec)) as Promise<ReplicaDocument<T>[]>,
    getPage: () => track(alias, 'query-page', () => alias.query.getPage(spec)) as Promise<import('../../api/replica-types.js').ReplicaQueryPage<T>>,
    watch: (onResult, onError) => {
      assertAlias(alias);
      const context = correlation(); diagnostic(alias.name, 'watch', 'start', undefined, context);
      let watching = true;
      const detach = alias.query.watch(spec, documents => {
        if (watching && !closed && !alias.removed) {
          diagnostic(alias.name, 'watch', 'result', undefined, context, { count: documents.length });
          onResult(documents as ReplicaDocument<T>[]);
        }
      }, error => {
        if (watching && !closed && !alias.removed && !alias.removing) {
          diagnostic(alias.name, 'watch', 'failed', error, context);
          if (onError) onError(error); else report(error);
        }
      });
      const stop = () => { if (!watching) return; watching = false; detach(); alias.watches.delete(stop); diagnostic(alias.name, 'watch', 'complete', undefined, context); };
      alias.watches.add(stop); return stop;
    },
  } satisfies ReplicaQuery<T>);
  const collection = <T>(name: string): ReplicaCollection<T> => {
    const alias = requireAlias(name);
    return Object.freeze({ ...query<T>(alias), alias: name, path: alias.definition.collection,
      doc: (id = alias.storage.generateId()) => reference<T>(alias, id),
      add: async (data: T) => { const document = reference<T>(alias, alias.storage.generateId()); await document.set(data); return document; },
    });
  };
  const selected = (name?: string) => name === undefined ? [...aliases.values()].filter(alias => !alias.removed).map(alias => { assertAlias(alias); return alias; }) : [requireAlias(name)];
  const all = async (targets: Alias[], name: string, action: (alias: Alias) => Promise<void>) => {
    const results = await Promise.allSettled(targets.map(alias => track(alias, name, () => action(alias))));
    throwCleanupFailures(results.flatMap(result => result.status === 'rejected' ? [result.reason] : []));
  };
  const removeCollection = (name: string): Promise<void> => {
    assert();
    if (typeof name !== 'string' || !name.trim()) throw new TypeError('Replica alias must be nonempty');
    const alias = aliases.get(name);
    if (alias) { assertAlias(alias); alias.removing = true; }
    const context = correlation(); diagnostic(name, 'remove', 'start', undefined, context);
    const removal = (async () => {
      try {
        if (alias) await alias.coordinator!.pause();
        await removeAliasStorage({ session, endpoint, database, name: options.name, alias: name,
          lockManager: environment.lockManager, storage: environment.storage, limits: options.storageLimits, signal: lifetime.signal });
        if (alias) {
          alias.removed = true;
          for (const stop of [...alias.watches]) stop();
          for (const subscription of alias.subscriptions) subscription.unsubscribe();
          if (alias.statusTimer !== undefined) clearTimeout(alias.statusTimer);
          const results = await Promise.allSettled([alias.coordinator!.close(), alias.query.close(), alias.storage.close(), alias.statusTask]);
          throwCleanupFailures(results.flatMap(result => result.status === 'rejected' ? [result.reason] : []));
        }
        publish(); diagnostic(name, 'remove', 'complete', undefined, context);
      } catch (error) { diagnostic(name, 'remove', 'failed', error, context); throw error; }
      finally { if (alias) alias.removing = false; }
    })();
    operations.add(removal); void removal.then(() => operations.delete(removal), () => operations.delete(removal)); return removal;
  };
  const facade: ReplicaDatabase = Object.freeze({ name: options.name, collection, removeCollection, close,
    sync: Object.freeze({
      pause: (name?: string) => all(selected(name), 'pause', async alias => { await alias.coordinator!.pause(); markStatus(alias); }),
      resume: (name?: string) => all(selected(name), 'resume', async alias => { await alias.coordinator!.resume(); markStatus(alias); }),
      inspect: <T>(name: string, inspectionOptions: { id?: string; readCurrent?: boolean } = {}) => {
        const alias = requireAlias(name); const copied = { ...inspectionOptions };
        return track(alias, 'inspect', async () => projectInspection(alias, copied.id, await alias.coordinator!.inspect({ logicalId: copied.id, readCurrent: copied.readCurrent }))) as Promise<ReplicaInspection<T>>;
      },
      resolve: <T>(name: string, decision: ReplicaRecoveryDecision<T>) => {
        const alias = requireAlias(name); const copied = structuredClone(decision);
        const action: RecoveryDecision = copied.kind === 'adopt-server' || copied.kind === 'merge-local'
          ? { ...copied, logicalId: copied.id, ...(copied.kind === 'merge-local' ? { data: decodeBusinessPayload(encodeBusinessPayload(copied.data)) } : {}) } as RecoveryDecision
          : copied;
        return track(alias, 'resolve', async () => { await alias.coordinator!.resolve(action); markStatus(alias); });
      },
      subscribe: (onStatus: (status: ReplicaSyncStatus) => void, onError?: (error: unknown) => void) => {
        assert(); const observer = { result: onStatus, error: onError }; observers.add(observer);
        try { onStatus(snapshot()); } catch (error) { observers.delete(observer); throw error; }
        return () => { observers.delete(observer); };
      },
    }),
  });
  let openingAlias: { name: string; context: ReturnType<typeof correlation> } | undefined;
  try {
    for (const descriptor of definitions) {
      assert();
      openingAlias = { name: descriptor.name, context: correlation() };
      diagnostic(descriptor.name, 'open', 'start', undefined, openingAlias.context);
      const storage = await openAliasStorage({ session, endpoint, database, name: options.name, alias: descriptor.name, source: descriptor.definition,
        limits: options.storageLimits, lockManager: environment.lockManager, storage: environment.storage });
      let queryClient: ReplicaQueryClient | undefined;
      try {
        assert(); queryClient = createReplicaQueryClient(storage, { limits: options.queryLimits });
        const durable = await storage.status();
        aliases.set(descriptor.name, { name: descriptor.name, definition: descriptor.frozen, storage, query: queryClient, durable,
          nativeStatus: { state: 'waiting', leader: false, ready: durable.sourceReady, generation: durable.generation },
          removed: false, removing: false, watches: new Set(), subscriptions: [], statusDirty: false });
        diagnostic(descriptor.name, 'open', 'complete', undefined, openingAlias.context, { physicalEpoch: durable.physicalEpoch });
        openingAlias = undefined;
      } catch (error) {
        const results = await Promise.allSettled([queryClient?.close(), storage.close()]);
        const failures = results.flatMap(result => result.status === 'rejected' ? [result.reason] : []);
        if (failures.length) throw new ReplicaCleanupError([error, ...failures]);
        throw error;
      }
    }
    assert();
    for (const alias of aliases.values()) {
      const source = createReplicaHttpSource({ axios, provider, database, definition: alias.definition });
      alias.coordinator = createReplicaDownstream({ storage: alias.storage,
        source: { ...source, read: async request => {
          const context = correlation(); diagnostic(alias.name, 'pull', 'start', undefined, context, { requestId: request.requestId });
          try {
            const page = await source.read(request);
            diagnostic(alias.name, 'pull', 'complete', undefined, context, { requestId: request.requestId, count: page.mode === 'events' ? page.events.length : page.documents.length });
            return page;
          } catch (error) { diagnostic(alias.name, 'pull', 'failed', error, context, { requestId: request.requestId }); throw error; }
        } },
        upstream: createReplicaHttpUpstream({ axios, provider, database, collection: alias.definition.collection }), options: options.sync }, environment.coordinator);
      alias.subscriptions.push(alias.storage.changes.subscribe({ next: () => markStatus(alias), error: report }));
      alias.subscriptions.push(alias.coordinator.status$.subscribe(status => {
        alias.nativeStatus = status; publish(); markStatus(alias);
      }));
    }
    assert(); finishOpening(); return facade;
  } catch (error) {
    if (openingAlias) diagnostic(openingAlias.name, 'open', 'failed', error, openingAlias.context);
    finishOpening();
    const failures: unknown[] = [];
    try { await closeOwned(); } catch (cleanupError) { failures.push(cleanupError); }
    if (failures.length) throw new ReplicaCleanupError([error, ...failures]);
    throw error;
  }
};
