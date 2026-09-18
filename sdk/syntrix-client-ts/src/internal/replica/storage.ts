import { getChangedDocumentsSince, type RxStorage, type RxStorageReplicationMeta } from 'rxdb';
import { Subject, type Observable, type Subscription } from 'rxjs';
import { openAliasBackend, BackendCleanupError, ReadBudget, validateStorageLimits, withRows, withScanPage, withMetadataScanPage, countRows, encodedRowBytes, type AliasBackend, type NativeRow, type NativeStorage, type PhysicalStorage } from './backend.js';
import { createNamespace } from './identity.js';
import { createAliasLocks, type AliasLockOwner, type ViewLockOwner } from './locks.js';
import { businessEqual, canonicalJson, decodeBusinessPayload, definitionHash, encodeBusinessPayload, freezeSourceDefinition, frozenConditions, matchesConditions, projectDocument, recordKey, validateLogicalId, validateManifestIdentity, validateRecordIdentity } from './records.js';
import type { ReplicaResource, ReplicaSession } from './session.js';
import { ReplicaStorageError, type AliasManifest, type DataRecord, type ReplicaCondition, type ReplicaDocument, type ReplicaRecord, type ReplicaSourceDefinition, type MemberRecord, type StorageLimits } from './storage-types.js';

export type RequestScope = Readonly<{ subject: string; sessionVersion: number; definitionHash: string; physicalEpoch: string; requestId: string; nativeInstanceId: string; }>;
export type AliasInvalidation = { type: 'row'; physicalEpoch: string; keys: string[]; } | { type: 'view'; physicalEpoch: string; activeSourceGeneration: string | null; };
export type MutationOptions = { ifMatch?: readonly ReplicaCondition[]; };
export type AliasStats = { knownIds: number; rows: number; bytes: number; retired: number; quota?: number; usage?: number; shouldCompact: boolean; };
export type MaintenanceAccess = {
  ownerSignal: AbortSignal;
  backend: AliasBackend;
  manifest: NativeRow<AliasManifest>;
  limits: StorageLimits;
  budget: ReadBudget;
  assertActive(): void;
  readManifest(): Promise<NativeRow<AliasManifest>>;
  writeManifest(next: AliasManifest): Promise<NativeRow<AliasManifest>>;
};
export type OpenAliasStorageOptions = {
  session: ReplicaSession; endpoint: string; database: string; name: string; alias: string;
  source: ReplicaSourceDefinition; limits?: Partial<StorageLimits>; lockManager?: LockManager; storage?: RxStorage<any, any>;
};
export type AliasStorage = {
  readonly changes: Observable<AliasInvalidation>;
  readonly namespace: string;
  readonly limits: StorageLimits;
  get(id: string, options?: { showDeleted?: boolean; }): Promise<ReplicaDocument | null>;
  read(id: string, options?: { showDeleted?: boolean; }): Promise<ReplicaDocument | null>;
  set(id: string, data: Record<string, unknown>, options?: MutationOptions): Promise<void>;
  update(id: string, patch: Record<string, unknown>, options?: MutationOptions): Promise<void>;
  delete(id: string, options?: MutationOptions): Promise<void>;
  add(data: Record<string, unknown>): Promise<string>;
  generateId(): string;
  captureScope(requestId?: string): Promise<RequestScope>;
  bind(scope: RequestScope, databaseIdentity: string, sourceHash: string): Promise<void>;
  guardNetwork(scope: RequestScope): Promise<Readonly<Record<string, string>>>;
  blockScope(): void;
  native(scope: RequestScope): Promise<{ fork: NativeStorage<ReplicaRecord>; meta: NativeStorage<RxStorageReplicationMeta<ReplicaRecord, any>>; identifier: string; ownerSignal: AbortSignal; }>;
  registerNative(resource: ReplicaResource): () => void;
  readManifest(): Promise<AliasManifest>;
  withMaintenance<T>(callback: (access: MaintenanceAccess) => Promise<T>): Promise<T>;
  stats(): Promise<AliasStats>;
  close(): Promise<void>;
};

const fail = (code: string, message: string): never => { throw new ReplicaStorageError(code, message); };
const isConflict = (error: unknown) => typeof error === 'object' && error !== null && 'status' in error && error.status === 409;
const checkpointRows = 3;

