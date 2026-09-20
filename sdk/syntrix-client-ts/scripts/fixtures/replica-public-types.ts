import {
  SyntrixClient,
  type ReplicaDatabase,
  type ReplicaDocument,
  type ReplicaInspection,
  type ReplicaRecoveryDecision,
  type ReplicaSyncStatus,
} from '@syntrix/client';

interface Task { projectId: string; title: string; status: string; score: bigint }

export const publicReplicaExample = async (endpoint: string, token: string): Promise<void> => {
  const client = new SyntrixClient(endpoint, { database: 'app', auth: { token } });
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
