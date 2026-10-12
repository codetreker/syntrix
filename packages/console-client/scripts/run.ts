export const run = async (command: string[]) => {
  const child = Bun.spawn(command, { stdout: 'inherit', stderr: 'inherit', stdin: 'ignore', timeout: 180_000 });
  const code = await child.exited;
  if (code !== 0) throw new Error(`${command[0]} failed with exit code ${code}.`);
};
