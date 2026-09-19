import type { WithDeletedAndAttachments } from 'rxdb';
import { encodedRowBytes } from './backend.js';
import { businessEqual, canonicalJson, encodeBusinessPayload, recordKey } from './records.js';
import type { NativeUpCheckpoint } from './replication-access.js';
import type { SourcePage } from './runtime.js';
import type { ReplicaSourceAdapter, SourceDocument, SourceEvent, SourceEventsPage, SourceWindow } from './source-types.js';
import type { AliasStorage, RequestScope } from './storage.js';
import { ReplicaStorageError, type ControlRecord, type DataRecord, type JsonObject, type MemberRecord, type ReplicaRecord, type WireMetadata } from './storage-types.js';

export type DownstreamCheckpoint = JsonObject & {
  version: 1; mode: 'events' | 'replace'; sourceCursor: string | null;
  generation: string; sourceGeneration: string; phase: string; bootstrapComplete: boolean;
  roundId: string; pageId: string; part: number; final: boolean; complete: boolean;
};
export type DownstreamOptions = {
  storage: AliasStorage; scope: RequestScope; source: ReplicaSourceAdapter;
  requestRefresh(): void;
  onReady?(): void | Promise<void>;
};
const maxPageBytes = 16 * 1024 * 1024;
const fail = (message: string): never => { throw new ReplicaStorageError('ReplicaSourceInvalid', message); };
const plain = <T extends ReplicaRecord>(record: T): T => Object.fromEntries(Object.entries(record).filter(([key]) => !key.startsWith('_'))) as T;
const sourceRow = (record: ReplicaRecord): WithDeletedAndAttachments<ReplicaRecord> => ({ ...plain(record), _deleted: false, _attachments: {} });
const progress = (cp: DownstreamCheckpoint): ControlRecord => ({ key: 'c:progress', kind: 'c', checkpoint: cp,
  generation: cp.generation, phase: cp.phase, bootstrapComplete: cp.bootstrapComplete, partialDelivery: !cp.final });
const checkpoint = (value: unknown): DownstreamCheckpoint => {
  if (!value || typeof value !== 'object') return fail('Missing downstream checkpoint');
  const c = value as DownstreamCheckpoint;
  if (c.version !== 1 || !['events', 'replace'].includes(c.mode) || (c.sourceCursor !== null && typeof c.sourceCursor !== 'string') ||
      typeof c.generation !== 'string' || !c.generation || typeof c.sourceGeneration !== 'string' || !c.sourceGeneration || typeof c.phase !== 'string' || typeof c.bootstrapComplete !== 'boolean' ||
      typeof c.roundId !== 'string' || !c.roundId || typeof c.pageId !== 'string' || !c.pageId ||
      !Number.isSafeInteger(c.part) || c.part < 0 || typeof c.final !== 'boolean' || typeof c.complete !== 'boolean' || (c.complete && !c.final)) {
    return fail('Invalid downstream checkpoint');
  }
  return c;
};
const dataFrom = async (document: SourceDocument): Promise<DataRecord> => {
  const { id, collection: _collection, version, createdAt, updatedAt, deleted: _deleted, ...payload } = document;
  return { key: await recordKey('d', id), kind: 'd', logicalId: id, existence: 'live',
    payload: encodeBusinessPayload(payload), editToken: null, pin: null,
    wire: { version: version.toString(), createdAt: createdAt.toString(), updatedAt: updatedAt.toString() } };
};
type PendingPage = { page: SourceEventsPage | SourceWindow; groups: SourceEvent[][]; index: number; part: number; cursor: string | null; generation: string; pageId: string; controlReserve: number };

