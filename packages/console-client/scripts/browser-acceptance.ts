import { chromium, expect, type Browser, type BrowserContext, type Page, type Request } from '@playwright/test';
import { SQL } from 'bun';
import { randomBytes } from 'node:crypto';
import { mkdir, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer as httpsServer } from 'node:https';
import { request as httpRequest } from 'node:http';
import { createServer as netServer } from 'node:net';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = fileURLToPath(new URL('..', import.meta.url));
const repositoryRoot = resolve(packageRoot, '../..');
const identity = randomBytes(6).toString('hex');
const output = join(packageRoot, '.tmp', 'browser', identity);
const temporary = join(output, 'runtime');
const image = process.env.CONSOLE_BROWSER_IMAGE ?? 'console-client:acceptance';
const configuredDSN = process.env.CONSOLE_BROWSER_POSTGRES_DSN;
if (!configuredDSN) throw new Error('CONSOLE_BROWSER_POSTGRES_DSN must select a dedicated acceptance PostgreSQL database.');
const databaseURL = new URL(configuredDSN);
if (!['postgres:', 'postgresql:'].includes(databaseURL.protocol)) throw new Error('CONSOLE_BROWSER_POSTGRES_DSN must be a PostgreSQL URL.');
const password = randomBytes(20).toString('base64url');
const cleanup: (() => Promise<void>)[] = [];
const receipts: { check: string; origin?: string; status?: number; requestId?: string }[] = [];
const deadline = Date.now() + 270_000;
let closing: Promise<void> | undefined;
let disposing = false;
let browser: Browser | undefined;
let failure = false;
let cleanupFailure = false;

const remaining = () => Math.max(1, deadline - Date.now());
const clean = () => closing ??= (async () => {
  disposing = true;
  for (const dispose of cleanup.reverse()) {
    try { await dispose(); }
    catch { failure = true; cleanupFailure = true; console.error('Acceptance cleanup failed for an owned resource.'); }
  }
  await rm(temporary, { recursive: true, force: true });
  await writeFile(join(output, 'receipts.json'), JSON.stringify({ passed: !failure, receipts, cleanup_completed: !cleanupFailure }, null, 2));
})();

const command = async (args: string[], options: { cwd?: string; env?: Record<string, string | undefined>; quiet?: boolean } = {}) => {
  const child = Bun.spawn(args, {
    cwd: options.cwd ?? packageRoot,
    env: { ...process.env, ...options.env },
    stdin: 'ignore', stdout: options.quiet ? 'pipe' : 'inherit', stderr: options.quiet ? 'pipe' : 'inherit',
    timeout: disposing ? 10_000 : Math.min(remaining(), 120_000),
  });
  const out = options.quiet ? new Response(child.stdout as ReadableStream).text() : Promise.resolve('');
  const error = options.quiet ? new Response(child.stderr as ReadableStream).text() : Promise.resolve('');
  const [code, text] = await Promise.all([child.exited, out, error]);
  if (code !== 0) throw new Error(`${args[0]} failed during browser acceptance (exit ${code}).`);
  return text.trim();
};

const sql = async (query: string, dsn = configuredDSN) => {
  const scoped = new URL(dsn);
  const schema = scoped.searchParams.get('search_path');
  const connection = new SQL(dsn, { max: 1, connectionTimeout: 5, idleTimeout: 0, connection: { ...(schema !== null ? { search_path: schema } : {}), statement_timeout: 10_000, lock_timeout: 5000 } });
  try { return await connection.unsafe(query); }
  finally { await connection.close({ timeout: 1 }); }
};
const freePort = () => new Promise<number>((accept, reject) => {
  const listener = netServer();
  listener.once('error', reject);
  listener.listen(0, '127.0.0.1', () => {
    const address = listener.address();
    if (!address || typeof address === 'string') { listener.close(); reject(new Error('Could not reserve an acceptance port.')); return; }
    listener.close((error) => error ? reject(error) : accept(address.port));
  });
});

const waitHTTP = async (url: string) => {
  const until = Math.min(deadline, Date.now() + 20_000);
  while (Date.now() < until) {
    try { const response = await fetch(url, { signal: AbortSignal.timeout(1000) }); if (response.ok) return; }
    catch { /* Startup readiness is retried until the bounded deadline. */ }
    await Bun.sleep(100);
  }
  throw new Error('Acceptance service did not become ready within its startup deadline.');
};

