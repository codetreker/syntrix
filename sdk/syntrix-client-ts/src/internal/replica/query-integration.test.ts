import { describe, expect, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import type { RxStorage } from 'rxdb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { createReplicaQueryClient } from './query.js';
import { encodeBusinessPayload, recordKey } from './records.js';
import { createReplicaSession } from './session.js';
import { openAliasStorage, type AliasStorage, type MaintenanceAccess } from './storage.js';
import type { DataRecord, MemberRecord, ReplicaDocument, ReplicaRecord } from './storage-types.js';

const jwt = (sub: string) => `${btoa('{}')}.${btoa(JSON.stringify({ sub, exp: 0 }))}.sig`.replace(/=/g, '');
const setup = async (storage: RxStorage<any, any> = getRxStorageDexie({ indexedDB, IDBKeyRange })) => {
  const provider = new DefaultTokenProvider({ token: jwt('alice') });
  const session = await createReplicaSession(provider);
  const options = {
    session, endpoint: 'https://query.test', database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage,
  };
  return { provider, options, storage: await openAliasStorage(options) };
};
const until = async (predicate: () => boolean, message: string) => {
  const deadline = Date.now() + 5_000;
  while (!predicate()) {
    if (Date.now() >= deadline) throw new Error(`Timed out: ${message}`);
    await new Promise(resolve => setTimeout(resolve, 5));
  }
};
const writeRecord = async (access: MaintenanceAccess, record: ReplicaRecord) => {
  const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
  const previous = (await physical.fork.findDocumentsById([record.key], false))[0];
  await access.backend.writeRecord(physical, record, previous, 'query-fixture');
  return physical;
};
const writeAssumed = async (access: MaintenanceAccess, record: DataRecord) => {
  const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
  const id = `${record.key}|0`;
  const previous = (await physical.meta.findDocumentsById([id], false))[0];
  const document = {
    id, itemId: record.key, isCheckpoint: '0', docData: { ...record, _deleted: false },
    _deleted: false, _attachments: {}, _meta: { lwt: Date.now() }, _rev: '1-fixture',
  };
  expect((await physical.meta.bulkWrite([{ document: document as any, previous }], 'query-fixture')).error).toEqual([]);
};
const data = async (id: string, value: Record<string, any> = { name: id }): Promise<DataRecord> => ({
  key: await recordKey('d', id), kind: 'd', logicalId: id, existence: 'live',
  payload: encodeBusinessPayload(value), editToken: null, pin: null, wire: {},
});
const seed = async (storage: AliasStorage, rows: { id: string; generation?: string; pending?: boolean }[]) => {
  await storage.withMaintenance(async access => {
    for (const row of rows) {
      const record = await data(row.id);
      await writeRecord(access, record);
      if (!row.pending) await writeAssumed(access, record);
      if (row.generation) {
        const member: MemberRecord = { key: await recordKey('m', row.id), kind: 'm', logicalId: row.id,
          slots: [{ generation: row.generation, member: true }], observedExistence: 'live', metadata: { version: '1' } };
        await writeRecord(access, member);
      }
    }
    await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'g1', stagedSourceGeneration: 'g2', sourceReady: true });
  });
};
const ids = (documents: readonly ReplicaDocument[]) => documents.map(document => document.id);

