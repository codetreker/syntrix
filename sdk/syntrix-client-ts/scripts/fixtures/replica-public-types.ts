import {
  SyntrixClient,
  type ReplicaDatabase,
  type ReplicaDocument,
  type ReplicaInspection,
  type ReplicaRecoveryDecision,
  type ReplicaSyncStatus,
} from '@syntrix/client';

// @ts-expect-error WebSocket clients are private replica transports.
type RemovedClient = import('@syntrix/client').RealtimeClient;
// @ts-expect-error The legacy notification wrapper is removed.
type RemovedListener = import('@syntrix/client').RealtimeListener;
// @ts-expect-error Raw WebSocket request options are not public.
type RemovedSubscribeOptions = import('@syntrix/client').SubscribeOptions;
// @ts-expect-error Raw WebSocket subscription callbacks are not public.
type RemovedSubscriptionCallbacks = import('@syntrix/client').SubscriptionCallbacks;
// @ts-expect-error Raw WebSocket connection options are not public.
type RemovedClientOptions = import('@syntrix/client').RealtimeClientOptions;
// @ts-expect-error The raw message envelope is not public.
type RemovedMessage = import('@syntrix/client').BaseMessage;
// @ts-expect-error Raw protocol constants are not public.
type RemovedMessageType = typeof import('@syntrix/client').MessageType;

// @ts-expect-error Realtime is owned by openReplica.
type RemovedRealtimeMethod = SyntrixClient['realtime'];
// @ts-expect-error Applications observe replica queries through watch.
type RemovedSubscribeMethod = SyntrixClient['subscribe'];

interface Task { projectId: string; title: string; status: string; score: bigint }

export const publicReplicaExample = async (endpoint: string, token: string): Promise<void> => {
  const client = new SyntrixClient(endpoint, { database: 'app', auth: { token } });
  const sse: import('@syntrix/client').RealtimeSSEClient = client.realtimeSSE();
  const sseCallbacks: import('@syntrix/client').RealtimeCallbacks = { onEvent: event => { void event.delta.id; } };
  void sse; void sseCallbacks;
  await client.pull<Task>('projects/p1/tasks');
  await client.collection<Task>('projects/p1/tasks').doc('task-1').get();
  const replica: ReplicaDatabase = await client.openReplica({
    name: 'task-cache',
    collections: {
      projectTasks: client.replicate<Task>('projects/p1/tasks').where('projectId', '==', 'p1'),
      recentTasks: client.replicate<Task>('projects/p1/tasks').orderBy('updatedAt', 'desc').limit(100),
    },
    sync: { pollIntervalMs: 10_000 },
    storageLimits: { maxKnownIds: 100_000 },
    queryLimits: { outputBytes: 16 * 1024 * 1024 },
    onDiagnostic: event => { const operation: string = event.operationId; void operation; },
  });
  const tasks = replica.collection<Task>('projectTasks');
  const created = await tasks.add({ projectId: 'p1', title: 'Review', status: 'open', score: 9007199254740993n });
  await created.update({ status: 'closed' });
  await tasks.doc('task-1').set({ projectId: 'p1', title: 'Replacement', status: 'open', score: 1n });
  await tasks.doc('task-1').ifMatch('version', '==', 1n).delete();
  const document: ReplicaDocument<Task> | null = await created.get({ showDeleted: true });
  if (document && !document.deleted) { const score: bigint = document.score; void score; }
  const query = tasks.where('status', '==', 'open').orderBy('score', 'desc').limit(20);
  const page = await query.getPage();
  if (page.nextCursor) await query.startAfter(page.nextCursor).getPage();
  const stopWatch = query.watch(rows => {
    for (const row of rows) if (!row.deleted) { const title: string = row.title; void title; }
  }, error => { void error; });
  const stopStatus = replica.sync.subscribe((status: ReplicaSyncStatus) => {
    const pending: number | undefined = status.aliases.projectTasks?.pending; void pending;
  });
  await replica.sync.pause('projectTasks');
  const inspected: ReplicaInspection<Task> = await replica.sync.inspect('projectTasks', { id: created.id, readCurrent: true });
  if (inspected.issueId && inspected.document) {
    const decision: ReplicaRecoveryDecision<Task> = {
      kind: 'merge-local', issueId: inspected.issueId, id: inspected.document.id,
      editToken: inspected.document.editToken, physicalEpoch: inspected.physicalEpoch,
      data: { projectId: 'p1', title: 'Merged', status: 'open', score: 2n },
    };
    await replica.sync.resolve('projectTasks', decision);
  }
  await replica.sync.resume('projectTasks');
  stopWatch(); stopStatus();
  await replica.removeCollection('recentTasks');
  await replica.close();
};
