import { describe, expect, test } from 'bun:test';
import axios from 'axios';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import type { RxReplicationWriteToMasterRow } from 'rxdb';
import { SyntrixError } from '../../api/errors.js';
import { decodeQueryValue, encodeQueryValue } from '../../api/value.js';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage } from './storage.js';
import { encodeBusinessPayload } from './records.js';
import { createUpstreamAdapter, UpstreamFailure } from './upstream.js';
import { createReplicaHttpUpstream } from './upstream-transport.js';
import type { DataRecord, ReplicaRecord } from './storage-types.js';

const checkpoint = { id: 'end', lwt: 10 };
const signal = () => new AbortController().signal;
const fixture = async () => {
  const token = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'alice' }))}.sig`.replace(/=/g, '');
  const provider = new DefaultTokenProvider({ token });
  const session = await createReplicaSession(provider);
  const storage = await openAliasStorage({ session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'users',
    source: { collection: 'users', filters: [] }, lockManager: createTestLockManager(), storage: getRxStorageDexie({ indexedDB, IDBKeyRange }) });
  const scope = await storage.captureScope();
  await storage.bind(scope, '0123456789abcdef', 'a'.repeat(64));
  await storage.withReplicationAccess(scope, access => access.writeManifest({ ...access.manifest, sourceReady: true, activeSourceGeneration: 'generation' }));
  const requests: { url: string; body: any; expected: unknown }[] = [];
  const handlers: ((body: any) => unknown | Promise<unknown>)[] = [];
  const http = axios.create({ adapter: async config => {
    const body = JSON.parse(config.data);
    requests.push({ url: config.url!, body, expected: config.headers.get('X-Syntrix-Expected-Database-Identity') });
    const handler = handlers.shift(); if (!handler) throw new Error('Unexpected request');
    return { data: JSON.stringify(await handler(body)), status: 200, statusText: 'OK', headers: {}, config };
  } });
  const transport = createReplicaHttpUpstream({ axios: http, provider, database: 'app', collection: 'users' });
  const settled: (string | undefined)[] = [];
  const adapter = createUpstreamAdapter({ storage, scope, transport, onSettlement: async (_cp, _signal, phaseId) => {
    settled.push(phaseId);
    if (phaseId) expect((await storage.readManifest()).dirtyUpstream?.id).toBe(phaseId);
  } });
  const local = async (id: string, value: unknown = 'desired') => {
    await storage.set(id, { value });
    return storage.withReplicationAccess(scope, access => access.withDocument(id, async ({ data }) => ({
      ...Object.fromEntries(Object.entries(data!).filter(([key]) => !key.startsWith('_'))), _deleted: false,
    } as DataRecord & { _deleted: boolean })));
  };
  const begin = async (rows: RxReplicationWriteToMasterRow<ReplicaRecord>[]) => adapter.upstreamPersistence.begin(rows.map(row => row.newDocumentState), checkpoint, signal());
  const execute = async (rows: RxReplicationWriteToMasterRow<ReplicaRecord>[]) => {
    await begin(rows);
    try { const result = await adapter.writeRemote(rows, signal()); await adapter.upstreamPersistence.complete(checkpoint, signal()); return result; }
    catch (error) { await adapter.upstreamPersistence.failed(error); throw error; }
  };
  return { storage, scope, transport, adapter, handlers, requests, settled, local, begin, execute, close: () => storage.close() };
};
const assumed = (desired: DataRecord, value: unknown, version?: string): DataRecord & { _deleted: boolean } => ({ ...desired,
  payload: encodeBusinessPayload({ value }), editToken: null, pin: null, wire: version === undefined ? {} : { version }, _deleted: false });
const remote = (id: string, value: unknown, version: bigint) => encodeQueryValue({ id, collection: 'users', value, version, createdAt: 1n, updatedAt: version });

describe('upstream native phase adapter', () => {
  test('create returns true success and clears only after settlement while retaining frozen targets', async () => {
    const env = await fixture();
    try {
      const desired = await env.local('alice', 9007199254740993n);
      env.handlers.push(async body => {
        const marker = (await env.storage.readManifest()).dirtyUpstream!;
        expect(marker.targets).toEqual([{ logicalId: 'alice', token: desired.editToken! }]); expect(marker.mayHaveDispatched).toBe(true);
        expect(body.changes[0].action).toBe('create');
        expect(decodeQueryValue(body.changes[0].document)).toEqual({ id: 'alice', value: 9007199254740993n });
        await env.storage.set('alice', { value: 'later' });
        return { conflicts: [] };
      });
      expect(await env.execute([{ newDocumentState: desired }])).toEqual([]);
      expect(env.requests[0].expected).toBe('0123456789abcdef'); expect(env.settled).toHaveLength(1);
      expect((await env.storage.readManifest()).dirtyUpstream).toBeNull();
      expect(await env.storage.get('alice')).toMatchObject({ value: 'later' });
    } finally { await env.close(); }
  });

  test('conditional retries refresh only wire version and do not resend accepted siblings', async () => {
    const env = await fixture();
    try {
      const a = await env.local('alice'), b = await env.local('bob');
      env.handlers.push(() => ({ conflicts: [{ changeIndex: 0, id: 'alice', reason: 'version_mismatch', current: remote('alice', 'base', 6n) }] }),
        body => { expect(body.changes).toHaveLength(1); expect(decodeQueryValue(body.changes[0].document)).toMatchObject({ id: 'alice', version: 6n }); return { conflicts: [] }; });
      expect(await env.execute([{ newDocumentState: a, assumedMasterState: assumed(a, 'base', '5') }, { newDocumentState: b }])).toEqual([]);
      expect(env.requests).toHaveLength(2);
    } finally { await env.close(); }
  });

  test('missing version reads the authoritative ID without source filters before conditional delete', async () => {
    const env = await fixture();
    try {
      const live = await env.local('alice');
      const deleted = { ...live, existence: 'deleted' as const, payload: '' };
      env.handlers.push(body => {
        expect(body).toMatchObject({ collection: 'users', limit: 1, showDeleted: true });
        expect(body.filters).toHaveLength(1); expect(body.filters[0].field).toBe('id');
        return { documents: [remote('alice', 'base', 8n)], nextCursor: null, effectiveOrder: [{ field: 'id', direction: 'asc' }] };
      }, body => {
        expect(body.changes[0].action).toBe('delete'); expect(decodeQueryValue(body.changes[0].document)).toEqual({ id: 'alice', version: 8n });
        return { conflicts: [] };
      });
      expect(await env.execute([{ newDocumentState: deleted, assumedMasterState: assumed(live, 'base') }])).toEqual([]);
      expect(env.requests.every(request => request.expected === '0123456789abcdef')).toBe(true);
    } finally { await env.close(); }
  });

  test('three pure version conflicts safely retry only after the complete phase is known unexecuted', async () => {
    const env = await fixture();
    try {
      const desired = await env.local('alice');
      for (const version of [2n, 3n, 4n]) env.handlers.push(() => ({ conflicts: [{ changeIndex: 0, id: 'alice', reason: 'version_mismatch', current: remote('alice', 'base', version) }] }));
      await expect(env.execute([{ newDocumentState: desired, assumedMasterState: assumed(desired, 'base', '1') }])).rejects.toMatchObject({ code: 'ReplicaWriteContention' });
      expect(env.requests).toHaveLength(3); expect(env.adapter.outcome?.kind).toBe('retry');
      expect((await env.storage.readManifest()).dirtyUpstream).toBeNull();
    } finally { await env.close(); }
  });

  test('a business conflict before an accepted response index retains the whole phase as uncertain', async () => {
    const env = await fixture();
    try {
      const a = await env.local('alice'), b = await env.local('bob');
      env.handlers.push(() => ({ conflicts: [{ changeIndex: 0, id: 'alice', reason: 'already_exists', current: remote('alice', 'other', 1n) }] }));
      await expect(env.execute([{ newDocumentState: a }, { newDocumentState: b }])).rejects.toMatchObject({ code: 'ReplicaWriteConflict' });
      const manifest = await env.storage.readManifest();
      expect(manifest.dirtyUpstream?.targets).toHaveLength(2); expect(manifest.issues[0]).toMatchObject({ logicalId: 'alice', token: a.editToken });
      expect(env.adapter.outcome?.kind).toBe('uncertain');
    } finally { await env.close(); }
  });

  test('an unknown network result keeps the phase, while proven rate rejection can clear and retry', async () => {
    for (const rateLimited of [false, true]) {
      const env = await fixture();
      try {
        const desired = await env.local('alice');
        const cause = rateLimited ? new SyntrixError('RATE_LIMITED', 'busy', 429, undefined, 2) : new Error('connection lost');
        env.handlers.push(() => { throw cause; });
        await expect(env.execute([{ newDocumentState: desired }])).rejects.toBeInstanceOf(UpstreamFailure);
        expect(env.adapter.outcome?.kind).toBe(rateLimited ? 'retry' : 'uncertain');
        expect(Boolean((await env.storage.readManifest()).dirtyUpstream)).toBe(!rateLimited);
        if (rateLimited) expect(env.adapter.outcome?.retryAfterMs).toBe(2000);
      } finally { await env.close(); }
    }
  });

  test('no-op phases settle without creating a marker or sending a request', async () => {
    const env = await fixture();
    try {
      await env.adapter.upstreamPersistence.begin([], checkpoint, signal());
      await env.adapter.upstreamPersistence.complete(checkpoint, signal());
      expect(env.requests).toEqual([]); expect(env.settled).toEqual([undefined]);
      expect((await env.storage.readManifest()).dirtyUpstream).toBeNull();
    } finally { await env.close(); }
  });

  test('marker persistence failure propagates the original IO error without dispatch', async () => {
    const env = await fixture(); const original = env.storage.withReplicationAccess;
    try {
      const desired = await env.local('alice'), cause = new Error('disk failed');
      env.storage.withReplicationAccess = (scope, consume) => original(scope, access => consume(new Proxy(access, { get(target, key) {
        if (key === 'writeManifest') return async () => { throw cause; };
        return Reflect.get(target, key, target);
      } })));
      await expect(env.execute([{ newDocumentState: desired }])).rejects.toBe(cause);
      expect(env.requests).toEqual([]); expect(env.adapter.outcome?.kind).toBe('blocked');
    } finally { env.storage.withReplicationAccess = original; await env.close(); }
  });

  test('an accepted sibling makes a later proven rejection uncertain for the entire phase', async () => {
    const env = await fixture();
    try {
      const a = await env.local('alice'), b = await env.local('bob');
      const rows = [{ newDocumentState: a }, { newDocumentState: b }];
      await env.begin(rows);
      env.handlers.push(() => ({ conflicts: [] }), () => { throw new SyntrixError('RATE_LIMITED', 'busy', 429); });
      expect(await env.adapter.writeRemote([rows[0]], signal())).toEqual([]);
      let failure: unknown;
      try { await env.adapter.writeRemote([rows[1]], signal()); } catch (error) { failure = error; }
      expect(failure).toBeInstanceOf(UpstreamFailure);
      await env.adapter.upstreamPersistence.failed(failure);
      expect(env.adapter.outcome?.kind).toBe('uncertain');
      expect((await env.storage.readManifest()).dirtyUpstream?.targets).toHaveLength(2);
    } finally { await env.close(); }
  });

  test('a state already matching desired remains uncertain when native metadata persistence fails', async () => {
    const env = await fixture();
    try {
      const desired = await env.local('alice');
      const rows = [{ newDocumentState: desired }]; await env.begin(rows);
      env.handlers.push(() => ({ conflicts: [{ changeIndex: 0, id: 'alice', reason: 'already_exists', current: remote('alice', 'desired', 8n) }] }));
      expect(await env.adapter.writeRemote(rows, signal())).toEqual([]);
      const error = new Error('metadata write failed'); await env.adapter.upstreamPersistence.failed(error);
      expect(env.adapter.outcome).toMatchObject({ kind: 'uncertain', error });
      expect((await env.storage.readManifest()).dirtyUpstream).not.toBeNull();
    } finally { await env.close(); }
  });

  test('wire serialization latches a failed request before an already queued sibling can dispatch', async () => {
    const env = await fixture();
    try {
      const a = await env.local('alice'), b = await env.local('bob');
      await env.begin([{ newDocumentState: a }, { newDocumentState: b }]);
      let release!: () => void;
      const gate = new Promise<void>(resolve => { release = resolve; });
      env.handlers.push(async () => { await gate; throw new Error('connection lost'); });
      const first = env.adapter.writeRemote([{ newDocumentState: a }], signal());
      const second = env.adapter.writeRemote([{ newDocumentState: b }], signal());
      release();
      const results = await Promise.allSettled([first, second]);
      expect(results.map(result => result.status)).toEqual(['rejected', 'rejected']);
      expect(env.requests).toHaveLength(1);
      if (results[0].status !== 'rejected') throw new Error('Expected rejection');
      await env.adapter.upstreamPersistence.failed(results[0].reason);
      expect(env.adapter.outcome?.kind).toBe('uncertain');
    } finally { await env.close(); }
  });
});
