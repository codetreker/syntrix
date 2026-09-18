import { describe, expect, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { defaultHashSha256, type RxStorage } from 'rxdb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { DefaultTokenProvider } from '../auth/provider.js';
import { countRows } from './backend.js';
import { compactAlias, createCompactionDeadline } from './compaction.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { businessEqual, encodeBusinessPayload, recordKey } from './records.js';
import { createLocalReplicationRuntime } from './runtime.js';
import { createLocalSession } from './session.js';
import { openAliasStorage, type AliasStorage, type MaintenanceAccess } from './storage.js';
import type { ControlRecord, DataRecord, LocalRecord, MemberRecord, StorageLimits } from './storage-types.js';

const checkpoint = { phase: 'live', token: 'remote-original' };
const control: ControlRecord = { key: 'c:progress', kind: 'c', checkpoint, generation: 'g1', phase: 'live', bootstrapComplete: true, partialDelivery: false };
const row = async (id: string): Promise<DataRecord> => ({ key: await recordKey('d', id), kind: 'd', logicalId: id,
  existence: 'live', payload: encodeBusinessPayload({ value: id }), editToken: null, pin: null, wire: { version: '1' } });
const meta = (record: LocalRecord) => ({ id: `${record.key}|0`, itemId: record.key, isCheckpoint: '0', docData: { ...record, _deleted: false },
  _deleted: false, _attachments: {}, _meta: { lwt: Date.now() }, _rev: '1-fixture' });
type Fault = (name: string, rows: any[], commit: () => Promise<any>) => Promise<any>;
const setup = async (fault?: Fault, limits?: Partial<StorageLimits>) => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const storage: RxStorage<any, any> = fault ? { ...original, createStorageInstance: async params => {
    const native = await original.createStorageInstance(params);
    return new Proxy(native, { get(target, property) {
      if (property === 'bulkWrite') return (rows: any[], context: string) => fault(params.collectionName, rows, () => target.bulkWrite(rows, context));
      const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } } : original;
  const token = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'alice', exp: 0 }))}.sig`.replace(/=/g, '');
  const session = await createLocalSession(new DefaultTokenProvider({ token }));
  const options = { session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage, limits };
  return { options, alias: await openAliasStorage(options) };
};
const prepare = async (alias: AliasStorage, count = 10, members = 3) => alias.withMaintenance(async access => {
  const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
  const write = async (record: LocalRecord) => {
    await access.backend.writeRecord(physical, record, undefined, 'fixture');
    expect((await physical.meta.bulkWrite([{ document: meta(record) as any }], 'fixture')).error).toEqual([]);
  };
  for (let i = 0; i < count; i++) {
    const data = await row(`user-${i}`);
    const member: MemberRecord = { key: await recordKey('m', data.logicalId), kind: 'm', logicalId: data.logicalId,
      slots: [{ generation: 'old', member: true }, { generation: 'g1', member: i < members }], observedExistence: 'live', metadata: { version: '7' } };
    await write(data); await write(member);
  }
  await write(control);
  for (const direction of ['up', 'down']) {
    const { docData: _ignored, ...base } = meta(control);
    const value = { ...base, id: `${direction}|1`, itemId: direction, isCheckpoint: '1',
      checkpointData: direction === 'down' ? { source: checkpoint } : { id: 'old-physical-cursor', lwt: 9999999999999 } };
    expect((await physical.meta.bulkWrite([{ document: value as any }], 'fixture')).error).toEqual([]);
  }
  await access.writeManifest({ ...access.manifest, boundDatabaseId: 'D1', sourceHash: 'source1', sourceReady: true, activeSourceGeneration: 'g1' });
  return physical.epoch;
});

describe('clean physical epoch compaction', () => {
  test('inactivity deadline follows actual progress and disposes its timer', async () => {
    type Timer = ReturnType<typeof setTimeout>;
    let now = 0;
    let serial = 0;
    const timers = new Map<Timer, { due: number; fire(): void }>();
    const clock = {
      set: (fire: () => void, delay: number) => {
        const timer = ++serial as unknown as Timer;
        timers.set(timer, { due: now + delay, fire }); return timer;
      },
      clear: (timer: Timer) => { timers.delete(timer); },
    };
    const advance = (duration: number) => {
      now += duration;
      for (const [timer, entry] of timers) if (entry.due <= now) { timers.delete(timer); entry.fire(); }
    };
    const deadline = createCompactionDeadline(clock);
    let failure: unknown;
    const settled = deadline.failure.catch(error => { failure = error; });
    for (let i = 0; i < 100; i++) { advance(29_000); deadline.progress(); }
    await Promise.resolve(); expect(failure).toBeUndefined(); expect(timers.size).toBe(1);
    advance(30_000); await settled;
    expect(failure).toMatchObject({ code: 'LocalMaintenanceTimeout' });
    deadline.progress(); expect(timers.size).toBe(0);
    const stopped = createCompactionDeadline(clock);
    stopped.dispose(); stopped.progress(); advance(60_000);
    expect(timers.size).toBe(0);
  });

  test('100 historical IDs compact to 10 clean members, preserving checkpoint and native assumed state across reopen', async () => {
    const env = await setup(); let alias = env.alias;
    try {
      const previous = await prepare(alias, 100, 10);
      const scope = await alias.captureScope();
      const stale = await alias.native(scope);
      const result = await compactAlias(alias);
      expect(result).toMatchObject({ status: 'compacted', previousEpoch: previous });
      const manifest = await alias.readManifest();
      expect(manifest.activePhysicalEpoch).not.toBe(previous);
      expect(manifest.physicalEpochs).toEqual([manifest.activePhysicalEpoch]);
      expect(manifest.maintenance).toBeNull();
      expect(manifest.activeSourceGeneration).toBe('g1');
      expect(await alias.get('user-99')).toBeNull();
      expect(await alias.get('user-9')).toMatchObject({ value: 'user-9', version: 7n });
      await expect(stale.fork.findDocumentsById([await recordKey('d', 'user-0')], false)).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      await alias.withMaintenance(async access => {
        const next = await access.backend.openPhysical(manifest.activePhysicalEpoch);
        expect(await countRows(next.fork)).toBe(21);
        expect(await countRows(next.meta)).toBe(23);
        const down = (await next.meta.findDocumentsById(['down|1'], false))[0];
        expect(down.checkpointData).toEqual({ source: checkpoint });
        const up = (await next.meta.findDocumentsById(['up|1'], false))[0];
        expect(up.checkpointData.id).not.toBe('old-physical-cursor');
      });
      await alias.close(); alias = await openAliasStorage(env.options);
      expect(await alias.get('user-0')).toMatchObject({ value: 'user-0', version: 7n });
      expect((await alias.stats()).knownIds).toBe(10);
      const native = await alias.native(await alias.captureScope());
      const pushed: any[] = [];
      const runtime = createLocalReplicationRuntime<LocalRecord, typeof checkpoint>({ ...native, forkInstance: native.fork, metaInstance: native.meta,
        hashFunction: defaultHashSha256, conflictHandler: { isEqual: businessEqual, resolve: async value => value.realMasterState },
        pullBatchSize: 3, pushBatchSize: 3, readBounds: { maxDocuments: 3, readBlockDocuments: 3 }, isControlDocument: value => value.kind !== 'd',
        readSource: async cursor => { expect(cursor).toEqual(checkpoint); return { documents: [], checkpoint, complete: true }; },
        writeRemote: async rows => { pushed.push(...rows); return []; },
      });
      try {
        await runtime.waitForIdle(); expect(pushed).toHaveLength(0);
        await alias.set('user-0', { value: 'edited' }); await runtime.waitForIdle();
        expect(pushed).toHaveLength(1);
        expect(pushed[0].assumedMasterState.payload).toBe(encodeBusinessPayload({ value: 'user-0' }));
        expect(pushed[0].newDocumentState.payload).toBe(encodeBusinessPayload({ value: 'edited' }));
      } finally { await runtime.close(); }
    } finally { await alias.close(); }
  }, 30_000);

  test('empty generation still stores final control, down checkpoint and a fresh upstream checkpoint', async () => {
    const { alias } = await setup();
    try {
      await prepare(alias, 4, 0); expect((await compactAlias(alias)).status).toBe('compacted');
      await alias.withMaintenance(async access => {
        const next = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        expect(await countRows(next.fork)).toBe(1); expect(await countRows(next.meta)).toBe(3);
        expect((await next.fork.findDocumentsById(['c:progress'], false))[0]).toMatchObject(control);
      });
    } finally { await alias.close(); }
  });

  test('byte-limited seed pages preserve the next unconsumed record', async () => {
    const { alias } = await setup(undefined, { maxRecordBytes: 700, maxMetadataBytes: 900 });
    try {
      await prepare(alias, 10, 10);
      expect((await compactAlias(alias)).status).toBe('compacted');
      for (let i = 0; i < 10; i++) expect(await alias.get(`user-${i}`)).toMatchObject({ value: `user-${i}` });
    } finally { await alias.close(); }
  });

  test('pins, unequal assumed business and incomplete bootstrap block compaction without allocating a shadow', async () => {
    const { alias } = await setup();
    try {
      const epoch = await prepare(alias);
      await alias.withMaintenance(async access => { await access.writeManifest({ ...access.manifest, partialDelivery: true }); });
      expect(await compactAlias(alias)).toEqual({ status: 'not-clean' });
      await alias.withMaintenance(async access => { await access.writeManifest({ ...access.manifest, partialDelivery: false }); });
      await alias.set('user-0', { changed: true });
      expect(await compactAlias(alias)).toEqual({ status: 'not-clean' });
      await alias.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(epoch);
        const saved = (await physical.fork.findDocumentsById([await recordKey('d', 'user-0')], false))[0] as any;
        await access.backend.writeRecord(physical, { ...saved, pin: null }, saved, 'fixture-no-pin');
      });
      expect(await compactAlias(alias)).toEqual({ status: 'not-clean' });
      expect((await alias.readManifest()).physicalEpochs).toEqual([epoch]);
    } finally { await alias.close(); }
  });

  for (const kind of ['record', 'metadata', 'quota', 'flip'] as const) {
    test(`${kind} failure preserves old selection and source data`, async () => {
      let enabled = false;
      const error = kind === 'quota' ? new DOMException('Quota exceeded', 'QuotaExceededError') : new Error(`${kind} failure`);
      const { alias } = await setup(async (name, rows, commit) => {
        const shadow = name.startsWith('records_') || name.startsWith('meta_');
        if (enabled && ((kind === 'record' && name.startsWith('records_')) || (kind === 'metadata' && name.startsWith('meta_')) ||
            (kind === 'quota' && shadow) || (kind === 'flip' && rows[0].document.maintenance?.stage === 'flipped'))) {
          enabled = false; throw error;
        }
        return commit();
      });
      try {
        const epoch = await prepare(alias); enabled = true;
        await expect(compactAlias(alias)).rejects.toBe(error);
        const manifest = await alias.readManifest();
        expect(manifest.activePhysicalEpoch).toBe(epoch); expect(manifest.physicalEpochs).toEqual([epoch]);
        expect(await alias.get('user-0')).toMatchObject({ value: 'user-0' });
      } finally { await alias.close(); }
    });
  }

  test('returned metadata write errors cannot select a partially seeded epoch', async () => {
    let enabled = false;
    const { alias } = await setup(async (name, rows, commit) => {
      if (enabled && name.startsWith('meta_') && rows[0].document.isCheckpoint === '0') {
        enabled = false;
        return { error: [{ isError: true, status: 409, documentId: rows[0].document.id, writeRow: rows[0] }] };
      }
      return commit();
    });
    try {
      const epoch = await prepare(alias); enabled = true;
      await expect(compactAlias(alias)).rejects.toMatchObject({ status: 409 });
      expect((await alias.readManifest()).physicalEpochs).toEqual([epoch]);
      expect(await alias.get('user-0')).toMatchObject({ value: 'user-0' });
    } finally { await alias.close(); }
  });

  test('lost flip acknowledgement rereads the committed selection and keeps the new epoch', async () => {
    let enabled = false;
    const { alias } = await setup(async (_name, rows, commit) => {
      const result = await commit();
      if (enabled && rows[0].document.maintenance?.stage === 'flipped') { enabled = false; throw new Error('lost acknowledgement'); }
      return result;
    });
    try {
      const epoch = await prepare(alias); enabled = true;
      expect(await compactAlias(alias)).toMatchObject({ status: 'compacted', previousEpoch: epoch });
      const manifest = await alias.readManifest();
      expect(manifest.activePhysicalEpoch).not.toBe(epoch); expect(manifest.physicalEpochs).toEqual([manifest.activePhysicalEpoch]);
      expect(await alias.get('user-0')).toMatchObject({ value: 'user-0' });
    } finally { await alias.close(); }
  });

  test('uncertain flip plus unreadable manifest retains both epochs for startup recovery', async () => {
    let enabled = false;
    const env = await setup(async (_name, rows, commit) => {
      const result = await commit();
      if (enabled && rows[0].document.maintenance?.stage === 'flipped') { enabled = false; throw new Error('lost acknowledgement'); }
      return result;
    });
    let alias = env.alias;
    try {
      const old = await prepare(alias); enabled = true;
      const wrapped = new Proxy(alias, { get(target, property) {
        if (property === 'withMaintenance') return (callback: (access: MaintenanceAccess) => Promise<unknown>) => target.withMaintenance(access => {
          const read = access.readManifest;
          let reads = 0;
          access.readManifest = async () => { if (++reads > 1) throw new Error('manifest unavailable'); return read(); };
          return callback(access);
        });
        return Reflect.get(target, property, target);
      } });
      await expect(compactAlias(wrapped)).rejects.toThrow('manifest unavailable');
      const selected = await alias.readManifest();
      expect(selected.activePhysicalEpoch).not.toBe(old); expect(selected.physicalEpochs).toHaveLength(2);
      await alias.close(); alias = await openAliasStorage(env.options);
      const recovered = await alias.readManifest();
      expect(recovered.activePhysicalEpoch).toBe(selected.activePhysicalEpoch); expect(recovered.physicalEpochs).toEqual([selected.activePhysicalEpoch]);
      expect(await alias.get('user-0')).toMatchObject({ value: 'user-0' });
    } finally { await alias.close(); }
  });

  test('cleanup failure after flip remains recoverable on reopen and does not admit a second shadow', async () => {
    const env = await setup(); let alias = env.alias;
    try {
      const old = await prepare(alias);
      let failCleanup = true;
      const wrapped = new Proxy(alias, { get(target, property) {
        if (property === 'withMaintenance') return (callback: (access: MaintenanceAccess) => Promise<unknown>) => target.withMaintenance(access => {
          const remove = access.backend.removePhysical;
          access.backend = new Proxy(access.backend, { get(backend, key) {
            if (key === 'removePhysical') return async (epoch: string) => {
              if (failCleanup) throw new Error('cleanup interrupted');
              return remove(epoch);
            };
            return Reflect.get(backend, key, backend);
          } });
          return callback(access);
        });
        return Reflect.get(target, property, target);
      } });
      await expect(compactAlias(wrapped)).rejects.toThrow('cleanup interrupted');
      const flipped = await alias.readManifest();
      expect(flipped.activePhysicalEpoch).not.toBe(old); expect(flipped.maintenance?.stage).toBe('flipped');
      await expect(compactAlias(wrapped)).rejects.toThrow('cleanup interrupted');
      expect((await alias.readManifest()).physicalEpochs).toEqual(flipped.physicalEpochs);
      failCleanup = false;
      await alias.close(); alias = await openAliasStorage(env.options);
      const recovered = await alias.readManifest();
      expect(recovered.activePhysicalEpoch).toBe(flipped.activePhysicalEpoch); expect(recovered.maintenance).toBeNull();
      expect(recovered.physicalEpochs).toEqual([recovered.activePhysicalEpoch]);
      expect(await alias.get('user-0')).toMatchObject({ value: 'user-0' });
    } finally { await alias.close(); }
  });
});