type ManifestView = Pick<AliasManifest, 'activePhysicalEpoch' | 'physicalEpochs' | 'activeSourceGeneration' | 'stagedSourceGeneration' | 'boundDatabaseId' | 'sourceHash' | 'sourceReady' | 'state'> & {
  protected: boolean;
  recovering: boolean;
};

class QueuedReadBudget extends ReadBudget {
  private reserved = 0;
  private retained = 0;
  private queuedPeak = 0;
  private waiting: { bytes: number; admit(): void; }[] = [];
  override get usedBytes() { return this.reserved; }
  override get peakBytes() { return this.queuedPeak; }
  override get availableBytes() { return this.maxBytes - this.retained; }
  retain(bytes: number): () => void {
    if (!Number.isSafeInteger(bytes) || bytes < 0 || bytes > this.maxBytes - this.reserved) throw new ReplicaStorageError('ReplicaReadBudgetExceeded', 'Retained rows exceed materialization budget');
    this.reserved += bytes; this.retained += bytes; this.queuedPeak = Math.max(this.queuedPeak, this.reserved);
    let active = true;
    return () => { if (active) { active = false; this.reserved -= bytes; this.retained -= bytes; this.pump(); } };
  }
  override async withReservation<T>(bytes: number, operation: () => Promise<T>): Promise<T> {
    if (!Number.isSafeInteger(bytes) || bytes <= 0 || bytes > this.availableBytes) throw new ReplicaStorageError('ReplicaReadBudgetExceeded', 'Read reservation exceeds materialization budget');
    await new Promise<void>(resolve => { this.waiting.push({ bytes, admit: resolve }); this.pump(); });
    try { return await operation(); }
    finally { this.reserved -= bytes; this.pump(); }
  }
  private pump() {
    while (this.waiting.length && this.waiting[0].bytes <= this.maxBytes - this.reserved) {
      const next = this.waiting.shift()!; this.reserved += next.bytes; this.queuedPeak = Math.max(this.queuedPeak, this.reserved); next.admit();
    }
  }
}

