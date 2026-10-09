import type { FilterOp, QueryOrder } from './types.js';
import type { QueryValue } from './value.js';

export interface ReplicaSource<T = Record<string, QueryValue>> {
  where(field: string, op: FilterOp, value: QueryValue): ReplicaSource<T>;
  orderBy(field: string, direction?: 'asc' | 'desc'): ReplicaSource<T>;
  limit(count: number): ReplicaSource<T>;
}
export interface ReplicaDocumentMetadata {
  readonly id: string;
  readonly collection: string;
  readonly version?: bigint;
  readonly createdAt?: bigint;
  readonly updatedAt?: bigint;
}
export type ReplicaDocument<T = Record<string, QueryValue>> =
  | (Omit<T, keyof ReplicaDocumentMetadata | 'deleted'> & ReplicaDocumentMetadata & { readonly deleted?: false })
  | (ReplicaDocumentMetadata & { readonly deleted: true });
export interface ReplicaQueryPage<T = Record<string, QueryValue>> {
  documents: ReplicaDocument<T>[];
  nextCursor: string | null;
  effectiveOrder: readonly Readonly<QueryOrder>[];
}
export interface ReplicaQuery<T = Record<string, QueryValue>> {
  where(field: string, op: FilterOp, value: QueryValue): ReplicaQuery<T>;
  orderBy(field: string, direction?: 'asc' | 'desc'): ReplicaQuery<T>;
  limit(count: number): ReplicaQuery<T>;
  startAfter(cursor: string): ReplicaQuery<T>;
  showDeleted(show?: boolean): ReplicaQuery<T>;
  get(): Promise<ReplicaDocument<T>[]>;
  getPage(): Promise<ReplicaQueryPage<T>>;
  watch(onResult: (documents: ReplicaDocument<T>[]) => void, onError?: (error: unknown) => void): () => void;
}
export interface ReplicaDocumentReference<T = Record<string, QueryValue>> {
  readonly id: string;
  readonly path: string;
  get(options?: { showDeleted?: boolean }): Promise<ReplicaDocument<T> | null>;
  ifMatch(field: string, op: FilterOp, value: QueryValue): ReplicaDocumentReference<T>;
  set(data: T): Promise<void>;
  update(data: Partial<T>): Promise<void>;
  delete(): Promise<void>;
}
export interface ReplicaCollection<T = Record<string, QueryValue>> extends ReplicaQuery<T> {
  readonly alias: string;
  readonly path: string;
  doc(id?: string): ReplicaDocumentReference<T>;
  add(data: T): Promise<ReplicaDocumentReference<T>>;
}
export interface ReplicaStorageLimits {
  maxRecordBytes: number;
  maxManifestBytes: number;
  maxMetadataBytes: number;
  maxKnownIds: number;
  maxStoredBytes: number;
}
export interface ReplicaQueryLimits {
  readBytes: number;
  payloadBytes: number;
  keyBytes: number;
  nodes: number;
  scanCandidates: number;
  scanBytes: number;
  queuedKeys: number;
  queuedBytes: number;
  outputBytes: number;
  rebuildAttempts: number;
}
export interface ReplicaSyncOptions {
  pollIntervalMs?: number;
  hintDelayMs?: number;
  retryBaseMs?: number;
  retryMaxMs?: number;
  maintenanceBackoffMs?: number;
}
export interface ReplicaDiagnostic {
  readonly replicaId: string;
  readonly operationId: string;
  readonly sessionVersion: number;
  readonly alias: string;
  readonly operation: string;
  readonly phase: string;
  readonly timestamp: number;
  readonly code?: string;
  readonly physicalEpoch?: string;
  readonly durationMs?: number;
  readonly count?: number;
  readonly requestId?: string;
  readonly subId?: string;
  readonly transportEpoch?: number;
  readonly mode?: 'ws' | 'http';
}
export interface OpenReplicaOptions {
  name: string;
  collections: Record<string, ReplicaSource<unknown>>;
  storageLimits?: Partial<ReplicaStorageLimits>;
  queryLimits?: Partial<ReplicaQueryLimits>;
  sync?: ReplicaSyncOptions;
  onDiagnostic?: (event: ReplicaDiagnostic) => void;
}
export type ReplicaDocumentState<T = Record<string, QueryValue>> =
  | { existence: 'absent'; document: null }
  | { existence: 'live' | 'deleted'; document: ReplicaDocument<T> };
export interface ReplicaInspection<T = Record<string, QueryValue>> {
  issueId: string | null;
  stateToken: string;
  physicalEpoch: string;
  phase: { id: string; state: 'prepared' | 'dispatched' } | null;
  recovering: boolean;
  availableActions: { kind: ReplicaRecoveryDecision['kind']; issueId: string }[];
  issues: { id: string; documentId: string | null; code: string }[];
  targets: { id: string; editToken: string }[];
  document?: {
    id: string;
    editToken: string | null;
    desired: ReplicaDocumentState<T> | null;
    assumed: ReplicaDocumentState<T> | null;
    current?: { source: 'authoritative-read'; state: ReplicaDocumentState<T> };
  };
}
export type ReplicaRecoveryDecision<T = Record<string, QueryValue>> =
  | { kind: 'adopt-server'; issueId: string; id: string; editToken: string | null; physicalEpoch: string }
  | { kind: 'merge-local'; issueId: string; id: string; editToken: string | null; physicalEpoch: string; data: T }
  | { kind: 'retry-uncertain'; issueId: string; acknowledgeRepeatedEffects: true }
  | { kind: 'reset-alias'; issueId: string; stateToken: string; discardPending: true };
export interface ReplicaAliasStatus {
  readonly alias: string;
  readonly mode: 'events' | 'replace';
  readonly state: 'waiting' | 'syncing' | 'idle' | 'retrying' | 'paused' | 'blocked' | 'closed';
  readonly leader: boolean;
  readonly ready: boolean;
  readonly sourceReady: boolean;
  readonly physicalEpoch: string;
  readonly generation: string | null;
  readonly checkpoint: string | null;
  readonly lastCompleteRound: string | null;
  readonly pending: number;
  readonly pins: number;
  readonly issues: readonly { id: string; documentId: string | null; code: string }[];
  readonly error?: unknown;
  readonly retryAt?: number;
}
export interface ReplicaSyncStatus {
  readonly aliases: Readonly<Record<string, ReplicaAliasStatus>>;
}
export interface ReplicaSync {
  pause(alias?: string): Promise<void>;
  resume(alias?: string): Promise<void>;
  inspect<T = Record<string, QueryValue>>(alias: string, options?: { id?: string; readCurrent?: boolean }): Promise<ReplicaInspection<T>>;
  resolve<T = Record<string, QueryValue>>(alias: string, decision: ReplicaRecoveryDecision<T>): Promise<void>;
  subscribe(onStatus: (status: ReplicaSyncStatus) => void, onError?: (error: unknown) => void): () => void;
}
export interface ReplicaDatabase {
  readonly name: string;
  readonly sync: ReplicaSync;
  collection<T = Record<string, QueryValue>>(alias: string): ReplicaCollection<T>;
  removeCollection(alias: string): Promise<void>;
  close(): Promise<void>;
}
