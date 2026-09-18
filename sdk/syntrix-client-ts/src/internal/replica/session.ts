import { AuthSessionChangedError } from '../../api/errors.js';
import type { TokenProvider } from '../auth/types.js';
import { registerAuthOwner, supportsAuthOwnership } from '../auth/lifecycle.js';
import { parseReplicaSubject } from './identity.js';

export interface ReplicaResource { invalidate(): void; close(): Promise<void> }
export interface ReplicaSession {
  readonly subject: string;
  readonly version: number;
  readonly signal: AbortSignal;
  assertCurrent(): void;
  track<T>(operation: () => Promise<T>): Promise<T>;
  register(resource: ReplicaResource): () => void;
  close(): Promise<void>;
  drain(): Promise<void>;
}

export const createReplicaSession = async (provider: TokenProvider): Promise<ReplicaSession> => {
  if (!supportsAuthOwnership(provider)) throw new Error('Replica sessions require authentication lifecycle support');
  const version = provider.getSessionVersion();
  const controller = new AbortController();
  const cancellation = new AuthSessionChangedError();
  let subject: string | undefined;
  const resources = new Set<ReplicaResource>();
  const pending = new Set<Promise<unknown>>();
  let closing: Promise<void> | undefined;
  let unregister = () => {};
  const assertCurrent = (): void => {
    if (controller.signal.aborted || version !== provider.getSessionVersion()) throw cancellation;
  };
  const invalidationFailures: unknown[] = [];
  const invalidate = (): void => {
    if (controller.signal.aborted) return;
    controller.abort(cancellation);
    for (const resource of resources) {
      try { resource.invalidate(); } catch (error) { invalidationFailures.push(error); }
    }
  };
  const drain = async (): Promise<void> => {
    while (pending.size) await Promise.allSettled([...pending]);
  };
  const close = (): Promise<void> => {
    if (closing) return closing;
    invalidate();
    closing = (async () => {
      const results = await Promise.allSettled([...resources].map(resource => Promise.resolve().then(() => resource.close())));
      await drain();
      for (const result of results) if (result.status === 'rejected') invalidationFailures.push(result.reason);
      resources.clear();
      if (invalidationFailures.length) throw invalidationFailures[0];
      unregister();
    })();
    return closing;
  };
  unregister = registerAuthOwner(provider, {
    acceptsToken: (candidate, previousToken) => {
      let expectedSubject = subject;
      if (expectedSubject === undefined) {
        // A refresh can complete between the token snapshot and its awaiting
        // continuation. Compare the pre-install identity while that open is pending.
        try { expectedSubject = parseReplicaSubject(previousToken); } catch { return false; }
      }
      return parseReplicaSubject(candidate) === expectedSubject;
    },
    invalidate, close,
  });
  try {
    const token = await provider.getToken();
    assertCurrent();
    subject = parseReplicaSubject(token);
  } catch (error) {
    await close();
    throw error;
  }
  const session: ReplicaSession = {
    subject, version, signal: controller.signal, assertCurrent, close, drain,
    track: <T>(operation: () => Promise<T>): Promise<T> => {
      assertCurrent();
      const promise = Promise.resolve().then(() => { assertCurrent(); return operation(); }).then(value => { assertCurrent(); return value; });
      pending.add(promise);
      void promise.then(() => pending.delete(promise), () => pending.delete(promise));
      return promise;
    },
    register: resource => {
      assertCurrent();
      resources.add(resource);
      return () => { resources.delete(resource); };
    },
  };
  return session;
};