const service = (args: string[], name: string, env: Record<string, string | undefined> = {}) => {
  const child = Bun.spawn(args, { cwd: packageRoot, env: { ...process.env, ...env }, stdin: 'ignore', stdout: Bun.file(join(output, `${name}.log`)), stderr: Bun.file(join(output, `${name}.stderr.log`)) });
  let stopped = false;
  const stop = async () => {
    if (stopped) return;
    stopped = true;
    child.kill('SIGTERM');
    const result = await Promise.race([child.exited, Bun.sleep(5000).then(() => null)]);
    if (result === null) { child.kill('SIGKILL'); await child.exited; }
  };
  cleanup.push(stop);
  return stop;
};

const pass = (check: string, origin?: string) => { receipts.push({ check, origin }); console.log(`PASS ${check}`); };
const backend = async (origin: string, name: string) => {
  const schema = `console_browser_${identity}_${name}`;
  await sql(`CREATE SCHEMA "${schema}"`);
  cleanup.push(async () => { await sql(`DROP SCHEMA "${schema}" CASCADE`); });
  const scoped = new URL(databaseURL);
  scoped.searchParams.set('search_path', schema);
  const dsn = scoped.toString();
  const port = await freePort();
  const kubeconfig = join(temporary, `${name}-kubeconfig.json`);
  await writeFile(kubeconfig, JSON.stringify({ apiVersion: 'v1', kind: 'Config', clusters: [{ name: 'test', cluster: { server: 'http://127.0.0.1:9' } }], contexts: [{ name: 'test', context: { cluster: 'test', user: 'test' } }], users: [{ name: 'test', user: {} }], 'current-context': 'test' }), { mode: 0o600 });
  const config = join(temporary, `${name}-config.json`);
  await writeFile(config, JSON.stringify({
    server: { address: `0.0.0.0:${port}`, public_origin: origin, shutdown_timeout: '3s' },
    database: { dsn },
    auth: { developers: [{ id: 'browser-alice', username: 'alice', password }, { id: 'browser-bob', username: 'bob', password }] },
    provisioning: { mode: 'kubeconfig', kubeconfig, context: 'test', namespace: 'browser-tests', installation_id: `browser-${identity}` },
    workload: { templates: [{ id: 'http-test', image: 'example/http-test:1', port: 8080, cpu_request: '25m', cpu_limit: '100m', memory_request: '32Mi', memory_limit: '64Mi', probe_path: '/' }] },
  }), { mode: 0o600 });
  const stop = service([join(temporary, 'console-server'), '--config', config], `${name}-backend`, { CONSOLE_POSTGRES_DSN: dsn });
  const url = `http://127.0.0.1:${port}`;
  await waitHTTP(`${url}/health/ready`);
  return { port, url, stop, dsn };
};

const authenticated = async (page: Page, username = 'alice') => {
  await expect(page).toHaveURL(/\/overview$/);
  await expect(page.locator('.sidebar-account strong')).toHaveText(username);
};
const signIn = async (page: Page, username = 'alice', secret = password) => {
  await page.locator('#console-username').fill(username);
  await page.locator('#console-password').fill(secret);
  await page.locator('button[type=submit]').click();
};
const anonymous = async (page: Page) => { await expect(page.locator('#console-username')).toBeVisible(); await expect(page.locator('.console-layout')).toHaveCount(0); };
const storageSafe = async (page: Page) => {
  const entries = await page.evaluate(() => ({ local: Object.entries(localStorage), session: Object.entries(sessionStorage) }));
  expect(entries.session).toEqual([]);
  expect(entries.local.map(([key]) => key)).toEqual(['syntrix.console.languagePreference']);
  expect(JSON.stringify(entries)).not.toContain(password);
  expect(JSON.stringify(entries)).not.toContain('csrf');
};