export const openAliasStorage = (options: OpenAliasStorageOptions): Promise<AliasStorage> => {
  const session = options.session;
  let stopped = false;
  let blocked = false;
  let maintaining = false;
  let backend: AliasBackend | undefined;
  let opening: Promise<AliasStorage>;
  let closing: Promise<void> | undefined;
  let cleanupFailure: unknown;
  let cleanup: Promise<void> | undefined;
  const inflight = new Set<Promise<unknown>>();
  const natives = new Set<ReplicaResource>();
  const subscriptions: Subscription[] = [];
  const changes = new Subject<AliasInvalidation>();
  const assertActive = () => { session.assertCurrent(); if (stopped) fail('ReplicaStorageClosed', 'Alias storage is closed'); };
  const stopNative = async () => {
    const resources = [...natives];
    resources.forEach(resource => resource.invalidate());
    const results = await Promise.allSettled(resources.map(resource => resource.close()));
    natives.clear();
    for (const result of results) if (result.status === 'rejected') throw result.reason;
  };
  const invalidate = () => { stopped = true; for (const resource of natives) resource.invalidate(); };
  const cleanupStorage = (): Promise<void> => {
    if (cleanup) return cleanup;
    cleanup = (async () => {
      subscriptions.forEach(subscription => subscription.unsubscribe());
      if (cleanupFailure) throw cleanupFailure;
      try { await backend?.close(); } catch (error) { cleanupFailure = error; throw error; }
      changes.complete(); unregister();
    })();
    return cleanup;
  };
  const close = (): Promise<void> => {
    if (closing) return closing;
    invalidate();
    closing = (async () => {
      await opening?.catch(() => undefined);
      await stopNative();
      while (inflight.size) await Promise.allSettled([...inflight]);
      await cleanupStorage();
    })();
    return closing;
  };
  const unregister = session.register({ invalidate, close });
  const run = async <T>(operation: () => Promise<T>): Promise<T> => {
    const promise = session.track(async () => { assertActive(); return operation(); });
    inflight.add(promise);
    void promise.then(() => inflight.delete(promise), () => inflight.delete(promise));
    return promise;
  };
  const construction = session.track(async () => {
    const identity = await createNamespace(options.endpoint, options.database, options.name, options.alias, session.subject);
    const definition = freezeSourceDefinition(options.source);
    const hash = await definitionHash(definition);
    const locks = createAliasLocks(identity.hash, options.lockManager);
    const budget = new QueuedReadBudget(64 * 1024 * 1024);
    const controlBudget = new QueuedReadBudget(2 * validateStorageLimits(options.limits).maxManifestBytes);
    let nativeInstanceId = crypto.randomUUID();
    let lease: { alias: AliasLockOwner; view: ViewLockOwner; } | undefined;
    const physical = new Map<string, PhysicalStorage>();
    type Account = { storage: NativeStorage<any>; maximum: number; checkpoint: any; rows: Map<string, { bytes: number; identityHash?: string; }>; };
    const accounts = new Map<string, Account>();
    const registerAccount = (name: string, storage: NativeStorage<any>, maximum: number) => {
      if (!accounts.has(name)) accounts.set(name, { storage, maximum, checkpoint: undefined, rows: new Map() });
    };
    const reconcileAccount = async (name: string, account: Account) => {
      account.rows.clear();
      const consume = async (rows: any[]) => {
        for (const row of rows) account.rows.set(row.key ?? row.id, { bytes: encodedRowBytes(row), ...((row.kind === 'd' || row.kind === 'm') ? { identityHash: row.key.slice(2) } : {}) });
      };
      if (name === 'manifest') { await withRows(account.storage, ['manifest'], account.maximum, controlBudget, consume); return; }
      let after: string | undefined;
      const count = Math.min(4, Math.floor(budget.availableBytes / account.maximum));
      while (true) {
        const consumePage = async (rows: any[]) => {
          await consume(rows);
          if (rows.length) after = (rows[rows.length - 1] as any).key ?? (rows[rows.length - 1] as any).id;
          return rows.length;
        };
        const length = name.startsWith('meta_') ?
          await withMetadataScanPage(account.storage, after, count, account.maximum, budget, consumePage) :
          await withScanPage(account.storage, after, count, account.maximum, budget, consumePage);
        if (length < count) return;
      }
    };
    const refreshAccounts = async (authoritativeBytes = false, skipManifest = false) => {
      for (const [name, account] of accounts) {
        if (skipManifest && name === 'manifest') continue;
        const pool = name === 'manifest' ? controlBudget : budget;
        while (true) {
          const count = Math.min(checkpointRows, Math.floor(pool.availableBytes / account.maximum));
          const result = await pool.withReservation(count * account.maximum, () => getChangedDocumentsSince(account.storage, count, account.checkpoint));
          for (const row of result.documents) {
            const key = row[account.storage.schema.primaryKey as string] ?? row.key ?? row.id;
            if (row._deleted) account.rows.delete(key);
            else account.rows.set(key, { bytes: encodedRowBytes(row), ...((row.kind === 'd' || row.kind === 'm') ? { identityHash: row.key.slice(2) } : {}) });
          }
          account.checkpoint = result.checkpoint;
          if (result.documents.length < count) break;
        }
        // Counts are authoritative across realms even when equal timestamps put
        // a new lower primary key behind the incremental checkpoint.
        if (authoritativeBytes || await countRows(account.storage) !== account.rows.size) await reconcileAccount(name, account);
      }
    };
    const totals = () => {
      const ids = new Set<string>(); let bytes = 0; let rows = 0;
      for (const account of accounts.values()) for (const row of account.rows.values()) {
        bytes += row.bytes; rows++; if (row.identityHash !== undefined) ids.add(row.identityHash);
      }
      return { ids, bytes, rows };
    };
    backend = await openAliasBackend({
      name: `syntrix-${identity.hash}`, limits: options.limits, storage: options.storage,
      writeMaterialization: { data: budget, control: controlBudget },
      beforeWrite: async (kind, name, rows) => {
        assertActive();
        if (!lease) fail('ReplicaWriteFence', 'Storage writes require an active view owner');
        locks.assertAliasOwner(lease!.alias); locks.assertViewOwner(lease!.view, 'exclusive');
        if (kind === 'manifest') {
          const previous = rows[0].previous;
          if (previous) accounts.get('manifest')!.rows.set('manifest', { bytes: encodedRowBytes(previous) });
        }
        await refreshAccounts(backend!.limits.maxStoredBytes !== Number.MAX_SAFE_INTEGER, kind === 'manifest');
        const current = totals();
        for (const { document } of rows) {
          const key = document.key ?? document.id;
          current.bytes += encodedRowBytes(document) - (accounts.get(name)?.rows.get(key)?.bytes ?? 0);
          if (document.kind === 'd' || document.kind === 'm') current.ids.add(document.key.slice(2));
        }
        if (current.ids.size > backend!.limits.maxKnownIds || current.bytes > backend!.limits.maxStoredBytes) fail('ReplicaStorageLimit', 'Alias storage capacity exceeded');
        assertActive();
      },
    });
    assertActive();
    const db = backend;
    registerAccount('manifest', db.manifestStorage, db.limits.maxManifestBytes);
    const assertManifest = async (row: AliasManifest) => {
      await validateManifestIdentity(row);
      if (canonicalJson(row.namespace) !== canonicalJson(identity.tuple) || row.definitionHash !== hash || canonicalJson(row.definition) !== canonicalJson(definition)) {
        fail('ReplicaScopeChanged', 'Alias namespace or source definition differs from its durable binding');
      }
    };
    const readManifest = () => withRows(db.manifestStorage, ['manifest'], db.limits.maxManifestBytes, controlBudget, async rows => {
      if (!rows.length) return fail('ReplicaStorageCorruption', 'Alias manifest is missing');
      await assertManifest(rows[0]); return rows[0];
    });
    const readViewManifest = (logicalId?: string): Promise<ManifestView> => withRows(db.manifestStorage, ['manifest'], db.limits.maxManifestBytes, controlBudget, async rows => {
      if (!rows.length) return fail('ReplicaStorageCorruption', 'Alias manifest is missing');
      const row = rows[0]; await assertManifest(row);
      return {
        activePhysicalEpoch: row.activePhysicalEpoch, physicalEpochs: row.physicalEpochs,
        activeSourceGeneration: row.activeSourceGeneration, stagedSourceGeneration: row.stagedSourceGeneration, boundDatabaseId: row.boundDatabaseId, sourceHash: row.sourceHash,
        sourceReady: row.sourceReady, state: row.state,
        protected: logicalId !== undefined && (!!row.dirtyUpstream?.targets.some(target => target.logicalId === logicalId) || row.issues.some(issue => issue.logicalId === logicalId)),
        recovering: logicalId !== undefined && row.recoveryIntent?.logicalId === logicalId
      };
    });
    const openPhysical = async (epoch: string) => {
      let opened = physical.get(epoch);
      if (!opened) {
        opened = await db.openPhysical(epoch); physical.set(epoch, opened);
        registerAccount(`records_${epoch}`, opened.fork, db.limits.maxRecordBytes);
        registerAccount(`meta_${epoch}`, opened.meta, db.limits.maxMetadataBytes);
        subscriptions.push(opened.fork.changeStream().subscribe(event => changes.next({ type: 'row', physicalEpoch: epoch, keys: event.events.map(item => item.documentId) })));
      }
      return opened;
    };
    const owned = async <T>(alias: AliasLockOwner, mode: 'shared' | 'exclusive', callback: () => Promise<T>) => locks.withView(alias, mode, async view => {
      if (mode === 'exclusive') lease = { alias, view };
      try { assertActive(); return await callback(); } finally { if (mode === 'exclusive') lease = undefined; }
    });
    let accessQueue: Promise<unknown> = Promise.resolve();
    const access = <T>(mode: 'shared' | 'exclusive', callback: (manifest: ManifestView, physical: PhysicalStorage) => Promise<T>, scope?: RequestScope, logicalId?: string): Promise<T> => {
      const previous = accessQueue;
      const operation = run(async () => {
        await previous; assertActive();
        return locks.withAlias('shared', alias => owned(alias, mode, async () => {
          const manifest = await readViewManifest(logicalId);
          const releaseView = controlBudget.retain(encodedRowBytes(manifest));
          try {
            for (const name of accounts.keys()) if (name !== 'manifest' && !manifest.physicalEpochs.some(epoch => name === `records_${epoch}` || name === `meta_${epoch}`)) accounts.delete(name);
            if (scope) checkScope(scope, manifest);
            return await callback(manifest, await openPhysical(manifest.activePhysicalEpoch));
          } finally { releaseView(); }
        }), session.signal);
      });
      accessQueue = operation.catch(() => undefined);
      return operation;
    };
    const checkScope = (scope: RequestScope, manifest: Pick<AliasManifest, 'activePhysicalEpoch'>) => {
      assertActive();
      if (scope.subject !== session.subject || scope.sessionVersion !== session.version || scope.definitionHash !== hash ||
        scope.physicalEpoch !== manifest.activePhysicalEpoch || scope.nativeInstanceId !== nativeInstanceId) fail('ReplicaScopeChanged', 'Request scope is obsolete');
    };
    const writeManifest = async (next: AliasManifest, previous: NativeRow<AliasManifest>) => {
      await assertManifest(next);
      const saved = await db.writeManifest(next, previous, 'alias-manifest');
      changes.next({ type: 'view', physicalEpoch: saved.activePhysicalEpoch, activeSourceGeneration: saved.activeSourceGeneration });
      return saved;
    };
    await locks.withAlias('exclusive', alias => owned(alias, 'exclusive', async () => {
      let manifest = await withRows(db.manifestStorage, ['manifest'], db.limits.maxManifestBytes, controlBudget, async rows => { if (rows[0]) await assertManifest(rows[0]); return rows[0]; });
      if (!manifest) {
        const epoch = crypto.randomUUID();
        manifest = await db.writeManifest({
          key: 'manifest', formatVersion: 1, namespace: identity.tuple, definition, definitionHash: hash,
          boundDatabaseId: null, sourceHash: null, state: 'creating', activePhysicalEpoch: epoch, physicalEpochs: [epoch], maintenance: null,
          activeSourceGeneration: null, stagedSourceGeneration: null, sourceReady: false, partialDelivery: false, dirtyUpstream: null, issues: [], recoveryIntent: null
        }, undefined, 'alias-create');
      }
      await assertManifest(manifest);
      const releaseInitialManifest = controlBudget.retain(encodedRowBytes(manifest));
      try {
        await openPhysical(manifest.activePhysicalEpoch);
        if (manifest.state === 'creating') manifest = await writeManifest({ ...manifest, state: 'ready' }, manifest);
        for (const epoch of manifest.physicalEpochs) if (epoch !== manifest.activePhysicalEpoch) {
          await db.removePhysical(epoch); physical.delete(epoch); accounts.delete(`records_${epoch}`); accounts.delete(`meta_${epoch}`);
        }
        if (manifest.physicalEpochs.length > 1 || manifest.maintenance) await writeManifest({ ...manifest, physicalEpochs: [manifest.activePhysicalEpoch], maintenance: null }, manifest);
      } finally { releaseInitialManifest(); }
    }), session.signal);
    subscriptions.push(db.manifestStorage.changeStream().subscribe(event => {
      for (const item of event.events) if (item.documentData) changes.next({ type: 'view', physicalEpoch: item.documentData.activePhysicalEpoch, activeSourceGeneration: item.documentData.activeSourceGeneration });
    }));
    const rowsFor = async (opened: PhysicalStorage, id: string) => {
      const dk = await recordKey('d', id), mk = await recordKey('m', id);
      const pair = await withRows(opened.fork, [dk, mk], db.limits.maxRecordBytes, budget, async rows => {
        for (const row of rows) { await validateRecordIdentity(row); if (row.kind === 'c' || row.logicalId !== id) fail('ReplicaStorageCorruption', 'Logical ID mismatch'); }
        return { data: rows.find(row => row.kind === 'd') as NativeRow<DataRecord> | undefined, member: rows.find(row => row.kind === 'm') as NativeRow<MemberRecord> | undefined };
      });
      return { ...pair, release: budget.retain((pair.data ? encodedRowBytes(pair.data) : 0) + (pair.member ? encodedRowBytes(pair.member) : 0)) };
    };
    const visible = async (manifest: ManifestView, opened: PhysicalStorage, data: DataRecord | undefined, member: MemberRecord | undefined) => {
      if (!data || data.existence === 'absent') return false;
      if (data.pin || member?.slots.some(slot => slot.generation === manifest.activeSourceGeneration && slot.member) ||
        manifest.protected) return true;
      return withRows(opened.meta, [`${data.key}|0`], db.limits.maxMetadataBytes, budget, async rows => !rows.length || !businessEqual(data, rows[0].docData));
    };
    const read = (id: string, readOptions: { showDeleted?: boolean; } = {}) => {
      validateLogicalId(id);
      return access('shared', async (manifest, opened) => {
        const { data, member, release } = await rowsFor(opened, id);
        try {
          if (!await visible(manifest, opened, data, member) || (!readOptions.showDeleted && data?.existence === 'deleted')) return null;
          return projectDocument(definition.collection, data ?? null, member ?? null);
        } finally { release(); }
      }, undefined, id);
    };
    const mutate = (id: string, type: 'set' | 'update' | 'delete', value: Record<string, unknown> | undefined, mutation: MutationOptions = {}) => {
      validateLogicalId(id);
      const conditions = frozenConditions(mutation.ifMatch ?? []);
      const payload = value === undefined ? undefined : encodeBusinessPayload(value);
      return access('exclusive', async (manifest, opened) => {
        if (manifest.recovering) fail('ReplicaRecoveryPending', 'Document is being recovered');
        for (let attempt = 0; attempt < 8; attempt++) {
          assertActive();
          const { data, member, release } = await rowsFor(opened, id);
          try {
            const shown = await visible(manifest, opened, data, member);
            const document = shown ? projectDocument(definition.collection, data ?? null, member ?? null) : null;
            if (!matchesConditions(document, conditions)) fail('ReplicaConditionFailed', 'Replica write condition failed');
            if (type === 'update' && (!document || document.deleted)) fail('ReplicaDocumentNotFound', 'Update requires a live replica document');
            if (type === 'delete' && (!document || document.deleted)) return;
            const token = crypto.randomUUID();
            const next: DataRecord = {
              key: await recordKey('d', id), kind: 'd', logicalId: id, existence: type === 'delete' ? 'deleted' : 'live',
              payload: type === 'delete' ? '' : type === 'update' ? encodeBusinessPayload({ ...decodeBusinessPayload(data!.payload), ...decodeBusinessPayload(payload!) }) : payload!,
              editToken: token, pin: { token, stage: 'await-settlement' }, wire: data?.wire ?? {}
            };
            try { await db.writeRecord(opened, next, data, 'local-edit'); return; }
            catch (error) { if (!isConflict(error)) throw error; }
          } finally { release(); }
        }
        fail('ReplicaWriteConflict', 'Replica CAS retry limit exceeded');
      }, undefined, id);
    };
    const storage: AliasStorage = {
      changes: changes.asObservable(), namespace: identity.hash, limits: db.limits,
      read, get: read, set: (id, value, mutation) => mutate(id, 'set', value, mutation),
      update: (id, value, mutation) => mutate(id, 'update', value, mutation), delete: (id, mutation) => mutate(id, 'delete', undefined, mutation),
      generateId: () => { assertActive(); return crypto.randomUUID(); },
      add: async value => { const id = crypto.randomUUID(); await mutate(id, 'set', value); return id; },
      captureScope: (requestId = crypto.randomUUID()) => access('shared', async manifest => Object.freeze({ subject: session.subject, sessionVersion: session.version, definitionHash: hash, physicalEpoch: manifest.activePhysicalEpoch, requestId, nativeInstanceId })),
      bind: (scope, databaseIdentity, sourceHash) => access('exclusive', async () => {
        let manifest = await readManifest();
        let release = controlBudget.retain(encodedRowBytes(manifest));
        try {
          if (!databaseIdentity || !sourceHash) throw new TypeError('Source binding requires database identity and source hash');
          for (let attempt = 0; attempt < 8; attempt++) {
            checkScope(scope, manifest);
            if (manifest.boundDatabaseId !== null) {
              if (manifest.boundDatabaseId !== databaseIdentity || manifest.sourceHash !== sourceHash) { blocked = true; fail('ReplicaScopeChanged', 'Database identity or source binding changed'); }
              return;
            }
            try { await writeManifest({ ...manifest, boundDatabaseId: databaseIdentity, sourceHash }, manifest); return; }
            catch (error) { if (!isConflict(error)) throw error; const next = await readManifest(); release(); manifest = next; release = controlBudget.retain(encodedRowBytes(manifest)); }
          }
          fail('ReplicaWriteConflict', 'Binding CAS retry limit exceeded');
        } finally { release(); }
      }, scope),
      guardNetwork: scope => access('shared', async manifest => {
        if (blocked) fail('ReplicaScopeChanged', 'Alias network admission is blocked');
        if (!manifest.boundDatabaseId || !manifest.sourceReady || manifest.state !== 'ready' || maintaining) fail('ReplicaSourceNotReady', 'Alias source is not ready for network writes');
        return Object.freeze({ 'X-Syntrix-Expected-Database-Identity': manifest.boundDatabaseId! });
      }, scope),
      blockScope: () => { blocked = true; },
      registerNative: resource => { assertActive(); if (maintaining) fail('ReplicaMaintenance', 'Alias is under maintenance'); natives.add(resource); return () => { natives.delete(resource); }; },
      readManifest: () => access('shared', async () => readManifest()),
      withMaintenance: callback => run(async () => {
        if (maintaining) fail('ReplicaMaintenance', 'Alias maintenance already in progress');
        maintaining = true;
        nativeInstanceId = crypto.randomUUID();
        try {
          await stopNative();
          return await locks.withAlias('exclusive', alias => owned(alias, 'exclusive', async () => {
            let manifest = await readManifest();
            let releaseManifest = controlBudget.retain(encodedRowBytes(manifest));
            const replaceManifest = (next: NativeRow<AliasManifest>) => { releaseManifest(); manifest = next; releaseManifest = controlBudget.retain(encodedRowBytes(manifest)); access.manifest = manifest; return manifest; };
            let active = true;
            const assertOwned = () => { assertActive(); if (!active) fail('ReplicaWriteFence', 'Maintenance access has expired'); locks.assertAliasOwner(alias, 'exclusive'); };
            const ownedStorage = <T>(native: NativeStorage<T>): NativeStorage<T> => new Proxy(native, {
              get(target, property) {
                if (property === 'underlyingPersistentStorage') return undefined;
                const value = Reflect.get(target, property, target);
                return typeof value === 'function' ? (...args: any[]) => { assertOwned(); return value.apply(target, args); } : value;
              },
            });
            const ownedBackend = new Proxy(db, {
              get(target, property) {
                const value = Reflect.get(target, property, target);
                if (property === 'openPhysical') return async (epoch: string) => {
                  assertOwned();
                  if (!manifest.physicalEpochs.includes(epoch)) fail('ReplicaWriteFence', 'Physical epoch is not owned by the maintenance manifest');
                  const opened = await openPhysical(epoch); assertOwned();
                  return { ...opened, fork: ownedStorage(opened.fork), meta: ownedStorage(opened.meta) };
                };
                if (property === 'manifestStorage') return ownedStorage(db.manifestStorage);
                if (property === 'removePhysical') return async (epoch: string) => { assertOwned(); await db.removePhysical(epoch); physical.delete(epoch); accounts.delete(`records_${epoch}`); accounts.delete(`meta_${epoch}`); };
                return typeof value === 'function' ? (...args: any[]) => { assertOwned(); return value.apply(target, args); } : value;
              }
            });
            const access: MaintenanceAccess = {
              backend: ownedBackend, manifest, limits: db.limits, budget, ownerSignal: session.signal, assertActive: assertOwned,
              readManifest: async () => { assertOwned(); return replaceManifest(await readManifest()); },
              writeManifest: async next => { assertOwned(); return replaceManifest(await writeManifest(next, manifest)); }
            };
            try { return await callback(access); } finally { active = false; releaseManifest(); }
          }), session.signal);
        } finally { maintaining = false; }
      }),
      native: scope => access('shared', async (_manifest, opened) => {
        const wrap = <T>(native: NativeStorage<T>, maximum: number): NativeStorage<T> => new Proxy(native, {
          get(target, property) {
            if (property === 'underlyingPersistentStorage') return undefined;
            if (property === 'bulkWrite') return (rows: any[], context: string) => access('exclusive', async () => target.bulkWrite(rows, context), scope);
            const boundedRead = async (requested: number, read: (limit: number, offset: number) => Promise<NativeRow<T>[]>) => {
              if (!Number.isSafeInteger(requested) || requested < 0) throw new TypeError('Invalid native read limit');
              const result: NativeRow<T>[] = [];
              const maxRetained = 128 * 1024 * 1024;
              const block = Math.min(4, Math.floor(budget.maxBytes / maximum));
              let retained = 0;
              for (let offset = 0; offset < requested;) {
                const count = Math.min(block, requested - offset);
                if (retained + count * maximum > maxRetained) fail('ReplicaReadBudgetExceeded', 'Native result exceeds materialization budget');
                const rows = await budget.withReservation(count * maximum, () => read(count, offset));
                if (rows.length > count) fail('ReplicaStorageCorruption', 'Native read exceeded requested row count');
                for (const row of rows) {
                  const bytes = encodedRowBytes(row);
                  if (bytes > maximum) fail('ReplicaRecordTooLarge', 'Native read contains an oversized row');
                  retained += bytes;
                }
                result.push(...rows); offset += count;
              }
              return result;
            };
            if (property === 'findDocumentsById') return (ids: string[], deleted: boolean) => access('shared', async () => {
              return boundedRead(ids.length, (limit, offset) => target.findDocumentsById(ids.slice(offset, offset + limit), deleted));
            }, scope);
            if (property === 'query') return (query: any) => access('shared', async () => {
              const limit = query.query.limit;
              if (query.query.skip !== 0 || !Number.isSafeInteger(limit) || limit < 1 || limit > 4 ||
                !query.queryPlan.selectorSatisfiedByIndex || !query.queryPlan.sortSatisfiedByIndex) {
                fail('ReplicaReadBudgetExceeded', 'Native queries require a bounded index-satisfied seek');
              }
              return budget.withReservation(limit * maximum, () => target.query(query));
            }, scope);
            if (property === 'getChangedDocumentsSince') return (limit: number, checkpoint: any) => access('shared', async () => {
              let next = checkpoint; let exhausted = false;
              const documents = await boundedRead(limit, async count => {
                if (exhausted) return [];
                const result = await getChangedDocumentsSince(target, count, next);
                next = result.checkpoint;
                if (result.documents.length < count) exhausted = true;
                return result.documents;
              });
              return { documents, checkpoint: next };
            }, scope);
            if (property === 'close' || property === 'remove' || property === 'cleanup') return async () => fail('ReplicaWriteFence', 'Physical lifecycle is owned by alias storage');
            const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
          },
        });
        return { fork: wrap(opened.fork, db.limits.maxRecordBytes), meta: wrap(opened.meta, db.limits.maxMetadataBytes), identifier: opened.identifier, ownerSignal: session.signal };
      }, scope),
      stats: () => access('exclusive', async (manifest, opened) => {
        await refreshAccounts(true);
        const total = totals(); let retired = 0; let after: string | undefined;
        while (true) {
          const page = await withScanPage(opened.fork, after, 1, db.limits.maxRecordBytes, budget, async rows => rows);
          if (!page.length) break;
          const row = page[0]; after = row.key;
          const releaseRow = budget.retain(encodedRowBytes(row));
          try {
            if (row.kind === 'd') {
              const view = await readViewManifest(row.logicalId);
              const member = await withRows(opened.fork, [await recordKey('m', row.logicalId)], db.limits.maxRecordBytes, budget, async rows => rows[0] as MemberRecord | undefined);
              const releaseMember = budget.retain(member ? encodedRowBytes(member) : 0);
              try { if (!await visible(view, opened, row, member)) retired++; } finally { releaseMember(); }
            } else if (row.kind === 'm' && !row.slots.some(slot => slot.member &&
              (slot.generation === manifest.activeSourceGeneration || slot.generation === manifest.stagedSourceGeneration))) {
              const hasData = await withRows(opened.fork, [await recordKey('d', row.logicalId)], db.limits.maxRecordBytes, budget, async rows => {
                if (rows[0]) {
                  await validateRecordIdentity(rows[0]);
                  if (rows[0].kind !== 'd' || rows[0].logicalId !== row.logicalId) fail('ReplicaStorageCorruption', 'Logical ID mismatch');
                }
                return rows.length > 0;
              });
              if (!hasData) retired++;
            }
          } finally { releaseRow(); }
        }
        const estimate = globalThis.navigator?.storage ? await navigator.storage.estimate() : {};
        const pressure = total.ids.size >= db.limits.maxKnownIds * 0.9 || total.bytes >= db.limits.maxStoredBytes * 0.9 ||
          (estimate.quota !== undefined && estimate.usage !== undefined && estimate.usage >= estimate.quota * 0.9);
        return { knownIds: total.ids.size, rows: total.rows, bytes: total.bytes, retired, ...estimate, shouldCompact: pressure || (retired >= 1000 && retired >= total.ids.size * 0.25) };
      }),
      close,
    };
    assertActive(); return storage;
  });
  opening = construction.catch(async error => {
    stopped = true;
    if (error instanceof BackendCleanupError) cleanupFailure = error;
    await cleanupStorage();
    throw error;
  });
  return opening;
};
