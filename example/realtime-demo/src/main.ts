import { SyntrixClient, type ReplicaAliasStatus, type ReplicaDatabase, type ReplicaDocument } from '@syntrix/client';

type PanelId = 1 | 2;
type Message = { text: string; sender: string; panel: number; sentAt: number };
type Scope = { endpoint: string; database: string; collection: string };
type SavedSession = { version: 1; scope: Scope; username: string; accessToken: string };
type Panel = {
  id: PanelId;
  client: SyntrixClient | null;
  replica: ReplicaDatabase | null;
  scope: Scope | null;
  username: string;
  saved: SavedSession | null;
  storedSession: boolean;
  busy: boolean;
  attempt: number;
  clientGeneration: number;
  viewGeneration: number;
  closeFailed: boolean;
  stopWatch?: () => void;
  stopStatus?: () => void;
  status: ReplicaAliasStatus | null;
};
class DemoError extends Error {}
const panelIds = [1, 2] as const;
const panels = new Map<PanelId, Panel>(panelIds.map(id => [id, {
  id, client: null, replica: null, scope: null, username: '', saved: null, storedSession: false, busy: false,
  attempt: 0, clientGeneration: 0, viewGeneration: 0, closeFailed: false, status: null,
}]));
const node = <T extends HTMLElement = HTMLElement>(id: string): T => {
  const value = document.getElementById(id);
  if (!value) throw new Error(`Missing demo element: ${id}`);
  return value as T;
};
const input = (id: string) => node<HTMLInputElement>(id);
const button = (id: string) => node<HTMLButtonElement>(id);
const key = (id: PanelId) => `syntrix-replica-demo-session-${id}`;
const sameScope = (left: Scope | null, right: Scope) => left !== null &&
  left.endpoint === right.endpoint && left.database === right.database && left.collection === right.collection;
