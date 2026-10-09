import { defaultHashSha256, type WithDeletedAndAttachments } from 'rxdb';
import { countRows, encodedRowBytes, withRows, withScanPage, type PhysicalStorage } from './backend.js';
import { businessEqual, canonicalJson, recordKey, validateRecordIdentity } from './records.js';
import { createReplicationRuntime, type ReplicationRuntime } from './runtime.js';
import type { AliasStorage, MaintenanceAccess } from './storage.js';
import { ReplicaStorageError, type ControlRecord, type JsonObject, type ReplicaRecord, type MemberRecord } from './storage-types.js';

export type CompactionResult = { status: 'compacted'; previousEpoch: string; activeEpoch: string } | { status: 'not-clean' };
const fail = (message: string): never => { throw new ReplicaStorageError('ReplicaStorageCorruption', message); };
const plain = <T extends ReplicaRecord>(row: T): T => Object.fromEntries(Object.entries(row).filter(([key]) => !key.startsWith('_'))) as T;
const sourceDocument = (row: ReplicaRecord): WithDeletedAndAttachments<ReplicaRecord> => ({ ...plain(row), _deleted: false, _attachments: {} });

type DeadlineTimer = ReturnType<typeof setTimeout>;
type DeadlineClock = { set(callback: () => void, milliseconds: number): DeadlineTimer; clear(timer: DeadlineTimer): void };
export const createCompactionDeadline = (clock: DeadlineClock = { set: setTimeout, clear: clearTimeout }) => {
  let timer: DeadlineTimer;
  let stopped = false;
  let reject: (error: unknown) => void;
  const failure = new Promise<never>((_resolve, rejectPromise) => { reject = rejectPromise; });
  const progress = () => {
    if (stopped) return;
    if (timer !== undefined) clock.clear(timer);
    timer = clock.set(() => {
      stopped = true;
      reject(new ReplicaStorageError('ReplicaMaintenanceTimeout', 'Compaction seed made no durable or scan progress'));
    }, 30_000);
    if (typeof timer === 'object') timer.unref?.();
  };
  progress();
  return { failure, progress, dispose: () => { stopped = true; clock.clear(timer); } };
};

const readRecord = (access: MaintenanceAccess, physical: PhysicalStorage, key: string) => {
  access.assertActive();
  return withRows(physical.fork, [key], access.limits.maxRecordBytes, access.budget, async rows => rows[0]);
};
const readMeta = (access: MaintenanceAccess, physical: PhysicalStorage, key: string) => {
  access.assertActive();
  return withRows(physical.meta, [key], access.limits.maxMetadataBytes, access.budget, async rows => rows[0]);
};
const scan = async function* (access: MaintenanceAccess, physical: PhysicalStorage, progress?: () => void) {
  let after: string | undefined;
  while (true) {
    access.assertActive();
    const row = await withScanPage(physical.fork, after, 1, access.limits.maxRecordBytes, access.budget, async rows => rows[0]);
    if (!row) return;
    await validateRecordIdentity(row);
    after = row.key;
    progress?.();
    yield row;
  }
};

const cleanSource = async (access: MaintenanceAccess, physical: PhysicalStorage): Promise<{ control: ControlRecord; checkpoint: JsonObject } | undefined> => {
  const manifest = access.manifest;
  if (manifest.state !== 'ready' || !manifest.sourceReady || !manifest.boundDatabaseId || !manifest.sourceHash ||
      !manifest.activeSourceGeneration || manifest.stagedSourceGeneration || manifest.partialDelivery || manifest.dirtyUpstream ||
      manifest.issues.length || manifest.recoveryIntent) return;
  const control = await readRecord(access, physical, 'c:progress');
  if (!control || control.kind !== 'c' || !control.bootstrapComplete || control.partialDelivery || control.generation !== manifest.activeSourceGeneration) return;
  const down = await readMeta(access, physical, 'down|1');
  if (!down || !down.checkpointData?.source || canonicalJson(down.checkpointData.source) !== canonicalJson(control.checkpoint)) return;
  for await (const row of scan(access, physical)) {
    if (row.kind !== 'd') continue;
    if (row.pin) return;
    const assumed = await readMeta(access, physical, `${row.key}|0`);
    if (!assumed || !businessEqual(row, assumed.docData)) return;
  }
  return { control: plain(control), checkpoint: structuredClone(down.checkpointData.source) };
};

// Reconstruct members from the active generation; physical history and old
// assumed revisions are deliberately not part of the next epoch.
const retained = async function* (access: MaintenanceAccess, physical: PhysicalStorage, progress?: () => void) {
  for await (const row of scan(access, physical, progress)) {
    if (row.kind !== 'm') continue;
    const slot = row.slots.find(item => item.generation === access.manifest.activeSourceGeneration && item.member);
    if (!slot) continue;
    const data = await readRecord(access, physical, await recordKey('d', row.logicalId));
    if (!data || data.kind !== 'd' || data.existence !== 'live') fail('Active membership has no live business record');
    progress?.();
    yield plain(data!);
    yield { ...plain(row), slots: [slot] } as MemberRecord;
  }
};

