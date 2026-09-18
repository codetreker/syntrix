import { describe, expect, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { fillObjectDataBeforeInsert, getChangedDocumentsSince, normalizeMangoQuery, prepareQuery, type RxStorage } from 'rxdb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createLocalSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage, type AliasStorage, type OpenAliasStorageOptions } from './storage.js';
import { decodeBusinessPayload, encodeBusinessPayload, recordKey } from './records.js';
import type { DataRecord, MemberRecord } from './storage-types.js';
const jwt = (subject: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub: subject, exp: 0 }))}.sig`.replace(/=/g, '');
const setup = async (extra: Partial<OpenAliasStorageOptions> = {}) => {
  const provider = new DefaultTokenProvider({ token: jwt('alice') });
  const session = await createLocalSession(provider);
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
      await expect(storage.update('missing', {})).rejects.toMatchObject({ code: 'LocalDocumentNotFound' });
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
      await expect(storage.update('alice', { value: 3 }, { ifMatch: [{ field: 'version', op: '==', value: 9n }] })).rejects.toMatchObject({ code: 'LocalConditionFailed' });
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
      const otherSession = await createLocalSession(new DefaultTokenProvider({ token: jwt('bob') }));
      const other = await openAliasStorage({ ...env.options, session: otherSession });
      try { expect(await other.get('alice')).toBeNull(); expect(other.namespace).not.toBe(env.storage.namespace); } finally { await other.close(); }
    } finally { await second.close(); await env.storage.close(); }
  });

  test('binding is permanent, outbound identity comes from manifest, and old epochs are fenced', async () => {
    const { storage } = await setup();
    try {
      let scope = await storage.captureScope('r1');
      await expect(storage.guardNetwork(scope)).rejects.toMatchObject({ code: 'LocalSourceNotReady' });
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
      await expect(storage.set('bob', {})).rejects.toMatchObject({ code: 'LocalStorageLimit' });
      expect(await storage.get('bob')).toBeNull();
      const scope = await storage.captureScope(); const native = await storage.native(scope);
      expect(await native.fork.findDocumentsById(['a', 'b', 'c', 'd', 'e'], false)).toEqual([]);
      await expect(native.fork.remove()).rejects.toMatchObject({ code: 'LocalWriteFence' });
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
      await expect(held.writeManifest(held.manifest)).rejects.toMatchObject({ code: 'LocalWriteFence' });
    } finally { sub.unsubscribe(); await second.close(); await env.storage.close(); }
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
    await expect(env.storage.update('alice', { own: true }, { ifMatch: [{ field: 'count', op: '==', value: 1 }] })).rejects.toMatchObject({ code: 'LocalConditionFailed' });
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
  const session = await createLocalSession(provider);
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
    await expect(env.storage.set(ids[1].id, {})).rejects.toMatchObject({ code: 'LocalStorageLimit' });
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
    await expect(env.storage.set('bob', {})).rejects.toMatchObject({ code: 'LocalStorageLimit' });
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
      await access.writeManifest({ ...access.manifest, recoveryIntent: { id: 'intent-id', action: 'merge', logicalId: 'alice', protectedToken: current.editToken, resultToken: 'recovery-result', current, desired } });
    });
    await expect(storage.set('alice', { value: 'race' })).rejects.toMatchObject({ code: 'LocalRecoveryPending' });
    await expect(storage.delete('alice')).rejects.toMatchObject({ code: 'LocalRecoveryPending' });
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
      await access.writeManifest({ ...access.manifest, recoveryIntent: { id: 'intent', action: 'merge', logicalId: 'recovery', protectedToken: null, resultToken: 'result', current, desired } });
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
    await expect(native.fork.query(query)).rejects.toMatchObject({ code: 'LocalReadBudgetExceeded' });
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
    await expect(openAliasStorage({ ...env.options, source: { collection: 'different', filters: [] } })).rejects.toMatchObject({ code: 'LocalStorageCleanupFailed' });
    env.provider.setToken(jwt('replacement'));
    await expect(env.provider.getToken()).rejects.toMatchObject({ code: 'LocalStorageCleanupFailed' });
    await expect(env.session.close()).rejects.toMatchObject({ code: 'LocalStorageCleanupFailed' });
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
  const session = await createLocalSession(provider);
  try {
    await expect(openAliasStorage({ session, storage, lockManager: createTestLockManager(), endpoint: 'https://example.test', database: 'app',
      name: crypto.randomUUID(), alias: 'users', source: { collection: 'users', filters: [] } })).rejects.toMatchObject({ code: 'LocalStorageCleanupFailed', cause: openingError });
    provider.setToken(jwt('replacement'));
    await expect(provider.getToken()).rejects.toMatchObject({ code: 'LocalStorageCleanupFailed' });
    await expect(session.close()).rejects.toMatchObject({ code: 'LocalStorageCleanupFailed' });
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
