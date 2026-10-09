import type { RxReplicationWriteToMasterRow, WithDeleted, WithDeletedAndAttachments } from 'rxdb';
import { SyntrixError } from '../../api/errors.js';
import { businessEqual, decodeBusinessPayload, encodeBusinessPayload, recordKey, validateRecordIdentity } from './records.js';
import type { NativeUpCheckpoint } from './replication-access.js';
import type { AliasStorage, RequestScope } from './storage.js';
import { ReplicaStorageError, type DataRecord, type ReplicaRecord, type UpstreamMarker } from './storage-types.js';
import { upstreamFailureDisposition } from './upstream-transport.js';
import type { PreparedPushBatch, PushOperation, RemoteDocument, UpstreamRequestContext, UpstreamTransport } from './upstream-types.js';

export type UpstreamOutcome = { kind: 'retry' | 'blocked' | 'uncertain'; error: unknown; retryAfterMs?: number };
export class UpstreamFailure extends ReplicaStorageError {
  constructor(readonly disposition: UpstreamOutcome['kind'], code: string, message: string, cause?: unknown) {
    super(code, message, cause === undefined ? undefined : { cause });
    this.name = 'UpstreamFailure';
  }
}
export type UpstreamOptions = {
  storage: AliasStorage; scope: RequestScope; transport: UpstreamTransport;
  onSettlement(checkpoint: NativeUpCheckpoint, signal: AbortSignal, phaseId?: string): Promise<void>;
};
type Phase = {
  marker: UpstreamMarker; checkpoint: NativeUpCheckpoint; registered: boolean; failed: boolean;
  accepted: boolean; satisfied: boolean; possible: boolean; conflict: boolean; persistenceUnknown: boolean;
  expectedDatabaseIdentity?: string;
  failure?: unknown;
};
type Change = { desired: DataRecord; assumed?: DataRecord; operation: PushOperation; attempts: number };
const fail = (message: string): never => { throw new ReplicaStorageError('ReplicaUpstreamPhaseInvalid', message); };
const failureKind = (error: unknown): 'retry' | 'blocked' => error instanceof SyntrixError
  ? error.status === 429 || error.status >= 500 ? 'retry' : 'blocked'
  : error instanceof ReplicaStorageError ? 'blocked' : 'retry';
const fromRemote = async (id: string, document: RemoteDocument | null): Promise<DataRecord> => {
  const key = await recordKey('d', id);
  if (!document) return { key, kind: 'd', logicalId: id, existence: 'absent', payload: '', editToken: null, pin: null, wire: {} };
  if (document.id !== id) return fail('Authoritative document identity differs from the submitted change');
  const { id: _id, collection: _collection, version, createdAt, updatedAt, deleted, ...payload } = document;
  return { key, kind: 'd', logicalId: id, existence: deleted ? 'deleted' : 'live', payload: deleted ? '' : encodeBusinessPayload(payload),
    editToken: null, pin: null, wire: { version: version.toString(), createdAt: createdAt.toString(), updatedAt: updatedAt.toString() } };
};