const seed = async (access: MaintenanceAccess, previous: PhysicalStorage, next: PhysicalStorage,
  source: { control: ControlRecord; checkpoint: JsonObject }) => {
  const deadline = createCompactionDeadline();
  const iterator = retained(access, previous, deadline.progress);
  let pending: IteratorResult<ReplicaRecord> | undefined;
  let finished = false;
  let page = 0;
  let runtime: ReplicationRuntime | undefined;
  try {
    runtime = createReplicationRuntime<ReplicaRecord, JsonObject>({
      identifier: next.identifier, forkInstance: next.fork, metaInstance: next.meta,
      ownerSignal: access.ownerSignal,
      hashFunction: defaultHashSha256,
      conflictHandler: { isEqual: businessEqual, resolve: async conflict => conflict.realMasterState },
      pullBatchSize: 3, pushBatchSize: 3,
      readBounds: { maxDocuments: 3, readBlockDocuments: 3, maxDocumentBytes: access.limits.maxRecordBytes,
        targetBytes: Math.min(8 * 1024 * 1024, access.limits.maxRecordBytes) },
      isControlDocument: row => row.kind !== 'd',
      onCheckpoint: async () => { deadline.progress(); },
      writeRemote: async () => { throw new ReplicaStorageError('ReplicaCompactionEcho', 'Clean compaction attempted a business Push'); },
      readSource: async (_checkpoint, limit) => {
        access.assertActive();
        if (finished) return { documents: [], checkpoint: source.checkpoint, complete: true };
        const documents: WithDeletedAndAttachments<ReplicaRecord>[] = [];
        while (documents.length < limit) {
          pending ??= await iterator.next();
          if (pending.done) {
            // A final control row guarantees that even an empty member set commits
            // the original opaque source checkpoint through native persistence.
            if (documents.length) break;
            finished = true;
            return { documents: [sourceDocument(source.control)], checkpoint: source.checkpoint, complete: true };
          }
          const document = sourceDocument(pending.value);
          const bytes = encodedRowBytes([...documents, document]);
          if (bytes > access.limits.maxRecordBytes) {
            if (documents.length) break;
            throw new ReplicaStorageError('ReplicaRecordTooLarge', 'Seed document exceeds the source page budget');
          }
          if (documents.length && bytes > Math.min(8 * 1024 * 1024, access.limits.maxRecordBytes)) break;
          documents.push(document);
          pending = undefined;
        }
        return { documents, checkpoint: { compaction: next.epoch, page: ++page }, complete: false };
      },
    });
    await Promise.race([runtime.waitForIdle(), deadline.failure]);
    if (!runtime.ready) fail('Compaction seed did not reach its final checkpoint');
  } finally {
    deadline.dispose();
    await runtime?.close();
    await iterator.return(undefined);
  }
};

const verify = async (access: MaintenanceAccess, previous: PhysicalStorage, next: PhysicalStorage,
  source: { control: ControlRecord; checkpoint: JsonObject }) => {
  let expected = 0;
  const verifyRecord = async (row: ReplicaRecord) => {
    const saved = await readRecord(access, next, row.key);
    const assumed = await readMeta(access, next, `${row.key}|0`);
    if (!saved || canonicalJson(plain(saved)) !== canonicalJson(plain(row)) || !assumed || !businessEqual(saved, assumed.docData)) {
      fail('Compaction seed differs from the selected clean member set');
    }
    expected++;
  };
  for await (const row of retained(access, previous)) await verifyRecord(row);
  await verifyRecord(source.control);
  access.assertActive();
  if (await countRows(next.fork) !== expected || await countRows(next.meta) !== expected + 2) fail('Compaction seed contains unexpected records');
  const down = await readMeta(access, next, 'down|1');
  const up = await readMeta(access, next, 'up|1');
  if (!up || !down || canonicalJson(down.checkpointData) !== canonicalJson({ source: source.checkpoint })) fail('Compaction checkpoint verification failed');
};

const cleanup = async (access: MaintenanceAccess) => {
  const current = await access.readManifest();
  for (const epoch of current.physicalEpochs) {
    if (epoch === current.activePhysicalEpoch) continue;
    access.assertActive();
    await access.backend.removePhysical(epoch);
  }
  if (current.maintenance || current.physicalEpochs.length !== 1) {
    await access.writeManifest({ ...current, maintenance: null, physicalEpochs: [current.activePhysicalEpoch] });
  }
};

export const compactAlias = (alias: AliasStorage): Promise<CompactionResult> => alias.withMaintenance(async access => {
  // Resolve an earlier interrupted attempt before allocating another shadow.
  await cleanup(access);
  const previousEpoch = access.manifest.activePhysicalEpoch;
  const previous = await access.backend.openPhysical(previousEpoch);
  const source = await cleanSource(access, previous);
  if (!source) return { status: 'not-clean' };
  const activeEpoch = crypto.randomUUID();
  const maintenance = { id: crypto.randomUUID(), oldEpoch: previousEpoch, newEpoch: activeEpoch, stage: 'staging' as const };
  try {
    await access.writeManifest({ ...access.manifest, maintenance, physicalEpochs: [previousEpoch, activeEpoch] });
    const next = await access.backend.openPhysical(activeEpoch);
    await seed(access, previous, next, source);
    await verify(access, previous, next, source);
    await access.writeManifest({ ...access.manifest, activePhysicalEpoch: activeEpoch, maintenance: { ...maintenance, stage: 'flipped' } });
  } catch (error) {
    // A rejected CAS can have committed. Authoritative selection must be known
    // before either epoch is removed; unreadable manifests retain both copies.
    const current = await access.readManifest();
    if (current.activePhysicalEpoch !== previousEpoch && current.activePhysicalEpoch !== activeEpoch) throw error;
    await cleanup(access);
    if (current.activePhysicalEpoch !== activeEpoch) throw error;
  }
  await cleanup(access);
  return { status: 'compacted', previousEpoch, activeEpoch };
});
