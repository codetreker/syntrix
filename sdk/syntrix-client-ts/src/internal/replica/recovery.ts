import { defaultHashSha256, getMetaWriteRow, type RxStorageInstanceReplicationState, type RxStorageReplicationMeta } from 'rxdb';
import { encodedRowBytes, ReadBudget, withRows, withScanPage, type NativeRow, type PhysicalStorage } from './backend.js';
import { encodeQueryValue } from '../../api/value.js';
import { canonicalJson, encodeBusinessPayload, recordKey, validateLogicalId, validateRecordIdentity } from './records.js';
import type { AliasStorage, MaintenanceAccess } from './storage.js';
import { ReplicaStorageError, type AliasManifest, type DataRecord, type RecoveryIntent, type StorageIssue } from './storage-types.js';
import type { RemoteDocument, UpstreamTransport } from './upstream-types.js';

export type RecoveryDecision =
  | { kind: 'adopt-server'; issueId: string; logicalId: string; editToken: string | null; physicalEpoch: string }
  | { kind: 'merge-local'; issueId: string; logicalId: string; editToken: string | null; physicalEpoch: string; data: Record<string, unknown> }
  | { kind: 'retry-uncertain'; issueId: string; acknowledgeRepeatedEffects: true }
  | { kind: 'reset-alias'; issueId: string; stateToken: string; discardPending: true };
export type ReplicaInspection = {
  issueId: string | null; stateToken: string; physicalEpoch: string;
  issues: StorageIssue[]; targets: { logicalId: string; token: string }[];
  phase: { id: string; state: 'prepared' | 'dispatched' } | null;
  recovering: boolean;
  availableActions: { kind: RecoveryDecision['kind']; issueId: string }[];
  document?: { desired: DataRecord | null; assumed: DataRecord | null;
    current?: { source: 'authoritative-read'; document: RemoteDocument | null } };
};
export type ReplicaInspectionOptions = { logicalId?: string; readCurrent?: boolean; signal?: AbortSignal };
const inspectionResponseBytes = 16 * 1024 * 1024;
const inspectionBudgets = new WeakMap<AliasStorage, ReadBudget>();

const fail: (code: string, message: string) => never = (code, message) => { throw new ReplicaStorageError(code, message); };
const plain = (row: DataRecord): DataRecord => ({ key: row.key, kind: 'd', logicalId: row.logicalId,
  existence: row.existence, payload: row.payload, editToken: row.editToken, pin: row.pin && { ...row.pin }, wire: { ...row.wire } });
const exact = (a: DataRecord, b: DataRecord) => canonicalJson(plain(a)) === canonicalJson(plain(b));
const issueExists = (manifest: AliasManifest, id: string) => manifest.dirtyUpstream?.id === id || manifest.issues.some(issue => issue.id === id);
const assertIssue = (manifest: AliasManifest, id: string, logicalId?: string) => {
  if (!issueExists(manifest, id)) fail('ReplicaRecoveryStale', 'The inspected issue is no longer current');
  if (logicalId === undefined) return;
  const issue = manifest.issues.find(value => value.id === id);
  if (issue?.logicalId === logicalId) return;
  if ((!issue || issue.logicalId === null) && manifest.dirtyUpstream?.targets.some(target => target.logicalId === logicalId)) return;
  fail('ReplicaRecoveryStale', 'The issue does not protect this document');
};