/** One bounded source response is retained until all of its native deliveries commit. */
export const createDownstreamAdapter = (options: DownstreamOptions) => {
  const { storage, scope, source } = options;
  let roundId: string = crypto.randomUUID();
  let reset = false;
  let complete = false;
  let started = false;
  let pending: PendingPage | undefined;
  let gate = Promise.resolve();
  const serial = <T>(operation: () => Promise<T>): Promise<T> => {
    const result = gate.then(operation); gate = result.then(() => undefined, () => undefined); return result;
  };
  const assert = async (signal: AbortSignal) => { signal.throwIfAborted(); await storage.guardSourceRead(scope); signal.throwIfAborted(); };
  const allowed = (manifest: Awaited<ReturnType<AliasStorage['readManifest']>>) => !manifest.dirtyUpstream && !manifest.recoveryIntent && !manifest.issues.length;

  const scanPins = async (signal: AbortSignal, action: 'settle' | 'clear', frontier?: NativeUpCheckpoint, finishedRound?: string) => {
    let after: string | undefined;
    let changed = false;
    while (true) {
      signal.throwIfAborted();
      let id: string | undefined;
      const next = await storage.withReplicationAccess(scope, access => access.scanData(after, async row => { if (row.pin) id = row.logicalId; }));
      if (next === undefined) break;
      after = next;
      if (!id) continue;
      await storage.withReplicationAccess(scope, async access => {
        if (!allowed(access.manifest)) return;
        await access.withDocument(id!, async ({ data, assumed }) => {
          if (!data?.pin || data.pin.token !== data.editToken || !assumed || !businessEqual(data, assumed)) return;
          if (action === 'settle') {
            if (data.pin.stage !== 'await-settlement' || !frontier || data._meta.lwt > frontier.lwt ||
                (data._meta.lwt === frontier.lwt && data.key > frontier.id)) return;
            await access.writeRecord({ ...plain(data), pin: { token: data.pin.token, stage: 'await-source', settledRound: roundId } }, data);
            changed = true;
          } else {
            if (data.pin.stage !== 'await-source' || !data.pin.settledRound || data.pin.settledRound === finishedRound) return;
            await access.writeRecord({ ...plain(data), pin: null }, data);
          }
        });
      });
    }
    if (changed) options.requestRefresh();
  };

  const finish = async (cp: DownstreamCheckpoint, signal: AbortSignal) => {
    await assert(signal);
    try {
      await storage.withReplicationAccess(scope, async access => {
        if (!allowed(access.manifest)) throw new ReplicaStorageError('ReplicaRecoveryRequired', 'Unresolved upstream work blocks source application');
        if (cp.complete) {
          await access.writeManifest({ ...access.manifest, activeSourceGeneration: cp.generation, stagedSourceGeneration: null,
            sourceReady: true, partialDelivery: false });
        } else if (cp.final) await access.writeManifest({ ...access.manifest, partialDelivery: false });
      });
    } catch (error) {
      // A lost acknowledgement must not make a committed generation look absent.
      const activated = await storage.withReplicationAccess(scope, async access => cp.complete &&
        access.manifest.activeSourceGeneration === cp.generation && access.manifest.sourceReady && !access.manifest.partialDelivery);
      await assert(signal);
      if (!activated) throw error;
    }
    if (cp.complete) {
      // Recovery can activate a durable generation, but an old completed round
      // cannot acknowledge pin transitions that may have happened after it.
      if (cp.roundId === roundId) {
        await scanPins(signal, 'clear', undefined, cp.roundId);
        complete = true;
      }
      await options.onReady?.();
    }
  };

  const project = async (events: SourceEvent[], generation: string, preflight = false): Promise<ReplicaRecord[]> => {
    const first = events[0], id = first.type === 'upsert' ? first.document.id : first.id;
    return storage.withReplicationAccess(scope, access => access.withDocument(id, async ({ data, member }) => {
      let desired: DataRecord | undefined;
      let known = Boolean(data || member);
      let metadata: WireMetadata = member?.metadata ?? data?.wire ?? {};
      let existence = member?.observedExistence ?? data?.existence ?? null;
      let present = false;
      let touched = false;
      for (const event of events) {
        if (event.type === 'upsert') {
          desired = await dataFrom(event.document); metadata = desired.wire; existence = 'live'; present = true; known = true; touched = true;
        } else if (known) {
          present = false; touched = true;
          if (event.type === 'delete') {
            desired = { key: await recordKey('d', id), kind: 'd', logicalId: id, existence: 'deleted', payload: '', editToken: null, pin: null, wire: { ...metadata } };
            existence = 'deleted';
          }
        }
      }
      if (!touched) return [];
      const active = access.manifest.activeSourceGeneration;
      const slots = (member?.slots ?? []).filter(slot => slot.generation === active && slot.generation !== generation);
      slots.push({ generation, member: present });
      const nextMember: MemberRecord = { key: await recordKey('m', id), kind: 'm', logicalId: id, slots, observedExistence: existence, metadata: { ...metadata } };
      if (preflight && desired && data) desired = { ...desired, editToken: data.editToken, pin: data.pin && { ...data.pin } };
      return desired ? [desired, nextMember] : [nextMember];
    }));
  };

  const load = async (previous: DownstreamCheckpoint | undefined, signal: AbortSignal) => {
    await assert(signal);
    const binding = await storage.withReplicationAccess(scope, async access => {
      if (!allowed(access.manifest)) throw new ReplicaStorageError('ReplicaRecoveryRequired', 'Unresolved upstream work blocks source reads');
      if (canonicalJson(access.manifest.definition) !== canonicalJson(source.definition)) fail('Source adapter differs from the frozen alias definition');
      return { databaseIdentity: access.manifest.boundDatabaseId, sourceHash: access.manifest.sourceHash };
    });
    const cursor = source.mode === 'events' && !reset ? previous?.sourceCursor ?? null : null;
    const page = await source.read({ checkpoint: cursor, requestId: crypto.randomUUID(), signal, sessionVersion: scope.sessionVersion,
      expectedDatabaseIdentity: binding.databaseIdentity, expectedSourceHash: binding.sourceHash });
    await assert(signal);
    if (page.mode !== source.mode) fail('Source response mode differs from the frozen definition');
    if (cursor !== null && page.generationId !== previous!.sourceGeneration) fail('Source generation changed while continuing its cursor');
    const events: SourceEvent[] = page.mode === 'events' ? page.events : page.documents.map(document => ({ type: 'upsert', document }));
    const generation = page.mode === 'replace' || cursor === null ? crypto.randomUUID() : previous!.generation;
    const pageId = crypto.randomUUID();
    const grouped = new Map<string, SourceEvent[]>();
    for (const event of events) {
      const id = event.type === 'upsert' ? event.document.id : event.id;
      const group = grouped.get(id); if (group) group.push(event); else grouped.set(id, [event]);
    }
    const admit = (rows: ReplicaRecord[]) => {
      for (const row of rows) {
        const bytes = encodedRowBytes(sourceRow(row));
        // Reserve the native revision/origin fields and assumed-state envelope.
        if (bytes + 1024 > storage.limits.maxRecordBytes || bytes + 2048 > storage.limits.maxMetadataBytes) {
          throw new ReplicaStorageError('ReplicaRecordTooLarge', 'Source record exceeds its projected storage budget');
        }
      }
    };
    let controlReserve = 0;
    for (const final of [false, true]) {
      const cp: DownstreamCheckpoint = { version: 1, mode: page.mode, sourceCursor: final && page.mode === 'events' ? page.checkpoint : cursor,
        generation, sourceGeneration: page.generationId, phase: page.mode === 'events' ? page.phase : 'window',
        bootstrapComplete: page.mode === 'replace' || page.bootstrapComplete,
        roundId, pageId, part: Math.max(1, grouped.size), final, complete: final && (page.mode === 'replace' || (page.caughtUp && page.bootstrapComplete)) };
      const control = progress(cp); admit([control]);
      controlReserve = Math.max(controlReserve, encodedRowBytes([sourceRow(control)]) + 1024);
    }
    // Read-only projection checks every group before binding or staging. Keeping
    // just one group bounded avoids retaining a second full normalized window.
    for (const group of grouped.values()) {
      signal.throwIfAborted();
      const rows = await project(group, generation, true); admit(rows);
      if (encodedRowBytes(rows.map(sourceRow)) + controlReserve > maxPageBytes) {
        throw new ReplicaStorageError('ReplicaRecordTooLarge', 'Source projection exceeds its delivery budget');
      }
    }
    await storage.bind(scope, page.databaseIdentity, page.sourceHash);
    await storage.withReplicationAccess(scope, async access => {
      await access.writeManifest({ ...access.manifest,
        stagedSourceGeneration: generation === access.manifest.activeSourceGeneration ? null : generation, partialDelivery: true });
    });
    pending = { page, groups: [...grouped.values()], index: 0, part: 0, cursor, generation, pageId, controlReserve };
    started = true;
    reset = false;
  };

  return {
    get complete() { return complete; },
    beginRound: (next: { reset?: boolean } = {}) => serial(async () => {
      if (started && !complete && !next.reset) return;
      if (pending) throw new ReplicaStorageError('ReplicaRoundBusy', 'A source page is still being delivered');
      roundId = crypto.randomUUID(); reset = next.reset === true; complete = false; started = false;
    }),
    readSource: (previous: DownstreamCheckpoint | undefined, limit: number, signal: AbortSignal): Promise<SourcePage<ReplicaRecord, DownstreamCheckpoint>> => serial(async () => {
      await assert(signal);
      if (!Number.isSafeInteger(limit) || limit < 3) throw new RangeError('Source projection needs at least three native rows');
      if (previous) checkpoint(previous);
      if (complete) return { documents: [], checkpoint: previous!, complete: true };
      if (!pending) await load(previous, signal);
      const current = pending!;
      const documents: WithDeletedAndAttachments<ReplicaRecord>[] = [];
      let bytes = current.controlReserve;
      while (current.index < current.groups.length) {
        const rows = (await project(current.groups[current.index], current.generation)).map(sourceRow);
        const size = encodedRowBytes(rows);
        if (rows.length + 1 > Math.min(201, limit) || size + current.controlReserve > maxPageBytes) throw new ReplicaStorageError('ReplicaRecordTooLarge', 'Source projection exceeds delivery budget');
        if (documents.length + rows.length + 1 > Math.min(201, limit) || bytes + size > maxPageBytes) break;
        documents.push(...rows); bytes += size; current.index++;
      }
      const final = current.index === current.groups.length;
      const pageComplete = current.page.mode === 'replace' || (current.page.caughtUp && current.page.bootstrapComplete);
      const cp: DownstreamCheckpoint = { version: 1, mode: current.page.mode, sourceCursor: final && current.page.mode === 'events' ? current.page.checkpoint : current.cursor,
        generation: current.generation, sourceGeneration: current.page.generationId, phase: current.page.mode === 'events' ? current.page.phase : 'window',
        bootstrapComplete: current.page.mode === 'replace' || current.page.bootstrapComplete,
        roundId, pageId: current.pageId, part: current.part++, final, complete: final && pageComplete };
      documents.push(sourceRow(progress(cp)));
      if (encodedRowBytes(documents) > maxPageBytes) throw new ReplicaStorageError('ReplicaRecordTooLarge', 'Encoded native delivery exceeds its page budget');
      return { documents, checkpoint: cp, complete: cp.complete };
    }),
    onCheckpoint: (value: DownstreamCheckpoint, _status: { complete: boolean }, signal: AbortSignal) => serial(async () => {
      const cp = checkpoint(value);
      if (cp.roundId !== roundId || pending?.pageId !== cp.pageId) throw new ReplicaStorageError('ReplicaScopeChanged', 'Source delivery belongs to an obsolete round');
      await finish(cp, signal);
      if (cp.final) pending = undefined;
    }),
    onUpCheckpoint: (frontier: NativeUpCheckpoint, signal: AbortSignal) => serial(() => scanPins(signal, 'settle', frontier)),
    recover: (signal: AbortSignal) => serial(async () => {
      await assert(signal);
      const durable = await storage.withReplicationAccess(scope, access => access.withDownCheckpoint(async saved => saved ? checkpoint(structuredClone(saved.source)) : undefined));
      if (durable?.final) {
        const matches = await storage.withReplicationAccess(scope, access => access.withControl(async control => control && canonicalJson(control.checkpoint) === canonicalJson(durable)));
        if (matches) await finish(durable, signal);
      }
      const frontier = await storage.withReplicationAccess(scope, access => access.withUpCheckpoint(async saved => saved && { ...saved }));
      if (frontier) await scanPins(signal, 'settle', frontier);
    }),
  };
};