describe('replica query with durable Dexie storage', () => {
  test('metadata churn during reads keeps one watch live across document edits without full rescans', async () => {
    const env = await setup();
    const writer = await openAliasStorage(env.options);
    await writer.set('A', { value: 0 });
    const scope = await writer.captureScope();
    let churn = true, writes = 0, scans = 0;
    const metadata = async () => {
      if (!churn) return;
      writes++;
      await writer.withReplicationAccess(scope, access => access.writeManifest({ ...access.manifest,
        sourceReady: writes % 2 === 0, partialDelivery: writes % 2 !== 0,
        stagedSourceGeneration: `staged-${writes}`,
      }));
    };
    const instrumented: AliasStorage = { ...env.storage, queryAccess: budget => {
      const access = env.storage.queryAccess(budget);
      return { ...access,
        scan: async (after, view) => { scans++; await metadata(); return access.scan(after, view); },
        withProjection: async (key, view, consume) => { await metadata(); return access.withProjection(key, view, consume); },
      };
    } };
    const client = createReplicaQueryClient(instrumented);
    const results: ReplicaDocument[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(rows), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'watch initialization under manifest churn');
      expect(errors).toEqual([]);
      const initialScans = scans;
      for (let value = 1; value <= 12; value++) {
        await writer.update('A', { value });
        await until(() => results.some(rows => rows[0] && 'value' in rows[0] && rows[0].value === value) || errors.length > 0, `watch value ${value} under continuing churn`);
        expect(errors).toEqual([]);
      }
      expect(writes).toBeGreaterThan(8);
      expect(scans).toBe(initialScans);
      expect(results[results.length - 1]).toMatchObject([{ id: 'A', value: 12 }]);
      expect(client.debugStats()).toMatchObject({ queries: 1, nodes: 1, cacheEntries: 1 });
    } finally { churn = false; stop(); await client.close(); await writer.close(); await env.storage.close(); }
    expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, cacheEntries: 0, payloadBytes: 0, keyBytes: 0 });
  }, 20_000);

  test('one durable watch survives more than eight real source-generation changes and resumes row updates', async () => {
    const env = await setup();
    const writer = await openAliasStorage(env.options);
    await seed(writer, [{ id: 'A', generation: 'g1' }, { id: 'B', generation: 'g2' }]);
    const scope = await writer.captureScope();
    let remaining = 13, changes = 0;
    const instrumented: AliasStorage = { ...env.storage, queryAccess: budget => {
      const access = env.storage.queryAccess(budget);
      return { ...access, scan: async (after, view) => {
        if (remaining > 0) {
          remaining--; changes++;
          await writer.withReplicationAccess(scope, current => current.writeManifest({ ...current.manifest,
            activeSourceGeneration: current.manifest.activeSourceGeneration === 'g1' ? 'g2' : 'g1',
            stagedSourceGeneration: null,
          }));
        }
        return access.scan(after, view);
      } };
    } };
    const client = createReplicaQueryClient(instrumented);
    const results: ReplicaDocument[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(rows), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'original observer recovers after generation contention');
      expect(errors).toEqual([]);
      expect(changes).toBe(13);
      expect(results.map(ids)).toEqual([['B']]);
      await writer.update('B', { name: 'updated after contention' });
      await until(() => results.some(rows => rows[0] && 'name' in rows[0] && rows[0].name === 'updated after contention') || errors.length > 0, 'same observer receives later row');
      expect(errors).toEqual([]);
      expect(results[results.length - 1]).toMatchObject([{ id: 'B', name: 'updated after contention' }]);
      expect(client.debugStats()).toMatchObject({ queries: 1, nodes: 1, cacheEntries: 1 });
    } finally { remaining = 0; stop(); await client.close(); await writer.close(); await env.storage.close(); }
    expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, cacheEntries: 0, payloadBytes: 0 });
  }, 20_000);

  test('overlapping dirty and issue protection changes visibility only when their ID union changes', async () => {
    const env = await setup();
    const writer = await openAliasStorage(env.options);
    await seed(writer, [{ id: 'protected' }]);
    const scope = await writer.captureScope();
    await writer.withReplicationAccess(scope, access => access.writeManifest({ ...access.manifest,
      dirtyUpstream: { id: 'phase-one', session: 1, physicalEpoch: access.manifest.activePhysicalEpoch,
        mayHaveDispatched: false, targets: [{ logicalId: 'protected', token: 'edit-one' }] },
      issues: [{ id: 'issue-one', logicalId: 'protected', code: 'conflict', token: 'edit-one' }],
    }));
    let scans = 0;
    const instrumented: AliasStorage = { ...env.storage, queryAccess: budget => {
      const access = env.storage.queryAccess(budget);
      return { ...access, scan: (...args) => { scans++; return access.scan(...args); } };
    } };
    const client = createReplicaQueryClient(instrumented);
    const results: string[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'overlapping protection becomes visible');
      expect(results).toEqual([['protected']]);
      const originalScans = scans;
      await writer.withReplicationAccess(scope, access => access.writeManifest({ ...access.manifest,
        dirtyUpstream: null,
        issues: [{ id: 'issue-two', logicalId: 'protected', code: 'uncertain', token: 'edit-two' }],
      }));
      expect(ids(await client.get())).toEqual(['protected']);
      expect(scans).toBe(originalScans);
      await writer.withReplicationAccess(scope, access => access.writeManifest({ ...access.manifest,
        dirtyUpstream: { id: 'phase-two', session: 9, physicalEpoch: access.manifest.activePhysicalEpoch,
          mayHaveDispatched: true, targets: [{ logicalId: 'protected', token: 'edit-three' }] }, issues: [],
      }));
      expect(ids(await client.get())).toEqual(['protected']);
      expect(scans).toBe(originalScans);
      await writer.withReplicationAccess(scope, access => access.writeManifest({ ...access.manifest, dirtyUpstream: null }));
      await until(() => results[results.length - 1]?.length === 0 || errors.length > 0, 'last protection is removed');
      expect(errors).toEqual([]);
      expect(results).toEqual([['protected'], []]);
      expect(scans).toBeGreaterThan(originalScans);
    } finally { stop(); await client.close(); await writer.close(); await env.storage.close(); }
  }, 20_000);

  test('a discarded near-limit scan yields and recovers, while a stable oversized view still fails', async () => {
    const env = await setup();
    const writer = await openAliasStorage(env.options);
    await seed(writer, ['A', 'B', 'C', 'D'].map(id => ({ id, pending: true })));
    const scope = await writer.captureScope();
    let projections = 0, switched = false;
    const activity: string[] = [];
    const instrumented: AliasStorage = { ...env.storage, queryAccess: budget => {
      const access = env.storage.queryAccess(budget);
      return { ...access, withProjection: async (key, view, consume) => {
        if (++projections === 4) {
          switched = true;
          await writer.withReplicationAccess(scope, current => current.writeManifest({ ...current.manifest,
            activeSourceGeneration: 'g3', stagedSourceGeneration: null,
          }));
        }
        return access.withProjection(key, view, consume);
      } };
    } };
    const client = createReplicaQueryClient(instrumented, { limits: { scanCandidates: 4 }, onActivity: event => activity.push(event.phase) });
    const results: string[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'near-limit watch recovers discarded work');
      expect(switched).toBe(true);
      expect(errors).toEqual([]);
      expect(results).toEqual([['A', 'B', 'C', 'D']]);
      expect(activity).toEqual(['contended', 'recovered']);
      stop(); await client.close();
      expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, payloadBytes: 0 });
      await writer.set('E', { name: 'actual extra candidate' });
      const bounded = createReplicaQueryClient(env.storage, { limits: { scanCandidates: 4 } });
      try {
        await expect(bounded.get()).rejects.toMatchObject({ code: 'QueryBudgetExceeded', kind: 'scanCandidates' });
        expect(bounded.debugStats()).toMatchObject({ queries: 0, nodes: 0, cacheEntries: 0, payloadBytes: 0 });
      } finally { await bounded.close(); }
    } finally { stop(); await client.close(); await writer.close(); await env.storage.close(); }
  }, 20_000);

  test('account replacement drains a contended durable watch and releases its retained resources', async () => {
    const env = await setup();
    const writer = await openAliasStorage(env.options);
    await seed(writer, [{ id: 'A', pending: true }]);
    const scope = await writer.captureScope();
    let scans = 0;
    const instrumented: AliasStorage = { ...env.storage, queryAccess: budget => {
      const access = env.storage.queryAccess(budget);
      return { ...access, scan: async (after, view) => {
        scans++;
        await writer.withReplicationAccess(scope, current => current.writeManifest({ ...current.manifest,
          activeSourceGeneration: current.manifest.activeSourceGeneration === 'g1' ? 'g2' : 'g1', stagedSourceGeneration: null,
        }));
        return access.scan(after, view);
      } };
    } };
    const activity: string[] = []; const results: ReplicaDocument[][] = []; const errors: unknown[] = [];
    const client = createReplicaQueryClient(instrumented, { onActivity: event => activity.push(event.phase) });
    const stop = client.watch({}, rows => results.push(rows), error => errors.push(error));
    try {
      await until(() => activity.includes('contended') || errors.length > 0, 'watch enters contention backoff');
      expect(errors).toEqual([]);
      expect(activity).toEqual(['contended']);
      env.provider.setToken(jwt('bob'));
      expect(await env.provider.getToken()).toBe(jwt('bob'));
      await client.close();
      const stoppedScans = scans;
      const session = await createReplicaSession(env.provider);
      const current = await openAliasStorage({ ...env.options, session });
      const next = createReplicaQueryClient(current);
      try {
        await current.set('B', { name: 'new account' });
        expect(ids(await next.get())).toEqual(['B']);
      } finally { await next.close(); await current.close(); }
      expect(scans).toBe(stoppedScans);
      expect(results).toEqual([]); expect(errors).toEqual([]); expect(activity).toEqual(['contended']);
      expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, cacheEntries: 0, payloadBytes: 0, keyBytes: 0, queuedKeys: 0, queuedBytes: 0 });
    } finally { stop(); await client.close(); await writer.close(); await env.storage.close(); }
  }, 20_000);

  test('manifest-only cross-handle generation activation finds staged members and retains pending edits', async () => {
    const env = await setup();
    const writer = await openAliasStorage(env.options);
    await seed(writer, [{ id: 'A', generation: 'g1' }, { id: 'B', generation: 'g2' }, { id: 'pending', pending: true }]);
    await writer.set('pinned', { name: 'local' });
    const client = createReplicaQueryClient(env.storage);
    const results: string[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'initial source members');
      expect(errors).toEqual([]);
      expect(results[results.length - 1]).toEqual(['A', 'pending', 'pinned']);
      await writer.withMaintenance(async access => {
        await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'g2', stagedSourceGeneration: null });
      });
      await until(() => results[results.length - 1]?.includes('B') === true || errors.length > 0, 'manifest activates B without another row write');
      expect(errors).toEqual([]);
      expect(results[results.length - 1]).toEqual(['B', 'pending', 'pinned']);
      await writer.withMaintenance(async access => {
        await access.writeManifest({ ...access.manifest, activeSourceGeneration: 'empty' });
      });
      await until(() => results[results.length - 1]?.length === 2 || errors.length > 0, 'manifest activates empty membership');
      expect(errors).toEqual([]);
      expect(results[results.length - 1]).toEqual(['pending', 'pinned']);
      expect(ids(await client.get())).toEqual(['pending', 'pinned']);
    } finally { stop(); await client.close(); await writer.close(); await env.storage.close(); }
  }, 20_000);

  test('assumed-only settlement removes the last visibility reason without a data or member write', async () => {
    const { storage } = await setup();
    await seed(storage, [{ id: 'pending', pending: true }]);
    const client = createReplicaQueryClient(storage);
    const results: string[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'pending visibility');
      expect(results[results.length - 1]).toEqual(['pending']);
      await storage.withMaintenance(async access => { await writeAssumed(access, await data('pending')); });
      await until(() => results[results.length - 1]?.length === 0 || errors.length > 0, 'assumed metadata notification');
      expect(errors).toEqual([]);
      expect(results[results.length - 1]).toEqual([]);
    } finally { stop(); await client.close(); await storage.close(); }
  }, 15_000);

  test('physical epoch replacement rebuilds membership and binds subsequent row changes from the new store', async () => {
    const env = await setup(); const writer = await openAliasStorage(env.options);
    await seed(writer, [{ id: 'A', generation: 'g1' }]);
    const client = createReplicaQueryClient(env.storage);
    const results: ReplicaDocument[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(rows), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'original physical epoch');
      expect(ids(results[results.length - 1])).toEqual(['A']);
      await writer.withMaintenance(async access => {
        const nextEpoch = crypto.randomUUID();
        await access.writeManifest({ ...access.manifest, physicalEpochs: [access.manifest.activePhysicalEpoch, nextEpoch] });
        const next = await access.backend.openPhysical(nextEpoch);
        await access.backend.writeRecord(next, await data('B'), undefined, 'query-fixture');
        await access.writeManifest({ ...access.manifest, activePhysicalEpoch: nextEpoch, activeSourceGeneration: 'g2', stagedSourceGeneration: null });
      });
      await until(() => results[results.length - 1]?.[0]?.id === 'B' || errors.length > 0, 'new physical epoch membership');
      expect(errors).toEqual([]);
      expect(ids(results[results.length - 1])).toEqual(['B']);
      await writer.update('B', { name: 'changed' });
      await until(() => (results[results.length - 1]?.[0] as any)?.name === 'changed' || errors.length > 0, 'new physical epoch feed');
      expect(errors).toEqual([]); expect(results[results.length - 1][0]).toMatchObject({ id: 'B', name: 'changed' });
    } finally { stop(); await client.close(); await writer.close(); await env.storage.close(); }
  }, 15_000);

  test('same-generation protection removal invalidates rows retained only by dirty state or issues', async () => {
    const { storage } = await setup();
    await seed(storage, [{ id: 'dirty' }, { id: 'issue' }]);
    await storage.withMaintenance(async access => {
      await access.writeManifest({ ...access.manifest,
        dirtyUpstream: { id: 'round', session: 1, physicalEpoch: access.manifest.activePhysicalEpoch,
          mayHaveDispatched: true, targets: [{ logicalId: 'dirty', token: 'edit' }] },
        issues: [{ id: 'conflict', logicalId: 'issue', code: 'conflict' }],
      });
    });
    const client = createReplicaQueryClient(storage);
    const results: string[][] = []; const errors: unknown[] = [];
    const stop = client.watch({}, rows => results.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'manifest-protected rows');
      expect(errors).toEqual([]); expect(results[results.length - 1]).toEqual(['dirty', 'issue']);
      await storage.withMaintenance(async access => {
        await access.writeManifest({ ...access.manifest, dirtyUpstream: null, issues: [] });
      });
      await until(() => results[results.length - 1]?.length === 0 || errors.length > 0, 'same-generation manifest reconciliation');
      expect(errors).toEqual([]); expect(results[results.length - 1]).toEqual([]);
    } finally { stop(); await client.close(); await storage.close(); }
  }, 15_000);

  test('canonical queries share candidates across handles and survive closing one owner', async () => {
    const env = await setup();
    const second = await openAliasStorage(env.options);
    await env.storage.set('A', { score: 1 }); await env.storage.set('B', { score: 2 });
    const firstClient = createReplicaQueryClient(env.storage);
    const secondClient = createReplicaQueryClient(second);
    const first: string[][] = []; const last: string[][] = []; const errors: unknown[] = [];
    const query = { orderBy: [{ field: 'score', direction: 'asc' as const }], limit: 1 };
    const unsubscribe = firstClient.watch(query, rows => first.push(ids(rows)), error => errors.push(error));
    const unsubscribeOther = secondClient.watch(query, rows => last.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => (first.length > 0 && last.length > 0) || errors.length > 0, 'shared initialization');
      expect(errors).toEqual([]);
      expect(firstClient.debugStats()).toMatchObject({ queries: 1, nodes: 2, cacheEntries: 2 });
      await firstClient.close(); await env.storage.close();
      const previousCount = first.length;
      await second.update('B', { score: 0 });
      await until(() => last[last.length - 1]?.[0] === 'B' || errors.length > 0, 'remaining handle update');
      expect(errors).toEqual([]); expect(last[last.length - 1]).toEqual(['B']); expect(first).toHaveLength(previousCount);
      expect(secondClient.debugStats()).toMatchObject({ queries: 1, nodes: 2 });
      unsubscribeOther();
      await until(() => secondClient.debugStats().queries === 0, 'final observer releases query');
      expect(secondClient.debugStats()).toMatchObject({ nodes: 0, cacheEntries: 0, payloadBytes: 0 });
    } finally { unsubscribe(); unsubscribeOther(); await firstClient.close(); await secondClient.close(); await second.close(); await env.storage.close(); }
  }, 15_000);

  test('off-window order-key growth terminates only the over-budget query and preserves stored edits', async () => {
    const { storage } = await setup();
    await storage.set('A', { rank: 'a' }); await storage.set('B', { rank: 'z' });
    const client = createReplicaQueryClient(storage, { limits: { keyBytes: 16 * 1024 } });
    const results: string[][] = []; const errors: unknown[] = [];
    const stop = client.watch({ orderBy: [{ field: 'rank', direction: 'asc' }], limit: 1 },
      rows => results.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'bounded-key initial window');
      expect(errors).toEqual([]); expect(results[results.length - 1]).toEqual(['A']);
      const large = 'z'.repeat(20 * 1024);
      await storage.update('B', { rank: large });
      await until(() => errors.length > 0, 'off-window key budget exceeded');
      expect(errors[0]).toMatchObject({ code: 'QueryBudgetExceeded' });
      expect(results).toEqual([['A']]);
      expect((await storage.get('B') as any)?.rank).toBe(large);
      expect(client.debugStats()).toMatchObject({ nodes: 0, cacheEntries: 0, payloadBytes: 0 });
      expect(ids(await client.get())).toEqual(['A', 'B']);
    } finally { stop(); await client.close(); await storage.close(); }
  }, 15_000);

  test('off-window payload growth is continuously charged after a limit-one watch initializes', async () => {
    const { storage } = await setup();
    await storage.set('A', { rank: 1 }); await storage.set('B', { rank: 2 });
    const client = createReplicaQueryClient(storage, { limits: { payloadBytes: 64 * 1024 } });
    const results: string[][] = []; const errors: unknown[] = [];
    const stop = client.watch({ orderBy: [{ field: 'rank', direction: 'asc' }], limit: 1 },
      rows => results.push(ids(rows)), error => errors.push(error));
    try {
      await until(() => results.length > 0 || errors.length > 0, 'bounded-payload initial window');
      expect(errors).toEqual([]); expect(results[results.length - 1]).toEqual(['A']);
      await storage.update('B', { body: 'b'.repeat(32 * 1024) });
      await until(() => errors.length > 0, 'off-window payload budget exceeded');
      expect(errors[0]).toMatchObject({ code: 'QueryBudgetExceeded' });
      expect(results).toEqual([['A']]);
      expect((await storage.get('B') as any)?.body).toHaveLength(32 * 1024);
      expect(client.debugStats()).toMatchObject({ nodes: 0, cacheEntries: 0, payloadBytes: 0 });
    } finally { stop(); await client.close(); await storage.close(); }
  }, 15_000);

  test('a legal four-MiB row is rejected before payload decoding when its reservation does not fit', async () => {
    const physicalLimits: number[] = [];
    const underlying = getRxStorageDexie({ indexedDB, IDBKeyRange });
    const bounded: RxStorage<any, any> = { ...underlying, createStorageInstance: async parameters => {
      const instance = await underlying.createStorageInstance(parameters);
      return new Proxy(instance, { get(target, property) {
        if (property === 'query') return async (query: any) => {
          if (parameters.collectionName.startsWith('records_')) physicalLimits.push(query.query.limit);
          return target.query(query);
        };
        const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
      } });
    } };
    const { storage } = await setup(bounded);
    await storage.set('large', { body: 'x'.repeat(4 * 1024 * 1024) });
    let decodes = 0;
    const instrumented: AliasStorage = { ...storage, queryAccess: budget => {
      const access = storage.queryAccess(budget);
      return { ...access, withProjection: (key, view, consume) => access.withProjection(key, view, projection => consume(projection && {
        ...projection, decode: () => { decodes++; return projection.decode(); },
      })) };
    } };
    const client = createReplicaQueryClient(instrumented, { limits: { payloadBytes: 8 * 1024 * 1024 } });
    physicalLimits.length = 0;
    try {
      await expect(client.get()).rejects.toMatchObject({ code: 'QueryBudgetExceeded' });
      expect(decodes).toBe(0);
      expect(physicalLimits.length).toBeGreaterThan(0);
      expect(physicalLimits.every(limit => limit >= 1 && limit <= 4)).toBe(true);
      expect(client.debugStats().readPeakBytes).toBeLessThanOrEqual(64 * 1024 * 1024);
      expect((await storage.get('large') as any)?.body).toHaveLength(4 * 1024 * 1024);
    } finally { await client.close(); await storage.close(); }
  }, 20_000);

  for (const cancellation of ['close', 'account-change'] as const) {
    test(`${cancellation} suppresses a late initialization result and releases query resources`, async () => {
      let release!: () => void; let entered!: () => void;
      const pending = new Promise<void>(resolve => { release = resolve; });
      const reading = new Promise<void>(resolve => { entered = resolve; });
      let armed = false;
      const underlying = getRxStorageDexie({ indexedDB, IDBKeyRange });
      const delayed: RxStorage<any, any> = { ...underlying, createStorageInstance: async parameters => {
        const instance = await underlying.createStorageInstance(parameters);
        return new Proxy(instance, { get(target, property) {
          if (property === 'query') return async (query: any) => {
            if (armed && parameters.collectionName.startsWith('records_')) {
              armed = false; entered(); await pending;
            }
            return target.query(query);
          };
          const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
        } });
      } };
      const { storage, provider } = await setup(delayed);
      await storage.set('A', { rank: 1 });
      const client = createReplicaQueryClient(storage);
      const results: ReplicaDocument[][] = [];
      armed = true;
      const stop = client.watch({}, rows => results.push(rows), () => undefined);
      try {
        await reading;
        let closed: Promise<unknown>;
        if (cancellation === 'close') closed = client.close();
        else { provider.setToken(jwt('bob')); closed = provider.getToken(); }
        release(); await closed;
        await client.close();
        expect(results).toEqual([]);
        expect(client.debugStats()).toMatchObject({ queries: 0, nodes: 0, cacheEntries: 0, payloadBytes: 0 });
      } finally { release(); stop(); await client.close(); await storage.close(); }
    }, 15_000);
  }
});
