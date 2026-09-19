import type { Observable } from 'rxjs';
import type { AliasInvalidation } from './storage.js';
import { ReplicaStorageError, type ReplicaDocument } from './storage-types.js';

export type QueryView = Readonly<{
  physicalEpoch: string;
  sourceGeneration: string | null;
  manifestRevision: string;
}>;

export type QueryProjection = Readonly<{
  id: string;
  key: string;
  revision: string;
  encodedBytes: number;
  visible: boolean;
  decode(): ReplicaDocument | null;
}>;

export class QueryViewChangedError extends ReplicaStorageError {
  constructor() { super('ReplicaQueryViewChanged', 'The authoritative query view changed'); }
}

export type AliasQueryAccess = {
  readonly databaseNamespace: string;
  readonly namespace: string;
  readonly signal: AbortSignal;
  readonly changes: Observable<AliasInvalidation>;
  view(): Promise<QueryView>;
  scan(after: string | undefined, expected: QueryView): Promise<{
    view: QueryView;
    rows: readonly { key: string; encodedBytes: number }[];
    lastKey?: string;
    done: boolean;
  }>;
  withProjection<T>(keyOrId: { key: string } | { id: string }, expected: QueryView,
    consume: (projection: QueryProjection | null) => Promise<T>): Promise<T>;
};
