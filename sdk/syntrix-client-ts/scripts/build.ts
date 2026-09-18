import { createHash } from 'node:crypto';
import { readdir, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { dirname, join, resolve } from 'node:path';
import { brotliCompressSync, gzipSync } from 'node:zlib';
import integrity from './runtime-integrity.json';
import supplementalLicenses from './runtime-licenses.json';

const root = resolve(import.meta.dir, '..');
const require = createRequire(import.meta.url);
const rxdbRoot = dirname(require.resolve('rxdb/package.json'));
const hash = (data: Buffer) => createHash('sha256').update(data).digest('hex');
const check: (condition: unknown, message: string) => asserts condition = (condition, message) => {
  if (!condition) throw new Error(message);
};

for (const [name, version] of Object.entries(integrity.versions)) {
  const location = require.resolve(`${name}/package.json`, { paths: [rxdbRoot] });
  const actual = JSON.parse(await readFile(location, 'utf8')).version;
  check(actual === version, `${name}: expected ${version}, found ${actual}`);
}
check(hash(await readFile(join(root, 'patches/rxdb@17.5.0.patch'))) === integrity.patch,
  'RxDB patch checksum changed; inspect and update the runtime integrity manifest');
for (const [file, expected] of Object.entries(integrity.files)) {
  check(hash(await readFile(join(rxdbRoot, file))) === expected, `RxDB patch missing or changed: ${file}`);
}

await rm(join(root, 'dist'), { recursive: true, force: true });
const compiler = Bun.spawn([process.execPath, require.resolve('typescript/bin/tsc'), '-p', 'tsconfig.build.json'], {
  cwd: root, stdout: 'inherit', stderr: 'inherit',
});
check(await compiler.exited === 0, 'TypeScript declaration build failed');
const local = await Bun.build({
  entrypoints: [join(root, 'src/internal/local/runtime.ts')],
  target: 'browser', format: 'esm', minify: true, metafile: true,
});
check(local.success, local.logs.map(String).join('\n'));
check(local.metafile && local.outputs.length === 1, 'Local runtime must build as one self-contained chunk');
for (const output of Object.values(local.metafile.outputs)) {
  check(output.imports.length === 0, 'Local runtime must not import consumer-installed vendor dependencies');
}
const localBytes = Buffer.from(await local.outputs[0].arrayBuffer());
await writeFile(join(root, 'dist/internal/local/runtime.js'), localBytes);

// License every bundled package, including transitive dependencies selected by tree shaking.
const packages = new Map<string, string>();
for (const input of Object.keys(local.metafile.inputs)) {
  if (!input.includes('node_modules/')) continue;
  let directory = dirname(await realpath(resolve(root, input)));
  while (directory !== dirname(directory)) {
    const files = await readdir(directory);
    if (files.includes('package.json')) {
      const pkg = JSON.parse(await readFile(join(directory, 'package.json'), 'utf8'));
      // Some packages use nested package.json files solely to declare module type.
      if (pkg.name && pkg.version) {
        packages.set(`${pkg.name}@${pkg.version}`, directory);
        break;
      }
    }
    directory = dirname(directory);
  }
}
const notices = ['Bundled local replication runtime dependencies.\nRxDB replication-protocol files are modified by the accompanying SDK source patch.\n'];
for (const [name, directory] of [...packages].sort(([a], [b]) => a.localeCompare(b))) {
  const files = (await readdir(directory)).filter((file) => /^(licen[sc]e|copying|notice)(\.|$)/i.test(file));
  notices.push(`\n===== ${name} =====\n`);
  if (files.length === 0) {
    const license = (supplementalLicenses as Record<string, { file: string; sha256: string; source: string }>)[name];
    check(license, `Missing bundled dependency license: ${name}`);
    const bytes = await readFile(join(root, license.file));
    check(hash(bytes) === license.sha256, `Bundled license checksum changed: ${name}`);
    notices.push(`Source: ${license.source}\n`, bytes.toString('utf8'));
  }
  for (const file of files.sort()) notices.push(await readFile(join(directory, file), 'utf8'));
}
await writeFile(join(root, 'dist/THIRD_PARTY_NOTICES.txt'), notices.join('\n'));

const remote = await Bun.build({
  entrypoints: [join(root, 'src/index.ts')], target: 'browser', format: 'esm',
  minify: true, metafile: true, external: ['axios'],
});
check(remote.success && remote.metafile, 'Remote entry validation failed');
check(!Object.keys(remote.metafile.inputs).some((input) => input.includes('/internal/local/')),
  'Remote entry must not load the private local runtime');
const remoteBytes = Buffer.from(await remote.outputs[0].arrayBuffer());
for (const [name, bytes] of [['local runtime', localBytes], ['REST/realtime entry (axios external)', remoteBytes]] as const) {
  console.log(`${name}: ${bytes.length} bytes, gzip ${gzipSync(bytes).length}, brotli ${brotliCompressSync(bytes).length}`);
}
console.log(`Bundled licenses: ${packages.size}; patched files verified: ${Object.keys(integrity.files).length}`);
