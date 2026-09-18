import {
  addConnectedStorageToCollection, createRxDatabase, fillObjectDataBeforeInsert,
  getPrimaryFieldOfPrimaryKey, getRxReplicationMetaInstanceSchema, normalizeMangoQuery, prepareQuery, writeSingle,
  type BulkWriteRow, type RxCollection, type RxDatabase, type RxDocumentData,
  type RxJsonSchema, type RxStorage, type RxStorageInstance, type RxStorageReplicationMeta,
} from 'rxdb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { canonicalJson, validateManifest, validateManifestIdentity, validateRecord, validateRecordIdentity } from './records.js';
import { defaultStorageLimits, LocalStorageError, type AliasManifest, type LocalRecord, type StorageLimits } from './storage-types.js';

export type NativeRow<T> = RxDocumentData<T>;
export type NativeStorage<T> = RxStorageInstance<T, any, any>;
export type WriteKind = 'record' | 'manifest' | 'metadata';
export type BeforeWrite = (kind: WriteKind, physicalName: string, rows: BulkWriteRow<any>[], context: string) => Promise<void>;
export class BackendCleanupError extends LocalStorageError {
  constructor(cause: unknown, readonly cleanupErrors: readonly unknown[]) {
    super('LocalStorageCleanupFailed', 'Local storage cleanup did not complete', { cause });
  }
}
export type PhysicalStorage = {
  epoch: string;
  records: RxCollection<LocalRecord>;
  fork: NativeStorage<LocalRecord>;
  meta: NativeStorage<RxStorageReplicationMeta<LocalRecord, any>>;
  identifier: string;
};

const positive = (value: number, label: string) => {
  if (!Number.isSafeInteger(value) || value <= 0) throw new RangeError(`${label} must be a positive safe integer`);
};
export const validateStorageLimits = (partial: Partial<StorageLimits> = {}): StorageLimits => {
  for (const name of Object.keys(partial)) if (!(name in defaultStorageLimits)) throw new TypeError(`Unknown storage limit: ${name}`);
  const limits = { ...defaultStorageLimits, ...partial };
  for (const [name, value] of Object.entries(limits)) positive(value, name);
  if (limits.maxMetadataBytes <= limits.maxRecordBytes) throw new RangeError('Metadata budget must exceed record budget');
  return limits;
};

export class ReadBudget {
  private used = 0;
  private peak = 0;
  constructor(readonly maxBytes: number) { positive(maxBytes, 'Read budget'); }
  get usedBytes() { return this.used; }
  get peakBytes() { return this.peak; }
  async withReservation<T>(bytes: number, operation: () => Promise<T>): Promise<T> {
    positive(bytes, 'Read reservation');
    if (bytes > this.maxBytes - this.used) throw new LocalStorageError('LocalReadBudgetExceeded', 'Read would exceed the materialization budget');
    this.used += bytes;
    this.peak = Math.max(this.peak, this.used);
    try { return await operation(); } finally { this.used -= bytes; }
  }
}

const readCount = (count: number) => {
  if (!Number.isSafeInteger(count) || count < 1 || count > 4) throw new RangeError('Physical reads require between one and four rows');
};
export const encodedRowBytes = (row: unknown): number => new TextEncoder().encode(canonicalJson(row)).byteLength;
const checkBytes = (row: unknown, maximum: number) => {
  if (encodedRowBytes(row) > maximum) throw new LocalStorageError('LocalRecordTooLarge', 'Encoded storage row exceeds its admission budget');
};

export const withRows = async <T, R>(storage: NativeStorage<T>, ids: string[], maxRowBytes: number, budget: ReadBudget,
  callback: (rows: RxDocumentData<T>[]) => Promise<R>): Promise<R> => {
  readCount(ids.length);
  positive(maxRowBytes, 'Maximum row bytes');
  return budget.withReservation(ids.length * maxRowBytes, async () => {
    const rows = await storage.findDocumentsById(ids, false);
    if (rows.length > ids.length) throw new LocalStorageError('LocalStorageCorruption', 'Storage returned more rows than requested');
    rows.forEach(row => checkBytes(row, maxRowBytes));
    return callback(rows);
  });
};

