import { run } from './run';

await run([process.execPath, 'run', 'i18n:check']);
await run(['node_modules/.bin/tsc', '--noEmit']);
await run(['node_modules/.bin/vite', 'build']);
