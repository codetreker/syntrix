import type { QueryValue } from '../../api/value.js';

export type UpstreamRequestContext = {
  signal: AbortSignal;
  sessionVersion: number;
  expectedDatabaseIdentity: string;
};
export type UpstreamPushContext = UpstreamRequestContext & { beforeDispatch?: () => Promise<void> };
export type PushOperation = {
  logicalId: string;
  action: 'create' | 'update' | 'delete';
  payload: Record<string, QueryValue>;
  baseVersion?: bigint;
};
export type RemoteDocument = Record<string, QueryValue> & {
  id: string;
  collection: string;
  version: bigint;
  createdAt: bigint;
  updatedAt: bigint;
  deleted?: boolean;
};
export type PushConflict = {
  changeIndex: number;
  id: string;
  reason: 'missing' | 'tombstoned' | 'version_mismatch' | 'already_exists' | 'precondition_failed';
  current: RemoteDocument | null;
};
export type PreparedPushBatch = {
  readonly body: string;
  readonly changes: readonly PushOperation[];
  readonly indexes: readonly number[];
  readonly httpBytes: number;
  readonly protobufBytes: number;
};
export type UpstreamTransport = {
  prepare(changes: readonly PushOperation[]): readonly PreparedPushBatch[];
  push(batch: PreparedPushBatch, context: UpstreamPushContext): Promise<{ conflicts: PushConflict[] }>;
  readCurrent(id: string, context: UpstreamRequestContext): Promise<RemoteDocument | null>;
};