const withData = async <T>(access: MaintenanceAccess, physical: PhysicalStorage, key: string, consume: (row: NativeRow<DataRecord> | undefined) => Promise<T>) => {
  const row = await withRows(physical.fork, [key], access.limits.maxRecordBytes, access.budget, async rows => {
    const row = rows[0];
    if (row) { await validateRecordIdentity(row); if (row.kind !== 'd') fail('ReplicaStorageCorruption', 'Recovery requires a data record'); }
    return row as NativeRow<DataRecord> | undefined;
  });
  const release = access.retainRows(row ? encodedRowBytes(row) : 0);
  try { return await consume(row); } finally { release(); }
};
const withMeta = async <T>(access: MaintenanceAccess, physical: PhysicalStorage, key: string,
  consume: (row: NativeRow<RxStorageReplicationMeta<DataRecord, any>> | undefined) => Promise<T>) => {
  const row = await withRows(physical.meta, [`${key}|0`], access.limits.maxMetadataBytes, access.budget, async rows => {
    const row = rows[0];
    if (row) { await validateRecordIdentity(row.docData); if (row.docData.kind !== 'd' || row.docData.key !== key) fail('ReplicaStorageCorruption', 'Recovery assumed identity differs'); }
    return row as NativeRow<RxStorageReplicationMeta<DataRecord, any>> | undefined;
  });
  const release = access.retainRows(row ? encodedRowBytes(row) : 0);
  try { return await consume(row); } finally { release(); }
};

// Chaining a digest keeps reset's all-ID edit fence bounded independently of
// collection size. New edits to IDs outside the reported issue also invalidate it.
const tokenSeed = (manifest: AliasManifest) => defaultHashSha256(canonicalJson({ epoch: manifest.activePhysicalEpoch, binding: manifest.boundDatabaseId,
  definition: manifest.definitionHash, issues: manifest.issues, marker: manifest.dirtyUpstream, intent: manifest.recoveryIntent?.id ?? null }));
const tokenAppend = (digest: string, row: DataRecord) => defaultHashSha256(canonicalJson([digest, row.key, row.editToken]));
const stateToken = async (access: MaintenanceAccess): Promise<string> => {
  const manifest = access.manifest;
  let digest = await tokenSeed(manifest);
  const physical = await access.backend.openPhysical(manifest.activePhysicalEpoch);
  let after = 'd:';
  while (true) {
    access.assertActive();
    const next = await withScanPage(physical.fork, after, 1, access.limits.maxRecordBytes, access.budget, async rows => {
      const row = rows[0];
      if (!row || row.kind !== 'd') return undefined;
      await validateRecordIdentity(row);
      digest = await tokenAppend(digest, row);
      return row.key;
    });
    if (!next) return digest;
    after = next;
  }
};

