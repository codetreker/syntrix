import { defaultHashSha256, getHeightOfRevision } from 'rxdb';
import { BackendCleanupError, openAliasBackend, ReadBudget, withRows, withScanPage, type AliasBackend } from './backend.js';
import { createNamespace } from './identity.js';
import { createAliasLocks } from './locks.js';
import { businessEqual, canonicalJson, validateManifestIdentity, validateRecordIdentity } from './records.js';
import { ReplicaStorageError, type AliasManifest } from './storage-types.js';
import type { OpenAliasStorageOptions } from './storage.js';

export type RemoveAliasStorageOptions = Omit<OpenAliasStorageOptions, 'source'> & { signal?: AbortSignal };
const fail = (code: string, message: string): never => { throw new ReplicaStorageError(code, message); };

/** The caller drains its coordinator before removal; storage locks fence peers. */
export const removeAliasStorage = (input: RemoveAliasStorageOptions): Promise<void> => {
  const options = { ...input };
  return options.session.track(async () => {
    const { session } = options;
    const signal = options.signal ? AbortSignal.any([session.signal, options.signal]) : session.signal;
    signal.throwIfAborted();
    const identity = await createNamespace(options.endpoint, options.database, options.name, options.alias, session.subject);
    const locks = createAliasLocks(identity.hash, options.lockManager);
    const budget = new ReadBudget(64 * 1024 * 1024);
    let backend: AliasBackend | undefined;
    await locks.withAlias('exclusive', async alias => {
      session.assertCurrent();
      let failure: unknown;
      let terminal = false;
      const admission = () => { session.assertCurrent(); if (!terminal) signal.throwIfAborted(); };
      try {
        backend = await openAliasBackend({ name: `syntrix-${identity.hash}`, limits: options.limits, storage: options.storage,
          beforeWrite: async () => { admission(); locks.assertAliasOwner(alias, 'exclusive'); } });
        const db = backend;
        const controlBudget = new ReadBudget(2 * db.limits.maxManifestBytes);
        const readManifest = () => withRows(db.manifestStorage, ['manifest'], db.limits.maxManifestBytes, controlBudget, async rows => {
          const manifest = rows[0];
          if (manifest) {
            await validateManifestIdentity(manifest);
            if (canonicalJson(manifest.namespace) !== canonicalJson(identity.tuple)) fail('ReplicaScopeChanged', 'Removal namespace differs from its durable binding');
          }
          return manifest;
        });
        let manifest = await readManifest();
        terminal = manifest?.state === 'removed';
        admission();
        if (!manifest) return;
        if (manifest.state !== 'removed') {
          if (manifest.dirtyUpstream || manifest.issues.length || manifest.recoveryIntent) fail('ReplicaRemovalBlocked', 'Resolve protected replica work before removing the alias');
          const physical = await db.openPhysical(manifest.activePhysicalEpoch);
          const originHash = await defaultHashSha256(physical.identifier);
          let after = 'd:';
          while (true) {
            admission();
            const next = await withScanPage(physical.fork, after, 1, db.limits.maxRecordBytes, budget, async rows => {
              const row = rows[0];
              if (!row || row.kind !== 'd') return undefined;
              await validateRecordIdentity(row);
              if (row.pin) fail('ReplicaRemovalBlocked', 'Settle pinned edits before removing the alias');
              const origin = row._meta.o;
              if (origin?.hash !== originHash || origin?._rev !== getHeightOfRevision(row._rev)) {
                await withRows(physical.meta, [`${row.key}|0`], db.limits.maxMetadataBytes, budget, async metadata => {
                  const assumed = metadata[0]?.docData;
                  if (assumed) {
                    await validateRecordIdentity(assumed);
                    if (assumed.kind !== 'd' || assumed.logicalId !== row.logicalId || assumed.key !== row.key) fail('ReplicaStorageCorruption', 'Removal assumed identity differs');
                  }
                  if (!assumed || !businessEqual(row, assumed)) fail('ReplicaRemovalBlocked', 'Synchronize pending edits before removing the alias');
                });
              }
              return row.key;
            });
            if (!next) break;
            after = next;
          }
          const next: AliasManifest = { ...manifest, state: 'removed', sourceReady: false, lastCompleteRound: null,
            activeSourceGeneration: null, stagedSourceGeneration: null, partialDelivery: false, maintenance: null };
          admission();
          try { manifest = await db.writeManifest(next, manifest, 'replica-remove'); }
          catch (error) {
            const actual = await readManifest();
            if (!actual || actual.lifecycleId !== next.lifecycleId || actual.state !== 'removed') throw error;
            manifest = actual;
          }
          terminal = true;
        }
        // A durable removed manifest remains as a lifetime fence. Reopening can
        // clean these owned stores after a crash before assigning a new lifetime.
        for (const epoch of manifest.physicalEpochs) {
          session.assertCurrent(); locks.assertAliasOwner(alias, 'exclusive');
          await db.removePhysical(epoch);
        }
        signal.throwIfAborted();
      } catch (error) { failure = error; throw error; }
      finally {
        if (backend) try { await backend.close(); }
        catch (error) { if (failure !== undefined) throw new BackendCleanupError(failure, [error]); throw error; }
      }
    }, signal);
  });
};
