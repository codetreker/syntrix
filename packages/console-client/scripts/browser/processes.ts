import type { BunFile } from 'bun';

type ProcessOptions = {
  cwd: string;
  env: Record<string, string | undefined>;
  stdout: 'inherit' | 'pipe' | BunFile;
  stderr: 'inherit' | 'pipe' | BunFile;
};

const signalGroup = (pid: number, signal: NodeJS.Signals) => {
  try { process.kill(-pid, signal); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code !== 'ESRCH') throw error;
  }
};

export const createProcessOwner = () => {
  const active = new Set<{ stop: () => Promise<void> }>();
  let closed = false;
  let closing: Promise<void> | undefined;

  const start = (args: string[], options: ProcessOptions) => {
    if (closed) throw new Error('Browser acceptance process owner is closed.');
    const child = Bun.spawn(args, { ...options, stdin: 'ignore', detached: true });
    let stopping: Promise<void> | undefined;
    const finished = child.exited.finally(() => {
      // A wrapper can exit before its children; its whole group remains owned.
      signalGroup(child.pid, 'SIGKILL');
      active.delete(owned);
    });
    const stop = () => stopping ??= (async () => {
      signalGroup(child.pid, 'SIGTERM');
      let timer: ReturnType<typeof setTimeout> | undefined;
      try {
        await Promise.race([finished, new Promise<void>((accept) => {
          timer = setTimeout(() => { signalGroup(child.pid, 'SIGKILL'); accept(); }, 2000);
        })]);
        signalGroup(child.pid, 'SIGKILL');
        await finished;
      } finally { clearTimeout(timer); }
    })();
    const owned = { stop };
    active.add(owned);
    return { child, finished, stop };
  };

  return {
    start,
    close: () => {
      closed = true;
      return closing ??= Promise.all([...active].map((owned) => owned.stop())).then(() => {});
    },
  };
};