export const inspectReplica = async (storage: AliasStorage, options: ReplicaInspectionOptions = {}, transport?: UpstreamTransport): Promise<ReplicaInspection> => {
  options = { ...options };
  if (options.logicalId !== undefined) validateLogicalId(options.logicalId);
  if (options.readCurrent !== undefined && typeof options.readCurrent !== 'boolean') throw new TypeError('readCurrent must be a boolean');
  if (options.readCurrent && (options.logicalId === undefined || !transport)) throw new TypeError('Authoritative inspection requires a document ID and upstream transport');
  const signal = options.signal ? AbortSignal.any([storage.signal, options.signal]) : storage.signal;
  signal.throwIfAborted();
  const inspect = async () => {
    const scope = await storage.captureScope();
    let binding: string | null = null, sourceHash: string | null = null, definitionHash = '', inspectionSeed = '', collection = '';
    const inspection = await storage.withReplicationAccess(scope, async access => {
      signal.throwIfAborted();
      const manifest = access.manifest;
      binding = manifest.boundDatabaseId; sourceHash = manifest.sourceHash; definitionHash = manifest.definitionHash; collection = manifest.definition.collection;
      inspectionSeed = await tokenSeed(manifest);
      let digest = await tokenSeed(manifest), after: string | undefined;
      do { after = await access.scanData(after, async row => { digest = await tokenAppend(digest, row); }); } while (after !== undefined);
      const issue = options.logicalId === undefined ? manifest.issues[0] : manifest.issues.find(value => value.logicalId === options.logicalId);
      const inspection: ReplicaInspection = { issueId: issue?.id ?? manifest.dirtyUpstream?.id ?? null,
        physicalEpoch: manifest.activePhysicalEpoch, stateToken: digest,
        phase: manifest.dirtyUpstream ? { id: manifest.dirtyUpstream.id, state: manifest.dirtyUpstream.mayHaveDispatched ? 'dispatched' : 'prepared' } : null,
        recovering: manifest.recoveryIntent !== null, availableActions: [],
        issues: manifest.issues.map(value => ({ ...value })), targets: manifest.dirtyUpstream?.targets.map(value => ({ ...value })) ?? [] };
      if (options.logicalId !== undefined) {
        await access.withDocument(options.logicalId, async ({ data, assumed }) => {
          inspection.document = { desired: data ? plain(data) : null, assumed: assumed ? plain(assumed) : null };
        });
      }
      if (inspection.issueId !== null) {
        const protectedTarget = options.logicalId !== undefined && (issue?.logicalId === options.logicalId ||
          manifest.dirtyUpstream?.targets.some(target => target.logicalId === options.logicalId));
        if (!inspection.recovering && binding && protectedTarget && inspection.document?.desired) {
          inspection.availableActions.push({ kind: 'adopt-server', issueId: inspection.issueId }, { kind: 'merge-local', issueId: inspection.issueId });
        }
        // A durable marker describes possible dispatch, not proven uncertainty.
        // Explicit retry remains advisory and cannot conceal a content conflict.
        const conflict = manifest.issues.some(entry => entry.logicalId !== null || ['ReplicaWriteConflict', 'ReplicaConflictUnresolved'].includes(entry.code));
        if (!inspection.recovering && manifest.dirtyUpstream && !conflict) {
          inspection.availableActions.push({ kind: 'retry-uncertain', issueId: manifest.dirtyUpstream.id });
        }
        inspection.availableActions.push({ kind: 'reset-alias', issueId: inspection.issueId });
      }
      return inspection;
    });
    signal.throwIfAborted();
    if (!options.readCurrent) return inspection;
    if (!binding) fail('ReplicaSourceNotReady', 'Authoritative inspection requires a bound database');
    const headers = await storage.guardSourceRead(scope);
    if (headers['X-Syntrix-Expected-Database-Identity'] !== binding) fail('ReplicaRecoveryStale', 'Inspection binding changed before its authoritative read');
    let current: RemoteDocument | null;
    try { current = await transport!.readCurrent(options.logicalId!, { signal, sessionVersion: scope.sessionVersion, expectedDatabaseIdentity: binding }); }
    catch (error) {
      if (typeof error === 'object' && error !== null && 'code' in error && error.code === 'DATABASE_IDENTITY_MISMATCH') storage.blockScope();
      throw error;
    }
    signal.throwIfAborted();
    if (current !== null && (current.id !== options.logicalId || current.collection !== collection)) {
      fail('ReplicaSourceInvalid', 'Authoritative inspection returned another document');
    }
    if (encodedRowBytes(encodeQueryValue(current)) > inspectionResponseBytes) fail('ReplicaReadBudgetExceeded', 'Authoritative inspection response exceeds its materialization budget');
    const fresh = await storage.captureScope();
    if (fresh.subject !== scope.subject || fresh.sessionVersion !== scope.sessionVersion || fresh.physicalEpoch !== scope.physicalEpoch || fresh.definitionHash !== scope.definitionHash) {
      fail('ReplicaRecoveryStale', 'Inspection scope changed during its authoritative read');
    }
    await storage.guardSourceRead(fresh);
    await storage.withReplicationAccess(fresh, async access => {
      signal.throwIfAborted();
      const manifest = access.manifest;
      if (manifest.boundDatabaseId !== binding || manifest.sourceHash !== sourceHash || manifest.definitionHash !== definitionHash || await tokenSeed(manifest) !== inspectionSeed) {
        fail('ReplicaRecoveryStale', 'Inspection issue or binding changed during its authoritative read');
      }
      await access.withDocument(options.logicalId!, async ({ data, assumed }) => {
        const previous = inspection.document!;
        if ((data === undefined) !== (previous.desired === null) || (assumed === undefined) !== (previous.assumed === null) ||
            (data && previous.desired && !exact(data, previous.desired)) || (assumed && previous.assumed && !exact(assumed, previous.assumed))) {
          fail('ReplicaRecoveryStale', 'Inspected document changed during its authoritative read');
        }
      });
    });
    signal.throwIfAborted();
    inspection.document!.current = { source: 'authoritative-read', document: current };
    return inspection;
  };
  if (!options.readCurrent) return inspect();
  let budget = inspectionBudgets.get(storage);
  if (!budget) { budget = new ReadBudget(128 * 1024 * 1024); inspectionBudgets.set(storage, budget); }
  // The response is bounded by the authoritative transport. Reserve it before
  // dispatch together with the retained local snapshots and manifest references.
  return budget.withReservation(2 * storage.limits.maxRecordBytes + storage.limits.maxManifestBytes + inspectionResponseBytes, inspect);
};

