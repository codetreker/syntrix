import { describe, expect, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { defaultHashSha256, getHeightOfRevision, type RxStorage } from 'rxdb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { EMPTY } from 'rxjs';
import { DefaultTokenProvider } from '../auth/provider.js';
import { openAliasBackend, ReadBudget } from './backend.js';
import { createNamespace } from './identity.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { encodeBusinessPayload, recordKey } from './records.js';
import { removeAliasStorage } from './removal.js';
import { createReplicaSession } from './session.js';
import { openAliasStorage, type AliasStorage } from './storage.js';
import type { DataRecord } from './storage-types.js';

type WriteFault = (rows: any[], context: string, commit: () => Promise<any>) => Promise<any>;
const setup = async (missManifestFeed = false) => {
  let writeFault: WriteFault | undefined;
  let removeFault: ((name: string) => void) | undefined;
  let readFailure: Error | undefined;
  const removed: string[] = [];
  const original = getRxStorageDexie({ indexedDB, IDBKeyRange });
  const storage: RxStorage<any, any> = { ...original, createStorageInstance: async params => {
    const instance = await original.createStorageInstance(params);
    return new Proxy(instance, { get(target, property) {
      if (property === 'bulkWrite') return (rows: any[], context: string) => writeFault
        ? writeFault(rows, context, () => target.bulkWrite(rows, context)) : target.bulkWrite(rows, context);
      if (property === 'remove') return async () => { removeFault?.(params.collectionName); await target.remove(); removed.push(params.collectionName); };
      if (property === 'findDocumentsById' && params.collectionName === 'manifest') return (...args: Parameters<typeof target.findDocumentsById>) => {
        if (readFailure) return Promise.reject(readFailure);
        return target.findDocumentsById(...args);
      };
      if (property === 'changeStream' && params.collectionName === 'manifest' && missManifestFeed) return () => EMPTY;
      const value = Reflect.get(target, property, target); return typeof value === 'function' ? value.bind(target) : value;
    } });
  } };
  const token = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'alice', exp: 0 }))}.sig`.replace(/=/g, '');
  const session = await createReplicaSession(new DefaultTokenProvider({ token }));
  const options = { session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'people',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage };
  const handles: AliasStorage[] = [];
  const open = async (source = options.source) => { const alias = await openAliasStorage({ ...options, source }); handles.push(alias); return alias; };
  const alias = await open();
  const { source: _source, ...removalOptions } = options;
  const inspect = async <T>(consume: (backend: Awaited<ReturnType<typeof openAliasBackend>>) => Promise<T>) => {
    const identity = await createNamespace(options.endpoint, options.database, options.name, options.alias, session.subject);
    const backend = await openAliasBackend({ name: `syntrix-${identity.hash}`, storage });
    try { return await consume(backend); } finally { await backend.close(); }
  };
  return { alias, options, removalOptions, open, inspect, removed,
    fault: (value?: WriteFault) => { writeFault = value; }, removeFault: (value?: (name: string) => void) => { removeFault = value; },
    readFailure: (value?: Error) => { readFailure = value; },
    close: async () => { writeFault = undefined; removeFault = undefined; readFailure = undefined; for (const handle of handles) await handle.close(); await session.close(); } };
};

const cleanRow = async (alias: AliasStorage, provenanceOnly = false) => alias.withMaintenance(async access => {
  const physical = await access.backend.openPhysical(access.manifest.activePhysicalEpoch);
  const row: DataRecord = { key: await recordKey('d', 'a'), kind: 'd', logicalId: 'a', existence: 'live',
    payload: encodeBusinessPayload({ value: 'retained' }), editToken: null, pin: null, wire: { version: '1' } };
  const saved = await access.backend.writeRecord(physical, row, undefined, 'fixture');
  if (provenanceOnly) {
    const revision = `${getHeightOfRevision(saved._rev) + 1}-source`;
    expect((await physical.fork.bulkWrite([{ previous: saved, document: { ...saved, _rev: revision,
      _meta: { ...saved._meta, o: { hash: await defaultHashSha256(physical.identifier), _rev: getHeightOfRevision(revision) } } } }], 'fixture')).error).toEqual([]);
  } else expect((await physical.meta.bulkWrite([{ document: { id: `${row.key}|0`, itemId: row.key, isCheckpoint: '0',
    docData: { ...row, _deleted: false }, _deleted: false, _attachments: {}, _meta: { lwt: Date.now() }, _rev: '1-fixture' } }], 'fixture')).error).toEqual([]);
  return { row, epoch: physical.epoch };
});

describe('explicit alias removal', () => {
  test('caller cancellation releases a removal waiting for a foreign exclusive lock without changing the alias', async () => {
    const env = await setup(); const abort = new AbortController();
    let entered!: () => void, release!: () => void;
    const acquired = new Promise<void>(resolve => { entered = resolve; });
    const barrier = new Promise<void>(resolve => { release = resolve; });
    let holder: Promise<void> | undefined;
    try {
      const identity = await createNamespace(env.options.endpoint, env.options.database, env.options.name, env.options.alias, env.options.session.subject);
      holder = env.options.lockManager.request(`syntrix:${identity.hash}:alias`, { mode: 'exclusive' }, async () => { entered(); await barrier; }).then(() => undefined);
      await acquired;
      const pending = removeAliasStorage({ ...env.removalOptions, signal: abort.signal });
      const reason = new Error('replica database closed'); abort.abort(reason);
      await expect(pending).rejects.toBe(reason);
      expect(env.removed).toEqual([]);
      release(); await holder;
      expect((await env.alias.readManifest()).state).toBe('ready');
    } finally { release?.(); await holder; await env.close(); }
  });

  test('caller cancellation after terminal commit finishes cleanup and retains its exact cancellation reason', async () => {
    const env = await setup(); const abort = new AbortController();
    try {
      const { epoch } = await cleanRow(env.alias); await env.alias.close();
      const reason = new Error('replica database closed');
      env.fault(async (_rows, context, commit) => {
        const result = await commit(); if (context === 'replica-remove') abort.abort(reason); return result;
      });
      await expect(removeAliasStorage({ ...env.removalOptions, signal: abort.signal })).rejects.toBe(reason);
      expect(env.removed).toEqual(expect.arrayContaining([`records_${epoch}`, `meta_${epoch}`]));
      await env.inspect(async backend => expect((await backend.manifestStorage.findDocumentsById(['manifest'], false))[0].state).toBe('removed'));
    } finally { await env.close(); }
  });

  test('empty unbound aliases and missing aliases are removable; recreation has a fresh lifetime and definition', async () => {
    const env = await setup();
    try {
      const before = await env.alias.readManifest(); await env.alias.close();
      await removeAliasStorage(env.removalOptions); await removeAliasStorage(env.removalOptions);
      await removeAliasStorage({ ...env.removalOptions, alias: 'never-opened' });
      const reopened = await env.open({ collection: 'projects', filters: [] });
      const after = await reopened.readManifest();
      expect(after.lifecycleId).not.toBe(before.lifecycleId); expect(after.activePhysicalEpoch).not.toBe(before.activePhysicalEpoch);
      expect(after.definition.collection).toBe('projects'); expect(after.boundDatabaseId).toBeNull();
      expect(reopened.namespace).not.toBe(env.alias.namespace);
      expect(env.removed).toContain(`records_${before.activePhysicalEpoch}`);
      expect(env.removed).toContain(`meta_${before.activePhysicalEpoch}`);
    } finally { await env.close(); }
  });

  test('historical alias removal needs no source configuration and reclaims fork plus native metadata', async () => {
    const env = await setup();
    try {
      const { epoch } = await cleanRow(env.alias); await env.alias.close();
      expect('source' in env.removalOptions).toBe(false);
      await removeAliasStorage(env.removalOptions);
      expect(env.removed).toEqual(expect.arrayContaining([`records_${epoch}`, `meta_${epoch}`]));
      await env.inspect(async backend => expect((await backend.manifestStorage.findDocumentsById(['manifest'], false))[0].state).toBe('removed'));
    } finally { await env.close(); }
  });

  for (const protectedWork of ['pin', 'difference', 'dirty', 'issue', 'intent'] as const) test(`rejects ${protectedWork} and retains durable data`, async () => {
    const env = await setup();
    try {
      const { row, epoch } = await cleanRow(env.alias);
      await env.alias.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(epoch);
        if (protectedWork === 'pin' || protectedWork === 'difference') {
          const previous = (await physical.fork.findDocumentsById([row.key], false))[0];
          await access.backend.writeRecord(physical, { ...row, payload: encodeBusinessPayload({ value: 'pending' }), editToken: 'edit',
            pin: protectedWork === 'pin' ? { token: 'edit', stage: 'await-settlement' } : null }, previous, 'fixture');
        } else await access.writeManifest({ ...access.manifest,
          ...(protectedWork === 'dirty' ? { dirtyUpstream: { id: 'phase', session: 0, physicalEpoch: epoch, mayHaveDispatched: true, targets: [{ logicalId: 'a', token: 'edit' }] } } : {}),
          ...(protectedWork === 'issue' ? { issues: [{ id: 'issue', logicalId: 'a', code: 'conflict', token: null }] } : {}),
          ...(protectedWork === 'intent' ? { recoveryIntent: { id: 'intent', issueId: 'issue', phaseId: null, physicalEpoch: epoch,
            action: 'adopt' as const, logicalId: 'a', protectedToken: null, resultToken: 'result', current: row, desired: { ...row, editToken: 'result' } } } : {}),
        });
      });
      await env.alias.close();
      await expect(removeAliasStorage(env.removalOptions)).rejects.toMatchObject({ code: 'ReplicaRemovalBlocked' });
      expect(env.removed).toEqual([]);
      await env.inspect(async backend => {
        expect((await backend.manifestStorage.findDocumentsById(['manifest'], false))[0].state).toBe('ready');
        const physical = await backend.openPhysical(epoch);
        expect(((await physical.fork.findDocumentsById([row.key], false))[0] as DataRecord).payload).toBe(encodeBusinessPayload({ value: protectedWork === 'pin' || protectedWork === 'difference' ? 'pending' : 'retained' }));
      });
    } finally { await env.close(); }
  });

  test('exact source-origin revision is removable before native assumed metadata exists', async () => {
    const env = await setup();
    try {
      const { epoch } = await cleanRow(env.alias, true); await env.alias.close();
      await removeAliasStorage(env.removalOptions);
      expect(env.removed).toContain(`records_${epoch}`);
    } finally { await env.close(); }
  });

  test('a later unpinned edit cannot reuse stale source-origin provenance to pass removal', async () => {
    const env = await setup();
    try {
      const { row, epoch } = await cleanRow(env.alias, true);
      await env.alias.withMaintenance(async access => {
        const physical = await access.backend.openPhysical(epoch);
        const previous = (await physical.fork.findDocumentsById([row.key], false))[0];
        if (previous.kind !== 'd') throw new Error('Expected data fixture');
        expect((await physical.fork.bulkWrite([{ previous, document: { ...previous,
          payload: encodeBusinessPayload({ value: 'unpinned-local' }), _rev: `${getHeightOfRevision(previous._rev) + 1}-local`,
        } }], 'fixture')).error).toEqual([]);
      });
      await env.alias.close();
      await expect(removeAliasStorage(env.removalOptions)).rejects.toMatchObject({ code: 'ReplicaRemovalBlocked' });
      expect(env.removed).toEqual([]);
    } finally { await env.close(); }
  });

  test('an edit admitted ahead of exclusive removal is included in the cleanliness check', async () => {
    const env = await setup();
    try {
      await cleanRow(env.alias);
      let signalEntered!: () => void, release!: () => void;
      const entered = new Promise<void>(resolve => { signalEntered = resolve; });
      const barrier = new Promise<void>(resolve => { release = resolve; });
      env.fault(async (rows, context, commit) => {
        if (context === 'local-edit') { signalEntered(); await barrier; }
        return commit();
      });
      const edit = env.alias.set('a', { value: 'raced' }); await Promise.race([entered, edit.then(() => { throw new Error('Edit did not enter fault'); })]);
      const removal = removeAliasStorage(env.removalOptions);
      const rejected = removal.then(() => undefined, error => error);
      release(); await edit; expect(await rejected).toMatchObject({ code: 'ReplicaRemovalBlocked' });
      expect(await env.alias.get('a')).toMatchObject({ value: 'raced' }); expect(env.removed).toEqual([]);
    } finally { await env.close(); }
  });

  for (const committed of [false, true]) test(`${committed ? 'lost acknowledgment' : 'precommit failure'} at terminal manifest write uses durable outcome`, async () => {
    const env = await setup();
    try {
      const { epoch, row } = await cleanRow(env.alias); await env.alias.close();
      const failure = new Error(committed ? 'lost acknowledgment' : 'quota failure');
      env.fault(async (rows, context, commit) => {
        if (context === 'replica-remove') { if (committed) await commit(); throw failure; }
        return commit();
      });
      if (committed) { await removeAliasStorage(env.removalOptions); expect(env.removed).toContain(`records_${epoch}`); }
      else {
        await expect(removeAliasStorage(env.removalOptions)).rejects.toBe(failure); expect(env.removed).toEqual([]);
        await env.inspect(async backend => {
          expect((await backend.manifestStorage.findDocumentsById(['manifest'], false))[0].state).toBe('ready');
          expect(((await (await backend.openPhysical(epoch)).fork.findDocumentsById([row.key], false))[0] as DataRecord).payload).toBe(row.payload);
        });
      }
    } finally { await env.close(); }
  });

  for (const recovery of ['retry', 'reopen'] as const) test(`physical cleanup failure preserves the lifetime fence and ${recovery} finishes cleanup`, async () => {
    const env = await setup();
    try {
      const { epoch } = await cleanRow(env.alias); const lifetime = env.alias.lifecycleId; await env.alias.close();
      env.removeFault(name => { if (name === `records_${epoch}`) throw new Error('remove storage failure'); });
      await expect(removeAliasStorage(env.removalOptions)).rejects.toThrow();
      await env.inspect(async backend => expect((await backend.manifestStorage.findDocumentsById(['manifest'], false))[0]).toMatchObject({ state: 'removed', lifecycleId: lifetime }));
      env.removeFault();
      if (recovery === 'retry') await removeAliasStorage(env.removalOptions);
      const next = await env.open(); expect(next.lifecycleId).not.toBe(lifetime);
      expect(env.removed).toContain(`records_${epoch}`); expect(env.removed).toContain(`meta_${epoch}`);
      expect(await next.get('a')).toBeNull();
    } finally { await env.close(); }
  });

  test('an unreadable terminal lost acknowledgment retains both physical stores until removal can be proved', async () => {
    const env = await setup();
    try {
      const { epoch } = await cleanRow(env.alias); await env.alias.close();
      const readFailure = new Error('manifest unavailable');
      env.fault(async (_rows, context, commit) => {
        if (context === 'replica-remove') { await commit(); env.readFailure(readFailure); throw new Error('lost acknowledgment'); }
        return commit();
      });
      await expect(removeAliasStorage(env.removalOptions)).rejects.toBe(readFailure);
      expect(env.removed).toEqual([]);
      env.readFailure(); env.fault();
      await env.inspect(async backend => expect((await backend.manifestStorage.findDocumentsById(['manifest'], false))[0].state).toBe('removed'));
      await removeAliasStorage(env.removalOptions);
      expect(env.removed).toEqual(expect.arrayContaining([`records_${epoch}`, `meta_${epoch}`]));
    } finally { await env.close(); }
  });

  for (const operation of ['write', 'query', 'native'] as const) test(`a missed manifest feed cannot let an old ${operation} handle follow recreation`, async () => {
    const env = await setup(true);
    try {
      const query = env.alias.queryAccess(new ReadBudget(64 * 1024 * 1024));
      const native = await env.alias.native(await env.alias.captureScope());
      await removeAliasStorage(env.removalOptions);
      const next = await env.open(); expect(next.lifecycleId).not.toBe(env.alias.lifecycleId);
      const attempt = operation === 'write' ? env.alias.set('a', { stale: true })
        : operation === 'query' ? query.view() : native.fork.findDocumentsById([await recordKey('d', 'a')], false);
      await expect(attempt).rejects.toMatchObject({ code: 'ReplicaRemoved' });
      expect(await next.get('a')).toBeNull();
    } finally { await env.close(); }
  });
});
