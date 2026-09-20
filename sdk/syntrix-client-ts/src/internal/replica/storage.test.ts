import { describe, expect, spyOn, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { defaultHashSha256, fillObjectDataBeforeInsert, getChangedDocumentsSince, normalizeMangoQuery, prepareQuery, type RxStorage } from 'rxdb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage, type AliasStorage, type OpenAliasStorageOptions } from './storage.js';
import { decodeBusinessPayload, encodeBusinessPayload, recordKey } from './records.js';
import type { DataRecord, MemberRecord } from './storage-types.js';
import { ReadBudget } from './backend.js';
import { QueryViewChangedError, type QueryProjection } from './query-source.js';
import type { ReplicationAccess } from './replication-access.js';
const jwt = (subject: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub: subject, exp: 0 }))}.sig`.replace(/=/g, '');
const setup = async (extra: Partial<OpenAliasStorageOptions> = {}) => {
  const provider = new DefaultTokenProvider({ token: jwt('alice') });
  const session = await createReplicaSession(provider);
  const options: OpenAliasStorageOptions = {
    session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage: getRxStorageDexie({ indexedDB, IDBKeyRange }), ...extra
  };
  return { provider, session, options, storage: await openAliasStorage(options) };
};
const member = async (id: string, version: string, generation = 'g1'): Promise<MemberRecord> => ({
  key: await recordKey('m', id), kind: 'm', logicalId: id,
  slots: [{ generation, member: true }], observedExistence: 'live', metadata: { version }
});
const seedMember = async (storage: AliasStorage, id: string, version: string) => storage.withMaintenance(async access => {
  const opened = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
  const next = await member(id, version);
  const previous = (await opened.fork.findDocumentsById([next.key], false))[0];
  await access.backend.writeRecord(opened, next, previous, 'test-member');
  await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'g1', sourceReady: true });
});

describe('downstream storage ownership', () => {
  test('status counts actual pending and pins, reports only durable checkpoint, and retains the last complete round across reopen', async () => {
    const env = await setup(); let storage = env.storage;
    try {
      expect(await storage.status()).toMatchObject({ mode: 'events', sourceReady: false, checkpoint: null, lastCompleteRound: null, pending: 0, pins: 0 });
      await storage.set('alice', { value: 1 });
      expect(await storage.status()).toMatchObject({ pending: 1, pins: 1 });
      await storage.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        const data = (await physical.fork.findDocumentsById([await recordKey('d', 'alice')], false))[0] as any;
        const clean = { ...data, pin: null };
        await access.backend.writeRecord(physical, clean, data, 'fixture');
        expect((await physical.meta.bulkWrite([{ document: { id: `${data.key}|0`, itemId: data.key, isCheckpoint: '0',
          docData: clean, _deleted: false, _attachments: {}, _meta: { lwt: Date.now() }, _rev: '1-fixture' } as any },
          { document: { id: 'down|1', itemId: 'down', isCheckpoint: '1', checkpointData: { source: { sourceCursor: 'committed', roundId: 'round-2', complete: false } },
            _deleted: false, _attachments: {}, _meta: { lwt: Date.now() }, _rev: '1-fixture' } as any }], 'fixture')).error).toEqual([]);
        await access.backend.writeRecord(physical, { key: 'c:progress', kind: 'c', checkpoint: { sourceCursor: 'not-yet-committed' },
          generation: 'g1', phase: 'live', bootstrapComplete: true, partialDelivery: true }, undefined, 'fixture');
        await access.writeManifest({ ...access.manifest, boundDatabaseId: 'D1', sourceHash: 'hash', sourceReady: true,
          activeSourceGeneration: 'g1', partialDelivery: true, lastCompleteRound: 'round-1' });
      });
      const result = await storage.status();
      expect(result).toMatchObject({ pending: 0, pins: 0, sourceReady: false, generation: 'g1', lastCompleteRound: 'round-1', checkpoint: { sourceCursor: 'committed' } });
      result.checkpoint!.sourceCursor = 'consumer-change';
      const lifecycle = storage.lifecycleId;
      await storage.close(); storage = await openAliasStorage(env.options);
      expect(storage.lifecycleId).toBe(lifecycle);
      expect(await storage.status()).toMatchObject({ lastCompleteRound: 'round-1', checkpoint: { sourceCursor: 'committed' } });
      await storage.set('bob', { value: 2 }); expect(await storage.status()).toMatchObject({ pending: 1, pins: 1 });
    } finally { await storage.close(); await env.session.close(); }
  });

  test('source reads admit initial binding and always carry the persisted identity before readiness', async () => {
    const { storage } = await setup();
    try {
      const scope = await storage.captureScope();
      expect(await storage.guardSourceRead(scope)).toEqual({});
      await storage.bind(scope, 'database-1', 'source-1');
      expect(await storage.guardSourceRead(scope)).toEqual({ 'X-Syntrix-Expected-Database-Identity': 'database-1' });
      await expect(storage.guardNetwork(scope)).rejects.toMatchObject({ code: 'ReplicaSourceNotReady' });
      storage.blockScope();
      await expect(storage.guardSourceRead(scope)).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
    } finally { await storage.close(); }
  });

  test('maintenance drains only native owners while alias close drains the coordinator before storage', async () => {
    const { storage } = await setup();
    const gate = deferred(); const events: string[] = [];
    storage.registerResource({ invalidate: () => { events.push('coordinator-invalidated'); }, close: async () => { events.push('coordinator-draining'); await gate.promise; events.push('coordinator-closed'); } });
    storage.registerNative({ invalidate: () => { events.push('native-invalidated'); }, close: async () => { events.push('native-closed'); } });
    try {
      await storage.withMaintenance(async () => { events.push('maintenance'); });
      expect(events).toEqual(['native-invalidated', 'native-closed', 'maintenance']);
      expect(storage.signal.aborted).toBe(false);
      let closed = false;
      const closing = storage.close().then(() => { closed = true; });
      await new Promise(resolve => setTimeout(resolve, 5));
      expect(storage.signal.aborted).toBe(true); expect(closed).toBe(false);
      expect(events).toContain('coordinator-draining');
      gate.resolve(); await closing;
      expect(events[events.length - 1]).toBe('coordinator-closed');
    } finally { gate.resolve(); await storage.close(); }
  });

  for (const outcome of ['complete', 'failure', 'cancel'] as const) test(`scope capture waits outside maintenance drain and observes ${outcome}`, async () => {
    const { storage } = await setup();
    const draining = deferred(); const release = deferred();
    const failure = new Error('maintenance storage failed');
    storage.registerNative({ invalidate: () => {}, close: async () => {
      draining.resolve(); await release.promise;
      // Draining native work still needs ordinary reads; queued captures must
      // not hold accessQueue while waiting for maintenance to finish.
      if (!storage.signal.aborted) await storage.readManifest();
    } });
    const previous = await storage.captureScope();
    const maintenance = storage.withMaintenance(async () => { if (outcome === 'failure') throw failure; }).then(() => undefined, error => error);
    let settled = false;
    let closing: Promise<void> | undefined;
    try {
      await draining.promise;
      const capture = storage.captureScope().then(scope => { settled = true; return scope; }, error => { settled = true; return error; });
      await new Promise(resolve => setTimeout(resolve, 5)); expect(settled).toBe(false);
      if (outcome === 'cancel') {
        closing = storage.close();
        expect(await capture).toBe(storage.signal.reason);
      }
      release.resolve();
      const result = await capture;
      if (outcome === 'complete') expect(result.nativeInstanceId).not.toBe(previous.nativeInstanceId);
      if (outcome === 'failure') expect(result).toBe(failure);
      expect(await maintenance).toBe(outcome === 'failure' ? failure : outcome === 'cancel' ? storage.signal.reason : undefined);
    } finally { release.resolve(); await maintenance; await closing; await storage.close(); }
  });

  test('bounded access keeps the native scope valid, scans data only, and expires after its callback', async () => {
    const { storage } = await setup();
    try {
      await storage.set('alice', { value: 1 }); await storage.set('bob', { value: 2 });
      const scope = await storage.captureScope();
      let held!: ReplicationAccess;
      const ids: string[] = [];
      await storage.withReplicationAccess(scope, async access => {
        held = access;
        await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'g1' });
        await access.writeRecord(await member('alice', '2'));
        await access.writeRecord({ key: 'c:progress', kind: 'c', checkpoint: { sequence: 1 }, generation: 'g1', phase: 'live', bootstrapComplete: true, partialDelivery: false });
        let after: string | undefined;
        do { after = await access.scanData(after, async row => { ids.push(row.logicalId); }); } while (after);
        await access.withDocument({ key: await recordKey('d', 'alice') }, async current => {
          expect(current.data?.pin?.stage).toBe('await-settlement');
          expect(current.member?.metadata.version).toBe('2'); expect(current.assumed).toBeUndefined();
          await access.writeRecord({ ...current.data!, pin: null }, current.data);
        });
        await access.withControl(async control => { expect(control?.checkpoint).toEqual({ sequence: 1 }); });
        await access.withUpCheckpoint(async checkpoint => { expect(checkpoint).toBeUndefined(); });
        await access.withDownCheckpoint(async checkpoint => { expect(checkpoint).toBeUndefined(); });
      });
      expect(ids.sort()).toEqual(['alice', 'bob']);
      expect((await storage.native(scope)).ownerSignal.aborted).toBe(false);
      await expect(held.withControl(async () => {})).rejects.toMatchObject({ code: 'ReplicaWriteFence' });
      await expect(held.writeRecord(await member('bad', '3'))).rejects.toMatchObject({ code: 'ReplicaWriteFence' });
    } finally { await storage.close(); }
  });

  test('native source application retains the current edit token and pin without weakening CAS', async () => {
    const { storage } = await setup();
    try {
      await storage.set('alice', { value: 1 });
      const native = await storage.native(await storage.captureScope());
      const key = await recordKey('d', 'alice');
      const previous = (await native.fork.findDocumentsById([key], false))[0];
      expect(previous.kind).toBe('d');
      const source = { ...previous, payload: encodeBusinessPayload({ value: 2 }), editToken: null, pin: null, wire: { version: '2' } };
      const result = await native.fork.bulkWrite([{ previous, document: source }], 'replication-downstream-test');
      expect(result.error).toEqual([]);
      const applied = (await native.fork.findDocumentsById([key], false))[0] as DataRecord;
      expect(applied.editToken).toBe((previous as DataRecord).editToken);
      expect(applied.pin).toEqual((previous as DataRecord).pin);
      expect(applied.wire.version).toBe('2');
      await storage.set('alice', { value: 3 });
      const stale = await native.fork.bulkWrite([{ previous, document: source }], 'replication-downstream-test');
      expect(stale.error[0].status).toBe(409);
      expect(await storage.get('alice')).toMatchObject({ value: 3 });
    } finally { await storage.close(); }
  });
});

describe('bounded query storage access', () => {
  for (const mode of ['current', 'historical-token', 'foreign', 'stale-revision', 'pin', 'membership', 'issue'] as const) {
    test(`get and query agree on native download provenance: ${mode}`, async () => {
      const { storage } = await setup();
      const key = await recordKey('d', 'alice');
      const token = crypto.randomUUID();
      try {
        await storage.withMaintenance(async access => {
          const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
          const data = { key, kind: 'd' as const, logicalId: 'alice', existence: 'live' as const,
            payload: encodeBusinessPayload({ value: 'downloaded' }), editToken: mode === 'historical-token' || mode === 'pin' ? token : null,
            pin: mode === 'pin' ? { token, stage: 'await-settlement' as const } : null, wire: {},
            _meta: { lwt: Date.now(), o: { hash: await defaultHashSha256(mode === 'foreign' ? 'foreign-source' : physical.identifier), _rev: mode === 'stale-revision' ? 0 : 1 } },
          };
          await access.backend.writeRecord(physical, data, undefined, 'provenance-fixture');
          if (mode === 'membership') await access.backend.writeRecord(physical, await member('alice', '1'), undefined, 'provenance-member');
          await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'g1',
            issues: mode === 'issue' ? [{ id: 'issue', logicalId: 'alice', code: 'conflict' }] : [] });
        });
        const expected = mode === 'current' || mode === 'historical-token' ? null : expect.objectContaining({ id: 'alice', value: 'downloaded' });
        expect(await storage.get('alice')).toEqual(expected);
        expect(await storage.status()).toMatchObject({ pending: mode === 'foreign' || mode === 'stale-revision' ? 1 : 0, pins: mode === 'pin' ? 1 : 0 });
        const budget = new ReadBudget(64 * 1024 * 1024);
        const source = storage.queryAccess(budget);
        expect(await source.withProjection({ id: 'alice' }, await source.view(), async projection => projection?.decode())).toEqual(expected);
        expect(budget.usedBytes).toBe(0);
      } finally { await storage.close(); }
    });
  }

  test('scans only bounded data descriptors and decodes only inside the reserved callback', async () => {
    const calls: { limit: number; skip: number; selector: any }[] = [];
    let observe = false;
    const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
    const native: RxStorage<any, any> = { ...original, createStorageInstance: async params => {
      const raw = await original.createStorageInstance(params);
      return new Proxy(raw, { get(target, property) {
        if (property === 'query') return (query: any) => {
          if (observe && params.collectionName.startsWith('records_')) calls.push(query.query);
          return target.query(query);
        };
        const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
      } });
    } };
    const env = await setup({ storage: native });
    let spy: ReturnType<typeof spyOn> | undefined;
    try {
      for (let i = 0; i < 9; i++) await env.storage.set(`person-${i}`, { exact: 9007199254740993n });
      await seedMember(env.storage, 'person-0', '10');
      const payload = encodeBusinessPayload({ exact: 9007199254740993n });
      await env.storage.withMaintenance(async access => {
        const current: DataRecord = { key: await recordKey('d', 'recovery'), kind: 'd', logicalId: 'recovery', existence: 'live', payload, editToken: null, pin: null, wire: {} };
        await access.writeManifest({ ...access.manifest, recoveryIntent: { id: 'recover', issueId: 'fixture-issue', phaseId: null, physicalEpoch: access.manifest.activePhysicalEpoch, action: 'merge', logicalId: 'recovery', protectedToken: null, resultToken: 'result', current, desired: { ...current, editToken: 'result' } } });
      });
      let decoded = 0;
      const parse = JSON.parse;
      spy = spyOn(JSON, 'parse').mockImplementation((text, reviver) => { if (text === payload) decoded++; return parse(text, reviver); });
      const budget = new ReadBudget(64 * 1024 * 1024), source = env.storage.queryAccess(budget), view = await source.view();
      observe = true;
      let after: string | undefined;
      const descriptors: { key: string; encodedBytes: number }[] = [];
      while (true) {
        const page = await source.scan(after, view); descriptors.push(...page.rows); after = page.lastKey;
        if (page.done) break;
      }
      expect(descriptors).toHaveLength(9); expect(decoded).toBe(0);
      expect(calls).toHaveLength(3);
      expect(calls.every(call => call.limit <= 4 && call.skip === 0 && call.selector.key.$lt === 'e:')).toBe(true);
      expect(Object.keys(descriptors[0]).sort()).toEqual(['encodedBytes', 'key']);
      let held!: QueryProjection;
      await source.withProjection({ key: await recordKey('m', 'person-0') }, view, async projection => {
        held = projection!; expect(decoded).toBe(0); expect(budget.usedBytes).toBeGreaterThan(0);
        expect(projection?.decode()).toMatchObject({ id: 'person-0', exact: 9007199254740993n, version: 10n });
        const count = decoded; expect(count).toBeGreaterThan(0); projection?.decode(); expect(decoded).toBe(count);
      });
      expect(() => held.decode()).toThrow('outside its read reservation');
      expect(budget.usedBytes).toBe(0); expect(budget.peakBytes).toBeLessThanOrEqual(budget.maxBytes);
      expect(await source.withProjection({ id: 'missing' }, view, async projection => projection)).toBeNull();
      await expect(source.scan('m:invalid', view)).rejects.toThrow('continuation');
      await expect(source.withProjection({ key: 'c:progress' }, view, async () => undefined)).rejects.toThrow('projection key');
    } finally { spy?.mockRestore(); await env.storage.close(); }
  });

  for (const corrupted of ['namespace', 'definition', 'lifecycle', 'protection-count'] as const) {
    test(`cached query fingerprints never bypass ${corrupted} validation`, async () => {
      let inject = false;
      const delegate = getRxStorageDexie({ indexedDB, IDBKeyRange });
      const native: RxStorage<any, any> = { ...delegate, createStorageInstance: async options => {
        const raw = await delegate.createStorageInstance(options);
        return new Proxy(raw, { get(target, property) {
          if (property === 'findDocumentsById' && options.collectionName === 'manifest') return async (ids: string[], deleted: boolean) => {
            const rows = await target.findDocumentsById(ids, deleted);
            if (!inject) return rows;
            return rows.map((row: any) => ({ ...row,
              ...(corrupted === 'namespace' ? { namespace: { ...row.namespace, subject: 'other-account' } } : {}),
              ...(corrupted === 'definition' ? { definition: { ...row.definition, collection: 'other-collection' } } : {}),
              ...(corrupted === 'lifecycle' ? { lifecycleId: 'other-lifecycle' } : {}),
              ...(corrupted === 'protection-count' ? { issues: Array.from({ length: 201 }, (_, index) => ({ id: `issue-${index}`, logicalId: 'alice', code: 'conflict' })) } : {}),
            }));
          };
          const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
        } });
      } };
      const { storage } = await setup({ storage: native });
      try {
        const budget = new ReadBudget(64 * 1024 * 1024);
        const source = storage.queryAccess(budget);
        const first = await source.view(); expect(first.visibilityHash).toHaveLength(64);
        inject = true;
        await expect(source.view()).rejects.toMatchObject({ code: corrupted === 'lifecycle' ? 'ReplicaRemoved' : 'ReplicaStorageCorruption' });
        expect(budget.usedBytes).toBe(0);
      } finally { inject = false; await storage.close(); }
    });
  }

  test('visibility fingerprints detect generation and same-generation protection changes', async () => {
    const { storage } = await setup();
    try {
      const key = await recordKey('d', 'alice');
      const data: DataRecord = { key, kind: 'd', logicalId: 'alice', existence: 'live', payload: encodeBusinessPayload({ value: 1 }), editToken: null, pin: null, wire: {} };
      await storage.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        await access.backend.writeRecord(physical, data, undefined, 'query-fixture');
        expect((await physical.meta.bulkWrite([{ document: {
          id: `${key}|0`, itemId: key, isCheckpoint: '0', docData: data,
          _deleted: false, _attachments: {}, _rev: '1-fixture', _meta: { lwt: Date.now() },
        } as any }], 'query-assumed')).error).toEqual([]);
      });
      const source = storage.queryAccess(new ReadBudget(64 * 1024 * 1024));
      const first = await source.view();
      expect(await source.withProjection({ key }, first, async projection => projection?.visible)).toBe(false);
      await storage.withMaintenance(async access => { await access.writeManifest({ ...access.manifest, issues: [{ id: 'issue', logicalId: 'alice', code: 'conflict' }] }); });
      await expect(source.scan(undefined, first)).rejects.toBeInstanceOf(QueryViewChangedError);
      const protectedView = await source.view(); expect(protectedView.sourceGeneration).toBe(first.sourceGeneration);
      expect(protectedView.visibilityHash).not.toBe(first.visibilityHash);
      expect(await source.withProjection({ key }, protectedView, async projection => projection?.decode())).toMatchObject({ id: 'alice', value: 1 });
      await storage.withMaintenance(async access => { await access.writeManifest({ ...access.manifest, issues: [], activeSourceGeneration: 'g2' }); });
      await expect(source.withProjection({ key }, protectedView, async () => undefined)).rejects.toBeInstanceOf(QueryViewChangedError);
      const last = await source.view(); expect(last.sourceGeneration).toBe('g2');
      expect(await source.withProjection({ key }, last, async projection => projection?.decode())).toBeNull();
    } finally { await storage.close(); }
  });

  test('assumed-only settlement invalidates row visibility without retaining metadata payloads', async () => {
    const { storage } = await setup();
    const key = await recordKey('d', 'alice');
    const seen: string[][] = [];
    const source = storage.queryAccess(new ReadBudget(64 * 1024 * 1024));
    const sub = source.changes.subscribe(event => { if (event.type === 'row') seen.push(event.keys); });
    try {
      const data: DataRecord = { key, kind: 'd', logicalId: 'alice', existence: 'live', payload: encodeBusinessPayload({ value: 'downloaded' }), editToken: null, pin: null, wire: {} };
      await storage.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        await access.backend.writeRecord(physical, data, undefined, 'query-fixture');
      });
      const view = await source.view();
      expect(await source.withProjection({ key }, view, async projection => projection?.visible)).toBe(true);
      seen.length = 0;
      await storage.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        expect((await physical.meta.bulkWrite([{ document: {
          id: `${key}|0`, itemId: key, isCheckpoint: '0', docData: data,
          _deleted: false, _attachments: {}, _rev: '1-fixture', _meta: { lwt: Date.now() },
        } as any }], 'query-assumed')).error).toEqual([]);
      });
      expect(seen).toContainEqual([key]);
      expect(await source.view()).toEqual(view);
      expect(await source.withProjection({ key }, view, async projection => projection?.visible)).toBe(false);
    } finally { sub.unsubscribe(); await storage.close(); }
  });

  test('query ownership is per handle and database budget identity excludes alias', async () => {
    const env = await setup(), second = await openAliasStorage(env.options), other = await openAliasStorage({ ...env.options, alias: 'others' });
    try {
      const budget = new ReadBudget(64 * 1024 * 1024);
      const firstAccess = env.storage.queryAccess(budget), secondAccess = second.queryAccess(budget), otherAccess = other.queryAccess(budget);
      expect(firstAccess.databaseNamespace).toBe(secondAccess.databaseNamespace);
      expect(firstAccess.databaseNamespace).toBe(otherAccess.databaseNamespace);
      expect(firstAccess.namespace).not.toBe(otherAccess.namespace);
      const view = await firstAccess.view(); await env.storage.close();
      expect(firstAccess.signal.aborted).toBe(true); expect(secondAccess.signal.aborted).toBe(false);
      await expect(firstAccess.scan(undefined, view)).rejects.toBe(firstAccess.signal.reason);
      expect(await secondAccess.view()).toEqual(view);
      const insufficient = second.queryAccess(new ReadBudget(1024));
      await expect(insufficient.view()).rejects.toMatchObject({ code: 'ReplicaReadBudgetExceeded' });
    } finally { await env.storage.close(); await second.close(); await other.close(); }
  });

  test('closing a handle aborts and drains an in-flight query without leaking read reservations', async () => {
    const entered = deferred(), release = deferred(); let pause = false;
    const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
    const native: RxStorage<any, any> = { ...original, createStorageInstance: async params => {
      const raw = await original.createStorageInstance(params);
      return new Proxy(raw, { get(target, property) {
        if (property === 'findDocumentsById') return async (ids: string[], deleted: boolean) => {
          if (pause && params.collectionName.startsWith('records_')) { pause = false; entered.resolve(); await release.promise; }
          return target.findDocumentsById(ids, deleted);
        };
        const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
      } });
    } };
    const { storage } = await setup({ storage: native });
    const budget = new ReadBudget(64 * 1024 * 1024), source = storage.queryAccess(budget);
    try {
      await storage.set('alice', { value: 1 }); const view = await source.view(); pause = true;
      const pending = source.withProjection({ id: 'alice' }, view, async projection => projection?.decode()).catch(error => error);
      await entered.promise; const closing = storage.close();
      expect(source.signal.aborted).toBe(true); release.resolve();
      expect(await pending).toBe(source.signal.reason); await closing;
      expect(budget.usedBytes).toBe(0);
    } finally { release.resolve(); await storage.close(); }
  });

  test('malformed invisible record envelopes fail before a query can hide them', async () => {
    let corrupt = false;
    const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
    const native: RxStorage<any, any> = { ...original, createStorageInstance: async params => {
      const raw = await original.createStorageInstance(params);
      return new Proxy(raw, { get(target, property) {
        if (property === 'findDocumentsById') return async (ids: string[], deleted: boolean) => {
          const rows = await target.findDocumentsById(ids, deleted);
          return corrupt && params.collectionName.startsWith('records_') ? rows.map(row => (row as any).kind === 'd' ? { ...row, existence: 'absent' } : row) : rows;
        };
        const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
      } });
    } };
    const { storage } = await setup({ storage: native });
    try {
      await storage.set('alice', { valid: true });
      const source = storage.queryAccess(new ReadBudget(64 * 1024 * 1024)), view = await source.view();
      corrupt = true;
      await expect(source.withProjection({ id: 'alice' }, view, async projection => projection?.visible)).rejects.toMatchObject({ code: 'ReplicaStorageCorruption' });
    } finally { corrupt = false; await storage.close(); }
  });
});

describe('private alias storage', () => {
  test('typed CRUD persists token and pin atomically and allows same ID recreation', async () => {
    const env = await setup(); let storage = env.storage;
    try {
      await storage.set('alice', { count: 9007199254740993n, nested: { _deleted: true, type: 'business' } });
      expect(await storage.get('alice')).toEqual({ id: 'alice', collection: 'users', count: 9007199254740993n, nested: { _deleted: true, type: 'business' } });
      await storage.update('alice', { greeting: 'hello' });
      await storage.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        const rows = await physical.fork.findDocumentsById([await recordKey('d', 'alice')], false);
        const row = rows[0] as DataRecord;
        expect(row.pin).toEqual({ token: row.editToken!, stage: 'await-settlement' });
        expect(decodeBusinessPayload(row.payload).count).toBe(9007199254740993n);
      });
      await storage.close(); storage = await openAliasStorage(env.options);
      expect((await storage.get('alice') as any)?.greeting).toBe('hello');
      await storage.delete('alice'); expect(await storage.get('alice')).toBeNull();
      expect(await storage.get('alice', { showDeleted: true })).toEqual({ id: 'alice', collection: 'users', deleted: true });
      await storage.set('alice', { recreated: true }); expect((await storage.get('alice') as any)?.recreated).toBe(true);
      await storage.delete('missing'); expect(await storage.get('missing', { showDeleted: true })).toBeNull();
      await expect(storage.update('missing', {})).rejects.toMatchObject({ code: 'ReplicaDocumentNotFound' });
      expect(await storage.add({ generated: true })).toBeTruthy();
      expect(storage.generateId()).not.toBe(storage.generateId());
    } finally { await storage.close(); }
  });

  test('metadata projection and conditional writes read source observation rather than data wire', async () => {
    const { storage } = await setup();
    try {
      await storage.set('alice', { value: 1 }); await seedMember(storage, 'alice', '9');
      expect((await storage.get('alice'))?.version).toBe(9n);
      await storage.update('alice', { value: 2 }, { ifMatch: [{ field: 'version', op: '==', value: 9n }] });
      await seedMember(storage, 'alice', '10');
      await expect(storage.update('alice', { value: 3 }, { ifMatch: [{ field: 'version', op: '==', value: 9n }] })).rejects.toMatchObject({ code: 'ReplicaConditionFailed' });
      expect((await storage.get('alice') as any)?.value).toBe(2);
      expect(() => storage.set('alice', { version: 10n })).toThrow('read-only');
      expect(() => storage.set('x/y', {})).toThrow('path segment');
    } finally { await storage.close(); }
  });

  test('parallel opens converge while source and subject namespaces remain distinct', async () => {
    const env = await setup();
    const second = await openAliasStorage(env.options);
    try {
      await env.storage.set('alice', { one: true });
      expect((await second.get('alice') as any)?.one).toBe(true);
      expect((await second.readManifest()).activePhysicalEpoch).toBe((await env.storage.readManifest()).activePhysicalEpoch);
      await expect(openAliasStorage({ ...env.options, source: { collection: 'other', filters: [] } })).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      const otherSession = await createReplicaSession(new DefaultTokenProvider({ token: jwt('bob') }));
      const other = await openAliasStorage({ ...env.options, session: otherSession });
      try { expect(await other.get('alice')).toBeNull(); expect(other.namespace).not.toBe(env.storage.namespace); } finally { await other.close(); }
    } finally { await second.close(); await env.storage.close(); }
  });

  test('binding is permanent, outbound identity comes from manifest, and old epochs are fenced', async () => {
    const { storage } = await setup();
    try {
      let scope = await storage.captureScope('r1');
      await expect(storage.guardNetwork(scope)).rejects.toMatchObject({ code: 'ReplicaSourceNotReady' });
      await storage.bind(scope, 'D1', 'source1'); await storage.bind(scope, 'D1', 'source1');
      await storage.withMaintenance(async access => { await access.writeManifest({ ...access.manifest, sourceReady: true }); });
      scope = await storage.captureScope('r1');
      const headers = await storage.guardNetwork(scope);
      expect(headers).toEqual({ 'X-Syntrix-Expected-Database-Identity': 'D1' }); expect(Object.isFrozen(headers)).toBe(true);
      const native = await storage.native(scope);
      await storage.withMaintenance(async access => {
        const next = crypto.randomUUID();
        await access.writeManifest({ ...access.manifest, physicalEpochs: [access.manifest.activePhysicalEpoch, next] });
        await access.backend.openPhysical(next);
        await access.writeManifest({ ...access.manifest, activePhysicalEpoch: next, physicalEpochs: [next] });
      });
      await expect(native.fork.findDocumentsById([await recordKey('d', 'alice')], false)).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      await expect(storage.bind(scope, 'D1', 'source1')).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      const current = await storage.captureScope();
      await expect(storage.bind(current, 'D2', 'source2')).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      await expect(storage.guardNetwork(current)).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      expect((await storage.readManifest()).boundDatabaseId).toBe('D1');
    } finally { await storage.close(); }
  });

  test('capacity counts d/m as one identity and native writes share admission', async () => {
    const { storage } = await setup({ limits: { maxKnownIds: 1 } });
    try {
      await storage.set('alice', { x: 1 }); await seedMember(storage, 'alice', '1');
      expect((await storage.stats()).knownIds).toBe(1);
      await expect(storage.set('bob', {})).rejects.toMatchObject({ code: 'ReplicaStorageLimit' });
      expect(await storage.get('bob')).toBeNull();
      const scope = await storage.captureScope(); const native = await storage.native(scope);
      expect(await native.fork.findDocumentsById(['a', 'b', 'c', 'd', 'e'], false)).toEqual([]);
      await expect(native.fork.remove()).rejects.toMatchObject({ code: 'ReplicaWriteFence' });
      await storage.update('alice', { y: 2 }); expect((await storage.stats()).knownIds).toBe(1);
    } finally { await storage.close(); }
  });

  test('manifest invalidations cross handles and expired maintenance owners cannot write', async () => {
    const env = await setup(); const second = await openAliasStorage(env.options); const events: string[] = [];
    const sub = second.changes.subscribe(event => { if (event.type === 'view') events.push(event.activeSourceGeneration ?? 'null'); });
    try {
      let held: any;
      await env.storage.withMaintenance(async access => { held = access; await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'g2' }); });
      await new Promise(resolve => setTimeout(resolve, 30));
      expect(events).toContain('g2'); expect((await second.readManifest()).activeSourceGeneration).toBe('g2');
      await expect(held.writeManifest(held.manifest)).rejects.toMatchObject({ code: 'ReplicaWriteFence' });
    } finally { sub.unsubscribe(); await second.close(); await env.storage.close(); }
  });

  for (const entry of ['native', 'source-guard'] as const) test(`foreign epoch retirement through ${entry} cancels captured ownership and admits a fresh scope`, async () => {
    const env = await setup();
    const second = await openAliasStorage(env.options);
    const firstScope = await env.storage.captureScope();
    const native = await env.storage.native(firstScope);
    try {
      const nextEpoch = crypto.randomUUID();
      await second.withMaintenance(async access => {
        await access.writeManifest({ ...access.manifest, physicalEpochs: [access.manifest.activePhysicalEpoch, nextEpoch] });
        await access.backend.openPhysical(nextEpoch);
        await access.writeManifest({ ...access.manifest, activePhysicalEpoch: nextEpoch });
      });
      await expect(entry === 'native' ? native.meta.findDocumentsById(['down|1'], false) : env.storage.guardSourceRead(firstScope)).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      expect(native.ownerSignal.aborted).toBe(true);
      await expect(native.fork.findDocumentsById([], false)).rejects.toBe(native.ownerSignal.reason);
      const nextScope = await env.storage.captureScope();
      expect(nextScope.nativeInstanceId).not.toBe(firstScope.nativeInstanceId);
      expect(nextScope.physicalEpoch).toBe(nextEpoch);
      const next = await env.storage.native(nextScope);
      expect(next.ownerSignal.aborted).toBe(false);
      await env.storage.set('after-retirement', { value: 1 });
      expect(await next.fork.findDocumentsById([await recordKey('d', 'after-retirement')], false)).toHaveLength(1);
    } finally { await second.close(); await env.storage.close(); }
  });

  test('maintenance seed lifetime survives native retirement and is canceled by alias close', async () => {
    const { storage, provider } = await setup();
    const native = await storage.native(await storage.captureScope());
    const entered = deferred();
    const gate = deferred();
    let ownerSignal!: AbortSignal;
    const maintenance = storage.withMaintenance(async access => {
      ownerSignal = access.ownerSignal;
      expect(native.ownerSignal.aborted).toBe(true);
      expect(ownerSignal.aborted).toBe(false);
      entered.resolve(); await gate.promise;
      access.assertActive();
    }).then(() => undefined, error => error);
    try {
      await entered.promise;
      const closing = storage.close();
      expect(ownerSignal.aborted).toBe(true);
      expect(ownerSignal.reason).not.toBe(native.ownerSignal.reason);
      gate.resolve();
      expect(await maintenance).toBe(ownerSignal.reason);
      await closing;
      provider.setToken(jwt('bob'));
      expect(await provider.getToken()).toBe(jwt('bob'));
    } finally { gate.resolve(); await storage.close(); }
  });

  test('authentication replacement closes and fences every old alias operation', async () => {
    const { storage, provider } = await setup();
    await storage.set('alice', { x: 1 }); provider.setToken(jwt('bob'));
    await provider.getToken();
    await expect(storage.get('alice')).rejects.toThrow();
    await storage.close();
  });
});

const deferred = () => { let resolve!: () => void; const promise = new Promise<void>(done => { resolve = done; }); return { promise, resolve }; };
const interceptStorage = (hook: (target: any, rows: any[], context: string) => Promise<any>) => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  return {
    ...original, createStorageInstance: async (params: any) => {
      const target = await original.createStorageInstance(params);
      return new Proxy(target, {
        get(object, property) {
          if (property === 'bulkWrite' && params.collectionName.startsWith('records_')) return (rows: any[], context: string) => hook(target, rows, context);
          const value = Reflect.get(object, property, object); return typeof value === 'function' ? value.bind(object) : value;
        }
      });
    }
  } as RxStorage<any, any>;
};

test('raw 409 rereads and shallow merges the competing edit without losing fields', async () => {
  let compete = false;
  const storage = interceptStorage(async (target, rows, context) => {
    if (compete && context === 'local-edit') {
      compete = false;
      const previous = rows[0].previous;
      const competing = { ...previous, payload: encodeBusinessPayload({ original: 1, competitor: true }), _rev: '2-competitor', _meta: { lwt: rows[0].document._meta.lwt - 0.01 } };
      const result = await target.bulkWrite([{ previous, document: competing }], 'competitor');
      expect(result.error).toEqual([]);
    }
    return target.bulkWrite(rows, context);
  });
  const env = await setup({ storage });
  try {
    await env.storage.set('alice', { original: 1 }); compete = true;
    await env.storage.update('alice', { own: true });
    expect(await env.storage.get('alice')).toMatchObject({ original: 1, competitor: true, own: true });
  } finally { await env.storage.close(); }
});

test('CAS retries reevaluate conditions and preserve original non-conflict storage errors', async () => {
  let compete = false, reject = false; const failure = new Error('write failed');
  const storage = interceptStorage(async (target, rows, context) => {
    if (reject && context === 'local-edit') throw failure;
    if (compete && context === 'local-edit') {
      compete = false;
      const previous = rows[0].previous;
      await target.bulkWrite([{ previous, document: { ...previous, payload: encodeBusinessPayload({ count: 2 }), _rev: '2-competitor', _meta: { lwt: rows[0].document._meta.lwt - 0.01 } } }], 'competitor');
    }
    return target.bulkWrite(rows, context);
  });
  const env = await setup({ storage });
  try {
    await env.storage.set('alice', { count: 1 }); compete = true;
    await expect(env.storage.update('alice', { own: true }, { ifMatch: [{ field: 'count', op: '==', value: 1 }] })).rejects.toMatchObject({ code: 'ReplicaConditionFailed' });
    expect(await env.storage.get('alice')).toMatchObject({ count: 2 });
    reject = true; await expect(env.storage.update('alice', { count: 3 })).rejects.toBe(failure);
    reject = false; await env.storage.update('alice', { count: 4 });
  } finally { await env.storage.close(); }
});

test('metadata-only native updates cannot interleave the conditional d/m view commit', async () => {
  const entered = deferred(), release = deferred(); let pause = false;
  const storage = interceptStorage(async (target, rows, context) => {
    if (pause && context === 'local-edit') { pause = false; entered.resolve(); await release.promise; }
    return target.bulkWrite(rows, context);
  });
  const env = await setup({ storage });
  try {
    await env.storage.set('alice', { count: 1 }); await seedMember(env.storage, 'alice', '5');
    const native = await env.storage.native(await env.storage.captureScope());
    const previous = (await native.fork.findDocumentsById([await recordKey('m', 'alice')], false))[0] as any;
    pause = true;
    const mutation = env.storage.update('alice', { count: 2 }, { ifMatch: [{ field: 'version', op: '==', value: 5n }] });
    await entered.promise;
    let metadataDone = false;
    const metadata = native.fork.bulkWrite([{ previous, document: { ...previous, metadata: { version: '6' } } }], 'native-meta').then(result => { expect(result.error).toEqual([]); metadataDone = true; });
    await new Promise(resolve => setTimeout(resolve, 10)); expect(metadataDone).toBe(false);
    release.resolve(); await mutation; await metadata;
    expect(await env.storage.get('alice')).toMatchObject({ count: 2, version: 6n });
  } finally { release.resolve(); await env.storage.close(); }
});

test('account change during backend creation drains the provisional owner before credentials settle', async () => {
  const entered = deferred(), release = deferred(); let paused = false;
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const storage: RxStorage<any, any> = {
    ...original, createStorageInstance: async params => {
      if (!paused) { paused = true; entered.resolve(); await release.promise; }
      return original.createStorageInstance(params);
    }
  };
  const provider = new DefaultTokenProvider({ token: jwt('opening') });
  const session = await createReplicaSession(provider);
  const opening = openAliasStorage({
    session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'users',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage
  }).catch(error => error);
  await entered.promise; provider.setToken(jwt('replacement'));
  let settled = false; const token = provider.getToken().then(() => { settled = true; });
  await Promise.resolve(); expect(settled).toBe(false); release.resolve();
  expect(await opening).toMatchObject({ code: 'AUTH_SESSION_CHANGED' }); await token; expect(settled).toBe(true);
});


test('equal-LWT lower-key foreign inserts cannot bypass known-ID admission', async () => {
  let raw: any;
  const storage = interceptStorage(async (target, rows, context) => { raw = target; return target.bulkWrite(rows, context); });
  const env = await setup({ storage, limits: { maxKnownIds: 2 } });
  try {
    const ids = await Promise.all(['a', 'b', 'c'].map(async id => ({ id, key: await recordKey('d', id) })));
    ids.sort((a, b) => a.key.localeCompare(b.key));
    await env.storage.set(ids[2].id, { original: true });
    await env.storage.update(ids[2].id, { establishAccounting: true });
    const original = (await raw.findDocumentsById([ids[2].key], false))[0];
    const inserted = { ...original, key: ids[0].key, logicalId: ids[0].id, _rev: '1-other-tab' };
    expect((await raw.bulkWrite([{ document: inserted }], 'foreign')).error).toEqual([]);
    await expect(env.storage.set(ids[1].id, {})).rejects.toMatchObject({ code: 'ReplicaStorageLimit' });
    expect((await env.storage.stats()).knownIds).toBe(2);
  } finally { await env.storage.close(); }
});

test('finite byte admission reconciles foreign same-count rewrites behind its checkpoint', async () => {
  let raw: any;
  const storage = interceptStorage(async (target, rows, context) => { raw = target; return target.bulkWrite(rows, context); });
  const env = await setup({ storage, limits: { maxStoredBytes: 12000 } });
  try {
    await env.storage.set('alice', { small: true }); await env.storage.update('alice', { establishAccounting: true });
    const previous = (await raw.findDocumentsById([await recordKey('d', 'alice')], false))[0];
    expect((await raw.bulkWrite([{ previous, document: { ...previous, payload: encodeBusinessPayload({ large: 'x'.repeat(13000) }), _rev: '3-other-tab' } }], 'foreign')).error).toEqual([]);
    await expect(env.storage.set('bob', {})).rejects.toMatchObject({ code: 'ReplicaStorageLimit' });
    expect((await env.storage.stats()).bytes).toBeGreaterThan(13000);
  } finally { await env.storage.close(); }
});


test('durable recovery intent blocks only its logical target while preserving ordinary edits', async () => {
  const { storage } = await setup();
  try {
    await storage.set('alice', { value: 'current' });
    await storage.withMaintenance(async access => {
      const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
      const current = (await physical.fork.findDocumentsById([await recordKey('d', 'alice')], false))[0] as DataRecord;
      const desired: DataRecord = { ...current, payload: encodeBusinessPayload({ value: 'recovered' }), editToken: 'recovery-result', pin: { token: 'recovery-result', stage: 'await-settlement' } };
      await access.writeManifest({ ...access.manifest, recoveryIntent: { id: 'intent-id', issueId: 'fixture-issue', phaseId: null, physicalEpoch: access.manifest.activePhysicalEpoch, action: 'merge', logicalId: 'alice', protectedToken: current.editToken, resultToken: 'recovery-result', current, desired } });
    });
    await expect(storage.set('alice', { value: 'race' })).rejects.toMatchObject({ code: 'ReplicaRecoveryPending' });
    await expect(storage.delete('alice')).rejects.toMatchObject({ code: 'ReplicaRecoveryPending' });
    await storage.set('intent-id', { independent: true });
    expect(await storage.get('alice')).toMatchObject({ value: 'current' });
    expect(await storage.get('intent-id')).toMatchObject({ independent: true });
  } finally { await storage.close(); }
});


test('ordinary concurrent reads retain only their compact manifest view', async () => {
  const entered = deferred(), release = deferred(); let observe = false, manifestReads = 0, dataReads = 0;
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const storage: RxStorage<any, any> = {
    ...original, createStorageInstance: async params => {
      const raw = await original.createStorageInstance(params);
      return new Proxy(raw, {
        get(target, property) {
          if (property === 'findDocumentsById') return async (ids: string[], deleted: boolean) => {
            if (observe && params.collectionName === 'manifest') manifestReads++;
            if (observe && params.collectionName.startsWith('records_')) { dataReads++; entered.resolve(); await release.promise; }
            return target.findDocumentsById(ids, deleted);
          };
          const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
        }
      });
    }
  };
  const env = await setup({ storage });
  try {
    await env.storage.set('alice', { small: true });
    await env.storage.withMaintenance(async access => {
      const current: DataRecord = { key: await recordKey('d', 'recovery'), kind: 'd', logicalId: 'recovery', existence: 'live', payload: encodeBusinessPayload({ value: 'x'.repeat(100_000) }), editToken: null, pin: null, wire: {} };
      const desired: DataRecord = { ...current, editToken: 'result' };
      await access.writeManifest({ ...access.manifest, recoveryIntent: { id: 'intent', issueId: 'fixture-issue', phaseId: null, physicalEpoch: access.manifest.activePhysicalEpoch, action: 'merge', logicalId: 'recovery', protectedToken: null, resultToken: 'result', current, desired } });
    });
    observe = true;
    const reads = Array.from({ length: 10 }, () => env.storage.get('alice'));
    await entered.promise; await new Promise(resolve => setTimeout(resolve, 15));
    expect(dataReads).toBe(1); expect(manifestReads).toBe(1);
    observe = false; release.resolve(); expect((await Promise.all(reads)).every(row => row?.id === 'alice')).toBe(true);
  } finally { release.resolve(); await env.storage.close(); }
});

test('native changed-document batches seek without offsets and reject unbounded generic queries before IO', async () => {
  const calls: { skip: number; limit: number; }[] = []; let observe = false;
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const storage: RxStorage<any, any> = {
    ...original, createStorageInstance: async params => {
      const raw = await original.createStorageInstance(params);
      return new Proxy(raw, {
        get(target, property) {
          if (property === 'query') return (query: any) => {
            if (observe && params.collectionName.startsWith('records_')) calls.push({ skip: query.query.skip, limit: query.query.limit });
            return target.query(query);
          };
          const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
        }
      });
    }
  };
  const env = await setup({ storage });
  try {
    for (let i = 0; i < 13; i++) await env.storage.set(String(i), { value: i });
    const native = await env.storage.native(await env.storage.captureScope()); observe = true;
    expect((await getChangedDocumentsSince(native.fork, 13, undefined)).documents).toHaveLength(13);
    expect(calls.every(call => call.skip === 0 && call.limit <= 4)).toBe(true);
    expect(calls).toHaveLength(4);
    const query = prepareQuery(native.fork.schema, normalizeMangoQuery(native.fork.schema, { selector: { _deleted: false }, sort: [{ key: 'asc' }], skip: 4, limit: 4 } as any));
    calls.length = 0;
    await expect(native.fork.query(query)).rejects.toMatchObject({ code: 'ReplicaReadBudgetExceeded' });
    expect(calls).toHaveLength(0);
    const keys = await Promise.all(Array.from({ length: 13 }, (_, i) => recordKey('d', String(i))));
    expect(await native.fork.findDocumentsById(keys, true)).toHaveLength(13);
  } finally { await env.storage.close(); }
});

test('failed open retains cleanup failure in authentication ownership', async () => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const cleanupFailure = new Error('cleanup failed'); const rawHandles: any[] = []; let failClose = false;
  const storage: RxStorage<any, any> = {
    ...original, createStorageInstance: async params => {
      const raw = await original.createStorageInstance(params); rawHandles.push(raw);
      return new Proxy(raw, {
        get(target, property) {
          if (property === 'close') return async () => { if (failClose && params.collectionName === 'manifest') throw cleanupFailure; return target.close(); };
          const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
        }
      });
    }
  };
  const env = await setup({ storage }); await env.storage.close(); failClose = true;
  try {
    await expect(openAliasStorage({ ...env.options, source: { collection: 'different', filters: [] } })).rejects.toMatchObject({ code: 'ReplicaStorageCleanupFailed' });
    env.provider.setToken(jwt('replacement'));
    await expect(env.provider.getToken()).rejects.toMatchObject({ code: 'ReplicaStorageCleanupFailed' });
    await expect(env.session.close()).rejects.toMatchObject({ code: 'ReplicaStorageCleanupFailed' });
  } finally { failClose = false; await Promise.allSettled(rawHandles.map(handle => handle.close())); }
});


test('constructor cleanup failure remains owned before an alias backend exists', async () => {
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const rawHandles: any[] = [];
  const openingError = new Error('manifest creation failed'), closingError = new Error('internal close failed');
  let failClose = true;
  const storage: RxStorage<any, any> = { ...original, createStorageInstance: async params => {
    if (params.collectionName === 'manifest') throw openingError;
    const raw = await original.createStorageInstance(params); rawHandles.push(raw);
    return new Proxy(raw, { get(target, property) {
      if (property === 'close') return async () => { if (failClose) throw closingError; return target.close(); };
      const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const provider = new DefaultTokenProvider({ token: jwt('opening') });
  const session = await createReplicaSession(provider);
  try {
    await expect(openAliasStorage({ session, storage, lockManager: createTestLockManager(), endpoint: 'https://example.test', database: 'app',
      name: crypto.randomUUID(), alias: 'users', source: { collection: 'users', filters: [] } })).rejects.toMatchObject({ code: 'ReplicaStorageCleanupFailed', cause: openingError });
    provider.setToken(jwt('replacement'));
    await expect(provider.getToken()).rejects.toMatchObject({ code: 'ReplicaStorageCleanupFailed' });
    await expect(session.close()).rejects.toMatchObject({ code: 'ReplicaStorageCleanupFailed' });
  } finally { failClose = false; await Promise.allSettled(rawHandles.map(handle => handle.close())); }
});


test('capacity statistics include retired member-only IDs without duplicating paired rows', async () => {
  const { storage } = await setup();
  try {
    await storage.set('paired', { pending: true });
    await storage.withMaintenance(async access => {
      const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
      const records = [{ ...(await member('retired', '1', 'old')), slots: [{ generation: 'old', member: false }] }];
      records.push(await member('active', '1', 'g1'), await member('staged', '1', 'g2'), { ...(await member('paired', '1', 'old')), slots: [] });
      const result = await physical.fork.bulkWrite(records.map(row => ({ document: fillObjectDataBeforeInsert(physical.records.schema, row) })), 'stats-fixture');
      expect(result.error).toEqual([]);
      await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'g1', stagedSourceGeneration: 'g2' });
    });
    const stats = await storage.stats();
    expect(stats.knownIds).toBe(4);
    expect(stats.retired).toBe(1);
    expect(stats.shouldCompact).toBe(false);
  } finally { await storage.close(); }
});
