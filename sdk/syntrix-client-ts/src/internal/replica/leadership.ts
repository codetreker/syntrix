import { getBroadcastChannelReference, removeBroadcastChannelReference } from 'rxdb';
import { getLeaderElectorByBroadcastChannel } from 'rxdb/plugins/leader-election';
import { ReplicaStorageError } from './storage-types.js';

export class ReplicaCleanupError extends ReplicaStorageError {
  constructor(readonly errors: readonly unknown[]) {
    super('ReplicaCleanupFailed', 'Replica cleanup encountered multiple failures', { cause: errors[0] });
  }
}
export const throwCleanupFailures = (errors: readonly unknown[]): void => {
  if (errors.length === 1) throw errors[0];
  if (errors.length > 1) throw new ReplicaCleanupError(errors);
};

export interface ReplicaLeadership {
  wait(signal: AbortSignal): Promise<void>;
  close(): Promise<void>;
}

export const createReplicaLeadership = (namespace: string): ReplicaLeadership => {
  const token = crypto.randomUUID();
  const owner = {};
  const channel = getBroadcastChannelReference('syntrix-replica-coordinator', token, namespace, owner);
  const elector = getLeaderElectorByBroadcastChannel(channel);
  let closing: Promise<void> | undefined;
  return {
    wait: signal => new Promise<void>((resolve, reject) => {
      const abort = () => { signal.removeEventListener('abort', abort); reject(signal.reason); };
      if (signal.aborted) { abort(); return; }
      signal.addEventListener('abort', abort, { once: true });
      // A follower's awaitLeadership can remain pending after die(). Only the
      // cancellable wrapper is owned by the coordinator's drain barrier.
      void elector.awaitLeadership().then(() => {
        signal.removeEventListener('abort', abort);
        if (signal.aborted) reject(signal.reason); else resolve();
      }, error => { signal.removeEventListener('abort', abort); reject(error); });
    }),
    close: () => closing ??= (async () => {
      const failures: unknown[] = [];
      try { await elector.die(); } catch (error) { failures.push(error); }
      try { await removeBroadcastChannelReference(token, owner); } catch (error) { failures.push(error); }
      throwCleanupFailures(failures);
    })(),
  };
};