const seekQuery = <T>(storage: NativeStorage<T>, after: string | undefined, limit?: number) => {
  const primary = getPrimaryFieldOfPrimaryKey(storage.schema.primaryKey);
  const query = prepareQuery(storage.schema, normalizeMangoQuery(storage.schema, {
    selector: { _deleted: { $eq: false }, ...(after === undefined ? {} : { [primary]: { $gt: after } }) } as any,
    sort: [{ [primary]: 'asc' }] as any, index: ['_deleted', primary], skip: 0, ...(limit === undefined ? {} : { limit }),
  }));
  if (!query.queryPlan.selectorSatisfiedByIndex || !query.queryPlan.sortSatisfiedByIndex || query.query.skip !== 0) {
    throw new LocalStorageError('LocalStorageCorruption', 'Physical scan requires an index-satisfied seek plan');
  }
  return { primary, query };
};

export const countRows = async <T>(storage: NativeStorage<T>): Promise<number> => {
  const result = await storage.count(seekQuery(storage, undefined).query);
  if (result.mode !== 'fast' || !Number.isSafeInteger(result.count) || result.count < 0) {
    throw new LocalStorageError('LocalStorageCorruption', 'Physical row count requires an index-only count');
  }
  return result.count;
};

const withPrimaryScanPage = async <T, R>(storage: NativeStorage<T>, after: string | undefined,
  limit: number, maxRowBytes: number, budget: ReadBudget, callback: (rows: RxDocumentData<T>[]) => Promise<R>): Promise<R> => {
  readCount(limit);
  positive(maxRowBytes, 'Maximum row bytes');
  const { primary, query } = seekQuery(storage, after, limit);
  return budget.withReservation(limit * maxRowBytes, async () => {
    const { documents } = await storage.query(query);
    if (documents.length > limit) throw new LocalStorageError('LocalStorageCorruption', 'Storage exceeded the scan bound');
    let previous = after;
    for (const row of documents) {
      checkBytes(row, maxRowBytes);
      const key = (row as Record<string, unknown>)[primary];
      if (typeof key !== 'string' || (previous !== undefined && key <= previous)) throw new LocalStorageError('LocalStorageCorruption', 'Storage scan is not in primary-key order');
      previous = key;
    }
    return callback(documents);
  });
};

export const withScanPage = <T extends { key: string }, R>(storage: NativeStorage<T>, after: string | undefined,
  limit: number, maxRowBytes: number, budget: ReadBudget, callback: (rows: RxDocumentData<T>[]) => Promise<R>): Promise<R> =>
  withPrimaryScanPage(storage, after, limit, maxRowBytes, budget, callback);

export const withMetadataScanPage = <T extends { id: string }, R>(storage: NativeStorage<T>, after: string | undefined,
  limit: number, maxRowBytes: number, budget: ReadBudget, callback: (rows: RxDocumentData<T>[]) => Promise<R>): Promise<R> =>
  withPrimaryScanPage(storage, after, limit, maxRowBytes, budget, callback);

// Domain validators own the discriminated union; the storage schema owns the
// primary/index layout. Validation is repeated below the RxDB revision wrapper.
const schema = <T>(title: string): RxJsonSchema<T> => ({
  title, version: 0, type: 'object', primaryKey: 'key',
  properties: { key: { type: 'string', maxLength: 66 } },
  required: ['key'], indexes: ['key'], additionalProperties: true,
} as unknown as RxJsonSchema<T>);
export const recordSchema = schema<LocalRecord>('SyntrixLocalRecords');
export const manifestSchema = schema<AliasManifest>('SyntrixAliasManifest');

const prepareRawCollection = <T>(collection: RxCollection<T>) => {
  // We use raw storage and our own bounded views, never RxDocument/RxQuery.
  // RxDB 17.5.0 retains full events even with eventReduce:false; disable that
  // history and drain lazy document-cache tasks synchronously without touching
  // the live storage/collection feeds needed by replication and invalidation.
  // https://github.com/pubkey/rxdb/blob/d88180e334512bf0097373ad62e9fbe6811010aa/src/change-event-buffer.ts
  // https://github.com/pubkey/rxdb/blob/d88180e334512bf0097373ad62e9fbe6811010aa/src/doc-cache.ts
  collection._changeEventBuffer.close();
  collection._changeEventBuffer.getBuffer().length = 0;
  collection._docCache.processTasks();
  collection._subs.push(collection.eventBulks$.subscribe(() => collection._docCache.processTasks()));
};

