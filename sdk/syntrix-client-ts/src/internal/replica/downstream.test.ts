import { describe, expect, test } from 'bun:test';
import { indexedDB, IDBKeyRange } from 'fake-indexeddb';
import { getRxStorageDexie } from 'rxdb/plugins/storage-dexie';
import { defaultHashSha256 } from 'rxdb';
import { DefaultTokenProvider } from '../auth/provider.js';
import { createReplicaSession } from './session.js';
import { createTestLockManager } from './lock-manager.test-fixture.js';
import { openAliasStorage } from './storage.js';
import { businessEqual, freezeSourceDefinition } from './records.js';
import { createDownstreamAdapter } from './downstream.js';
import { createReplicationRuntime, type ReplicationRuntime } from './runtime.js';
import { decodeSourceResponse } from './source.js';
import { encodeQueryValue } from '../../api/value.js';
import type { ReplicaSourceAdapter, SourceDocument, SourceEventsPage, SourceWindow } from './source-types.js';
import type { ReplicaRecord, StorageLimits } from './storage-types.js';

const document = (id: string, value: unknown, version = 1n): SourceDocument => ({ id, collection: 'users', version, createdAt: 1n, updatedAt: version, value } as SourceDocument);
const page = (events: SourceEventsPage['events'], overrides: Partial<SourceEventsPage> = {}): SourceEventsPage => ({
  protocolVersion: 1, mode: 'events', databaseIdentity: 'database-id', sourceHash: 'hash', events,
  checkpoint: crypto.randomUUID(), generationId: 'server-generation', phase: 'live', caughtUp: true, bootstrapComplete: true, ...overrides,
});
const fixture = async (mode: 'events' | 'replace' = 'events', limits?: Partial<StorageLimits>) => {
  const token = `${btoa('{}')}.${btoa(JSON.stringify({ sub: 'alice' }))}.sig`.replace(/=/g, '');
  const session = await createReplicaSession(new DefaultTokenProvider({ token }));
  const definition = { collection: 'users', filters: [], ...(mode === 'replace' ? { limit: 1000 } : {}) };
  const storage = await openAliasStorage({ session, endpoint: 'https://example.test', database: 'app', name: crypto.randomUUID(), alias: 'users',
    source: definition, limits, lockManager: createTestLockManager(), storage: getRxStorageDexie({ indexedDB, IDBKeyRange }) });
  const scope = await storage.captureScope();
  const native = await storage.native(scope);
  const requests: (string | null)[] = [];
  const pages: (SourceEventsPage | SourceWindow)[] = [];
  const source: ReplicaSourceAdapter = { definition: freezeSourceDefinition(definition), mode, read: async context => {
    requests.push(context.checkpoint);
    const next = pages.shift(); if (!next) throw new Error('Unexpected source read'); return next;
  } };
  let refreshes = 0;
  const adapter = createDownstreamAdapter({ storage, scope, source, requestRefresh: () => { refreshes++; } });
  const pushed: ReplicaRecord[] = [];
  const activations: { final: boolean; complete: boolean; before: string | null; after: string | null }[] = [];
  let runtime: ReplicationRuntime | undefined;
  const start = (upstreamEnabled = false) => runtime = createReplicationRuntime({
    identifier: native.identifier, forkInstance: native.fork, metaInstance: native.meta, ownerSignal: native.ownerSignal,
    conflictHandler: { isEqual: businessEqual, resolve: async conflict => conflict.newDocumentState }, hashFunction: defaultHashSha256,
    upstreamEnabled, readSource: adapter.readSource, onCheckpoint: async (cp, status, signal) => {
      const before = (await storage.readManifest()).activeSourceGeneration;
      await adapter.onCheckpoint(cp, status, signal);
      activations.push({ final: cp.final, complete: cp.complete, before, after: (await storage.readManifest()).activeSourceGeneration });
    }, onUpCheckpoint: adapter.onUpCheckpoint,
    isControlDocument: row => row.kind !== 'd', writeRemote: async rows => { pushed.push(...rows.map(row => row.newDocumentState)); return []; },
  });
  const state = (id: string) => storage.withReplicationAccess(scope, access => access.withDocument(id, async value => structuredClone(value)));
  const close = async () => { await Promise.allSettled(runtime ? [runtime.close()] : []); await storage.close(); };
  return { storage, scope, native, source, adapter, requests, pages, start, state, close, pushed, activations, stop: () => runtime!.close(), get refreshes() { return refreshes; } };
};

