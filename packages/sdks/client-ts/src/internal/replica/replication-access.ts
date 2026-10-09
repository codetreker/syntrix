import type { NativeRow } from './backend.js';
import type { AliasManifest, ControlRecord, DataRecord, MemberRecord, ReplicaRecord, StorageLimits } from './storage-types.js';

export type NativeUpCheckpoint = { id: string; lwt: number };
export type ReplicationDocument = {
  data: NativeRow<DataRecord> | undefined;
  member: NativeRow<MemberRecord> | undefined;
  assumed: DataRecord | undefined;
};

/** Row references are owned by the callback's materialization reservation. */
export type ReplicationAccess = {
  readonly manifest: NativeRow<AliasManifest>;
  readonly limits: StorageLimits;
  assertActive(): void;
  withDocument<T>(identity: string | { key: string }, consume: (document: ReplicationDocument) => Promise<T>): Promise<T>;
  scanData(afterKey: string | undefined, consume: (data: NativeRow<DataRecord>) => Promise<void>): Promise<string | undefined>;
  withControl<T>(consume: (control: NativeRow<ControlRecord> | undefined) => Promise<T>): Promise<T>;
  withUpCheckpoint<T>(consume: (checkpoint: NativeUpCheckpoint | undefined) => Promise<T>): Promise<T>;
  withDownCheckpoint<T>(consume: (checkpoint: { source: unknown } | undefined) => Promise<T>): Promise<T>;
  writeRecord(next: ReplicaRecord, previous?: NativeRow<ReplicaRecord>): Promise<void>;
  writeManifest(next: AliasManifest): Promise<void>;
};