const saveManifest = async (access: MaintenanceAccess, next: AliasManifest) => {
  try { await access.writeManifest(next); }
  catch (error) {
    const actual = await access.readManifest();
    const withoutSystem = (value: AliasManifest) => Object.fromEntries(Object.entries(value).filter(([key]) => !key.startsWith('_')));
    if (canonicalJson(withoutSystem(actual)) !== canonicalJson(withoutSystem(next))) throw error;
  }
};

const finishIntent = (manifest: AliasManifest, intent: RecoveryIntent): AliasManifest => {
  let marker = manifest.dirtyUpstream;
  if (intent.phaseId !== null) {
    if (marker?.id !== intent.phaseId) fail('ReplicaRecoveryStale', 'The recovery phase has changed');
    const targets = marker.targets.filter(target => target.logicalId !== intent.logicalId);
    marker = targets.length ? { ...marker, targets } : null;
  }
  const issues = manifest.issues.filter(issue => issue.id !== intent.issueId ||
    (issue.logicalId === null && marker !== null && marker.id === intent.phaseId));
  return { ...manifest, dirtyUpstream: marker, issues, recoveryIntent: null };
};

const replay = async (access: MaintenanceAccess) => {
  const intent = access.manifest.recoveryIntent;
  if (!intent) return;
  if (intent.physicalEpoch !== access.manifest.activePhysicalEpoch) fail('ReplicaRecoveryStale', 'Recovery belongs to another physical epoch');
  if (intent.phaseId !== null && access.manifest.dirtyUpstream?.id !== intent.phaseId) fail('ReplicaRecoveryStale', 'Recovery belongs to another upstream phase');
  assertIssue(access.manifest, intent.issueId, intent.logicalId);
  const physical = await access.backend.openPhysical(intent.physicalEpoch);
  await withData(access, physical, intent.desired.key, async data => {
    if (!data || (data.editToken !== intent.protectedToken && data.editToken !== intent.resultToken)) fail('ReplicaRecoveryStale', 'Document changed after the recovery decision');
    if (data.editToken === intent.resultToken && !exact(data, intent.desired)) fail('ReplicaStorageCorruption', 'Recovery result token has different contents');
    if (!exact(data, intent.desired)) {
      try { await access.backend.writeRecord(physical, intent.desired, data, 'replica-recovery-fork'); }
      catch (error) {
        const committed = await withData(access, physical, intent.desired.key, async actual => !!actual && exact(actual, intent.desired));
        if (!committed) throw error;
      }
    }
  });
  await withMeta(access, physical, intent.current.key, async previous => {
    if (previous && exact(previous.docData, intent.current) && previous.isResolvedConflict === undefined) return;
    // getMetaWriteRow only consumes these fields. Constructing its metadata input
    // avoids starting a scheduler that could acknowledge unrelated pending IDs.
    const metadataState = { primaryPath: 'key', input: { metaInstance: physical.meta },
      checkpointKey: defaultHashSha256([physical.identifier, physical.fork.databaseName, physical.fork.collectionName].join('||')).then(hash => `rx_storage_replication_${hash}`)
    } as RxStorageInstanceReplicationState<DataRecord>;
    const row = await getMetaWriteRow(metadataState, { ...intent.current, _deleted: false }, previous);
    delete row.document.isResolvedConflict;
    try {
      const result = await physical.meta.bulkWrite([row as any], 'replica-recovery-meta');
      if (result.error.length) throw result.error[0];
    } catch (error) {
      const committed = await withMeta(access, physical, intent.current.key, async actual => !!actual && exact(actual.docData, intent.current) && actual.isResolvedConflict === undefined);
      if (!committed) throw error;
    }
  });
  const verified = await withData(access, physical, intent.desired.key, async data => !!data && exact(data, intent.desired));
  const assumed = await withMeta(access, physical, intent.current.key, async data => !!data && exact(data.docData, intent.current));
  if (!verified || !assumed) {
    fail('ReplicaStorageCorruption', 'Recovery did not persist its selected states');
  }
  await saveManifest(access, finishIntent(access.manifest, intent));
};