describe('durable source projection', () => {
  test('only the live owned upstream phase permits source progress and activation', async () => {
    const env = await fixture();
    let runtime: ReplicationRuntime | undefined;
    try {
      const marker = { id: 'owned-phase', session: env.scope.sessionVersion, physicalEpoch: env.scope.physicalEpoch, mayHaveDispatched: true, targets: [] };
      await env.storage.withReplicationAccess(env.scope, async access => { await access.writeManifest({ ...access.manifest, dirtyUpstream: marker }); });
      env.pages.push(page([{ type: 'upsert', document: document('alice', 'remote') }]));
      await expect(env.adapter.readSource(undefined, 201, new AbortController().signal)).rejects.toMatchObject({ code: 'ReplicaRecoveryRequired' });
      expect(env.requests).toHaveLength(0);
      const adapter = createDownstreamAdapter({ storage: env.storage, scope: env.scope, source: env.source, requestRefresh() {},
        ownsPhase: current => current.id === marker.id && current.session === marker.session && current.physicalEpoch === marker.physicalEpoch });
      runtime = createReplicationRuntime({ identifier: env.native.identifier, forkInstance: env.native.fork, metaInstance: env.native.meta,
        ownerSignal: env.native.ownerSignal, upstreamEnabled: false, hashFunction: defaultHashSha256,
        conflictHandler: { isEqual: businessEqual, resolve: async conflict => conflict.newDocumentState },
        isControlDocument: row => row.kind !== 'd', readSource: adapter.readSource, onCheckpoint: adapter.onCheckpoint });
      await runtime.waitForIdle();
      expect((await env.storage.readManifest()).sourceReady).toBe(true);
      expect(await env.storage.get('alice')).toMatchObject({ value: 'remote' });
      await env.storage.withReplicationAccess(env.scope, async access => { await access.writeManifest({ ...access.manifest, dirtyUpstream: { ...marker, id: 'foreign-phase' } }); });
      await adapter.beginRound();
      await expect(adapter.readSource(undefined, 201, new AbortController().signal)).rejects.toMatchObject({ code: 'ReplicaRecoveryRequired' });
      expect(env.requests).toHaveLength(1);
    } finally { await runtime?.close(); await env.close(); }
  });
  for (const kind of ['upsert', 'leave', 'delete', 'control'] as const) {
    test(`preflights ${kind} storage budgets before binding or staging a valid decoded response`, async () => {
      const env = await fixture('events', { maxRecordBytes: kind === 'control' ? 512 : 3000 });
      try {
        const id = 'a'.repeat(2000);
        if (kind === 'leave' || kind === 'delete') await env.storage.set(id, { value: 'local' });
        const before = await env.storage.readManifest();
        const events = kind === 'control' ? [] : kind === 'upsert'
          ? [{ type: 'upsert', document: encodeQueryValue(document('alice', 'x'.repeat(2500))) }]
          : [{ type: kind, id }];
        const response = JSON.stringify({ ...page([]), databaseIdentity: '0123456789abcdef', sourceHash: 'a'.repeat(64),
          generationId: '01234567-89ab-cdef-0123-456789abcdef', events });
        env.source.read = async context => decodeSourceResponse(response, env.source.definition, context);
        await expect(env.adapter.readSource(undefined, 201, new AbortController().signal)).rejects.toMatchObject({ code: 'ReplicaRecordTooLarge' });
        expect(await env.storage.readManifest()).toEqual(before);
        expect(await env.storage.withReplicationAccess(env.scope, access => access.withDownCheckpoint(async cp => cp))).toBeUndefined();
        if (kind === 'leave' || kind === 'delete') expect(await env.storage.get(id)).toMatchObject({ value: 'local' });
      } finally { await env.close(); }
    });
  }

  test('native Dexie applies ordered same-ID events, metadata, unknown progress and activation', async () => {
    const env = await fixture();
    try {
      env.pages.push(page([
        { type: 'upsert', document: document('alice', 9007199254740993n, 9n) },
        { type: 'delete', id: 'alice' },
        { type: 'upsert', document: document('alice', 'recreated', 2n) },
        { type: 'leave', id: 'never-known' }, { type: 'delete', id: 'also-unknown' },
      ]));
      const runtime = env.start(); await runtime.waitForIdle();
      expect(env.adapter.complete).toBe(true);
      expect(await env.storage.get('alice')).toMatchObject({ value: 'recreated', version: 2n });
      expect((await env.state('never-known')).member).toBeUndefined();
      expect((await env.state('also-unknown')).data).toBeUndefined();
      expect(env.requests).toHaveLength(1);
      expect((await env.storage.readManifest()).sourceReady).toBe(true);
      expect(env.pushed).toEqual([]);
    } finally { await env.close(); }
  });

  test('pending business writes survive leave and source delete while independent members advance', async () => {
    const env = await fixture();
    try {
      await env.storage.set('alice', { value: 'offline' });
      env.pages.push(page([{ type: 'upsert', document: document('alice', 'server') }, { type: 'leave', id: 'alice' }]));
      const runtime = env.start(); await runtime.waitForIdle();
      expect(await env.storage.get('alice')).toMatchObject({ value: 'offline', version: 1n });
      const first = await env.state('alice');
      expect(first.data?.pin?.stage).toBe('await-settlement');
      expect(first.member?.slots[0].member).toBe(false);
      env.pages.push(page([{ type: 'delete', id: 'alice' }]));
      await env.adapter.beginRound(); runtime.requestResync(); await runtime.waitForIdle();
      expect(await env.storage.get('alice')).toMatchObject({ value: 'offline' });
      expect((await env.state('alice')).member?.observedExistence).toBe('deleted');
      expect(env.requests[1]).not.toBeNull();
    } finally { await env.close(); }
  });

  test('business tombstones preserve observed metadata and do not invent delete versions', async () => {
    const env = await fixture();
    try {
      env.pages.push(page([{ type: 'upsert', document: document('alice', true, 33n) }]));
      const runtime = env.start(); await runtime.waitForIdle();
      env.pages.push(page([{ type: 'delete', id: 'alice' }]));
      await env.adapter.beginRound(); runtime.requestResync(); await runtime.waitForIdle();
      const row = await env.state('alice');
      expect(row.data?.existence).toBe('deleted'); expect(row.data?.payload).toBe('');
      expect(row.member?.metadata.version).toBe('33');
      expect(row.member?.slots[0].member).toBe(false);
      expect(env.pushed).toEqual([]);
    } finally { await env.close(); }
  });

  test('a different server generation cannot advance an existing cursor or mutate the manifest', async () => {
    const env = await fixture();
    try {
      env.pages.push(page([{ type: 'upsert', document: document('alice', 'stable') }]));
      const runtime = env.start(); await runtime.waitForIdle();
      const manifest = await env.storage.readManifest();
      const durable = await env.storage.withReplicationAccess(env.scope, access => access.withDownCheckpoint(async saved => structuredClone(saved)));
      env.pages.push(page([{ type: 'upsert', document: document('alice', 'wrong') }], { generationId: 'different-generation' }));
      await env.adapter.beginRound(); runtime.requestResync();
      await expect(runtime.waitForIdle()).rejects.toThrow('generation changed');
      expect(await env.storage.readManifest()).toEqual(manifest);
      expect(await env.storage.withReplicationAccess(env.scope, access => access.withDownCheckpoint(async saved => structuredClone(saved)))).toEqual(durable);
      expect(await env.storage.get('alice')).toMatchObject({ value: 'stable' });
    } finally { await env.close(); }
  });

  test('window chunks preserve the old active generation until the final native checkpoint', async () => {
    const env = await fixture('replace');
    try {
      const window = (documents: SourceDocument[]): SourceWindow => ({ protocolVersion: 1, mode: 'replace', databaseIdentity: 'database-id', sourceHash: 'hash',
        requestId: crypto.randomUUID(), generationId: crypto.randomUUID(), complete: true, effectiveOrder: [], documents });
      env.pages.push(window([document('old', 1)]));
      const runtime = env.start(); await runtime.waitForIdle();
      const old = (await env.storage.readManifest()).activeSourceGeneration;
      env.pages.push(window(Array.from({ length: 240 }, (_, i) => document(`new-${i}`, i))));
      await env.adapter.beginRound(); runtime.requestResync(); await runtime.waitForIdle();
      // Native full batches continue immediately; the final short batch activates.
      expect(env.adapter.complete).toBe(true);
      expect(env.requests).toHaveLength(2);
      expect(env.activations.slice(1, -1)).toEqual([
        { final: false, complete: false, before: old, after: old },
        { final: false, complete: false, before: old, after: old },
      ]);
      expect(env.activations[env.activations.length - 1]).toMatchObject({ final: true, complete: true, before: old });
      expect(env.activations[env.activations.length - 1]?.after).not.toBe(old);
      expect((await env.storage.readManifest()).activeSourceGeneration).not.toBe(old);
      expect(await env.storage.get('old')).toBeNull();
      expect(await env.storage.get('new-239')).toMatchObject({ value: 239 });
      expect((await env.state('old')).member?.slots).toHaveLength(1);
      env.pages.push(window([])); await env.adapter.beginRound(); runtime.requestResync(); await runtime.waitForIdle();
      expect(await env.storage.get('new-239')).toBeNull();
      expect(env.adapter.complete).toBe(true);
    } finally { await env.close(); }
  }, 20000);

  test('short nonterminal empty pages durably advance cursor before a coalesced continuation', async () => {
    const env = await fixture();
    try {
      env.pages.push(page([], { checkpoint: 'filtered-prefix', caughtUp: false, bootstrapComplete: false }));
      env.pages.push(page([], { checkpoint: 'head' }));
      const runtime = env.start(); await runtime.waitForIdle();
      expect(env.refreshes).toBe(0);
      expect(env.requests).toEqual([null, 'filtered-prefix']);
      expect(env.adapter.complete).toBe(true);
    } finally { await env.close(); }
  });

  test('early leave remains visible through settlement and disappears only after a fresh empty round', async () => {
    const env = await fixture();
    try {
      await env.storage.set('alice', { value: 'local' });
      env.pages.push(page([{ type: 'leave', id: 'alice' }]));
      const runtime = env.start(true); await runtime.waitForIdle();
      expect(env.pushed.filter(row => row.kind === 'd')).toHaveLength(1);
      expect((await env.state('alice')).data?.pin?.stage).toBe('await-source');
      expect(await env.storage.get('alice')).toMatchObject({ value: 'local' });
      env.pages.push(page([])); await env.adapter.beginRound(); runtime.requestResync(); await runtime.waitForIdle();
      expect((await env.state('alice')).data?.pin).toBeNull();
      expect(await env.storage.get('alice')).toBeNull();
    } finally { await env.close(); }
  });

  test('A to B to A settles on a durable native no-op frontier without masterWrite', async () => {
    const env = await fixture();
    try {
      env.pages.push(page([{ type: 'upsert', document: document('alice', 'A') }]));
      await env.start().waitForIdle();
      await env.stop();
      await env.storage.set('alice', { value: 'B' });
      await env.storage.set('alice', { value: 'A' });
      // The disabled upstream has not consumed this local prefix.
      const oldPin = (await env.state('alice')).data?.pin;
      expect(oldPin?.stage).toBe('await-settlement');
      // A manually observed frontier is insufficient until the native up checkpoint exists.
      const saved = await env.storage.withReplicationAccess(env.scope, access => access.withUpCheckpoint(async cp => cp));
      if (saved) await env.adapter.onUpCheckpoint(saved, new AbortController().signal);
      expect((await env.state('alice')).data?.pin?.stage).toBe('await-settlement');
      env.pages.push(page([{ type: 'leave', id: 'alice' }]));
      await env.adapter.beginRound();
      const runtime = env.start(true); await runtime.waitForIdle();
      expect(env.pushed).toEqual([]);
      expect((await env.state('alice')).data?.pin?.stage).toBe('await-source');
      env.pages.push(page([])); await env.adapter.beginRound(); runtime.requestResync(); await runtime.waitForIdle();
      expect((await env.state('alice')).data?.pin).toBeNull();
      expect(await env.storage.get('alice')).toBeNull();
    } finally { await env.close(); }
  });

  test('checkpoint-before-pin failure recovers settlement and a stale L1 frontier cannot settle L2', async () => {
    const env = await fixture();
    const original = env.storage.withReplicationAccess;
    let inject = true;
    try {
      await env.storage.set('alice', { value: 'L1' });
      env.storage.withReplicationAccess = (scope, consume) => original(scope, access => consume(new Proxy(access, {
        get(target, name) {
          if (name === 'writeRecord') return async (next: ReplicaRecord, previous: any) => {
            if (inject && next.kind === 'd' && next.pin?.stage === 'await-source') {
              inject = false; throw new Error('pin-write-failed');
            }
            return target.writeRecord(next, previous);
          };
          return Reflect.get(target, name, target);
        },
      })));
      env.pages.push(page([{ type: 'leave', id: 'alice' }]));
      const runtime = env.start(true);
      await expect(runtime.waitForIdle()).rejects.toThrow('pin-write-failed');
      expect(env.pushed.filter(row => row.kind === 'd')).toHaveLength(1);
      expect((await env.state('alice')).data?.pin?.stage).toBe('await-settlement');
      const frontier = await original(env.scope, access => access.withUpCheckpoint(async cp => cp && { ...cp }));
      expect(frontier).toBeDefined();
      await env.adapter.recover(new AbortController().signal);
      expect((await env.state('alice')).data?.pin?.stage).toBe('await-source');
      await env.storage.set('alice', { value: 'L2' });
      const token = (await env.state('alice')).data?.editToken;
      await env.adapter.onUpCheckpoint(frontier!, new AbortController().signal);
      const current = await env.state('alice');
      expect(current.data?.editToken).toBe(token);
      expect(current.data?.pin?.stage).toBe('await-settlement');
      expect(await env.storage.get('alice')).toMatchObject({ value: 'L2' });
    } finally { env.storage.withReplicationAccess = original; await env.close(); }
  });
});