const validateMetadata = async (row: RxDocumentData<RxStorageReplicationMeta<LocalRecord, any>>) => {
  if (row._deleted !== false || !row._meta || !Number.isFinite(row._meta.lwt) || typeof row._rev !== 'string' || !row._rev ||
      !row._attachments || Object.keys(row._attachments).length ||
      !['0', '1'].includes(row.isCheckpoint) || row.id !== `${row.itemId}|${row.isCheckpoint}`) {
    throw new LocalStorageError('LocalStorageCorruption', 'Invalid native replication metadata');
  }
  if (row.isCheckpoint === '0') {
    validateRecord(row.docData);
    await validateRecordIdentity(row.docData);
    if (row.itemId !== row.docData.key) throw new LocalStorageError('LocalStorageCorruption', 'Assumed metadata identity differs from its document');
  } else if (!['up', 'down'].includes(row.itemId) || !row.checkpointData || typeof row.checkpointData !== 'object' || Array.isArray(row.checkpointData)) {
    throw new LocalStorageError('LocalStorageCorruption', 'Invalid native checkpoint');
  }
};

export type AliasBackendOptions = { name: string; limits?: Partial<StorageLimits>; storage?: RxStorage<any, any>; beforeWrite?: BeforeWrite };
export type AliasBackend = {
  database: RxDatabase;
  limits: StorageLimits;
  manifest: RxCollection<AliasManifest>;
  manifestStorage: NativeStorage<AliasManifest>;
  openPhysical(epoch: string): Promise<PhysicalStorage>;
  closePhysical(epoch: string): Promise<void>;
  removePhysical(epoch: string): Promise<void>;
  writeRecord(physical: PhysicalStorage, row: LocalRecord, previous: RxDocumentData<LocalRecord> | undefined, context: string): Promise<RxDocumentData<LocalRecord>>;
  writeManifest(row: AliasManifest, previous: RxDocumentData<AliasManifest> | undefined, context: string): Promise<RxDocumentData<AliasManifest>>;
  close(): Promise<void>;
};

