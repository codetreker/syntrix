type Request = { mode: LockMode; signal?: AbortSignal; start(): void; reject(error: unknown): void };
export const createTestLockManager = (): LockManager => {
  const states = new Map<string, { active: LockMode[]; queue: Request[] }>();
  const request = (name: string, options: LockOptions, callback: LockGrantedCallback<unknown>): Promise<unknown> => {
    let state = states.get(name);
    if (!state) { state = { active: [], queue: [] }; states.set(name, state); }
    const target = state;
    const mode = options.mode ?? 'exclusive';
    return new Promise((resolve, reject) => {
      const pump = () => {
        while (target.queue.length) {
          const head = target.queue[0];
          if (target.active.includes('exclusive') || (head.mode === 'exclusive' && target.active.length)) return;
          target.queue.shift(); head.start();
        }
      };
      const abort = () => {
        const index = target.queue.indexOf(item);
        if (index >= 0) { target.queue.splice(index, 1); reject(options.signal?.reason); pump(); }
      };
      const item: Request = {
        mode, signal: options.signal, reject,
        start: () => {
          options.signal?.removeEventListener('abort', abort);
          target.active.push(mode);
          Promise.resolve().then(() => callback({ name, mode } as Lock)).then(resolve, reject).finally(() => {
            target.active.splice(target.active.indexOf(mode), 1); pump();
          });
        },
      };
      if (options.signal?.aborted) { reject(options.signal.reason); return; }
      options.signal?.addEventListener('abort', abort, { once: true });
      target.queue.push(item); pump();
    });
  };
  return { request } as LockManager;
};
