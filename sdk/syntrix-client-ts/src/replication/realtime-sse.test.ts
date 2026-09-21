import { describe, it, expect, mock } from 'bun:test';
import { RealtimeSSEClient } from './realtime-sse';
import { AuthSessionChangedError } from '../api/errors';

function buildSseResponse(chunks: string[]) {
  const encoder = new TextEncoder();
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      chunks.forEach((c) => controller.enqueue(encoder.encode(c)));
      controller.close();
    },
  });
  return new Response(stream, { status: 200 });
}


const deferred = <T>() => {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
};

const flush = async () => {
  for (let i = 0; i < 12; i++) await Promise.resolve();
};

const eventChunk = (id: string) => `data: ${JSON.stringify({
  type: 'event',
  payload: { subId: 'default', delta: { type: 'create', id, timestamp: 1 } },
})}\n\n`;

const openStream = () => {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const canceled = mock(() => {});
  const body = new ReadableStream<Uint8Array>({
    start(value) { controller = value; },
    cancel: canceled,
  });
  return { controller, body, canceled, response: new Response(body, { status: 200 }) };
};

describe('RealtimeSSEClient', () => {
  it('notifies once when an established HTTP SSE connection is explicitly disconnected', async () => {
    const server = Bun.serve({
      hostname: '127.0.0.1', port: 0,
      fetch: () => new Response(new ReadableStream<Uint8Array>({
        start(controller) { controller.enqueue(new TextEncoder().encode(': connected\n\n')); },
      }), { headers: { 'Content-Type': 'text/event-stream' } }),
    });
    const provider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient(server.url.toString(), provider, 'test-db');
    const connected = deferred<void>();
    const onDisconnect = mock(() => {
      expect(client.getState()).toBe('disconnected');
      client.disconnect();
    });
    const pending = client.connect({ onConnect: () => connected.resolve(), onDisconnect }).catch(error => error);
    try {
      await connected.promise;
      client.disconnect();
      client.disconnect();
      expect(await pending).toBeInstanceOf(DOMException);
      expect(onDisconnect).toHaveBeenCalledTimes(1);
    } finally {
      client.disconnect();
      await server.stop(true);
    }
  });

  it('should emit events and snapshots from SSE stream', async () => {
    const eventMsg = `data: ${JSON.stringify({
      type: 'event',
      payload: { subId: 'default', delta: { type: 'create', id: '1', timestamp: 1, document: { foo: 'bar' } } },
    })}\n\n`;
    const snapshotMsg = `data: ${JSON.stringify({
      type: 'snapshot',
      payload: { subId: 'default', documents: [{ foo: 'bar' }] },
    })}\n\n`;

    const fetchMock = mock(async () => buildSseResponse([eventMsg, snapshotMsg]));
    const tokenProvider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', tokenProvider, 'test-db');

    let eventSeen = false;
    let snapshotSeen = false;

    await client.connect({
      onEvent: (evt) => {
        eventSeen = true;
        expect(evt.delta.id).toBe('1');
      },
      onSnapshot: (snap) => {
        snapshotSeen = true;
        expect(snap.documents[0].foo).toBe('bar');
      },
    }, { fetchImpl: fetchMock, collection: 'users' });

    expect(eventSeen).toBe(true);
    expect(snapshotSeen).toBe(true);
  });

  it('preserves a replacement created by the disconnect notification and abort listeners', async () => {
    const provider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const oldStream = openStream();
    const replacementStream = openStream();
    const replacementFetch = mock(async () => replacementStream.response);
    const replacementDisconnect = mock(() => {});
    let replacement!: Promise<void>;
    const onDisconnect = mock(() => {
      replacement = client.connect({ onDisconnect: replacementDisconnect }, { fetchImpl: replacementFetch });
    });
    const original = client.connect({ onDisconnect }, { fetchImpl: async (_url, init) => {
      init!.signal!.addEventListener('abort', () => {
        oldStream.controller.close();
        void client.connect({}, { fetchImpl: replacementFetch });
      });
      return oldStream.response;
    } }).catch(error => error);
    await flush();
    client.disconnect();
    expect(await original).toBeInstanceOf(DOMException);
    await flush();
    expect(onDisconnect).toHaveBeenCalledTimes(1);
    expect(replacementFetch).toHaveBeenCalledTimes(1);
    expect(client.getState()).toBe('connected');
    replacementStream.controller.close();
    await replacement;
    expect(replacementDisconnect).toHaveBeenCalledTimes(1);
  });

  it('notifies once when disconnect is called from an established connection error callback', async () => {
    const provider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const stream = openStream();
    const onDisconnect = mock(() => {});
    const failure = new Error('Stream failed');
    const pending = client.connect({ onError: () => client.disconnect(), onDisconnect }, {
      fetchImpl: async () => stream.response,
    }).catch(error => error);
    await flush();
    stream.controller.error(failure);
    expect(await pending).toBe(failure);
    expect(onDisconnect).toHaveBeenCalledTimes(1);
    expect(stream.body.locked).toBe(false);
  });

  it('still aborts the old fetch when its disconnect notification throws', async () => {
    const provider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const stream = openStream();
    const failure = new Error('Disconnect callback failed');
    const onDisconnect = mock(() => { throw failure; });
    const abort = mock(() => stream.controller.close());
    const pending = client.connect({ onDisconnect }, { fetchImpl: async (_url, init) => {
      init!.signal!.addEventListener('abort', abort);
      return stream.response;
    } }).catch(error => error);
    await flush();
    expect(() => client.disconnect()).toThrow(failure);
    expect(await pending).toBeInstanceOf(DOMException);
    client.disconnect();
    expect(onDisconnect).toHaveBeenCalledTimes(1);
    expect(abort).toHaveBeenCalledTimes(1);
    expect(stream.body.locked).toBe(false);
  });

  it('should surface fetch errors', async () => {
    const fetchMock = mock(async () => new Response(null, { status: 401 }));
    const tokenProvider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', tokenProvider, 'test-db');

    try {
      await client.connect({ onError: () => {} }, { fetchImpl: fetchMock });
      expect(true).toBe(false);
    } catch (err: any) {
      expect(err.message).toContain('SSE connection failed');
    }
  });

  for (const change of ['disconnect', 'session'] as const) {
    it(`stops token acquisition after ${change} without starting a fetch`, async () => {
      let version = 0;
      const token = deferred<string>();
      const provider = { getSessionVersion: () => version, getToken: () => token.promise } as any;
      const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
      const fetchMock = mock(async () => buildSseResponse([]));
      const connected = mock(() => {});
      const error = mock(() => {});
      const disconnected = mock(() => {});
      const pending = client.connect({ onConnect: connected, onError: error, onDisconnect: disconnected }, { fetchImpl: fetchMock });
      const outcome = pending.catch(error => error);
      if (change === 'session') version++;
      else client.disconnect();
      token.resolve('old-token');
      const failure = await outcome;
      expect(failure).toBeInstanceOf(change === 'session' ? AuthSessionChangedError : DOMException);
      expect(fetchMock).not.toHaveBeenCalled();
      expect(connected).not.toHaveBeenCalled();
      expect(error).toHaveBeenCalledTimes(change === 'session' ? 1 : 0);
      expect(disconnected).not.toHaveBeenCalled();
      expect(client.getState()).toBe('disconnected');
    });
  }

  it('reports current token acquisition failures through the owned connection', async () => {
    const failure = new Error('Token acquisition failed');
    const provider = { getSessionVersion: () => 0, getToken: async () => { throw failure; } } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const onError = mock(() => {});
    const onDisconnect = mock(() => {});
    const fetchMock = mock(async () => buildSseResponse([]));
    await expect(client.connect({ onError, onDisconnect }, { fetchImpl: fetchMock })).rejects.toBe(failure);
    expect(onError).toHaveBeenCalledWith(failure);
    expect(onDisconnect).toHaveBeenCalledTimes(1);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('cancels a late response without clearing a replacement connection', async () => {
    const provider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const oldResponse = deferred<Response>();
    const oldStream = openStream();
    const currentStream = openStream();
    const oldDisconnect = mock(() => {});
    const oldError = mock(() => {});
    const old = client.connect({ onError: oldError, onDisconnect: oldDisconnect }, { fetchImpl: () => oldResponse.promise });
    const outcome = old.catch(error => error);
    await flush();
    client.disconnect();
    const current = client.connect({}, { fetchImpl: async () => currentStream.response });
    await flush();
    expect(client.getState()).toBe('connected');
    oldResponse.resolve(oldStream.response);
    expect(await outcome).toBeInstanceOf(DOMException);
    expect(client.getState()).toBe('connected');
    expect(oldStream.canceled).toHaveBeenCalledTimes(1);
    expect(oldDisconnect).not.toHaveBeenCalled();
    expect(oldError).not.toHaveBeenCalled();
    currentStream.controller.close();
    await current;
    expect(client.getState()).toBe('disconnected');
  });

  it('rejects an old read after session replacement and releases its stream', async () => {
    let version = 0;
    const provider = { getSessionVersion: () => version, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const stream = openStream();
    const onEvent = mock(() => {});
    const onError = mock(() => {});
    const onDisconnect = mock(() => {});
    const pending = client.connect({ onEvent, onError, onDisconnect }, { fetchImpl: async () => stream.response });
    const outcome = pending.catch(error => error);
    await flush();
    version++;
    stream.controller.enqueue(new TextEncoder().encode(eventChunk('old')));
    expect(await outcome).toBeInstanceOf(AuthSessionChangedError);
    expect(onEvent).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(expect.any(AuthSessionChangedError));
    expect(onDisconnect).not.toHaveBeenCalled();
    expect(stream.canceled).toHaveBeenCalledTimes(1);
    expect(stream.body.locked).toBe(false);
  });

  for (const change of ['disconnect', 'session'] as const) {
    it(`stops buffered event dispatch after a callback causes ${change}`, async () => {
      let version = 0;
      const provider = { getSessionVersion: () => version, getToken: async () => 'token' } as any;
      const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
      const onEvent = mock(() => {
        if (change === 'session') version++;
        else client.disconnect();
      });
      const onError = mock(() => {});
      const onDisconnect = mock(() => {});
      const pending = client.connect({ onEvent, onError, onDisconnect }, {
        fetchImpl: async () => buildSseResponse([eventChunk('one') + eventChunk('two')]),
      });
      if (change === 'session') await expect(pending).rejects.toBeInstanceOf(AuthSessionChangedError);
      else await expect(pending).rejects.toBeInstanceOf(DOMException);
      expect(onEvent).toHaveBeenCalledTimes(1);
      expect(onError).toHaveBeenCalledTimes(change === 'session' ? 1 : 0);
      expect(onDisconnect).toHaveBeenCalledTimes(change === 'disconnect' ? 1 : 0);
    });
  }

  it('checks ownership after a connected state callback replaces the session', async () => {
    let version = 0;
    const provider = { getSessionVersion: () => version, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const onConnect = mock(() => {});
    const onError = mock(() => {});
    const onEvent = mock(() => {});
    await expect(client.connect({
      onStateChange: state => { if (state === 'connected') version++; },
      onConnect, onError, onEvent,
    }, { fetchImpl: async () => buildSseResponse([eventChunk('old')]) })).rejects.toBeInstanceOf(AuthSessionChangedError);
    expect(onConnect).not.toHaveBeenCalled();
    expect(onError).toHaveBeenCalledWith(expect.any(AuthSessionChangedError));
    expect(onEvent).not.toHaveBeenCalled();
  });


  it('detaches before abort callbacks create a replacement connection', async () => {
    const provider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const oldResponse = deferred<Response>();
    const oldStream = openStream();
    const currentStream = openStream();
    let replacement!: Promise<void>;
    const original = client.connect({}, { fetchImpl: async (_url, init) => {
      init!.signal!.addEventListener('abort', () => {
        replacement = client.connect({}, { fetchImpl: async () => currentStream.response });
      });
      return oldResponse.promise;
    } });
    const outcome = original.catch(error => error);
    await flush();
    client.disconnect();
    await flush();
    expect(replacement).toBeDefined();
    expect(client.getState()).toBe('connected');
    oldResponse.resolve(oldStream.response);
    expect(await outcome).toBeInstanceOf(DOMException);
    expect(client.getState()).toBe('connected');
    expect(oldStream.canceled).toHaveBeenCalledTimes(1);
    currentStream.controller.close();
    await replacement;
  });

  for (const result of ['resolve', 'reject'] as const) {
    it(`rejects a changed session when its pending fetch completes with ${result}`, async () => {
      let version = 0;
      const provider = { getSessionVersion: () => version, getToken: async () => 'token' } as any;
      const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
      const response = deferred<Response>();
      const stream = openStream();
      const onError = mock(() => {});
      const onConnect = mock(() => {});
      const pending = client.connect({ onError, onConnect }, { fetchImpl: () => response.promise });
      const outcome = pending.catch(error => error);
      await flush();
      version++;
      if (result === 'resolve') response.resolve(stream.response);
      else response.reject(new Error('Old fetch failed'));
      const failure = await outcome;
      expect(failure).toBeInstanceOf(AuthSessionChangedError);
      expect(onError).toHaveBeenCalledWith(failure);
      expect(onConnect).not.toHaveBeenCalled();
      if (result === 'resolve') expect(stream.canceled).toHaveBeenCalledTimes(1);
      else await stream.body.cancel();
    });
  }


  it('preserves a connection started by an abort callback during explicit session replacement', async () => {
    let version = 0;
    const provider = { getSessionVersion: () => version, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const oldResponse = deferred<Response>();
    const oldStream = openStream();
    const currentStream = openStream();
    let replacement!: Promise<void>;
    const original = client.connect({}, { fetchImpl: async (_url, init) => {
      init!.signal!.addEventListener('abort', () => {
        replacement = client.connect({}, { fetchImpl: async () => currentStream.response });
      });
      return oldResponse.promise;
    } });
    const outcome = original.catch(error => error);
    await flush();
    version++;
    const duplicateFetch = mock(async () => buildSseResponse([]));
    await client.connect({}, { fetchImpl: duplicateFetch });
    await flush();
    expect(duplicateFetch).not.toHaveBeenCalled();
    expect(client.getState()).toBe('connected');
    oldResponse.resolve(oldStream.response);
    expect(await outcome).toBeInstanceOf(AuthSessionChangedError);
    expect(client.getState()).toBe('connected');
    currentStream.controller.close();
    await replacement;
  });


  it('allows an abort listener to reconnect while the completed connection is cleaning up', async () => {
    const provider = { getSessionVersion: () => 0, getToken: async () => 'token' } as any;
    const client = new RealtimeSSEClient('http://example.com', provider, 'test-db');
    const currentStream = openStream();
    const oldDisconnect = mock(() => {});
    let replacement!: Promise<void>;
    await client.connect({ onDisconnect: oldDisconnect }, { fetchImpl: async (_url, init) => {
      init!.signal!.addEventListener('abort', () => {
        replacement = client.connect({}, { fetchImpl: async () => currentStream.response });
      });
      return buildSseResponse([]);
    } });
    await flush();
    expect(client.getState()).toBe('connected');
    expect(oldDisconnect).not.toHaveBeenCalled();
    currentStream.controller.close();
    await replacement;
  });

});
