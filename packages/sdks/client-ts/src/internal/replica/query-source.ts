import type { Observable } from 'rxjs';
import { defaultHashSha256 } from 'rxdb';
import type { ReadBudget } from './backend.js';
import { canonicalJson } from './records.js';
import type { AliasInvalidation } from './storage.js';
import { ReplicaStorageError, type AliasManifest, type ReplicaDocument } from './storage-types.js';

export type QueryView = Readonly<{
  physicalEpoch: string;
  sourceGeneration: string | null;
  visibilityHash: string;
}>;

type VisibilityManifest = Pick<AliasManifest, 'activePhysicalEpoch' | 'activeSourceGeneration' | 'dirtyUpstream' | 'issues'> & { _rev: string };

const jsonStringSize = (value: string): { units: number; bytes: number } => {
  let units = 2, bytes = 2;
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code === 34 || code === 92 || code === 8 || code === 9 || code === 10 || code === 12 || code === 13) { units += 2; bytes += 2; }
    else if (code < 32) { units += 6; bytes += 6; }
    else if (code >= 0xd800 && code <= 0xdbff && index + 1 < value.length && value.charCodeAt(index + 1) >= 0xdc00 && value.charCodeAt(index + 1) <= 0xdfff) {
      units += 2; bytes += 4; index++;
    } else if (code >= 0xd800 && code <= 0xdfff) { units += 6; bytes += 6; }
    else { units++; bytes += code < 128 ? 1 : code < 2048 ? 2 : 3; }
  }
  return { units, bytes };
};

/** Call only after validating the current manifest's identity and lifecycle. */
export const createVisibilityHashCache = () => {
  let latest: { revision: string; hash: string } | undefined;
  return async (manifest: VisibilityManifest, budget: ReadBudget): Promise<string> => {
    if (latest?.revision === manifest._rev) return latest.hash;
    const count = (manifest.dirtyUpstream?.targets.length ?? 0) + manifest.issues.length;
    return budget.withReservation(1024 + count * 128, async () => {
      const unique = new Set<string>();
      for (const target of manifest.dirtyUpstream?.targets ?? []) unique.add(target.logicalId);
      for (const issue of manifest.issues) if (issue.logicalId !== null) unique.add(issue.logicalId);
      const ids = [...unique].sort();
      // Outer/inner brackets and separators are ASCII; each scalar is counted
      // without allocating JSON or UTF-8. The reservation covers six UTF-16
      // canonical intermediates and both the encoder and digest input buffers.
      let units = 6 + Math.max(0, ids.length - 1), bytes = units;
      const add = (value: string) => { const size = jsonStringSize(value); units += size.units; bytes += size.bytes; };
      add(manifest.activePhysicalEpoch);
      if (manifest.activeSourceGeneration === null) { units += 4; bytes += 4; }
      else add(manifest.activeSourceGeneration);
      for (const id of ids) add(id);
      return budget.withReservation(1024 + units * 12 + bytes * 2, async () => {
        const hash = await defaultHashSha256(canonicalJson([manifest.activePhysicalEpoch, manifest.activeSourceGeneration, ids]));
        latest = { revision: manifest._rev, hash };
        return hash;
      });
    });
  };
};

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
