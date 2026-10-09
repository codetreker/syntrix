import { expect, test } from 'bun:test';
import * as sdk from './index.js';

test('public exports keep REST, replica and SSE while excluding the legacy WebSocket API', () => {
  for (const name of ['RealtimeClient', 'RealtimeListener', 'MessageType', 'ReplicationCoordinator', 'createReplicaWebSocket']) {
    expect(name in sdk).toBe(false);
  }
  expect(typeof sdk.RealtimeSSEClient).toBe('function');
  const client = new sdk.SyntrixClient('https://example.test', { database: 'app' });
  expect('realtime' in client).toBe(false);
  expect('subscribe' in client).toBe(false);
  expect(typeof client.realtimeSSE).toBe('function');
  expect(typeof client.pull).toBe('function');
  expect(typeof client.replicate).toBe('function');
  expect(typeof client.openReplica).toBe('function');
});
