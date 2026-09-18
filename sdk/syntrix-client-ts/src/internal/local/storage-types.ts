import type { FilterOp, QueryOrder } from '../../api/types.js';
import type { QueryValue, TypedValue } from '../../api/value.js';

export type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue };
export type JsonObject = { [key: string]: JsonValue };
export type Existence = 'live' | 'deleted' | 'absent';
export type LocalCondition = { field: string; op: FilterOp; value: QueryValue };
export type LocalSourceDefinition = {
  collection: string;
  filters: LocalCondition[];
  orderBy?: QueryOrder[];
  limit?: number;
};
export type FrozenSourceDefinition = {
  collection: string;
  filters: { field: string; op: FilterOp; value: TypedValue }[];
  orderBy: QueryOrder[];
  limit: number | null;
};
export type NamespaceTuple = { endpoint: string; subject: string; database: string; name: string; alias: string };
export type WireMetadata = { version?: string; createdAt?: string; updatedAt?: string };
export type EditPin = { token: string; stage: 'await-settlement' | 'await-source'; settledRound?: string };
export type DataRecord = {
  key: string;
  kind: 'd';
  logicalId: string;
  existence: Existence;
  payload: string;
  editToken: string | null;
  pin: EditPin | null;
  wire: WireMetadata;
};
export type MemberRecord = {
  key: string;
  kind: 'm';
  logicalId: string;
  slots: { generation: string; member: boolean }[];
  observedExistence: Existence | null;
  metadata: WireMetadata;
};
export type ControlRecord = {
  key: 'c:progress';
  kind: 'c';
  checkpoint: JsonObject;
  generation: string | null;
  phase: string;
  bootstrapComplete: boolean;
  partialDelivery: boolean;
};
export type LocalRecord = DataRecord | MemberRecord | ControlRecord;
export type StorageIssue = { id: string; logicalId: string | null; code: string };
export type UpstreamMarker = {
  id: string;
  session: number;
  physicalEpoch: string;
  mayHaveDispatched: boolean;
  targets: { logicalId: string; token: string }[];
};
export type MaintenanceState = { id: string; oldEpoch: string; newEpoch: string; stage: 'staging' | 'flipped' };
export type RecoveryIntent = {
  id: string;
  action: 'adopt' | 'merge';
  logicalId: string;
  protectedToken: string | null;
  resultToken: string;
  current: DataRecord;
  desired: DataRecord;
};
export type AliasManifest = {
  key: 'manifest';
  formatVersion: 1;
  namespace: NamespaceTuple;
  definition: FrozenSourceDefinition;
  definitionHash: string;
  boundDatabaseId: string | null;
  sourceHash: string | null;
  state: 'creating' | 'ready';
  activePhysicalEpoch: string;
  physicalEpochs: string[];
  maintenance: MaintenanceState | null;
  activeSourceGeneration: string | null;
  stagedSourceGeneration: string | null;
  sourceReady: boolean;
  partialDelivery: boolean;
  dirtyUpstream: UpstreamMarker | null;
  issues: StorageIssue[];
  recoveryIntent: RecoveryIntent | null;
};
export type LocalMetadata = {
  readonly id: string;
  readonly collection: string;
  readonly version?: bigint;
  readonly createdAt?: bigint;
  readonly updatedAt?: bigint;
};
export type LocalDocument<T = Record<string, QueryValue>> =
  | (Omit<T, keyof LocalMetadata | 'deleted'> & LocalMetadata & { readonly deleted?: false })
  | (LocalMetadata & { readonly deleted: true });
export type StorageLimits = {
  maxRecordBytes: number;
  maxManifestBytes: number;
  maxMetadataBytes: number;
  maxKnownIds: number;
  maxStoredBytes: number;
};
export const defaultStorageLimits: StorageLimits = {
  maxRecordBytes: 16 * 1024 * 1024,
  maxManifestBytes: 34 * 1024 * 1024,
  // Native assumed metadata nests a record and needs space for its own envelope.
  maxMetadataBytes: 17 * 1024 * 1024,
  maxKnownIds: 100_000,
  maxStoredBytes: Number.MAX_SAFE_INTEGER,
};

export class LocalStorageError extends Error {
  constructor(readonly code: string, message: string, options?: { cause: unknown }) {
    super(message);
    this.name = 'LocalStorageError';
    if (options) Object.assign(this, { cause: options.cause });
  }
}