const readScope = (): Scope => {
  let url: URL;
  try { url = new URL(input('endpoint').value.trim()); }
  catch { throw new DemoError('Enter a complete HTTP or HTTPS API endpoint.'); }
  if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password || url.search || url.hash) {
    throw new DemoError('The API endpoint cannot contain credentials, a query, or a fragment.');
  }
  const database = input('database').value.trim(), collection = input('collection').value.trim();
  if (!database || !collection) throw new DemoError('Enter the database and collection.');
  return { endpoint: url.href.replace(/\/+$/, ''), database, collection };
};
const errorCode = (error: unknown): string => {
  let current = error;
  const seen = new Set<unknown>();
  let found = 'ERROR';
  while (current && typeof current === 'object' && !seen.has(current)) {
    seen.add(current);
    const code = Reflect.get(current, 'code');
    if (code === 'ReplicaUpstreamUncertain') return code;
    if (typeof code === 'string' && /^[A-Za-z][A-Za-z0-9_]{0,79}$/.test(code)) found = code;
    current = Reflect.get(current, 'cause');
  }
  return found;
};
const queryErrorCode = (error: unknown): string => {
  const code = errorCode(error);
  const kind = error !== null && typeof error === 'object' ? Reflect.get(error, 'kind') : undefined;
  return code === 'QueryBudgetExceeded' && typeof kind === 'string' && /^[A-Za-z][A-Za-z0-9]{0,40}$/.test(kind)
    ? `${code}: ${kind}` : code;
};
const describeError = (error: unknown): string => {
  if (error instanceof DemoError) return error.message;
  const code = errorCode(error);
  if (code === 'QueryBudgetExceeded') return `${queryErrorCode(error)}: The local watch stopped at its resource limit. Close and reopen to restart it; local edits are retained.`;
  if (code === 'UNAUTHORIZED' || code === 'AUTH_SESSION_CHANGED') return `${code}: Close the replica and log in again. Local changes are retained.`;
  if (code === 'FORBIDDEN') return 'FORBIDDEN: Use the database owner or an account with matching db_admin access.';
  if (code === 'DATABASE_IDENTITY_MISMATCH' || code === 'ReplicaScopeChanged') return `${code}: The database binding changed. Cached data and pending changes are retained.`;
  if (code === 'ReplicaUpstreamUncertain' || code === 'ReplicaRecoveryRequired' || code === 'ReplicaConflictUnresolved') {
    return `${code}: Synchronization needs inspection and an explicit recovery decision. Resume does not discard or repeat uncertain writes.`;
  }
  return `${code}: The operation did not complete. Local data has not been intentionally discarded.`;
};
const log = (panel: Panel, message: string, type = 'info') => {
  const entry = document.createElement('div');
  entry.className = `log-entry ${type}`;
  entry.textContent = `[${new Date().toLocaleTimeString()}] ${message}`;
  const entries = node(`log${panel.id}`);
  entries.prepend(entry);
  while (entries.children.length > 50) entries.lastElementChild!.remove();
};
const showError = (panel: Panel, error: unknown) => {
  const message = describeError(error);
  node(`error${panel.id}`).textContent = message;
  log(panel, message, 'error');
};
const showSession = (panel: Panel) => {
  let scope: Scope;
  try { scope = readScope(); } catch { node(`session${panel.id}`).textContent = 'Enter a valid API scope.'; return; }
  const username = input(`username${panel.id}`).value.trim();
  const usable = panel.saved && sameScope(panel.saved.scope, scope) && panel.saved.username === username;
  node(`session${panel.id}`).textContent = usable
    ? panel.storedSession ? `Saved access token for ${username} in this tab. Passwords and refresh tokens are not saved.`
      : `Session available for ${username} in memory. This tab could not persist its access token.`
    : 'Log in with an existing database owner or db_admin account. Both panels may use the same account.';
};
const persistSession = (panel: Panel, client: SyntrixClient, generation: number, scope: Scope, username: string, accessToken: string) => {
  if (panel.client !== client || panel.clientGeneration !== generation || !sameScope(panel.scope, scope) || panel.username !== username) return;
  panel.saved = { version: 1, scope: { ...scope }, username, accessToken };
  try { sessionStorage.setItem(key(panel.id), JSON.stringify(panel.saved)); panel.storedSession = true; }
  catch { panel.storedSession = false; showError(panel, new DemoError('This tab could not save its access token. Reopening after reload may require login.')); }
  showSession(panel);
};
const restoreSession = (panel: Panel) => {
  try {
    const raw = sessionStorage.getItem(key(panel.id));
    if (raw === null) return;
    panel.storedSession = true;
    const value = JSON.parse(raw) as Partial<SavedSession>;
    if (value.version !== 1 || !value.scope || typeof value.scope.endpoint !== 'string' ||
        typeof value.scope.database !== 'string' || typeof value.scope.collection !== 'string' ||
        typeof value.username !== 'string' || !value.username || typeof value.accessToken !== 'string' || !value.accessToken) {
      throw new DemoError('The saved tab session is invalid. Sign out to clear it, then log in again.');
    }
    panel.saved = { version: 1, scope: { ...value.scope }, username: value.username, accessToken: value.accessToken };
    input(`username${panel.id}`).value = value.username;
  } catch (error) {
    showError(panel, error instanceof DemoError ? error : new DemoError('The saved tab session could not be read. Enter credentials to log in.'));
  }
};
const capable = window.isSecureContext && typeof indexedDB !== 'undefined' &&
  typeof navigator.locks?.request === 'function' && typeof crypto.subtle?.digest === 'function' && typeof crypto.randomUUID === 'function';