/** The coordinator must be stopped before entering explicit recovery. */
export const replayReplicaRecovery = (storage: AliasStorage): Promise<void> => storage.withMaintenance(replay);

const currentRecord = async (logicalId: string, collection: string, current: RemoteDocument | null): Promise<DataRecord> => {
  const key = await recordKey('d', logicalId);
  if (!current) return { key, kind: 'd', logicalId, existence: 'absent', payload: '', wire: {}, editToken: null, pin: null };
  if (current.id !== logicalId || current.collection !== collection) fail('ReplicaSourceInvalid', 'Authoritative recovery returned another document');
  const { id: _id, collection: _collection, version, createdAt, updatedAt, deleted, ...payload } = current;
  const record: DataRecord = { key, kind: 'd', logicalId, existence: deleted ? 'deleted' : 'live',
    payload: deleted ? '' : encodeBusinessPayload(payload), wire: { version: version.toString(), createdAt: createdAt.toString(), updatedAt: updatedAt.toString() }, editToken: null, pin: null };
  await validateRecordIdentity(record);
  return record;
};

const reset = async (access: MaintenanceAccess, decision: Extract<RecoveryDecision, { kind: 'reset-alias' }>) => {
  if (decision.discardPending !== true) throw new TypeError('Reset requires explicit pending-data discard');
  assertIssue(access.manifest, decision.issueId);
  if (await stateToken(access) !== decision.stateToken) fail('ReplicaRecoveryStale', 'Alias changed after reset inspection');
  if (access.manifest.physicalEpochs.length !== 1 || access.manifest.maintenance) fail('ReplicaMaintenance', 'An earlier physical transition requires cleanup');
  const oldEpoch = access.manifest.activePhysicalEpoch, newEpoch = crypto.randomUUID();
  const maintenance = { id: crypto.randomUUID(), oldEpoch, newEpoch, stage: 'staging' as const };
  try {
    await saveManifest(access, { ...access.manifest, physicalEpochs: [oldEpoch, newEpoch], maintenance });
    await access.backend.openPhysical(newEpoch);
    await saveManifest(access, { ...access.manifest, activePhysicalEpoch: newEpoch, maintenance: { ...maintenance, stage: 'flipped' },
      activeSourceGeneration: null, stagedSourceGeneration: null, sourceReady: false, lastCompleteRound: null, partialDelivery: false,
      dirtyUpstream: null, issues: [], recoveryIntent: null });
  } catch (error) {
    const actual = await access.readManifest();
    if (actual.activePhysicalEpoch !== newEpoch) {
      if (actual.activePhysicalEpoch === oldEpoch && actual.physicalEpochs.includes(newEpoch)) {
        await access.backend.removePhysical(newEpoch);
        await saveManifest(access, { ...actual, physicalEpochs: [oldEpoch], maintenance: null });
      }
      throw error;
    }
  }
  await access.backend.removePhysical(oldEpoch);
  await saveManifest(access, { ...access.manifest, physicalEpochs: [newEpoch], maintenance: null });
};

