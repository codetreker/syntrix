type Mode = 'shared' | 'exclusive';
export interface AliasLockOwner { readonly mode: Mode }
export interface ViewLockOwner { readonly mode: Mode }
export interface AliasLocks {
  withAlias<T>(mode: Mode, callback: (owner: AliasLockOwner) => Promise<T>, signal?: AbortSignal): Promise<T>;
  withView<T>(owner: AliasLockOwner, mode: Mode, callback: (owner: ViewLockOwner) => Promise<T>): Promise<T>;
  assertAliasOwner(owner: AliasLockOwner, mode?: Mode): void;
  assertViewOwner(owner: ViewLockOwner, mode?: Mode): void;
}

export const createAliasLocks = (namespaceHash: string, lockManager?: LockManager): AliasLocks => {
  const manager = lockManager ?? globalThis.navigator?.locks;
  if (!manager || (!lockManager && !globalThis.indexedDB)) throw new Error('Local storage requires IndexedDB and Web Locks');
  if (!/^[a-f0-9]{64}$/.test(namespaceHash)) throw new Error('Invalid namespace hash');
  const aliases = new Map<AliasLockOwner, { signal?: AbortSignal; activeView: boolean }>();
  const views = new Set<ViewLockOwner>();
  const assertAliasOwner = (owner: AliasLockOwner, mode?: Mode): void => {
    if (!aliases.has(owner) || (mode === 'exclusive' && owner.mode !== 'exclusive')) throw new Error('Invalid or released alias lock owner');
  };
  const assertViewOwner = (owner: ViewLockOwner, mode?: Mode): void => {
    if (!views.has(owner) || (mode === 'exclusive' && owner.mode !== 'exclusive')) throw new Error('Invalid or released view lock owner');
  };
  return {
    assertAliasOwner, assertViewOwner,
    withAlias: async <T>(mode: Mode, callback: (owner: AliasLockOwner) => Promise<T>, signal?: AbortSignal): Promise<T> => {
      return manager.request(`syntrix:${namespaceHash}:alias`, { mode, signal }, async () => {
        signal?.throwIfAborted();
        const owner = Object.freeze({ mode });
        aliases.set(owner, { signal, activeView: false });
        try { return await callback(owner); } finally { aliases.delete(owner); }
      });
    },
    withView: async <T>(owner: AliasLockOwner, mode: Mode, callback: (view: ViewLockOwner) => Promise<T>): Promise<T> => {
      assertAliasOwner(owner);
      const state = aliases.get(owner)!;
      state.signal?.throwIfAborted();
      if (state.activeView) throw new Error('Nested view lock acquisition is not supported');
      state.activeView = true;
      const run = async (): Promise<T> => {
        assertAliasOwner(owner);
        state.signal?.throwIfAborted();
        const view = Object.freeze({ mode });
        views.add(view);
        try { return await callback(view); } finally { views.delete(view); }
      };
      try {
        // An exclusive alias owner already excludes every ordinary view operation.
        return owner.mode === 'exclusive' ? await run() : await manager.request(`syntrix:${namespaceHash}:view`, { mode, signal: state.signal }, run);
      } finally { state.activeView = false; }
    },
  };
};
