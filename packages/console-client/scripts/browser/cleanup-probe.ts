import { SQL } from 'bun';
import { randomBytes } from 'node:crypto';
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = fileURLToPath(new URL('../..', import.meta.url));
const directory = join(packageRoot, '.tmp', 'browser-cleanup', randomBytes(6).toString('hex'));
const marker = join(directory, 'submission.json');
const release = join(directory, 'release');
const wrapper = join(directory, 'bin', 'docker');
const worker = join(directory, 'delayed-run.ts');
const docker = Bun.which('docker');
if (!docker) throw new Error('Docker is required for the acceptance cleanup probe.');
const dsn = process.env.CONSOLE_BROWSER_POSTGRES_DSN;
if (!dsn) throw new Error('CONSOLE_BROWSER_POSTGRES_DSN must select the dedicated acceptance database.');
await mkdir(join(directory, 'bin'), { recursive: true, mode: 0o700 });

await writeFile(worker, `import { writeFile, access, rename } from 'node:fs/promises';
const args = process.argv.slice(2);
process.on('SIGTERM', () => {});
await writeFile(${JSON.stringify(marker + '.pending')}, JSON.stringify({ pid: process.pid, group: process.ppid, name: args[args.indexOf('--name') + 1] }));
await rename(${JSON.stringify(marker + '.pending')}, ${JSON.stringify(marker)});
for (;;) { try { await access(${JSON.stringify(release)}); break; } catch (error) { if (error.code !== 'ENOENT') throw error; } await Bun.sleep(50); }
const child = Bun.spawn([${JSON.stringify(docker)}, ...args], { stdout: 'inherit', stderr: 'inherit' });
process.exit(await child.exited);
`, { mode: 0o600 });
await writeFile(wrapper, `#!/usr/bin/env bun
const args = process.argv.slice(2);
const command = args[0] === 'run' ? [process.execPath, ${JSON.stringify(worker)}, ...args] : [${JSON.stringify(docker)}, ...args];
const child = Bun.spawn(command, { stdout: 'inherit', stderr: 'inherit' });
process.exit(await child.exited);
`, { mode: 0o700 });

const harness = Bun.spawn([process.execPath, join(packageRoot, 'scripts', 'browser-acceptance.ts')], {
  cwd: packageRoot,
  env: { ...process.env, PATH: `${join(directory, 'bin')}:${process.env.PATH}` },
  stdin: 'ignore', stdout: Bun.file(join(directory, 'harness.log')), stderr: Bun.file(join(directory, 'harness.stderr.log')),
  detached: true,
});
let submitted: { pid: number; group: number; name: string } | undefined;
let interrupted = false;
const interrupt = () => {
  interrupted = true;
  try { process.kill(harness.pid, 'SIGTERM'); }
  catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ESRCH') throw error; }
};
for (const signal of ['SIGINT', 'SIGTERM'] as const) process.once(signal, interrupt);
const dockerCommand = async (args: string[]) => {
  const child = Bun.spawn([docker, ...args], { stdin: 'ignore', stdout: 'pipe', stderr: 'pipe', timeout: 10_000 });
  const [exit, text] = await Promise.all([child.exited, new Response(child.stdout).text(), new Response(child.stderr).text()]);
  if (exit !== 0) throw new Error('Docker failed while validating acceptance cleanup.');
  return text.trim();
};
const signalGroup = (group: number) => {
  try { process.kill(-group, 'SIGKILL'); }
  catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ESRCH') throw error; }
};
let passed = false;
try {
  console.log('Waiting for the delayed Docker acquisition and its TERM-resistant descendant.');
  const until = Date.now() + 75_000;
  while (Date.now() < until && !submitted) {
    if (interrupted) throw new Error('Acceptance cleanup probe was interrupted.');
    try { submitted = JSON.parse(await readFile(marker, 'utf8')) as typeof submitted; }
    catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error; }
    if (!submitted) await Bun.sleep(100);
  }
  if (!submitted || !/^console-client-browser-[a-f0-9]{12}$/.test(submitted.name)) throw new Error('The cleanup probe did not reach owned Docker acquisition.');
  process.kill(harness.pid, 'SIGTERM');
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    const exit = await Promise.race([harness.exited, new Promise<never>((_accept, reject) => { timer = setTimeout(() => reject(new Error('Acceptance SIGTERM did not finish within 20 seconds.')), 20_000); })]);
    if (exit === 0) throw new Error('The interrupted acceptance run incorrectly reported success.');
  } finally { clearTimeout(timer); }
  const identity = submitted.name.slice('console-client-browser-'.length);
  const receipt = JSON.parse(await readFile(join(packageRoot, '.tmp', 'browser', identity, 'receipts.json'), 'utf8')) as { passed: boolean; cleanup_completed: boolean };
  if (receipt.passed || !receipt.cleanup_completed) throw new Error('The interruption receipt does not confirm failed work and completed cleanup.');
  await writeFile(release, 'release');
  await Bun.sleep(2000);
  const existing = await dockerCommand(['container', 'ls', '--all', '--filter', `name=^/${submitted.name}$`, '--format', '{{.Names}}']);
  if (existing !== '') throw new Error('A delayed Docker acquisition created a container after harness exit.');
  try {
    const args = await readFile(`/proc/${submitted.pid}/cmdline`, 'utf8');
    if (args.includes(worker)) throw new Error('A TERM-resistant acquisition descendant survived cleanup.');
  } catch (error) { if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error; }
  passed = true;
  console.log('PASS SIGTERM drains delayed Docker acquisition and descendants before resource cleanup.');
} finally {
  if (harness.exitCode === null) {
    interrupt();
    let timer: ReturnType<typeof setTimeout> | undefined;
    try { await Promise.race([harness.exited, new Promise<void>((accept) => { timer = setTimeout(accept, 20_000); })]); }
    finally { clearTimeout(timer); }
  }
  signalGroup(harness.pid);
  await harness.exited;
  if (submitted) {
    signalGroup(submitted.group);
    const existing = await dockerCommand(['container', 'ls', '--all', '--filter', `name=^/${submitted.name}$`, '--format', '{{.Names}}']);
    if (existing !== '') await dockerCommand(['rm', '--force', submitted.name]);
    const identity = submitted.name.slice('console-client-browser-'.length);
    const database = new SQL(dsn, { max: 1, connectionTimeout: 5, connection: { statement_timeout: 10_000 } });
    try { for (const phase of ['development', 'production']) await database.unsafe(`DROP SCHEMA IF EXISTS "console_browser_${identity}_${phase}" CASCADE`); }
    finally { await database.close({ timeout: 1 }); }
  }
  await rm(join(directory, 'bin'), { recursive: true, force: true });
  await rm(worker, { force: true });
  await rm(release, { force: true });
  await writeFile(join(directory, 'receipts.json'), JSON.stringify({ passed, harness_identity: submitted?.name, descendants_stopped: passed, late_container_absent: passed }, null, 2));
  console.log(`Cleanup probe receipts: ${directory}`);
}
