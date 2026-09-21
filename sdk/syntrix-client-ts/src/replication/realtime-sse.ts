import { TokenProvider } from '../internal/auth/types';
import { AuthSessionChangedError } from '../api/errors';

interface BaseMessage { type: string; payload?: any }

export interface RealtimeEvent {
  subId: string;
  delta: {
    type: 'create' | 'update' | 'delete';
    id: string;
    document?: Record<string, any>;
    timestamp: number;
  };
}

export interface SnapshotEvent {
  subId: string;
  documents: Record<string, any>[];
}

export type ConnectionState = 'disconnected' | 'connecting' | 'connected' | 'error';

export interface RealtimeCallbacks {
  onConnect?: () => void;
  onDisconnect?: () => void;
  onError?: (error: Error) => void;
  onEvent?: (event: RealtimeEvent) => void;
  onSnapshot?: (snapshot: SnapshotEvent) => void;
  onStateChange?: (state: ConnectionState) => void;
}

type FetchLike = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

export interface RealtimeSSEOptions {
  collection?: string;
  fetchImpl?: FetchLike;
}

export class RealtimeSSEClient {
  private controller: AbortController | null = null;
  private sessionVersion: number | null = null;
  private disconnectNotification: (() => void) | null = null;
  private state: ConnectionState = 'disconnected';
  private database: string;

  constructor(private baseUrl: string, private tokenProvider: TokenProvider, database: string) {
    this.database = database;
  }

  getState(): ConnectionState {
    return this.state;
  }

  disconnect(): void {
    const controller = this.controller;
    const notify = controller && !controller.signal.aborted
      && this.sessionVersion === this.tokenProvider.getSessionVersion()
      ? this.disconnectNotification : null;
    this.controller = null;
    this.sessionVersion = null;
    this.disconnectNotification = null;
    this.state = 'disconnected';
    // Notify before abort listeners can reconnect, after detaching the old owner.
    try {
      notify?.();
    } finally {
      controller?.abort();
    }
  }

  async connect(callbacks: RealtimeCallbacks = {}, options: RealtimeSSEOptions = {}): Promise<void> {
    const sessionVersion = this.tokenProvider.getSessionVersion();
    if (this.controller && !this.controller.signal.aborted && this.sessionVersion === sessionVersion) return;
    if (this.controller) {
      this.disconnect();
      if (this.tokenProvider.getSessionVersion() !== sessionVersion) throw new AuthSessionChangedError();
      if (this.controller) return;
    }

    const controller = new AbortController();
    this.controller = controller;
    this.sessionVersion = sessionVersion;
    const isCurrent = () => this.controller === controller && !controller.signal.aborted
      && this.tokenProvider.getSessionVersion() === sessionVersion;
    const assertCurrent = () => {
      if (this.tokenProvider.getSessionVersion() !== sessionVersion) throw new AuthSessionChangedError();
      if (this.controller !== controller || controller.signal.aborted) {
        throw new DOMException('SSE connection disconnected', 'AbortError');
      }
    };
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined;
    let response: Response | undefined;
    let failed = false;

    try {
      this.setState('connecting', callbacks);
      assertCurrent();
      const token = await this.tokenProvider.getToken();
      assertCurrent();
      const fetchImpl: FetchLike = options.fetchImpl || fetch;
      response = await fetchImpl(this.buildUrl(options.collection || ''), {
        method: 'GET',
        headers: token ? { Authorization: `Bearer ${token}` } : undefined,
        signal: controller.signal,
      });
      assertCurrent();
      if (!response.ok || !response.body) throw new Error(`SSE connection failed: ${response.status}`);

      reader = response.body.getReader();
      this.disconnectNotification = callbacks.onDisconnect ?? null;
      this.setState('connected', callbacks);
      assertCurrent();
      callbacks.onConnect?.();
      assertCurrent();
      const decoder = new TextDecoder();
      let buffer = '';

      while (true) {
        const { value, done } = await reader.read();
        assertCurrent();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let idx = buffer.indexOf('\n\n');
        while (idx >= 0) {
          assertCurrent();
          const chunk = buffer.slice(0, idx);
          buffer = buffer.slice(idx + 2);
          this.processChunk(chunk, callbacks, isCurrent);
          assertCurrent();
          idx = buffer.indexOf('\n\n');
        }
      }
    } catch (error) {
      failed = true;
      const notificationVersion = this.tokenProvider.getSessionVersion();
      const failure = notificationVersion === sessionVersion ? error : new AuthSessionChangedError();
      if (this.controller === controller && !controller.signal.aborted) {
        this.setState('error', callbacks);
        if (this.controller === controller && !controller.signal.aborted
          && this.tokenProvider.getSessionVersion() === notificationVersion) callbacks.onError?.(failure as Error);
      }
      throw failure;
    } finally {
      controller.abort();
      try {
        if (reader) await reader.cancel();
        else if (response?.body) await response.body.cancel();
      } catch (error) {
        // Preserve the original authentication or transport failure when cancellation also fails.
        if (!failed) throw error;
      } finally {
        reader?.releaseLock();
        if (this.controller === controller) {
          this.controller = null;
          this.sessionVersion = null;
          this.disconnectNotification = null;
          if (this.tokenProvider.getSessionVersion() === sessionVersion) {
            this.setState('disconnected', callbacks);
            if (!this.controller && this.tokenProvider.getSessionVersion() === sessionVersion) callbacks.onDisconnect?.();
          } else {
            this.state = 'disconnected';
          }
        }
      }
    }
  }

  private processChunk(chunk: string, callbacks: RealtimeCallbacks, isCurrent: () => boolean) {
    const dataLine = chunk.split('\n').find((l) => l.startsWith('data:'));
    if (!dataLine) return;
    const data = dataLine.slice(5).trim();
    if (!data) return;
    try {
      const msg: BaseMessage = JSON.parse(data);
      if (isCurrent()) this.handleMessage(msg, callbacks);
    } catch (err) {
      if (isCurrent()) callbacks.onError?.(err as Error);
    }
  }

  private handleMessage(msg: BaseMessage, callbacks: RealtimeCallbacks) {
    switch (msg.type) {
      case 'event': {
        const event: RealtimeEvent = typeof msg.payload === 'string' ? JSON.parse(msg.payload) : msg.payload;
        callbacks.onEvent?.(event);
        break;
      }
      case 'snapshot': {
        const snapshot: SnapshotEvent = typeof msg.payload === 'string' ? JSON.parse(msg.payload) : msg.payload;
        callbacks.onSnapshot?.(snapshot);
        break;
      }
      case 'error': {
        const errPayload = typeof msg.payload === 'string' ? JSON.parse(msg.payload) : msg.payload;
        callbacks.onError?.(new Error(errPayload?.message || 'Realtime SSE error'));
        break;
      }
      default:
        break;
    }
  }

  private buildUrl(collection: string): string {
    const cleanBase = this.baseUrl.replace(/\/$/, '');
    const path = `${cleanBase}/realtime/v1/databases/${this.database}/sse`;
    if (!collection) return path;
    const encoded = encodeURIComponent(collection);
    return `${path}?collection=${encoded}`;
  }

  private setState(state: ConnectionState, callbacks: RealtimeCallbacks) {
    if (this.state !== state) {
      this.state = state;
      callbacks.onStateChange?.(state);
    }
  }
}