/** One marker covers the native unit through its metadata and checkpoint commit. */
export const createUpstreamAdapter = (options: UpstreamOptions) => {
  const { storage, scope, transport } = options;
  let phase: Phase | undefined;
  let outcome: UpstreamOutcome | undefined;
  let wire = Promise.resolve();
  const ownsPhase = (marker: UpstreamMarker | null | undefined): boolean => Boolean(marker && phase && !phase.failed &&
    marker.id === phase.marker.id && marker.session === scope.sessionVersion && marker.physicalEpoch === scope.physicalEpoch);
  const current = (): Phase => {
    if (phase?.failed && phase.failure !== undefined) throw phase.failure;
    if (!phase || phase.failed) return fail('No active native upstream persistence phase');
    return phase;
  };
  const context = async (p: Phase, signal: AbortSignal): Promise<UpstreamRequestContext> => {
    signal.throwIfAborted();
    if (phase !== p || p.failed) fail('Upstream phase is obsolete');
    const headers = await storage.guardNetwork(scope);
    const identity = headers['X-Syntrix-Expected-Database-Identity'];
    if (!identity) fail('Upstream requests require a bound database identity');
    if (p.expectedDatabaseIdentity !== undefined && p.expectedDatabaseIdentity !== identity) fail('Upstream database binding changed');
    p.expectedDatabaseIdentity = identity;
    signal.throwIfAborted();
    return { signal, sessionVersion: scope.sessionVersion, expectedDatabaseIdentity: identity };
  };
  const register = async (p: Phase) => {
    if (p.registered) return;
    try {
      await storage.withReplicationAccess(scope, async access => {
        if (access.manifest.dirtyUpstream || access.manifest.recoveryIntent || access.manifest.issues.length) {
          throw new UpstreamFailure('blocked', 'ReplicaRecoveryRequired', 'Unresolved upstream work blocks dispatch');
        }
        await access.writeManifest({ ...access.manifest, dirtyUpstream: p.marker });
      });
      p.registered = true;
    } catch (error) { p.persistenceUnknown = !(error instanceof UpstreamFailure); throw error; }
  };
  const conflict = async (p: Phase, desired: DataRecord, cause?: unknown): Promise<never> => {
    p.conflict = true;
    await storage.withReplicationAccess(scope, async access => {
      if (!ownsPhase(access.manifest.dirtyUpstream)) fail('Conflict no longer owns its durable upstream marker');
      const issue = { id: `${p.marker.id}:${desired.key}`, logicalId: desired.logicalId, code: 'ReplicaWriteConflict', token: desired.editToken };
      await access.writeManifest({ ...access.manifest, issues: [...access.manifest.issues.filter(entry => entry.id !== issue.id), issue] });
    });
    throw new UpstreamFailure('blocked', 'ReplicaWriteConflict', `Remote document ${desired.logicalId} differs from the assumed business state`, cause);
  };
  const prepare = (changes: Change[]): readonly PreparedPushBatch[] => {
    try { return transport.prepare(changes.map(change => change.operation)); }
    catch (error) { throw new UpstreamFailure('blocked', 'ReplicaPushRejected', 'Pending changes cannot be encoded for Push', error); }
  };
  const push = async (p: Phase, batch: PreparedPushBatch, signal: AbortSignal) => {
    let persistenceError: unknown;
    const requestContext = await context(p, signal);
    try {
      const result = await transport.push(batch, { ...requestContext, beforeDispatch: async () => {
        try {
          await context(p, signal);
          await storage.withReplicationAccess(scope, async access => {
            if (!ownsPhase(access.manifest.dirtyUpstream) || access.manifest.issues.length || access.manifest.recoveryIntent) fail('Upstream dispatch lost its durable phase');
            await access.writeManifest({ ...access.manifest, dirtyUpstream: { ...p.marker, mayHaveDispatched: true } });
          });
        } catch (error) { persistenceError = error; p.persistenceUnknown = true; throw error; }
        p.marker.mayHaveDispatched = true;
        p.possible = true;
      } });
      p.possible = false;
      return result;
    } catch (error) {
      if (persistenceError !== undefined) throw persistenceError;
      const disposition = upstreamFailureDisposition(error);
      if (disposition !== 'unknown') p.possible = false;
      const kind = p.possible ? 'uncertain' : failureKind(error);
      throw new UpstreamFailure(kind, kind === 'uncertain' ? 'ReplicaUpstreamUncertain' : 'ReplicaPushFailed', 'Push request did not complete', error);
    }
  };

  const write = async (rows: RxReplicationWriteToMasterRow<ReplicaRecord>[], signal: AbortSignal): Promise<WithDeleted<ReplicaRecord>[]> => {
    const p = current(); signal.throwIfAborted();
    if (rows.length > 50) fail('Native business batch exceeds 50 changes');
    const frozen = rows.map(row => ({ desired: structuredClone(row.newDocumentState), assumed: row.assumedMasterState && structuredClone(row.assumedMasterState) }));
    for (const row of frozen) {
      await validateRecordIdentity(row.desired);
      const desired = row.desired;
      if (desired.kind !== 'd' || !desired.editToken ||
          !p.marker.targets.some(target => target.logicalId === desired.logicalId && target.token === desired.editToken)) fail('Native callback is outside its frozen phase targets');
      if (row.assumed) {
        await validateRecordIdentity(row.assumed);
        if (row.assumed.kind !== 'd' || row.assumed.key !== row.desired.key) fail('Assumed state differs from the desired logical identity');
      }
    }
    await register(p);
    const changes: Change[] = [];
    for (const row of frozen) {
      const desired = row.desired as DataRecord, assumed = row.assumed as DataRecord | undefined;
      if (desired.existence === 'absent') fail('Ordinary upstream cannot upload an absent recovery state');
      if (assumed && businessEqual(desired, assumed)) continue;
      if (assumed?.existence !== 'live' && desired.existence === 'deleted') continue;
      const operation: PushOperation = { logicalId: desired.logicalId,
        action: assumed?.existence === 'live' ? desired.existence === 'deleted' ? 'delete' : 'update' : 'create',
        payload: desired.existence === 'live' ? decodeBusinessPayload(desired.payload) : {} };
      if (assumed?.existence === 'live') {
        if (assumed.wire.version !== undefined) operation.baseVersion = BigInt(assumed.wire.version);
        else {
          let observed: RemoteDocument | null;
          const requestContext = await context(p, signal);
          try { observed = await transport.readCurrent(desired.logicalId, requestContext); }
          catch (error) { throw new UpstreamFailure(failureKind(error), 'ReplicaPreflightFailed', 'Authoritative version lookup failed', error); }
          const remote = await fromRemote(desired.logicalId, observed);
          if (remote.existence !== 'live' || !businessEqual(remote, assumed)) await conflict(p, desired);
          operation.baseVersion = BigInt(remote.wire.version!);
        }
      }
      changes.push({ desired, assumed, operation, attempts: 0 });
    }
    let remaining = changes;
    while (remaining.length) {
      // Preparing the complete callback first prevents a later oversized item
      // from being discovered after an earlier HTTP batch has already executed.
      const batches = prepare(remaining);
      const retry: Change[] = [];
      for (const batch of batches) {
        signal.throwIfAborted();
        const submitted = batch.indexes.map(index => remaining[index]);
        submitted.forEach(change => { change.attempts++; });
        const response = await push(p, batch, signal);
        const conflicts = new Map(response.conflicts.map(entry => [entry.changeIndex, entry]));
        if (conflicts.size < submitted.length) p.accepted = true;
        const observed = new Map<number, DataRecord>();
        for (const [index, rejected] of conflicts) {
          const remote = await fromRemote(submitted[index].desired.logicalId, rejected.current);
          observed.set(index, remote);
          if (businessEqual(remote, submitted[index].desired)) p.satisfied = true;
        }
        for (let index = 0; index < submitted.length; index++) {
          const change = submitted[index], rejected = conflicts.get(index);
          if (!rejected) { p.accepted = true; continue; }
          const remote = observed.get(index)!;
          if (businessEqual(remote, change.desired)) { p.satisfied = true; continue; }
          if (change.operation.action !== 'create' && change.assumed && remote.existence === 'live' && businessEqual(remote, change.assumed)) {
            if (change.attempts >= 3) throw new UpstreamFailure('retry', 'ReplicaWriteContention', 'Conditional update exhausted three version attempts');
            change.operation = { ...change.operation, baseVersion: BigInt(remote.wire.version!) }; retry.push(change);
          } else await conflict(p, change.desired);
        }
      }
      remaining = retry;
    }
    return [];
  };

  return {
    ownsPhase,
    get outcome() { return outcome; },
    writeRemote: (rows: RxReplicationWriteToMasterRow<ReplicaRecord>[], signal: AbortSignal) => {
      const result = wire.then(() => write(rows, signal)).catch(error => {
        if (phase) { phase.failed = true; phase.failure = error; }
        throw error;
      });
      wire = result.then(() => undefined, () => undefined); return result;
    },
    upstreamPersistence: {
      begin: async (documents: WithDeletedAndAttachments<ReplicaRecord>[], checkpoint: NativeUpCheckpoint, signal: AbortSignal) => {
        signal.throwIfAborted(); if (phase) fail('Native upstream phases must not overlap');
        const targets = new Map<string, string>();
        for (const document of documents) if (document.kind === 'd' && document.editToken) targets.set(document.logicalId, document.editToken);
        if (targets.size > 200) fail('Native upstream phase exceeds 200 business targets');
        phase = { marker: { id: crypto.randomUUID(), session: scope.sessionVersion, physicalEpoch: scope.physicalEpoch,
          mayHaveDispatched: false, targets: [...targets].map(([logicalId, token]) => ({ logicalId, token })) }, checkpoint: { ...checkpoint },
          registered: false, failed: false, accepted: false, satisfied: false, possible: false, conflict: false, persistenceUnknown: false };
      },
      complete: async (checkpoint: NativeUpCheckpoint, signal: AbortSignal) => {
        const p = current();
        if (checkpoint.id !== p.checkpoint.id || checkpoint.lwt !== p.checkpoint.lwt) fail('Native phase completed a different checkpoint');
        await options.onSettlement(checkpoint, signal, p.registered ? p.marker.id : undefined);
        if (p.registered) await storage.withReplicationAccess(scope, async access => {
          if (!ownsPhase(access.manifest.dirtyUpstream)) fail('Settlement lost its upstream marker');
          if (access.manifest.issues.length || access.manifest.recoveryIntent) fail('Unresolved work cannot finish upstream settlement');
          await access.writeManifest({ ...access.manifest, dirtyUpstream: null });
        });
        phase = undefined;
      },
      failed: async (error: unknown) => {
        const p = phase;
        if (!p) return;
        p.failed = true;
        const uncertain = p.accepted || p.satisfied || p.possible;
        const retain = uncertain || p.conflict || p.persistenceUnknown;
        outcome = { kind: uncertain ? 'uncertain' : error instanceof UpstreamFailure ? error.disposition : 'blocked', error };
        const cause = error instanceof UpstreamFailure ? (error as Error & { cause?: unknown }).cause : error;
        if (cause instanceof SyntrixError && cause.retryAfter !== undefined) outcome.retryAfterMs = cause.retryAfter * 1000;
        if (!retain && p.registered) await storage.withReplicationAccess(scope, async access => {
          if (access.manifest.dirtyUpstream?.id !== p.marker.id) fail('Failed phase cannot clear another marker');
          await access.writeManifest({ ...access.manifest, dirtyUpstream: null });
        });
      },
    },
  };
};