const flows = async (context: BrowserContext, origin: string, dsn: string, phase: string) => {
  const page = await context.newPage();
  await page.goto(`${origin}/overview`);
  await anonymous(page);
  await page.locator('select').selectOption('en');
  await page.screenshot({ path: join(output, `${phase}-login-en.png`), fullPage: true });
  const denied = page.waitForResponse((response) => response.url().endsWith('/auth/v1/login'));
  await signIn(page, 'alice', 'incorrect-password');
  const response = await denied;
  expect(response.status()).toBe(401);
  receipts.push({ check: `${phase} invalid credentials`, origin, status: response.status(), requestId: response.headers()['x-request-id'] });
  await expect(page.getByRole('alert')).toContainText('username or password is incorrect');
  await page.locator('select').selectOption('zh-CN');
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN');
  await expect(page.getByRole('alert')).not.toContainText('username or password is incorrect');
  await expect(page.locator('#console-username')).toHaveValue('alice');
  await expect(page.locator('#console-password')).toHaveValue('incorrect-password');
  await page.locator('select').selectOption('en');
  await signIn(page);
  await authenticated(page);
  const cookies = await context.cookies(origin);
  const sessionCookie = cookies.find((cookie) => cookie.name === 'console_session');
  expect(sessionCookie).toMatchObject({ httpOnly: true, secure: origin.startsWith('https:'), sameSite: 'Strict', path: '/' });
  await storageSafe(page);
  await page.reload(); await authenticated(page);
  await page.screenshot({ path: join(output, `${phase}-overview-en.png`), fullPage: true });
  await page.locator('select').selectOption('zh-CN');
  await page.reload(); await authenticated(page);
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh-CN');
  await page.screenshot({ path: join(output, `${phase}-overview-zh.png`), fullPage: true });
  await page.locator('select').selectOption('en');
  await sql("UPDATE console_sessions SET expires_at=CURRENT_TIMESTAMP-INTERVAL '1 second'", dsn);
  await page.reload(); await anonymous(page);
  pass(`${phase} absolute session expiry`, origin);
  await signIn(page); await authenticated(page);
  await page.route('**/auth/v1/me', async (route) => { await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"operation unavailable"}' }); });
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Could not confirm your session' })).toBeVisible();
  await expect(page.locator('.console-layout')).toHaveCount(0);
  await expect(page.locator('#console-username')).toHaveCount(0);
  await page.screenshot({ path: join(output, `${phase}-unavailable.png`), fullPage: true });
  await page.unroute('**/auth/v1/me');
  await page.getByRole('button', { name: 'Check session again' }).click();
  await authenticated(page);
  pass(`${phase} 503 restoration remains distinct and retryable`, origin);
  const csrfDenied = await page.evaluate(async () => {
    const response = await fetch('/auth/v1/logout', { method: 'POST', credentials: 'same-origin' });
    return { status: response.status, requestId: response.headers.get('X-Request-ID') };
  });
  expect(csrfDenied.status).toBe(403);
  await page.reload(); await authenticated(page);
  pass(`${phase} missing CSRF cannot revoke the session`, origin);
  await page.route('**/auth/v1/logout', async (route) => { await route.fulfill({ status: 503, contentType: 'application/json', body: '{"error":"operation unavailable"}' }); });
  await page.locator('.logout-button').click();
  await authenticated(page);
  await expect(page.getByRole('alert')).toContainText('Sign-out was not confirmed');
  await page.unroute('**/auth/v1/logout');
  pass(`${phase} failed logout retains verified authority and retry`, origin);
  const logout = page.waitForResponse((result) => result.url().endsWith('/auth/v1/logout'));
  await page.locator('.logout-button').click();
  expect((await logout).status()).toBe(204);
  await anonymous(page);
  await page.goto(`${origin}/overview`); await anonymous(page);
  expect((await context.cookies(origin)).find((cookie) => cookie.name === 'console_session')).toBeUndefined();
  pass(`${phase} login/reload/bilingual persistence/logout/cookie contract`, origin);
  return page;
};

type Fault = { path: string; kind: 'malformed-body' | 'lost-response' | 'hold-body'; headers: () => void; release?: Promise<void> };
let fault: Fault | null = null;
const production = async () => {
  const port = await freePort();
  const origin = `https://localhost:${port}`;
  const api = await backend(origin, 'production');
  const webPort = await freePort();
  const container = `console-client-browser-${identity}`;
  await command(['docker', 'image', 'inspect', image], { quiet: true });
  cleanup.push(async () => { await command(['docker', 'rm', '--force', container], { quiet: true }); });
  await command(['docker', 'run', '--detach', '--name', container, '--add-host', 'host.docker.internal:host-gateway', '--publish', `127.0.0.1:${webPort}:8080`, '--env', `CONSOLE_API_ORIGIN=http://host.docker.internal:${api.port}`, image], { quiet: true });
  await waitHTTP(`http://127.0.0.1:${webPort}/login`);
  const key = join(temporary, 'localhost.key');
  const certificate = join(temporary, 'localhost.crt');
  await command(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-keyout', key, '-out', certificate, '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost'], { quiet: true });
  const web = httpsServer({ key: await readFile(key), cert: await readFile(certificate) }, (request, response) => {
    const selected = fault?.path === request.url ? fault : null;
    if (selected) fault = null;
    const upstream = httpRequest({ host: '127.0.0.1', port: webPort, path: request.url, method: request.method, headers: { ...request.headers, 'x-forwarded-proto': 'https' } }, (result) => {
      if (selected?.kind === 'lost-response') { result.resume(); selected.headers(); response.destroy(); return; }
      const headers = { ...result.headers };
      delete headers['content-length']; delete headers['transfer-encoding']; delete headers.connection;
      response.writeHead(result.statusCode ?? 502, headers);
      response.flushHeaders();
      selected?.headers();
      if (selected) {
        const chunks: Buffer[] = [];
        result.on('data', (chunk: Buffer) => chunks.push(chunk));
        result.on('end', () => {
          const finish = () => { if (!response.destroyed) response.end(selected.kind === 'malformed-body' ? '{"developer":' : Buffer.concat(chunks)); };
          if (selected.release) void selected.release.then(finish); else finish();
        });
      } else result.pipe(response);
    });
    upstream.on('error', () => { if (!response.headersSent) response.writeHead(502); response.end(); });
    response.on('close', () => upstream.destroy());
    request.pipe(upstream);
  });
  await new Promise<void>((accept, reject) => { web.once('error', reject); web.listen(port, '127.0.0.1', accept); });
  cleanup.push(async () => { web.closeAllConnections(); await new Promise<void>((accept) => web.close(() => accept())); });
  return { origin, api, webPort };
};

const faultRecovery = async (context: BrowserContext, page: Page, origin: string) => {
  let logins = 0;
  let logouts = 0;
  const countMutation = (request: Request) => {
    if (request.url().endsWith('/auth/v1/login')) logins++;
    if (request.url().endsWith('/auth/v1/logout')) logouts++;
  };
  page.on('request', countMutation);
  await page.goto(`${origin}/login`); await anonymous(page);
  fault = { path: '/auth/v1/login', kind: 'malformed-body', headers: () => {} };
  await signIn(page); await authenticated(page);
  expect(logins).toBe(1);
  pass('login cookie headers with truncated JSON recover through authoritative me', origin);
  fault = { path: '/auth/v1/logout', kind: 'lost-response', headers: () => {} };
  await page.locator('.logout-button').click(); await anonymous(page);
  expect(logouts).toBe(1);
  pass('committed logout with lost response recovers without replay', origin);
  let headers!: () => void;
  let release!: () => void;
  const observed = new Promise<void>((accept) => { headers = accept; });
  const body = new Promise<void>((accept) => { release = accept; });
  fault = { path: '/auth/v1/login', kind: 'hold-body', headers, release: body };
  await signIn(page);
  await Promise.race([observed, Bun.sleep(10_000).then(() => { throw new Error('Split-response fault did not observe login cookie headers.'); })]);
  const peer = await context.newPage();
  await peer.goto(`${origin}/overview`); await authenticated(peer);
  await peer.locator('.logout-button').click(); await anonymous(peer);
  await expect(page.locator('.console-layout')).toHaveCount(0);
  release();
  await anonymous(page);
  await Promise.all([page.reload(), peer.reload()]);
  await Promise.all([anonymous(page), anonymous(peer)]);
  pass('overlapping cross-tab login/logout converges after settlement and me', origin);
  expect(logins).toBe(2);
  page.off('request', countMutation);
  await peer.close();
};

const distribution = async (page: Page, origin: string) => {
  for (const path of ['/login', '/overview']) {
    const response = await page.request.get(origin + path);
    expect(response.status()).toBe(200); expect(response.headers()['content-type']).toContain('text/html');
  }
  for (const path of ['/assets/absent.js', '/mockup.html', '/auth/v1/absent', '/api/v1/absent']) {
    const response = await page.request.get(origin + path);
    expect(response.status()).toBe(404);
    if (path.startsWith('/auth') || path.startsWith('/api')) expect(response.headers()['content-type']).not.toContain('text/html');
  }
  const source = await (await page.request.get(origin + '/login')).text();
  const assets = [...source.matchAll(/(?:src|href)="(\/assets\/[^\"]+)"/g)].map((match) => match[1]!);
  expect(assets.length).toBeGreaterThan(0);
  for (const asset of assets) {
    const response = await page.request.get(origin + asset);
    expect(response.status()).toBe(200);
    const text = await response.text();
    expect(text).not.toContain('fixture-password');
    expect(text).not.toContain('scenario-controls');
  }
  pass('shipped image preserves routes/assets/API errors and excludes mockup', origin);
};

const main = async () => {
  await mkdir(temporary, { recursive: true, mode: 0o700 });
  // Chromium's Unix socket limit requires a shorter path than nested worktrees.
  const commonDirectory = await command(['git', 'rev-parse', '--path-format=absolute', '--git-common-dir'], { cwd: repositoryRoot, quiet: true });
  const browserTemporary = join(dirname(commonDirectory), '.tmp', `browser-${identity}`);
  await mkdir(browserTemporary, { recursive: true, mode: 0o700 });
  cleanup.push(async () => { await rm(browserTemporary, { recursive: true, force: true }); });
  process.env.TMPDIR = browserTemporary;
  console.log('Building the real Console backend for browser acceptance.');
  await command(['go', 'build', '-o', join(temporary, 'console-server'), './cmd/console-server'], { cwd: join(repositoryRoot, 'packages', 'console-server') });
  browser = await chromium.launch({ ...(process.env.CONSOLE_BROWSER_EXECUTABLE ? { executablePath: process.env.CONSOLE_BROWSER_EXECUTABLE } : {}), headless: true });
  cleanup.push(async () => { await browser?.close(); });
  const vitePort = await freePort();
  const viteOrigin = `http://localhost:${vitePort}`;
  const development = await backend(viteOrigin, 'development');
  const stopVite = service(['node_modules/.bin/vite', '--host', '127.0.0.1', '--port', String(vitePort), '--strictPort'], 'vite', { CONSOLE_CLIENT_PORT: String(vitePort), CONSOLE_API_TARGET: development.url });
  await waitHTTP(`${viteOrigin}/login`);
  const developmentContext = await browser.newContext({ locale: 'en-US', viewport: { width: 1280, height: 800 } });
  developmentContext.setDefaultTimeout(10_000);
  await flows(developmentContext, viteOrigin, development.dsn, 'development');
  await developmentContext.close(); await stopVite(); await development.stop();
  console.log('Checking the shipped static image through localhost HTTPS.');
  const web = await production();
  const context = await browser.newContext({ locale: 'en-US', ignoreHTTPSErrors: true, viewport: { width: 1280, height: 800 } });
  context.setDefaultTimeout(10_000);
  const page = await flows(context, web.origin, web.api.dsn, 'production');
  await distribution(page, web.origin);
  await faultRecovery(context, page, web.origin);
  await signIn(page); await authenticated(page);
  await page.setViewportSize({ width: 320, height: 740 });
  await page.getByRole('button', { name: 'Open navigation' }).click();
  await expect(page.getByRole('dialog', { name: 'Navigation' })).toBeVisible();
  await page.screenshot({ path: join(output, 'production-mobile-navigation.png'), fullPage: true });
  await page.keyboard.press('Escape');
  await expect(page.getByRole('button', { name: 'Open navigation' })).toBeFocused();
  pass('320px shipped navigation preserves keyboard focus', web.origin);
  await context.close();
};

const watchdog = setTimeout(() => {
  failure = true;
  console.error('Browser acceptance exceeded its bounded runtime.');
  void clean().finally(() => process.exit(1));
}, 280_000);
for (const signal of ['SIGINT', 'SIGTERM'] as const) process.once(signal, () => { failure = true; void clean().finally(() => process.exit(1)); });
try { await main(); }
catch (error) { failure = true; console.error(error instanceof Error ? error.message : 'Browser acceptance failed.'); process.exitCode = 1; }
finally { clearTimeout(watchdog); await clean(); }
console.log(`Browser acceptance receipts: ${output}`);
