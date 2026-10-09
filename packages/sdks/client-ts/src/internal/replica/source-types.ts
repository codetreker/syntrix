import type { QueryOrder } from '../../api/types.js';
import type { QueryValue } from '../../api/value.js';
import type { FrozenSourceDefinition } from './storage-types.js';
import type { RequestScope } from './storage.js';

export type SourceDocument = Record<string, QueryValue> & {
  id: string;
  collection: string;
  version: bigint;
  createdAt: bigint;
  updatedAt: bigint;
  deleted?: false;
};
export type SourceEvent = { type: 'upsert'; document: SourceDocument } | { type: 'leave' | 'delete'; id: string };
export type SourceEventsPage = {
  protocolVersion: 1;
  mode: 'events';
  databaseIdentity: string;
  sourceHash: string;
  events: SourceEvent[];
  checkpoint: string;
  generationId: string;
  phase: 'scan' | 'replay' | 'live';
  caughtUp: boolean;
  bootstrapComplete: boolean;
};
export type SourceWindow = {
  protocolVersion: 1;
  mode: 'replace';
  databaseIdentity: string;
  sourceHash: string;
  requestId: string;
  generationId: string;
  complete: true;
  effectiveOrder: QueryOrder[];
  documents: SourceDocument[];
};
export type SourceReadContext = {
  checkpoint: string | null;
  requestId: string;
  signal: AbortSignal;
  sessionVersion: number;
  expectedDatabaseIdentity: string | null;
  expectedSourceHash: string | null;
};
export type ReplicaSourceAdapter = {
  readonly definition: FrozenSourceDefinition;
  readonly mode: 'events' | 'replace';
  read(context: SourceReadContext): Promise<SourceEventsPage | SourceWindow>;
  committed?(requestId: string): void;
  released?(requestId: string): void;
  /** Explicit recovery may retry connection authentication without clearing source backoff. */
  resume?(): void;
  acquire?(context: SourceLeaseContext): SourceLease;
};
export type SourceLeaseContext = { scope: RequestScope; signal: AbortSignal; hint(): void };
export type SourceLease = ReplicaSourceAdapter & {
  /** Receipt failures are transport diagnostics and cannot reject a durable commit. */
  committed(requestId: string): void;
  released(requestId: string): void;
  invalidate(): void;
  close(): Promise<void>;
};