const renderControls = () => {
  const values = [...panels.values()];
  const scopeLocked = values.some(panel => panel.busy || panel.replica !== null);
  for (const id of ['endpoint', 'database', 'collection']) input(id).disabled = scopeLocked;
  button('openBothBtn').disabled = !capable || values.some(panel => panel.busy) || values.every(panel => panel.replica !== null);
  button('closeBothBtn').disabled = values.some(panel => panel.busy) || values.every(panel => panel.replica === null);
  for (const panel of values) {
    const suffix = panel.id, opened = panel.replica !== null;
    input(`username${suffix}`).disabled = panel.busy || opened;
    input(`password${suffix}`).disabled = panel.busy || opened;
    button(`openBtn${suffix}`).disabled = !capable || panel.busy || opened;
    button(`closeBtn${suffix}`).disabled = panel.busy || !opened;
    button(`closeBtn${suffix}`).textContent = panel.closeFailed ? 'Retry close' : 'Close';
    button(`signoutBtn${suffix}`).disabled = panel.busy || (!panel.client && !panel.saved && !panel.storedSession);
    button(`sendBtn${suffix}`).disabled = panel.busy || !opened || panel.closeFailed;
    input(`messageText${suffix}`).disabled = panel.busy || !opened || panel.closeFailed;
    button(`pauseBtn${suffix}`).disabled = panel.busy || !opened || panel.closeFailed || panel.status?.state === 'paused';
    button(`resumeBtn${suffix}`).disabled = panel.busy || !opened || panel.closeFailed || panel.status?.state !== 'paused';
    button(`inspectBtn${suffix}`).disabled = panel.busy || !opened || panel.closeFailed;
    showSession(panel);
  }
};
const renderStatus = (panel: Panel) => {
  const status = panel.status;
  let label = panel.replica ? 'Local replica open' : 'Closed — local data retained';
  if (panel.busy) label = 'Working…';
  else if (panel.closeFailed) label = 'Close failed — cleanup still required';
  else if (status) {
    const labels: Record<ReplicaAliasStatus['state'], string> = {
      waiting: 'Waiting for synchronization owner', syncing: 'Synchronizing', idle: 'Idle between refreshes',
      retrying: 'Waiting to retry', paused: 'Synchronization paused', blocked: 'Synchronization blocked', closed: 'Closed',
    };
    label = labels[status.state];
  }
  node(`status${panel.id}Text`).textContent = label;
  node(`panel${panel.id}StatusDot`).className = `status-dot ${panel.closeFailed || status?.state === 'blocked' ? 'blocked' : panel.busy || status?.state === 'syncing' || status?.state === 'retrying' ? 'working' : panel.replica ? 'open' : 'closed'}`;
  node(`pending${panel.id}`).textContent = status ? String(status.pending) : '—';
  node(`pins${panel.id}`).textContent = status ? String(status.pins) : '—';
  node(`sourceReady${panel.id}`).textContent = status ? status.sourceReady ? 'Complete source received' : 'Initializing source' : 'Not open';
  node(`leader${panel.id}`).textContent = status ? status.leader ? 'Leader' : 'Follower' : '—';
};
const renderSnapshot = (panel: Panel, documents: ReplicaDocument<Message>[]) => {
  const container = node(`data${panel.id}`);
  const entries = document.createDocumentFragment();
  for (const value of documents) {
    if (value.deleted) continue;
    const row = document.createElement('article');
    row.className = 'data-item'; row.dataset.documentId = value.id;
    const sender = document.createElement('strong');
    sender.textContent = typeof value.sender === 'string' ? value.sender : 'Document';
    const message = document.createElement('p');
    message.textContent = typeof value.text === 'string' ? value.text : '(This document has no message text.)';
    const metadata = document.createElement('small');
    const timestamp = typeof value.sentAt === 'number' && Number.isFinite(value.sentAt) && Math.abs(value.sentAt) <= 8.64e15
      ? new Date(value.sentAt).toLocaleTimeString() : 'No message time';
    metadata.textContent = `${timestamp} · ${value.id}`;
    row.append(sender, message, metadata); entries.append(row);
  }
  if (!entries.childNodes.length) {
    const empty = document.createElement('p'); empty.className = 'empty-state';
    empty.textContent = panel.replica ? 'No messages in the local result yet.' : 'Open a replica to read its persisted local snapshot.';
    entries.append(empty);
  }
  container.replaceChildren(entries);
};
const run = async (panel: Panel, operation: () => Promise<void>) => {
  if (panel.busy) return;
  panel.busy = true; const attempt = ++panel.attempt;
  node(`error${panel.id}`).textContent = '';
  renderControls(); renderStatus(panel);
  try { await operation(); }
  catch (error) { if (panel.attempt === attempt) showError(panel, error); }
  finally {
    if (panel.attempt === attempt) { panel.busy = false; renderControls(); renderStatus(panel); }
  }
};
const closeReplica = async (panel: Panel) => {
  ++panel.viewGeneration;
  panel.stopWatch?.(); panel.stopStatus?.(); panel.stopWatch = undefined; panel.stopStatus = undefined;
  node(`queryStatus${panel.id}`).textContent = 'Local query closed';
  node(`queryStatus${panel.id}`).className = 'query-status';
  if (!panel.replica) return;
  try { await panel.replica.close(); }
  catch (error) { panel.closeFailed = true; throw error; }
  panel.replica = null; panel.status = null; panel.closeFailed = false;
  renderSnapshot(panel, []);
  log(panel, 'Replica closed. Local data and pending edits are retained.');
};
const openPanel = (panel: Panel) => run(panel, async () => {
  if (panel.replica) return;
  const scope = readScope(), username = input(`username${panel.id}`).value.trim();
  if (!username) throw new DemoError('Enter the username of an existing owner or db_admin account.');
  const password = input(`password${panel.id}`).value;
  input(`password${panel.id}`).value = '';
  const reuse = !password && panel.client?.isAuthenticated() && sameScope(panel.scope, scope) && panel.username === username;
  if (!reuse) {
    const saved = !password && panel.saved && sameScope(panel.saved.scope, scope) && panel.saved.username === username ? panel.saved : null;
    if (!saved && !password) throw new DemoError('Enter a password to log in, or restore a saved session with matching scope and username.');
    const generation = ++panel.clientGeneration;
    const client = new SyntrixClient(scope.endpoint, { database: scope.database, auth: {
      ...(saved ? { token: saved.accessToken } : {}),
      onTokenRefresh: accessToken => persistSession(panel, client, generation, scope, username, accessToken),
    } });
    panel.client = client; panel.scope = scope; panel.username = username;
    if (!saved) {
      const result = await client.login(username, password);
      if (panel.client !== client || panel.clientGeneration !== generation) return;
      persistSession(panel, client, generation, scope, username, result.access_token);
      log(panel, `Logged in as ${username}.`);
    } else log(panel, `Restored the saved identity for ${username}. Server access is checked during synchronization.`);
  }
  const client = panel.client!;
  const view = ++panel.viewGeneration;
  const replica = await client.openReplica({ name: `demo-panel-${panel.id}-${scope.collection}`,
    collections: { messages: client.replicate<Message>(scope.collection) }, sync: { pollIntervalMs: 1000 } });
  panel.replica = replica;
  const current = () => panel.replica === replica && panel.viewGeneration === view;
  try {
    let watchStopped = false;
    node(`queryStatus${panel.id}`).textContent = 'Loading local query';
    node(`queryStatus${panel.id}`).className = 'query-status';
    panel.stopWatch = replica.collection<Message>('messages').orderBy('sentAt', 'desc').limit(20).watch(
      documents => {
        if (!current() || watchStopped) return;
        renderSnapshot(panel, documents);
        node(`queryStatus${panel.id}`).textContent = 'Watching local query';
      }, error => {
        if (!current()) return;
        watchStopped = true;
        node(`queryStatus${panel.id}`).textContent = `Local query stopped (${queryErrorCode(error)}). Close and reopen to restart; local edits are retained.`;
        node(`queryStatus${panel.id}`).className = 'query-status stopped';
        showError(panel, error);
      });
    let previous = '';
    panel.stopStatus = replica.sync.subscribe(snapshot => {
      if (!current()) return;
      const status = snapshot.aliases.messages;
      if (!status) return;
      panel.status = status;
      const signature = `${status.state}:${status.pending}:${status.pins}:${status.sourceReady}:${status.leader}`;
      if (signature !== previous) { previous = signature; log(panel, `${status.state}; pending ${status.pending}; pins ${status.pins}; source ${status.sourceReady ? 'ready' : 'initializing'}.`); }
      if (status.state === 'blocked' && status.error) node(`error${panel.id}`).textContent = describeError(status.error);
      renderStatus(panel); renderControls();
    }, error => { if (current()) showError(panel, error); });
    log(panel, 'Local replica opened. Synchronization continues independently.', 'event');
  } catch (error) {
    try { await closeReplica(panel); } catch (cleanupError) { showError(panel, cleanupError); }
    throw error;
  }
});
const signOut = (panel: Panel) => run(panel, async () => {
  ++panel.clientGeneration;
  panel.saved = null;
  const failures: unknown[] = [];
  try { sessionStorage.removeItem(key(panel.id)); panel.storedSession = false; }
  catch { failures.push(new DemoError('The saved access token could not be removed from this tab.')); }
  try { await closeReplica(panel); } catch (error) { failures.push(error); }
  try { await panel.client?.logout(); } catch (error) { failures.push(error); }
  if (failures.length) {
    for (const error of failures) showError(panel, error);
    return;
  }
  panel.client = null; panel.scope = null; panel.username = '';
  input(`password${panel.id}`).value = '';
  log(panel, 'Signed out and removed the saved access token. Account-local data remains on this browser.');
});
const sendMessage = (panel: Panel) => run(panel, async () => {
  if (!panel.replica || panel.closeFailed) throw new DemoError('Open a local replica first.');
  const text = input(`messageText${panel.id}`).value.trim();
  if (!text) throw new DemoError('Enter a message.');
  const document = await panel.replica.collection<Message>('messages').add({ text, sender: panel.username, panel: panel.id, sentAt: Date.now() });
  input(`messageText${panel.id}`).value = '';
  log(panel, `Saved locally: ${document.id}.`, 'event');
});

