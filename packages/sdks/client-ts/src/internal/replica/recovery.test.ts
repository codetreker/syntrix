import { describe, expect, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import type { RxStorage } from 'rxdb';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { createReplicaSession } from './session.js';
import { openAliasStorage, type AliasStorage } from './storage.js';
import { decodeBusinessPayload, encodeBusinessPayload, recordKey } from './records.js';
import { inspectReplica, replayReplicaRecovery, resolveReplica, type RecoveryDecision } from './recovery.js';
import type { DataRecord, ReplicaRecord } from './storage-types.js';
import type { UpstreamTransport } from './upstream-types.js';
import { openAliasBackend } from './backend.js';
import { createNamespace } from './identity.js';

type Fault = (rows: any[], context: string, commit: () => Promise<any>) => Promise<any>;
const setup = async () => {
  let fault: Fault | undefined;
  let readFailure: Error | undefined;
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const engine: RxStorage<any, any> = { ...original, createStorageInstance: async options => {
    const storage = await original.createStorageInstance(options);
    return new Proxy(storage, { get(target, property) {
      if (property === 'bulkWrite') return (rows: any[], context: string) => fault ? fault(rows, context, () => target.bulkWrite(rows, context)) : target.bulkWrite(rows, context);
      if (property === 'findDocumentsById') return (...args: Parameters<typeof target.findDocumentsById>) => {
        if (options.collectionName === 'manifest' && readFailure) return Promise.reject(readFailure);
        return target.findDocumentsById(...args);
      };
      const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const token = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'alice', exp: 0 }))}.sig`.replace(/=/g, '');
  const session = await createReplicaSession(new DefaultTokenProvider({ token }));
  const options = { session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage: engine };
  let storage = await openAliasStorage(options);
  return { options, get storage() { return storage; }, setFault: (value?: Fault) => { fault = value; }, setReadFailure: (value?: Error) => { readFailure = value; },
    reopen: async (closeFailure?: unknown) => {
      if (closeFailure === undefined) await storage.close(); else await expect(storage.close()).rejects.toBe(closeFailure);
      storage = await openAliasStorage(options);
    },
    close: async () => { await storage.close(); await session.close(); } };
};
const meta = (row: ReplicaRecord) => ({ id: `${row.key}|0`, itemId: row.key, isCheckpoint: '0', docData: { ...row, _deleted: false },
  _deleted: false, _attachments: {}, _meta: { lwt: Date.now() }, _rev: '1-fixture' });
const prepare = async (storage: AliasStorage) => {
  await storage.set('a', { value: 'a-pending' }); await storage.set('b', { value: 'b-pending' });
  await storage.withMaintenance(async access => {
    const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
    const targets = [];
    for (const id of ['a', 'b']) {
      const row = (await physical.fork.findDocumentsById([await recordKey('d', id)], false))[0] as DataRecord;
      targets.push({ logicalId: id, token: row.editToken! });
      const assumed = { ...row, payload: encodeBusinessPayload({ value: `${id}-old` }), editToken: null, pin: null, wire: { version: '5' } };
      expect((await physical.meta.bulkWrite([{ document: meta(assumed) as any }], 'fixture')).error).toEqual([]);
    }
    const example = meta({ key: 'c:progress', kind: 'c', checkpoint: {}, generation: 'g1', phase: 'live', bootstrapComplete: true, partialDelivery: false });
    const { docData: _ignored, ...base } = example;
    for (const direction of ['up', 'down']) {
      expect((await physical.meta.bulkWrite([{ document: { ...base, id: `${direction}|1`, itemId: direction, isCheckpoint: '1',
        checkpointData: direction === 'up' ? { id: 'unchanged-up', lwt: 1 } : { source: { cursor: 'unchanged-down' } } } as any }], 'fixture')).error).toEqual([]);
    }
    await access.writeManifest({ ...access.manifest, boundDatabaseId: 'D1', sourceHash: 'source1', sourceReady: true,
      activeSourceGeneration: 'g1', dirtyUpstream: { id: 'phase', session: 0, physicalEpoch: physical.epoch, mayHaveDispatched: true, targets }, issues: [] });
  });
};
const transport = (read: UpstreamTransport['readCurrent'] = async () => null): UpstreamTransport => ({
  prepare: () => { throw new Error('Recovery must not prepare Push'); }, push: async () => { throw new Error('Recovery must not Push'); }, readCurrent: read,
});
const decision = async (storage: AliasStorage, kind: 'adopt-server' | 'merge-local' = 'adopt-server'): Promise<RecoveryDecision> => {
  const inspection = await inspectReplica(storage, { logicalId: 'a' });
  const base = { issueId: inspection.issueId!, logicalId: 'a', editToken: inspection.document!.desired!.editToken, physicalEpoch: inspection.physicalEpoch };
  return kind === 'adopt-server' ? { kind, ...base } : { kind, ...base, data: { value: 'merged', exact: 9007199254740993n } };
};
const assertPreserved = async (storage: AliasStorage) => {
  const inspection = await inspectReplica(storage, { logicalId: 'b' });
  expect(decodeBusinessPayload(inspection.document!.desired!.payload)).toEqual({ value: 'b-pending' });
  expect(decodeBusinessPayload(inspection.document!.assumed!.payload)).toEqual({ value: 'b-old' });
  expect(inspection.targets.map(target => target.logicalId)).toEqual(['b']);
  await storage.withMaintenance(async access => {
    const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
    const rows = await physical.meta.findDocumentsById(['up|1', 'down|1'], false);
    expect(rows.find(row => row.id === 'up|1')!.checkpointData).toEqual({ id: 'unchanged-up', lwt: 1 });
    expect(rows.find(row => row.id === 'down|1')!.checkpointData).toEqual({ source: { cursor: 'unchanged-down' } });
  });
};

describe('explicit replica recovery', () => {
  test('inspection exposes durable phase facts and explicit action issue identities without hiding content conflicts', async () => {
    const env = await setup();
    try {
      expect(await inspectReplica(env.storage)).toMatchObject({ phase: null, recovering: false, availableActions: [] });
      await prepare(env.storage);
      expect(await inspectReplica(env.storage)).toMatchObject({ phase: { id: 'phase', state: 'dispatched' }, recovering: false,
        availableActions: [{ kind: 'retry-uncertain', issueId: 'phase' }, { kind: 'reset-alias', issueId: 'phase' }] });
      await env.storage.withMaintenance(async access => { await access.writeManifest({ ...access.manifest,
        dirtyUpstream: { ...access.manifest.dirtyUpstream!, mayHaveDispatched: false } }); });
      expect(await inspectReplica(env.storage, { logicalId: 'a' })).toMatchObject({ phase: { id: 'phase', state: 'prepared' },
        availableActions: [{ kind: 'adopt-server', issueId: 'phase' }, { kind: 'merge-local', issueId: 'phase' },
          { kind: 'retry-uncertain', issueId: 'phase' }, { kind: 'reset-alias', issueId: 'phase' }] });
      await env.storage.withMaintenance(async access => { await access.writeManifest({ ...access.manifest,
        issues: [{ id: 'conflict-a', logicalId: 'a', code: 'ReplicaWriteConflict' }] }); });
      const conflict = await inspectReplica(env.storage, { logicalId: 'a' });
      expect(conflict.availableActions).toEqual([{ kind: 'adopt-server', issueId: 'conflict-a' }, { kind: 'merge-local', issueId: 'conflict-a' },
        { kind: 'reset-alias', issueId: 'conflict-a' }]);
      expect((await inspectReplica(env.storage, { logicalId: 'b' })).availableActions.some(action => action.kind === 'retry-uncertain')).toBe(false);
      await env.storage.withMaintenance(async access => { await access.writeManifest({ ...access.manifest, boundDatabaseId: null, sourceHash: null }); });
      expect((await inspectReplica(env.storage, { logicalId: 'a' })).availableActions).toEqual([{ kind: 'reset-alias', issueId: 'conflict-a' }]);
    } finally { await env.close(); }
  });

  test('an interrupted recovery offers only the guarded reset action', async () => {
    const env = await setup();
    try {
      await prepare(env.storage);
      const selected = await decision(env.storage);
      const failure = new Error('interrupt recovery');
      env.setFault(async (_rows, context, commit) => { if (context === 'replica-recovery-meta') throw failure; return commit(); });
      await expect(resolveReplica(env.storage, transport(), selected)).rejects.toBe(failure);
      env.setFault();
      expect(await inspectReplica(env.storage, { logicalId: 'a' })).toMatchObject({ recovering: true, phase: { id: 'phase', state: 'dispatched' },
        availableActions: [{ kind: 'reset-alias', issueId: 'phase' }] });
      await replayReplicaRecovery(env.storage);
      expect((await inspectReplica(env.storage)).recovering).toBe(false);
    } finally { env.setFault(); await env.close(); }
  });

  test('optional authority inspection distinguishes unknown, missing, live and tombstoned current without persisting observations', async () => {
    const env = await setup(); let reads = 0;
    try {
      await prepare(env.storage);
      const current = { id: 'a', collection: 'users', version: 9n, createdAt: 10n, updatedAt: 11n, value: 9007199254740993n };
      let response: typeof current | (typeof current & { deleted: true }) | null = current;
      const wire = transport(async (id, context) => { reads++; expect(id).toBe('a'); expect(context.expectedDatabaseIdentity).toBe('D1'); return response; });
      expect((await inspectReplica(env.storage, { logicalId: 'a' }, wire)).document!.current).toBeUndefined(); expect(reads).toBe(0);
      for (const next of [current, null, { ...current, deleted: true as const }]) {
        response = next;
        const inspected = await inspectReplica(env.storage, { logicalId: 'a', readCurrent: true }, wire);
        expect(inspected.document!.current).toEqual({ source: 'authoritative-read', document: next });
        expect(decodeBusinessPayload(inspected.document!.desired!.payload)).toEqual({ value: 'a-pending' });
        expect(decodeBusinessPayload(inspected.document!.assumed!.payload)).toEqual({ value: 'a-old' });
      }
      expect(reads).toBe(3); expect((await env.storage.readManifest()).recoveryIntent).toBeNull();
      expect((await inspectReplica(env.storage, { logicalId: 'a' })).document!.current).toBeUndefined();
      await expect(inspectReplica(env.storage, { readCurrent: true }, wire)).rejects.toThrow('document ID');
      await expect(inspectReplica(env.storage, { logicalId: 'a', readCurrent: true })).rejects.toThrow('transport');
    } finally { await env.close(); }
  });

  for (const changed of ['edit', 'issue', 'epoch', 'assumed'] as const) test(`authority inspection rejects changed ${changed} after an unlocked read`, async () => {
    const env = await setup();
    try {
      await prepare(env.storage);
      const before = await inspectReplica(env.storage);
      await expect(inspectReplica(env.storage, { logicalId: 'a', readCurrent: true }, transport(async () => {
        if (changed === 'edit') await env.storage.set('a', { newer: true });
        if (changed === 'epoch') await resolveReplica(env.storage, transport(), { kind: 'reset-alias', issueId: 'phase', stateToken: before.stateToken, discardPending: true });
        if (changed === 'issue' || changed === 'assumed') await env.storage.withMaintenance(async access => {
          if (changed === 'issue') await access.writeManifest({ ...access.manifest, dirtyUpstream: { ...access.manifest.dirtyUpstream!, id: 'another-phase' } });
          else {
            const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch), key = await recordKey('d', 'a');
            const previous = (await physical.meta.findDocumentsById([`${key}|0`], false))[0];
            expect((await physical.meta.bulkWrite([{ previous, document: { ...previous, _rev: '2-fixture',
              docData: { ...previous.docData, wire: { version: '6' } } as any } }], 'fixture')).error).toEqual([]);
          }
        });
        return null;
      }))).rejects.toMatchObject({ code: 'ReplicaRecoveryStale' });
      const next = await inspectReplica(env.storage, { logicalId: 'a', readCurrent: true }, transport());
      expect(next.document!.current).toEqual({ source: 'authoritative-read', document: null });
    } finally { await env.close(); }
  });

  test('authority inspection bounds concurrent retained snapshots, freezes options, and releases the budget after cancellation', async () => {
    const env = await setup(); const abort = new AbortController();
    let entered!: () => void, release!: () => void;
    const enteredPromise = new Promise<void>(resolve => { entered = resolve; });
    const gate = new Promise<void>(resolve => { release = resolve; });
    try {
      await prepare(env.storage);
      const options = { logicalId: 'a', readCurrent: true, signal: abort.signal };
      const pending = inspectReplica(env.storage, options, transport(async (id, context) => {
        expect(id).toBe('a'); entered(); await gate; context.signal.throwIfAborted(); return null;
      }));
      options.logicalId = 'b'; options.readCurrent = false;
      await enteredPromise;
      await expect(inspectReplica(env.storage, { logicalId: 'a', readCurrent: true }, transport())).rejects.toMatchObject({ code: 'ReplicaReadBudgetExceeded' });
      await env.storage.set('b', { freeWhileReading: true });
      const cancelled = new Error('inspection cancelled'); abort.abort(cancelled); release();
      await expect(pending).rejects.toBe(cancelled);
      expect((await inspectReplica(env.storage, { logicalId: 'a', readCurrent: true }, transport())).document!.current!.document).toBeNull();
    } finally { release?.(); await env.close(); }
  });

  test('authority inspection requires binding and an identity mismatch blocks reads while preserving desired data', async () => {
    const env = await setup();
    try {
      let calls = 0;
      await expect(inspectReplica(env.storage, { logicalId: 'a', readCurrent: true }, transport(async () => { calls++; return null; }))).rejects.toMatchObject({ code: 'ReplicaSourceNotReady' });
      expect(calls).toBe(0); await prepare(env.storage);
      const failure = Object.assign(new Error('database replaced'), { code: 'DATABASE_IDENTITY_MISMATCH' });
      await expect(inspectReplica(env.storage, { logicalId: 'a', readCurrent: true }, transport(async () => { throw failure; }))).rejects.toBe(failure);
      await expect(env.storage.guardSourceRead(await env.storage.captureScope())).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
      const offline = await inspectReplica(env.storage, { logicalId: 'a' });
      expect(offline.document!.current).toBeUndefined(); expect(decodeBusinessPayload(offline.document!.desired!.payload)).toEqual({ value: 'a-pending' });
      expect((await env.storage.readManifest()).boundDatabaseId).toBe('D1');
    } finally { await env.close(); }
  });

  test('inspection does not retire native scope and derives crash uncertainty without issue snapshots', async () => {
    const env = await setup();
    try {
      expect((await inspectReplica(env.storage)).issueId).toBeNull();
      await prepare(env.storage);
      const scope = await env.storage.captureScope(), native = await env.storage.native(scope);
      const inspected = await inspectReplica(env.storage, { logicalId: 'a' });
      expect(inspected.issueId).toBe('phase'); expect(inspected.issues).toEqual([]);
      expect(inspected.targets).toHaveLength(2); expect(native.ownerSignal.aborted).toBe(false);
      await expect(env.storage.native(scope)).resolves.toBeDefined();
      expect((await inspectReplica(env.storage, { logicalId: 'unknown' })).document).toEqual({ desired: null, assumed: null });
    } finally { await env.close(); }
  });

  for (const kind of ['adopt-server', 'merge-local'] as const) test(`${kind} replays the fork/meta crash gap without acknowledging another ID`, async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const selected = await decision(env.storage, kind);
      const failure = new Error('metadata unavailable');
      env.setFault(async (_rows, context, commit) => { if (context === 'replica-recovery-meta') throw failure; return commit(); });
      await expect(resolveReplica(env.storage, transport(), selected)).rejects.toBe(failure);
      expect((await env.storage.readManifest()).recoveryIntent).not.toBeNull();
      await expect(env.storage.set('a', { tooLate: true })).rejects.toMatchObject({ code: 'ReplicaRecoveryPending' });
      env.setFault(); await env.reopen(); await replayReplicaRecovery(env.storage);
      const inspected = await inspectReplica(env.storage, { logicalId: 'a' });
      expect(inspected.document!.assumed!.existence).toBe('absent');
      expect(inspected.document!.desired!.existence).toBe(kind === 'adopt-server' ? 'absent' : 'live');
      if (kind === 'merge-local') expect(decodeBusinessPayload(inspected.document!.desired!.payload)).toEqual({ value: 'merged', exact: 9007199254740993n });
      else expect(await env.storage.get('a', { showDeleted: true })).toBeNull();
      expect((await env.storage.readManifest()).recoveryIntent).toBeNull();
      await assertPreserved(env.storage);
    } finally { env.setFault(); await env.close(); }
  });

  test('metadata-before-intent-clear crash replays idempotently and removes obsolete conflict provenance', async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const selected = await decision(env.storage);
      const failure = new Error('manifest finish unavailable');
      env.setFault(async (rows, _context, commit) => {
        if (rows.some(row => row.previous?.recoveryIntent && row.document.recoveryIntent === null)) throw failure;
        return commit();
      });
      await expect(resolveReplica(env.storage, transport(), selected)).rejects.toBe(failure);
      const intent = (await env.storage.readManifest()).recoveryIntent!; expect(intent).not.toBeNull();
      env.setFault();
      await env.storage.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        const previous = (await physical.meta.findDocumentsById([`${intent.current.key}|0`], false))[0];
        const fork = (await physical.fork.findDocumentsById([intent.current.key], false))[0];
        expect((await physical.meta.bulkWrite([{ previous, document: { ...previous, isResolvedConflict: fork._rev, _rev: '2-fixture' } }], 'fixture')).error).toEqual([]);
      });
      await env.reopen(); await replayReplicaRecovery(env.storage); await replayReplicaRecovery(env.storage);
      await env.storage.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
        expect((await physical.meta.findDocumentsById([`${intent.current.key}|0`], false))[0].isResolvedConflict).toBeUndefined();
      });
      await assertPreserved(env.storage);
    } finally { env.setFault(); await env.close(); }
  });

  for (const point of ['intent', 'fork', 'meta', 'finish'] as const) test(`lost ${point} acknowledgement verifies its committed value`, async () => {
    const env = await setup(); let injected = false;
    try {
      await prepare(env.storage); const selected = await decision(env.storage);
      env.setFault(async (rows, context, commit) => {
        const matched = point === 'fork' ? context === 'replica-recovery-fork' : point === 'meta' ? context === 'replica-recovery-meta' :
          point === 'intent' ? rows.some(row => row.document.recoveryIntent && !row.previous?.recoveryIntent) :
            rows.some(row => row.previous?.recoveryIntent && row.document.recoveryIntent === null);
        const result = await commit();
        if (!injected && matched) { injected = true; throw new Error('lost committed acknowledgement'); }
        return result;
      });
      await resolveReplica(env.storage, transport(), selected);
      expect(injected).toBe(true); expect((await env.storage.readManifest()).recoveryIntent).toBeNull();
      await assertPreserved(env.storage);
    } finally { env.setFault(); await env.close(); }
  });

  test('an edit during the authoritative read rejects stale recovery before creating an intent', async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const selected = await decision(env.storage);
      await expect(resolveReplica(env.storage, transport(async (_id, context) => {
        expect(context.expectedDatabaseIdentity).toBe('D1');
        await env.storage.set('a', { value: 'later-edit' }); return null;
      }), selected)).rejects.toMatchObject({ code: 'ReplicaRecoveryStale' });
      expect((await env.storage.readManifest()).recoveryIntent).toBeNull();
      expect(await env.storage.get('a')).toMatchObject({ value: 'later-edit' });
    } finally { await env.close(); }
  });

  test('per-ID conflict recovery preserves authoritative live/tombstone metadata and clears the phase only after both targets', async () => {
    const env = await setup();
    try {
      await prepare(env.storage);
      await env.storage.withMaintenance(async access => {
        await access.writeManifest({ ...access.manifest, issues: access.manifest.dirtyUpstream!.targets.map(target =>
          ({ id: `conflict-${target.logicalId}`, logicalId: target.logicalId, code: 'ReplicaWriteConflict', token: target.token })) });
      });
      await resolveReplica(env.storage, transport(async () => ({ id: 'a', collection: 'users', version: 9n, createdAt: 10n, updatedAt: 11n, value: 'server' })), await decision(env.storage));
      const a = await inspectReplica(env.storage, { logicalId: 'a' });
      expect(a.document!.desired!.wire).toEqual({ version: '9', createdAt: '10', updatedAt: '11' });
      expect(decodeBusinessPayload(a.document!.desired!.payload)).toEqual({ value: 'server' });
      expect(a.issues.map(issue => issue.logicalId)).toEqual(['b']);
      expect(a.targets.map(target => target.logicalId)).toEqual(['b']);
      const b = await inspectReplica(env.storage, { logicalId: 'b' });
      await resolveReplica(env.storage, transport(async () => ({ id: 'b', collection: 'users', version: 10n, createdAt: 10n, updatedAt: 12n, deleted: true })),
        { kind: 'adopt-server', issueId: b.issueId!, logicalId: 'b', physicalEpoch: b.physicalEpoch, editToken: b.document!.desired!.editToken });
      const done = await inspectReplica(env.storage, { logicalId: 'b' });
      expect(done.document!.desired!.existence).toBe('deleted'); expect(done.document!.desired!.payload).toBe('');
      expect(done.issueId).toBeNull(); expect(done.issues).toEqual([]); expect(done.targets).toEqual([]);
    } finally { await env.close(); }
  });

  test('returned metadata errors retain the durable intent and a later replay repairs the baseline', async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const selected = await decision(env.storage);
      const failure: any = { status: 500, documentId: `${await recordKey('d', 'a')}|0` };
      env.setFault(async (rows, context, commit) => {
        if (context !== 'replica-recovery-meta') return commit();
        failure.writeRow = rows[0]; return { error: [failure] };
      });
      await expect(resolveReplica(env.storage, transport(), selected)).rejects.toBe(failure);
      expect((await env.storage.readManifest()).recoveryIntent).not.toBeNull();
      env.setFault(); await replayReplicaRecovery(env.storage); await assertPreserved(env.storage);
    } finally { env.setFault(); await env.close(); }
  });

  test('authority identity failure preserves binding and blocks later reads without constructing absence', async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const selected = await decision(env.storage);
      const failure = Object.assign(new Error('different database'), { code: 'DATABASE_IDENTITY_MISMATCH' });
      await expect(resolveReplica(env.storage, transport(async () => { throw failure; }), selected)).rejects.toBe(failure);
      expect((await env.storage.readManifest()).boundDatabaseId).toBe('D1');
      expect((await env.storage.readManifest()).recoveryIntent).toBeNull();
      await expect(env.storage.guardSourceRead(await env.storage.captureScope())).rejects.toMatchObject({ code: 'ReplicaScopeChanged' });
    } finally { await env.close(); }
  });

  test('recovery cancellation reaches its read and cannot prepare a subsequent intent', async () => {
    const env = await setup(); const abort = new AbortController();
    try {
      await prepare(env.storage); const selected = await decision(env.storage);
      const failure = new Error('closed coordinator');
      await expect(resolveReplica(env.storage, transport(async (_id, context) => {
        abort.abort(failure); expect(context.signal.aborted).toBe(true); return null;
      }), selected, { signal: abort.signal })).rejects.toBe(failure);
      expect((await env.storage.readManifest()).recoveryIntent).toBeNull();
    } finally { await env.close(); }
  });

  test('retry is explicitly authorized and never changes the native baselines or checkpoint', async () => {
    const env = await setup();
    try {
      await prepare(env.storage);
      await expect(resolveReplica(env.storage, transport(), { kind: 'retry-uncertain', issueId: 'phase', acknowledgeRepeatedEffects: false } as any)).rejects.toThrow('authorization');
      await resolveReplica(env.storage, transport(), { kind: 'retry-uncertain', issueId: 'phase', acknowledgeRepeatedEffects: true });
      expect((await env.storage.readManifest()).dirtyUpstream).toBeNull();
      const data = await inspectReplica(env.storage, { logicalId: 'a' });
      expect(decodeBusinessPayload(data.document!.desired!.payload)).toEqual({ value: 'a-pending' });
      expect(decodeBusinessPayload(data.document!.assumed!.payload)).toEqual({ value: 'a-old' });
      await expect(resolveReplica(env.storage, transport(), { kind: 'retry-uncertain', issueId: 'phase', acknowledgeRepeatedEffects: true })).rejects.toMatchObject({ code: 'ReplicaRecoveryStale' });
    } finally { await env.close(); }
  });

  test('reset rejects unrelated new edits, then selects a clean epoch while retaining remote binding', async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const old = await inspectReplica(env.storage);
      await env.storage.set('unrelated', { fresh: true });
      await expect(resolveReplica(env.storage, transport(), { kind: 'reset-alias', issueId: 'phase', stateToken: old.stateToken, discardPending: true })).rejects.toMatchObject({ code: 'ReplicaRecoveryStale' });
      expect(await env.storage.get('unrelated')).toMatchObject({ fresh: true });
      const latest = await inspectReplica(env.storage);
      let injected = false;
      env.setFault(async (rows, _context, commit) => {
        const result = await commit();
        if (!injected && rows.some(row => row.document.maintenance?.stage === 'flipped')) { injected = true; throw new Error('lost reset flip acknowledgement'); }
        return result;
      });
      await resolveReplica(env.storage, transport(), { kind: 'reset-alias', issueId: 'phase', stateToken: latest.stateToken, discardPending: true });
      expect(injected).toBe(true); expect(await env.storage.get('a')).toBeNull(); expect(await env.storage.get('unrelated')).toBeNull();
      const manifest = await env.storage.readManifest();
      expect(manifest.activePhysicalEpoch).not.toBe(latest.physicalEpoch);
      expect(manifest.physicalEpochs).toEqual([manifest.activePhysicalEpoch]);
      expect(manifest.boundDatabaseId).toBe('D1'); expect(manifest.sourceHash).toBe('source1');
      expect(manifest.sourceReady).toBe(false); expect(manifest.dirtyUpstream).toBeNull();
      env.setFault(); await env.reopen(); expect(await env.storage.get('b')).toBeNull();
    } finally { env.setFault(); await env.close(); }
  });

  test('failed reset flip keeps the old interrupted intent, and explicit reset can subsequently discard it', async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const selected = await decision(env.storage);
      const failure = new Error('injected before commit');
      env.setFault(async (_rows, context, commit) => { if (context === 'replica-recovery-meta') throw failure; return commit(); });
      await expect(resolveReplica(env.storage, transport(), selected)).rejects.toBe(failure);
      env.setFault(); const inspected = await inspectReplica(env.storage);
      const choice: RecoveryDecision = { kind: 'reset-alias', issueId: 'phase', stateToken: inspected.stateToken, discardPending: true };
      env.setFault(async (rows, _context, commit) => {
        if (rows.some(row => row.document.maintenance?.stage === 'flipped')) throw failure;
        return commit();
      });
      await expect(resolveReplica(env.storage, transport(), choice)).rejects.toBe(failure);
      const retained = await env.storage.readManifest();
      expect(retained.activePhysicalEpoch).toBe(inspected.physicalEpoch); expect(retained.recoveryIntent).not.toBeNull();
      expect(retained.physicalEpochs).toEqual([inspected.physicalEpoch]); expect(retained.maintenance).toBeNull();
      env.setFault(); await resolveReplica(env.storage, transport(), choice);
      expect((await env.storage.readManifest()).recoveryIntent).toBeNull(); expect(await env.storage.get('b')).toBeNull();
    } finally { env.setFault(); await env.close(); }
  });

  test('unreadable post-commit reset outcome retains both physical stores until startup can select the manifest', async () => {
    const env = await setup();
    try {
      await prepare(env.storage); const inspected = await inspectReplica(env.storage);
      const unreadable = new Error('manifest read unavailable');
      env.setFault(async (rows, _context, commit) => {
        const result = await commit();
        if (rows.some(row => row.document.maintenance?.stage === 'flipped')) {
          env.setReadFailure(unreadable); throw new Error('flip result lost');
        }
        return result;
      });
      await expect(resolveReplica(env.storage, transport(), { kind: 'reset-alias', issueId: 'phase', stateToken: inspected.stateToken, discardPending: true })).rejects.toBe(unreadable);
      if (!env.storage.signal.aborted) await new Promise<void>(resolve => env.storage.signal.addEventListener('abort', () => resolve(), { once: true }));
      expect(env.storage.signal.reason).toBe(unreadable);
      env.setFault(); env.setReadFailure();
      await expect(env.storage.close()).rejects.toBe(unreadable);
      const identity = await createNamespace(env.options.endpoint, env.options.database, env.options.name, env.options.alias, env.options.session.subject);
      const backend = await openAliasBackend({ name: `syntrix-${identity.hash}`, storage: env.options.storage });
      let selected: string;
      try {
        const manifest = (await backend.manifestStorage.findDocumentsById(['manifest'], false))[0];
        expect(manifest.physicalEpochs).toHaveLength(2); expect(manifest.activePhysicalEpoch).not.toBe(inspected.physicalEpoch);
        selected = manifest.activePhysicalEpoch;
        const old = await backend.openPhysical(inspected.physicalEpoch);
        expect(await old.fork.findDocumentsById([await recordKey('d', 'b')], false)).toHaveLength(1);
      } finally { await backend.close(); }
      await env.reopen(unreadable); expect(await env.storage.get('b')).toBeNull();
      expect((await env.storage.readManifest()).physicalEpochs).toEqual([selected]);
    } finally { env.setFault(); env.setReadFailure(); await env.close(); }
  });
});
