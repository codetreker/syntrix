import { mkdtemp, mkdir, readdir, readFile, rm, copyFile, writeFile } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { createRequire } from 'node:module';

const root = resolve(import.meta.dir, '..');
const temporaryRoot = resolve(root, '../../.tmp');
await mkdir(temporaryRoot, { recursive: true });
const directory = await mkdtemp(join(temporaryRoot, 'sdk-package-'));
const run = async (command: string[], cwd: string) => {
  const child = Bun.spawn(command, { cwd, stdout: 'inherit', stderr: 'inherit' });
  if (await child.exited !== 0) throw new Error(`Package validation failed: ${command[0]}`);
};
try {
  await run(['pnpm', 'pack', '--pack-destination', directory], root);
  const tarball = (await readdir(directory)).find((name) => name.endsWith('.tgz'));
  if (!tarball) throw new Error('pnpm pack did not create a tarball');
  const consumer = join(directory, 'consumer');
  await mkdir(consumer);
  await writeFile(join(consumer, 'package.json'), JSON.stringify({ private: true, type: 'module' }));
  await run(['npm', 'install', '--ignore-scripts', '--no-audit', '--no-fund', '--cache', join(directory, 'cache'),
    join(directory, tarball), 'fake-indexeddb@6.2.5'], consumer);
  const installed = await readdir(join(consumer, 'node_modules'));
  for (const vendor of ['rxdb', 'dexie', 'rxjs']) {
    if (installed.includes(vendor)) throw new Error(`Consumer unexpectedly installed ${vendor}`);
  }
  const notices = await readFile(join(consumer, 'node_modules/@syntrix/client/dist/THIRD_PARTY_NOTICES.txt'), 'utf8');
  if (!notices.includes('rxdb@17.5.0') || !notices.includes('dexie@4.4.2') || !notices.includes('rxjs@7.8.2')) {
    throw new Error('Packed runtime dependency licenses are incomplete');
  }
  await copyFile(join(root, 'scripts/fixtures/package-consumer.mjs'), join(consumer, 'consumer.mjs'));
  const locks = await readFile(join(root, 'src/internal/replica/lock-manager.test-fixture.ts'), 'utf8');
  await writeFile(join(consumer, 'test-locks.mjs'), new Bun.Transpiler({ loader: 'ts', target: 'browser' }).transformSync(locks));
  await writeFile(join(consumer, 'consumer.ts'),
    "import { SyntrixClient } from '@syntrix/client';\nconst client: typeof SyntrixClient = SyntrixClient;\nvoid client;\n");
  const require = createRequire(import.meta.url);
  await run([process.execPath, require.resolve('typescript/bin/tsc'), '--noEmit', '--strict',
    '--target', 'ES2020', '--module', 'ESNext', '--moduleResolution', 'bundler', '--lib', 'ES2020,DOM', 'consumer.ts'], consumer);
  await run([process.execPath, 'consumer.mjs'], consumer);
} finally {
  await rm(directory, { recursive: true, force: true });
}
