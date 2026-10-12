import { run } from './run';

await run([process.execPath, 'run', 'test']);
await run([process.execPath, 'run', 'build']);