const defaults = new URL(window.location.href);
defaults.port = '8080'; defaults.pathname = '/'; defaults.search = ''; defaults.hash = '';
input('endpoint').value = defaults.href.replace(/\/$/, '');
for (const panel of panels.values()) restoreSession(panel);
const restoredScope = panels.get(1)!.saved?.scope ?? panels.get(2)!.saved?.scope;
if (restoredScope) for (const field of ['endpoint', 'database', 'collection'] as const) input(field).value = restoredScope[field];
if (!capable) node('environmentNotice').textContent = 'Replica storage needs a secure browser context with IndexedDB, Web Locks, and Web Crypto. Use localhost or HTTPS.';
else node('environmentNotice').textContent = 'Each panel has its own persisted replica. Local writes remain available while synchronization is paused or the loaded page is offline.';
for (const panel of panels.values()) {
  const id = panel.id;
  button(`openBtn${id}`).addEventListener('click', () => { void openPanel(panel); });
  button(`closeBtn${id}`).addEventListener('click', () => { void run(panel, () => closeReplica(panel)); });
  button(`signoutBtn${id}`).addEventListener('click', () => { void signOut(panel); });
  button(`sendBtn${id}`).addEventListener('click', () => { void sendMessage(panel); });
  input(`messageText${id}`).addEventListener('keydown', event => { if (event.key === 'Enter') { event.preventDefault(); void sendMessage(panel); } });
  button(`pauseBtn${id}`).addEventListener('click', () => { void run(panel, async () => { await panel.replica!.sync.pause('messages'); log(panel, 'Synchronization paused. Local editing remains available.'); }); });
  button(`resumeBtn${id}`).addEventListener('click', () => { void run(panel, async () => { await panel.replica!.sync.resume('messages'); log(panel, 'Synchronization resumed.'); }); });
  button(`inspectBtn${id}`).addEventListener('click', () => { void run(panel, async () => {
    const inspection = await panel.replica!.sync.inspect('messages');
    log(panel, `Inspection: ${inspection.issues.length} issue(s), ${inspection.targets.length} protected target(s), phase ${inspection.phase?.state ?? 'none'}, recovering ${inspection.recovering}.`);
    node(`error${id}`).textContent = inspection.issueId
      ? 'Synchronization has protected work. The README links the SDK inspection and recovery guide; this demo never discards changes or repeats uncertain writes automatically.'
      : 'No persisted synchronization issue is recorded.';
  }); });
  input(`username${id}`).addEventListener('input', () => showSession(panel));
  renderSnapshot(panel, []); renderStatus(panel);
}
button('openBothBtn').addEventListener('click', () => { void Promise.all(panelIds.map(id => openPanel(panels.get(id)!))); });
button('closeBothBtn').addEventListener('click', () => { void Promise.all(panelIds.map(id => run(panels.get(id)!, () => closeReplica(panels.get(id)!)))); });
for (const field of ['endpoint', 'database', 'collection']) input(field).addEventListener('input', renderControls);
const showConnectivity = () => { node('connectivity').textContent = navigator.onLine ? 'Browser network: available (not a synchronization receipt)' : 'Browser network: offline'; };
window.addEventListener('online', showConnectivity); window.addEventListener('offline', showConnectivity);
showConnectivity(); renderControls();
