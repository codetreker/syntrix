/** Opt-in ownership hooks; the remote client does not load replica storage code. */
export interface AuthOwner {
  acceptsToken(token: string, previousToken: string | null): boolean;
  invalidate(): void;
  close(): Promise<void>;
}

interface AuthOwnership {
  readonly version: 1;
  register(owner: AuthOwner): () => void;
  acceptsToken(token: string, previousToken: string | null): boolean;
  invalidate(): Promise<void> | null;
}

// The lazy replica bundle and remote entry have separate module instances. The
// capability belongs to the provider, so both copies operate on the same owners.
const ownershipKey = Symbol.for('@syntrix/client/auth-ownership/v1');
const getOwnership = (provider: object): AuthOwnership | undefined =>
  Object.getOwnPropertyDescriptor(provider, ownershipKey)?.value as AuthOwnership | undefined;

export const enableAuthOwnership = (provider: object): void => {
  if (getOwnership(provider)) return;
  const owners = new Set<AuthOwner>();
  const ownership: AuthOwnership = {
    version: 1,
    register: owner => {
      owners.add(owner);
      return () => { owners.delete(owner); };
    },
    acceptsToken: (token, previousToken) => {
      for (const owner of owners) if (!owner.acceptsToken(token, previousToken)) return false;
      return true;
    },
    invalidate: () => {
      if (!owners.size) return null;
      const current = [...owners];
      owners.clear();
      const failures: unknown[] = [];
      for (const owner of current) {
        try { owner.invalidate(); } catch (error) { failures.push(error); }
      }
      return Promise.allSettled(current.map(owner => Promise.resolve().then(() => owner.close())))
        .then(results => {
          for (const result of results) if (result.status === 'rejected') failures.push(result.reason);
          if (failures.length) throw failures[0];
        });
    },
  };
  Object.defineProperty(provider, ownershipKey, { value: Object.freeze(ownership) });
};

export const supportsAuthOwnership = (provider: object): boolean => getOwnership(provider)?.version === 1;
export const registerAuthOwner = (provider: object, owner: AuthOwner): (() => void) => {
  const ownership = getOwnership(provider);
  if (!ownership || ownership.version !== 1) throw new Error('Replica sessions require authentication lifecycle support');
  return ownership.register(owner);
};
export const authOwnersAcceptToken = (provider: object, token: string, previousToken: string | null): boolean =>
  getOwnership(provider)?.acceptsToken(token, previousToken) ?? true;
export const invalidateAuthOwners = (provider: object): Promise<void> | null =>
  getOwnership(provider)?.invalidate() ?? null;