export const openAliasBackend = async (options: AliasBackendOptions): Promise<AliasBackend> => {
  const limits = validateStorageLimits(options.limits);
  const underlying = options.storage ?? getRxStorageDexie();
  const rawClosers = new Set<() => Promise<void>>();
  const guarded: RxStorage<any, any> = {
    ...underlying,
    // Each guarded owner has a distinct in-process storage identity, while the
    // delegate retains the shared durable database and multi-instance channel.
    name: `${underlying.name}-owner-${crypto.randomUUID()}`,
    async createStorageInstance(params) {
      const raw = await underlying.createStorageInstance(params);
      let closing: Promise<void> | undefined;
      const close = () => {
        closing ??= raw.close().then(() => { rawClosers.delete(close); });
        return closing;
      };
      rawClosers.add(close);
      const kind: WriteKind | undefined = params.collectionName === 'manifest' ? 'manifest' :
        params.collectionName.startsWith('records_') ? 'record' : params.collectionName.startsWith('meta_') ? 'metadata' : undefined;
      return new Proxy(raw, {
        get(target, property) {
          if (property === 'close') return close;
          if (property === 'remove') return async () => {
            await target.remove();
            rawClosers.delete(close);
          };
          if (property === 'bulkWrite' && kind) return async (rows: BulkWriteRow<any>[], context: string) => {
            for (const { document } of rows) {
              if (kind === 'record') { validateRecord(document); await validateRecordIdentity(document); }
              else if (kind === 'manifest') {
                validateManifest(document);
                await validateManifestIdentity(document);
                if (document.recoveryIntent) {
                  checkBytes(document.recoveryIntent.current, limits.maxRecordBytes);
                  checkBytes(document.recoveryIntent.desired, limits.maxRecordBytes);
                }
              }
              else await validateMetadata(document);
              checkBytes(document, kind === 'record' ? limits.maxRecordBytes : kind === 'manifest' ? limits.maxManifestBytes : limits.maxMetadataBytes);
            }
            await options.beforeWrite?.(kind, params.collectionName, rows, context);
            return target.bulkWrite(rows, context);
          };
          const value = Reflect.get(target, property, target);
          return typeof value === 'function' ? value.bind(target) : value;
        },
      });
    },
  };
  let database!: RxDatabase;
  const physical = new Map<string, Promise<PhysicalStorage>>();
  let manifest: RxCollection<AliasManifest>;
  try {
    database = await createRxDatabase({ name: options.name, storage: guarded, multiInstance: true, eventReduce: false });
    manifest = (await database.addCollections({ manifest: { schema: manifestSchema } })).manifest;
    prepareRawCollection(manifest);
  } catch (error) {
    const failures: unknown[] = [];
    try { if (database) await database.close(); } catch (cleanupError) { failures.push(cleanupError); }
    const cleanup = await Promise.allSettled(Array.from(rawClosers, close => close()));
    for (const result of cleanup) if (result.status === 'rejected') failures.push(result.reason);
    if (failures.length) throw new BackendCleanupError(error, [...new Set(failures)]);
    throw error;
  }
  let cleanupFailure: BackendCleanupError | undefined;
  const names = (epoch: string) => {
    if (!/^[a-zA-Z0-9-]{1,64}$/.test(epoch)) throw new TypeError('Invalid physical epoch');
    return { records: `records_${epoch}`, meta: `meta_${epoch}` };
  };
  const openPhysical = async (epoch: string): Promise<PhysicalStorage> => {
    const namesForEpoch = names(epoch);
    let opening = physical.get(epoch);
    if (!opening) {
      opening = (async () => {
        const records = (await database.addCollections({ [namesForEpoch.records]: { schema: recordSchema } }))[namesForEpoch.records] as RxCollection<LocalRecord>;
        try {
          prepareRawCollection(records);
          const metaSchema = getRxReplicationMetaInstanceSchema(records.schema.jsonSchema, false);
          // Register first: collection removal must also recover metadata whose
          // creation completed immediately before a process crash.
          await addConnectedStorageToCollection(records, namesForEpoch.meta, metaSchema);
          const meta = await guarded.createStorageInstance({ databaseName: database.name, databaseInstanceToken: database.token,
            collectionName: namesForEpoch.meta, schema: metaSchema, multiInstance: true, options: {}, devMode: false });
          return { epoch, records, fork: records.storageInstance, meta, identifier: `syntrix-local-${epoch}` };
        } catch (error) {
          try { await records.close(); }
          catch (cleanupError) { cleanupFailure = new BackendCleanupError(error, [cleanupError]); throw cleanupFailure; }
          throw error;
        }
      })();
      physical.set(epoch, opening);
      void opening.catch(() => { if (physical.get(epoch) === opening) physical.delete(epoch); });
    }
    return opening;
  };
  const closePhysical = async (epoch: string) => {
    const opening = physical.get(epoch);
    if (!opening) return;
    const opened = await opening;
    await opened.meta.close();
    await opened.records.close();
    physical.delete(epoch);
  };
  return {
    database, limits, manifest, manifestStorage: manifest.storageInstance, openPhysical, closePhysical,
    async removePhysical(epoch) {
      const opened = await openPhysical(epoch);
      await opened.meta.close();
      await opened.records.remove();
      physical.delete(epoch);
    },
    writeRecord: (opened, row, previous, context) => writeSingle(opened.fork,
      { document: fillObjectDataBeforeInsert(opened.records.schema, row), ...(previous ? { previous } : {}) }, context),
    writeManifest: (row, previous, context) => writeSingle(manifest.storageInstance,
      { document: fillObjectDataBeforeInsert(manifest.schema, row), ...(previous ? { previous } : {}) }, context),
    async close() {
      const failures: unknown[] = [];
      for (const epoch of physical.keys()) {
        try { await closePhysical(epoch); } catch (error) { failures.push(error); }
      }
      try { await database.close(); } catch (error) { failures.push(error); }
      const remaining = await Promise.allSettled(Array.from(rawClosers, close => close()));
      for (const result of remaining) if (result.status === 'rejected') failures.push(result.reason);
      if (cleanupFailure) throw cleanupFailure;
      if (failures.length) {
        cleanupFailure = new BackendCleanupError(failures[0], [...new Set(failures)]);
        throw cleanupFailure;
      }
    },
  };
};
