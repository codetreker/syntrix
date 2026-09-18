import { describe, expect, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { getRxStorageMemory } from 'rxdb/plugins/storage-memory';
import { addRxPlugin, type RxDatabase, type RxStorage } from 'rxdb';
import { BackendCleanupError, countRows, encodedRowBytes, openAliasBackend, ReadBudget, validateStorageLimits, withMetadataScanPage, withRows, withScanPage } from './backend.js';
import { decodeBusinessPayload, definitionHash, encodeBusinessPayload, freezeSourceDefinition, recordKey } from './records.js';
import type { AliasManifest, DataRecord, LocalRecord } from './storage-types.js';

const data = async (id = 'alice'): Promise<DataRecord> => ({
  key: await recordKey('d', id), kind: 'd', logicalId: id, existence: 'live',
  payload: encodeBusinessPayload({ count: 9007199254740993n }), editToken: null, pin: null, wire: {},
});
const manifestRow = async (): Promise<AliasManifest> => {
  const definition = freezeSourceDefinition({ collection: 'users', filters: [] });
  return { key: 'manifest', formatVersion: 1,
    namespace: { endpoint: 'https://example.test', subject: 'user', database: 'db', name: 'local', alias: 'users' },
    definition, definitionHash: await definitionHash(definition), boundDatabaseId: null, sourceHash: null,
    state: 'ready', activePhysicalEpoch: 'p1', physicalEpochs: ['p1'], maintenance: null,
    activeSourceGeneration: null, stagedSourceGeneration: null, sourceReady: false, partialDelivery: false,
    dirtyUpstream: null, issues: [], recoveryIntent: null };
};
const dexie = () => getRxStorageDexie({ indexedDB, IDBKeyRange });
const name = () => `backend-${crypto.randomUUID()}`;
const nativeMeta = (row: LocalRecord) => ({ id: `${row.key}|0`, itemId: row.key, isCheckpoint: '0',
  docData: { ...row, _deleted: false }, _deleted: false, _attachments: {}, _meta: { lwt: Date.now() }, _rev: '1-test' });

describe('alias physical storage', () => {
  test('Dexie persists exact typed rows, revisions and manifest across close and reopen', async () => {
    const options = { name: name(), storage: dexie() };
    let backend = await openAliasBackend(options);
    let opened = await backend.openPhysical('p1');
    const row = await data();
    const saved = await backend.writeRecord(opened, row, undefined, 'test');
    expect(saved._rev).toMatch(/^1-/);
    expect(saved._meta.lwt).toBeGreaterThan(0);
    await backend.writeManifest(await manifestRow(), undefined, 'manifest');
    await backend.close();
    backend = await openAliasBackend(options);
    opened = await backend.openPhysical('p1');
    try {
      const budget = new ReadBudget(64 * 1024 * 1024);
      await withRows(opened.fork, [row.key], backend.limits.maxRecordBytes, budget, async rows => {
        expect(rows[0]._rev).toBe(saved._rev);
        expect(decodeBusinessPayload((rows[0] as DataRecord).payload)).toEqual({ count: 9007199254740993n });
      });
      await withRows(backend.manifestStorage, ['manifest'], backend.limits.maxManifestBytes, budget, async rows => {
        expect(rows[0].activePhysicalEpoch).toBe('p1');
      });
      expect(budget.usedBytes).toBe(0);
    } finally { await backend.close(); }
  });

  test('validates final wrapped rows and preserves CAS and storage failures', async () => {
    const contexts: string[] = [];
    const fault = new Error('quota');
    const backend = await openAliasBackend({ name: name(), storage: getRxStorageMemory(), beforeWrite: async (kind, _name, rows, context) => {
      contexts.push(`${kind}:${context}`);
      expect(rows[0].document._rev).toBeTruthy();
      expect(rows[0].document._meta.lwt).toBeGreaterThan(0);
      if (context === 'fail') throw fault;
    } });
    try {
      const opened = await backend.openPhysical('p1');
      const row = await data();
      const saved = await backend.writeRecord(opened, row, undefined, 'insert');
      await expect(backend.writeRecord(opened, row, undefined, 'conflict')).rejects.toMatchObject({ status: 409 });
      await expect(backend.writeRecord(opened, row, saved, 'fail')).rejects.toBe(fault);
      const updated = await backend.writeRecord(opened, { ...row, payload: encodeBusinessPayload({ value: 'new' }) }, saved, 'update');
      expect(updated._rev).toMatch(/^2-/);
      await expect(backend.writeRecord(opened, { ...row, logicalId: 'wrong' }, updated, 'bad')).rejects.toThrow('identity');
      await expect(opened.fork.bulkWrite([{ previous: updated, document: { ...updated, _deleted: true } }], 'delete')).rejects.toThrow('tombstones');
      expect(contexts).toEqual(['record:insert', 'record:conflict', 'record:fail', 'record:update']);
    } finally { await backend.close(); }
  });

  test('enforces record, control, assumed, checkpoint and manifest budgets before persistence', async () => {
    const backend = await openAliasBackend({ name: name(), storage: getRxStorageMemory(),
      limits: { maxRecordBytes: 700, maxMetadataBytes: 900, maxManifestBytes: 1500 } });
    try {
      const opened = await backend.openPhysical('p1');
      const row = await data();
      await backend.writeRecord(opened, row, undefined, 'small');
      const oversized = { ...await data('large'), payload: encodeBusinessPayload({ text: 'x'.repeat(800) }) };
      await expect(backend.writeRecord(opened, oversized, undefined, 'large')).rejects.toThrow('budget');
      const control = { key: 'c:progress', kind: 'c', checkpoint: { large: 'x'.repeat(800) }, generation: null,
        phase: 'scan', bootstrapComplete: false, partialDelivery: false } as const;
      await expect(backend.writeRecord(opened, control, undefined, 'control')).rejects.toThrow('budget');
      await expect(opened.meta.bulkWrite([{ document: nativeMeta(oversized) as any }], 'assumed')).rejects.toThrow('budget');
      const checkpoint = { ...nativeMeta(row), id: 'down|1', itemId: 'down', isCheckpoint: '1', checkpointData: { huge: 'x'.repeat(1000) } };
      delete (checkpoint as any).docData;
      await expect(opened.meta.bulkWrite([{ document: checkpoint as any }], 'checkpoint')).rejects.toThrow('budget');
      const manifest = await manifestRow();
      await backend.writeManifest(manifest, undefined, 'manifest');
      await expect(backend.writeManifest({ ...manifest, recoveryIntent: {
        id: 'intent', action: 'merge', logicalId: oversized.logicalId, protectedToken: null, resultToken: 'result',
        current: oversized, desired: { ...oversized, editToken: 'result', pin: { token: 'result', stage: 'await-settlement' } },
      } }, undefined, 'large-manifest')).rejects.toThrow('budget');
      await expect(backend.writeManifest({ ...manifest, namespace: { ...manifest.namespace, name: 'x'.repeat(1500) } }, undefined, 'large-envelope')).rejects.toThrow('budget');
      expect(await opened.fork.findDocumentsById([oversized.key, 'c:progress'], false)).toHaveLength(0);
      expect(await opened.meta.findDocumentsById([`${oversized.key}|0`, 'down|1'], false)).toHaveLength(0);
    } finally { await backend.close(); }
  });

  test('admission counts revision and metadata inserted by the native wrapper', async () => {
    const row = await data();
    const backend = await openAliasBackend({ name: name(), storage: getRxStorageMemory(), limits: { maxRecordBytes: encodedRowBytes(row) } });
    try {
      const opened = await backend.openPhysical('p1');
      await expect(backend.writeRecord(opened, row, undefined, 'insert')).rejects.toThrow('budget');
      expect(await opened.fork.findDocumentsById([row.key], false)).toEqual([]);
    } finally { await backend.close(); }
  });

  test('raw record and manifest writes retain no native query history or document-cache tasks', async () => {
    const backend = await openAliasBackend({ name: name(), storage: dexie() });
    try {
      const opened = await backend.openPhysical('p1');
      let recordEvents = 0;
      let manifestEvents = 0;
      let rawRecordEvents = 0;
      let rawManifestEvents = 0;
      const subscriptions = [
        opened.records.eventBulks$.subscribe(bulk => { recordEvents += bulk.events.length; }),
        backend.manifest.eventBulks$.subscribe(bulk => { manifestEvents += bulk.events.length; }),
        opened.fork.changeStream().subscribe(bulk => { rawRecordEvents += bulk.events.length; }),
        backend.manifestStorage.changeStream().subscribe(bulk => { rawManifestEvents += bulk.events.length; }),
      ];
      try {
        const manifest = await manifestRow();
        let previous: Awaited<ReturnType<typeof backend.writeManifest>> | undefined;
        const ids: string[] = [];
        for (let index = 0; index < 6; index++) {
          const row = { ...await data(`large${index}`), payload: encodeBusinessPayload({ text: 'x'.repeat(32 * 1024) }) };
          ids.push(row.key);
          await backend.writeRecord(opened, row, undefined, 'history');
          previous = await backend.writeManifest({ ...manifest, namespace: { ...manifest.namespace, name: 'x'.repeat(32 * 1024) },
            activeSourceGeneration: `g${index}` }, previous, 'history');
          for (const collection of [opened.records, backend.manifest]) {
            expect(collection._changeEventBuffer.getBuffer()).toHaveLength(0);
            expect(collection._docCache.tasks.size).toBe(0);
            expect(collection._docCache.cacheItemByDocId.size).toBe(0);
          }
        }
        expect([recordEvents, manifestEvents, rawRecordEvents, rawManifestEvents]).toEqual([6, 6, 6, 6]);
        expect((await opened.fork.findDocumentsById([ids[0]], false))[0].key).toBe(ids[0]);
        expect((await backend.manifestStorage.findDocumentsById(['manifest'], false))[0].activeSourceGeneration).toBe('g5');
      } finally { subscriptions.forEach(subscription => subscription.unsubscribe()); }
    } finally { await backend.close(); }
  });

  test('connected metadata is removed with its epoch and reopening starts empty', async () => {
    const backend = await openAliasBackend({ name: name(), storage: dexie() });
    try {
      let opened = await backend.openPhysical('p1');
      const row = await data();
      await backend.writeRecord(opened, row, undefined, 'insert');
      expect((await opened.meta.bulkWrite([{ document: nativeMeta(row) as any }], 'assumed')).error).toEqual([]);
      expect(await countRows(opened.fork)).toBe(1);
      expect(await countRows(opened.meta)).toBe(1);
      expect(await withMetadataScanPage(opened.meta, undefined, 1, backend.limits.maxMetadataBytes,
        new ReadBudget(backend.limits.maxMetadataBytes), async rows => rows.map(row => row.id))).toEqual([`${row.key}|0`]);
      await backend.closePhysical('p1');
      opened = await backend.openPhysical('p1');
      expect(await opened.meta.findDocumentsById([`${row.key}|0`], false)).toHaveLength(1);
      await backend.removePhysical('p1');
      await backend.removePhysical('p1');
      opened = await backend.openPhysical('p1');
      expect(await opened.fork.findDocumentsById([row.key], false)).toEqual([]);
      expect(await opened.meta.findDocumentsById([`${row.key}|0`], false)).toEqual([]);
      expect(await countRows(opened.meta)).toBe(0);
    } finally { await backend.close(); }
  });

  test('independent owners share durable state and receive cross-owner writes', async () => {
    const options = { name: name(), storage: dexie() };
    const first = await openAliasBackend(options);
    const second = await openAliasBackend(options);
    try {
      const a = await first.openPhysical('p1');
      const b = await second.openPhysical('p1');
      const row = await data();
      const event = new Promise<void>(resolve => {
        const subscription = b.fork.changeStream().subscribe(value => {
          if (value.events.some(event => event.documentId === row.key)) { subscription.unsubscribe(); resolve(); }
        });
      });
      await first.writeRecord(a, row, undefined, 'first');
      await event;
      expect(await b.fork.findDocumentsById([row.key], false)).toHaveLength(1);
      expect(await first.database.storageToken).toBe(await second.database.storageToken);
      const manifestEvent = new Promise<void>(resolve => {
        const subscription = second.manifestStorage.changeStream().subscribe(value => {
          if (value.events.some(event => event.documentId === 'manifest')) { subscription.unsubscribe(); resolve(); }
        });
      });
      await first.writeManifest(await manifestRow(), undefined, 'first-manifest');
      await manifestEvent;
      expect((await second.manifestStorage.findDocumentsById(['manifest'], false))[0].activePhysicalEpoch).toBe('p1');
    } finally { await second.close(); await first.close(); }
  }, 5000);

  test('preserves opening and cleanup failures before the backend can be returned', async () => {
    const underlying = getRxStorageMemory();
    const openingFailure = new Error('manifest creation rejected');
    const cleanupFailure = new Error('internal close rejected');
    const resources: { close(): Promise<void> }[] = [];
    const storage: RxStorage<any, any> = { ...underlying, async createStorageInstance(options) {
      if (options.collectionName === 'manifest') throw openingFailure;
      const raw = await underlying.createStorageInstance(options);
      resources.push(raw);
      return new Proxy(raw, { get(target, property) {
        if (property === 'close') return async () => { throw cleanupFailure; };
        const value = Reflect.get(target, property, target);
        return typeof value === 'function' ? value.bind(target) : value;
      } });
    } };
    try {
      const failure = await openAliasBackend({ name: name(), storage }).catch(error => error);
      expect(failure).toBeInstanceOf(BackendCleanupError);
      expect(failure.code).toBe('LocalStorageCleanupFailed');
      expect(failure.cause).toBe(openingFailure);
      expect(failure.cleanupErrors).toEqual([cleanupFailure]);
    } finally { for (const resource of resources) await resource.close(); }
  });

  test('drains raw storage if database construction fails before returning the database', async () => {
    const databaseName = name();
    const failure = new Error('database hook rejected');
    let created: RxDatabase | undefined;
    addRxPlugin({ name: `backend-constructor-${crypto.randomUUID()}`, rxdb: true, hooks: {
      createRxDatabase: { after: async ({ database }: { database: RxDatabase }) => {
        if (database.name === databaseName) { created = database; throw failure; }
      } },
    } });
    let closed = 0;
    const underlying = getRxStorageMemory();
    const storage: RxStorage<any, any> = { ...underlying, async createStorageInstance(options) {
      const raw = await underlying.createStorageInstance(options);
      return new Proxy(raw, { get(target, property) {
        if (property === 'close') return async () => { closed++; await target.close(); };
        const value = Reflect.get(target, property, target);
        return typeof value === 'function' ? value.bind(target) : value;
      } });
    } };
    try {
      await expect(openAliasBackend({ name: databaseName, storage })).rejects.toBe(failure);
      expect(closed).toBe(1);
    } finally { await created?.close(); }
  });
});

describe('pre-allocation read bounds', () => {
  test('reserves before IO and releases on callback or storage error', async () => {
    let reads = 0;
    const backend = await openAliasBackend({ name: name(), storage: getRxStorageMemory() });
    try {
      const opened = await backend.openPhysical('p1');
      const row = await data();
      await backend.writeRecord(opened, row, undefined, 'insert');
      const storage = new Proxy(opened.fork, { get(target, prop) {
        if (prop === 'findDocumentsById') return async (...args: Parameters<typeof target.findDocumentsById>) => {
          reads++; expect(budget.usedBytes).toBe(1000); return target.findDocumentsById(...args);
        };
        const value = Reflect.get(target, prop, target); return typeof value === 'function' ? value.bind(target) : value;
      } });
      const budget = new ReadBudget(1000);
      await expect(withRows(storage, [row.key, row.key], 1000, budget, async () => undefined)).rejects.toThrow('materialization');
      expect(reads).toBe(0);
      await expect(withRows(storage, [row.key], 1000, budget, async () => { throw new Error('callback'); })).rejects.toThrow('callback');
      expect(budget.usedBytes).toBe(0);
      expect(budget.peakBytes).toBe(1000);
      await expect(withRows(storage, [], 1000, budget, async () => undefined)).rejects.toThrow('four');
      await expect(withRows(storage, Array(5).fill(row.key), 1000, budget, async () => undefined)).rejects.toThrow('four');
    } finally { await backend.close(); }
  });

  test('Dexie scans use an index-satisfied primary seek and materialize at most four rows', async () => {
    const underlying = dexie();
    const limits: number[] = [];
    const storage: RxStorage<any, any> = { ...underlying, async createStorageInstance(options) {
      const raw = await underlying.createStorageInstance(options);
      const query = raw.query.bind(raw);
      raw.query = async prepared => {
        if (options.collectionName.startsWith('records_')) {
          expect(prepared.queryPlan.selectorSatisfiedByIndex).toBe(true);
          expect(prepared.queryPlan.sortSatisfiedByIndex).toBe(true);
          expect(prepared.queryPlan.index).toEqual(['_deleted', 'key']);
          expect(prepared.query.skip).toBe(0);
          limits.push(prepared.query.limit!);
        }
        return query(prepared);
      };
      return raw;
    } };
    const backend = await openAliasBackend({ name: name(), storage });
    try {
      const opened = await backend.openPhysical('p1');
      const keys: string[] = [];
      for (let index = 0; index < 11; index++) {
        const row = await data(`id${index}`); keys.push(row.key);
        await backend.writeRecord(opened, row, undefined, 'insert');
      }
      const seen: string[] = [];
      let after: string | undefined;
      const budget = new ReadBudget(4000);
      for (;;) {
        const page = await withScanPage(opened.fork, after, 4, 1000, budget, async rows => rows.map(row => row.key));
        seen.push(...page);
        if (page.length < 4) break;
        after = page[page.length - 1];
      }
      expect(seen).toEqual(keys.sort());
      expect(limits).toEqual([4, 4, 4]);
      expect(budget.peakBytes).toBe(4000);
      await expect(withScanPage(opened.fork, undefined, 5, 1000, budget, async () => undefined)).rejects.toThrow('four');
    } finally { await backend.close(); }
  });

  test('rejects invalid limit configurations', () => {
    expect(() => validateStorageLimits({ maxRecordBytes: 0 })).toThrow();
    expect(() => validateStorageLimits({ maxMetadataBytes: 1 })).toThrow();
    expect(() => validateStorageLimits({ maxKnownIds: Infinity })).toThrow();
    expect(() => validateStorageLimits({ typo: 2 } as any)).toThrow();
    expect(() => new ReadBudget(0)).toThrow();
  });
});
