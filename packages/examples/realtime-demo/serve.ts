import { readdir, realpath } from 'node:fs/promises';
import { join, sep } from 'node:path';
import { file, serve } from 'bun';

const port = Number(process.argv[2]);
if (!Number.isSafeInteger(port) || port < 1 || port > 65535) {
  throw new RangeError('Static server requires a port from 1 through 65535');
}
const root = await realpath(import.meta.dir);
const dist = await realpath(join(root, 'dist'));
if (!dist.startsWith(root + sep)) throw new Error('Demo dist must remain inside the demo directory');
const chunks = await readdir(join(dist, 'chunks'), { withFileTypes: true });
const built = new Set(['main.js', ...chunks.filter(entry => entry.isFile() && /^[\w-]+-[\w]{8,}\.js$/.test(entry.name))
  .map(entry => `chunks/${entry.name}`)]);

const server = serve({
  hostname: '127.0.0.1',
  port,
  async fetch(request) {
    if (request.method !== 'GET' && request.method !== 'HEAD') {
      return new Response('Method not allowed', { status: 405, headers: { Allow: 'GET, HEAD' } });
    }
    let pathname: string;
    try { pathname = decodeURIComponent(new URL(request.url).pathname); }
    catch { return new Response('Invalid path', { status: 400 }); }
    if (pathname === '/favicon.ico') return new Response(null, { status: 204 });
    const html = pathname === '/' || pathname === '/index.html';
    const asset = pathname.startsWith('/dist/') ? pathname.slice('/dist/'.length) : '';
    if (!html && !built.has(asset)) return new Response('Not found', { status: 404 });
    const filename = html ? join(root, 'index.html') : join(dist, asset);
    try {
      const actual = await realpath(filename);
      if (html ? actual !== filename : !actual.startsWith(dist + sep)) {
        return new Response('Not found', { status: 404 });
      }
      const content = file(actual);
      return new Response(request.method === 'HEAD' ? null : content, { headers: {
        'Content-Type': html ? 'text/html; charset=utf-8' : 'text/javascript; charset=utf-8',
        'Content-Length': String(content.size),
        'X-Content-Type-Options': 'nosniff',
        'Cache-Control': !html && asset.startsWith('chunks/') ? 'public, max-age=31536000, immutable' : 'no-cache',
      } });
    } catch (error) {
      if (typeof error === 'object' && error !== null && 'code' in error && ['ENOENT', 'ENOTDIR'].includes(String(error.code))) {
        return new Response('Not found', { status: 404 });
      }
      console.error(error);
      return new Response('Static file read failed', { status: 500 });
    }
  },
});

const stop = () => { server.stop(true); };
process.once('SIGINT', stop);
process.once('SIGTERM', stop);
console.log(`Serving the demo at http://127.0.0.1:${server.port}/`);
