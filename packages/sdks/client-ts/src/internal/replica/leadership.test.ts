import { expect, spyOn, test } from 'bun:test';
import { BROADCAST_CHANNEL_BY_TOKEN } from 'rxdb';
import { getLeaderElectorByBroadcastChannel } from 'rxdb/plugins/leader-election';
import { createReplicaLeadership } from './leadership.js';

test('fresh native electors have one leader, followers cancel, and ownership can restart', async () => {
  const baseline = BROADCAST_CHANNEL_BY_TOKEN.size;
  const namespace = `election-${crypto.randomUUID()}`;
  const first = createReplicaLeadership(namespace), second = createReplicaLeadership(namespace);
  const a = new AbortController(), b = new AbortController();
  let one = false, two = false;
  const p1 = first.wait(a.signal).then(() => { one = true; }, error => { if (error !== a.signal.reason) throw error; });
  const p2 = second.wait(b.signal).then(() => { two = true; }, error => { if (error !== b.signal.reason) throw error; });
  const until = async (predicate: () => boolean) => {
    const deadline = Date.now() + 5_000;
    while (!predicate()) { if (Date.now() > deadline) throw new Error('Election deadline exceeded'); await Bun.sleep(10); }
  };
  try {
    await until(() => one || two);
    expect(one && two).toBe(false);
    if (one) { b.abort(new Error('follower closed')); await p2; await second.close(); await first.close(); }
    else { a.abort(new Error('follower closed')); await p1; await first.close(); await second.close(); }
    const replacement = createReplicaLeadership(namespace);
    try { await replacement.wait(new AbortController().signal); }
    finally { await replacement.close(); }
  } finally { a.abort(); b.abort(); await Promise.all([first.close(), second.close()]); await Promise.all([p1, p2]); }
  expect(BROADCAST_CHANNEL_BY_TOKEN.size).toBe(baseline);
}, 15_000);

test('elector and channel cleanup failures retain both causes and the cached failed close', async () => {
  const before = new Set(BROADCAST_CHANNEL_BY_TOKEN.keys());
  const leadership = createReplicaLeadership(`cleanup-${crypto.randomUUID()}`);
  const token = [...BROADCAST_CHANNEL_BY_TOKEN.keys()].find(key => !before.has(key))!;
  const channel = BROADCAST_CHANNEL_BY_TOKEN.get(token)!.bc;
  const elector = getLeaderElectorByBroadcastChannel(channel);
  const dieFailure = new Error('elector die failed'), channelFailure = new Error('channel close failed');
  const actualDie = elector.die.bind(elector), actualClose = channel.close.bind(channel);
  const dieSpy = spyOn(elector, 'die').mockImplementation(async () => { await actualDie(); throw dieFailure; });
  const closeSpy = spyOn(channel, 'close').mockImplementation(async () => { dieSpy.mockRestore(); await actualClose(); throw channelFailure; });
  try {
    const first = leadership.close();
    const error = await first.catch(value => value);
    expect(error.cause).toBe(dieFailure); expect(error.errors).toEqual([dieFailure, channelFailure]);
    expect(leadership.close()).toBe(first);
    expect(await leadership.close().catch(value => value)).toBe(error);
    expect(closeSpy).toHaveBeenCalledTimes(1);
  } finally { dieSpy.mockRestore(); closeSpy.mockRestore(); await actualDie(); await actualClose(); }
});