export const resolveReplica = async (storage: AliasStorage, transport: UpstreamTransport, decision: RecoveryDecision,
  options: { signal?: AbortSignal } = {}): Promise<void> => {
  const signal = options.signal ? AbortSignal.any([storage.signal, options.signal]) : storage.signal;
  signal.throwIfAborted();
  if (!decision || typeof decision.issueId !== 'string' || !decision.issueId) throw new TypeError('Recovery requires an issue identity');
  decision = { ...decision };
  if (decision.kind === 'reset-alias') return storage.withMaintenance(access => { signal.throwIfAborted(); return reset(access, decision); });
  if (decision.kind === 'retry-uncertain') {
    if (decision.acknowledgeRepeatedEffects !== true) throw new TypeError('Retry requires explicit repeated-effects authorization');
    return storage.withMaintenance(async access => {
      signal.throwIfAborted();
      assertIssue(access.manifest, decision.issueId);
      if (access.manifest.recoveryIntent) fail('ReplicaRecoveryPending', 'Complete the existing recovery intent before retry');
      const marker = access.manifest.dirtyUpstream;
      if (!marker || (marker.id !== decision.issueId && !access.manifest.issues.some(issue => issue.id === decision.issueId && issue.logicalId === null))) {
        fail('ReplicaRecoveryStale', 'Retry requires the current uncertain phase');
      }
      await saveManifest(access, { ...access.manifest, dirtyUpstream: null, issues: access.manifest.issues.filter(issue => issue.id !== decision.issueId) });
    });
  }
  if (decision.kind !== 'adopt-server' && decision.kind !== 'merge-local') throw new TypeError('Unknown recovery action');
  validateLogicalId(decision.logicalId);
  const payload = decision.kind === 'merge-local' ? encodeBusinessPayload(decision.data) : undefined;
  const scope = await storage.captureScope();
  const manifest = await storage.readManifest();
  assertIssue(manifest, decision.issueId, decision.logicalId);
  if (!manifest.boundDatabaseId || scope.physicalEpoch !== decision.physicalEpoch) fail('ReplicaRecoveryStale', 'Recovery requires the original bound physical epoch');
  const headers = await storage.guardSourceRead(scope);
  if (headers['X-Syntrix-Expected-Database-Identity'] !== manifest.boundDatabaseId) fail('ReplicaScopeChanged', 'Recovery database identity changed');
  let remote: RemoteDocument | null;
  try { remote = await transport.readCurrent(decision.logicalId,
    { signal, sessionVersion: scope.sessionVersion, expectedDatabaseIdentity: manifest.boundDatabaseId }); }
  catch (error) {
    if (typeof error === 'object' && error !== null && 'code' in error && error.code === 'DATABASE_IDENTITY_MISMATCH') storage.blockScope();
    throw error;
  }
  const current = await currentRecord(decision.logicalId, manifest.definition.collection, remote);
  signal.throwIfAborted();
  await storage.guardSourceRead(scope);
  return storage.withMaintenance(async access => {
    signal.throwIfAborted();
    assertIssue(access.manifest, decision.issueId, decision.logicalId);
    if (access.manifest.recoveryIntent) fail('ReplicaRecoveryPending', 'Another recovery intent is pending');
    if (access.manifest.activePhysicalEpoch !== decision.physicalEpoch || access.manifest.boundDatabaseId !== manifest.boundDatabaseId) fail('ReplicaRecoveryStale', 'Recovery scope changed during its authoritative read');
    const physical = await access.backend.openPhysical(decision.physicalEpoch);
    await withData(access, physical, current.key, async data => {
      if (!data || data.editToken !== decision.editToken) fail('ReplicaRecoveryStale', 'Document changed during its authoritative read');
    });
    const resultToken = crypto.randomUUID();
    const desired: DataRecord = decision.kind === 'adopt-server' ? { ...current, editToken: resultToken } :
      { ...current, existence: 'live', payload: payload!, editToken: resultToken, pin: { token: resultToken, stage: 'await-settlement' } };
    const intent: RecoveryIntent = { id: crypto.randomUUID(), issueId: decision.issueId,
      phaseId: access.manifest.dirtyUpstream?.targets.some(target => target.logicalId === decision.logicalId) ? access.manifest.dirtyUpstream.id : null,
      physicalEpoch: decision.physicalEpoch, action: decision.kind === 'adopt-server' ? 'adopt' : 'merge', logicalId: decision.logicalId,
      protectedToken: decision.editToken, resultToken, current, desired };
    signal.throwIfAborted();
    await saveManifest(access, { ...access.manifest, recoveryIntent: intent });
    await replay(access);
  });
};
