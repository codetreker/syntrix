import { TokenProvider } from '../internal/auth/types';
import { AuthSessionChangedError } from '../api/errors';

// Message types matching server protocol
export const MessageType = {
  Auth: 'auth',
  AuthAck: 'auth_ack',
  Subscribe: 'subscribe',
  SubscribeAck: 'subscribe_ack',
  Unsubscribe: 'unsubscribe',
  UnsubscribeAck: 'unsubscribe_ack',
  Event: 'event',
  Snapshot: 'snapshot',
  Error: 'error',
  Heartbeat: 'heartbeat',
} as const;

export interface BaseMessage {
  id?: string;
  type: string;
  payload?: any;
}

export interface SubscribeQuery {
  collection: string;
  filters?: Array<{ field: string; op: string; value: any }>;
}

export interface SubscribeOptions {
  query: SubscribeQuery;
  includeData?: boolean;
  sendSnapshot?: boolean;
}

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

export interface RealtimeClientOptions {
  maxReconnectAttempts?: number;
  reconnectDelayMs?: number;
  activityTimeoutMs?: number;
}

export interface SubscriptionCallbacks {
  onEvent?: (event: RealtimeEvent) => void;
  onSnapshot?: (snapshot: SnapshotEvent) => void;
  onError?: (error: Error) => void;
  onReady?: () => void;
}

interface Subscription {
  options: SubscribeOptions;
  callbacks: SubscriptionCallbacks;
  sent: boolean;
  acknowledged: boolean;
  failed: boolean;
}

interface ConnectionAttempt {
  sessionVersion: number;
  socket: WebSocket | null;
  promise: Promise<void>;
  resolve: () => void;
  reject: (error: Error) => void;
  authenticated: boolean;
  authId: string | null;
  authRetryAttempted: boolean;
}

export class RealtimeClient {
  private callbacks: RealtimeCallbacks = {};
  private subscriptions = new Map<string, Subscription>();
  private attempt: ConnectionAttempt | null = null;
  private subIdCounter = 0;
  private state: ConnectionState = 'disconnected';
  private disposed = false;
  private lifecycle = 0;
  private reconnectEnabled = false;
  private reconnectAttempts = 0;
  private maxReconnectAttempts: number;
  private reconnectDelay: number;
  private activityTimeoutMs: number;
  private lastMessageTime = 0;
  private activityCheckTimer: ReturnType<typeof setInterval> | null = null;
  private handshakeTimer: ReturnType<typeof setTimeout> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;

  constructor(
    private wsUrl: string,
    private tokenProvider: TokenProvider,
    private database: string,
    options?: RealtimeClientOptions,
  ) {
    this.maxReconnectAttempts = options?.maxReconnectAttempts ?? 5;
    this.reconnectDelay = options?.reconnectDelayMs ?? 1000;
    this.activityTimeoutMs = options?.activityTimeoutMs ?? 90000;
  }

  getLastMessageTime(): number {
    return this.lastMessageTime;
  }

  on<K extends keyof RealtimeCallbacks>(event: K, callback: RealtimeCallbacks[K]): this {
    this.assertUsable();
    this.callbacks[event] = callback;
    return this;
  }

  private assertUsable(): void {
    if (this.disposed) throw new Error('Realtime client has been disposed');
  }

  private setState(state: ConnectionState): void {
    if (this.state !== state) {
      this.state = state;
      this.invoke(this.callbacks.onStateChange, [state]);
    }
  }

  private invoke<T extends unknown[]>(callback: ((...args: T) => void) | undefined, args: T): void {
    const lifecycle = this.lifecycle;
    const sessionVersion = this.tokenProvider.getSessionVersion();
    try {
      callback?.(...args);
    } catch (error) {
      if (this.isCurrentLifecycle(lifecycle) && this.tokenProvider.getSessionVersion() === sessionVersion) {
        this.reportError(this.asError(error), null);
      }
    }
  }

  private isCurrentLifecycle(lifecycle: number): boolean {
    return !this.disposed && this.lifecycle === lifecycle;
  }

  private asError(error: unknown): Error {
    return error instanceof Error ? error : new Error(String(error));
  }

  // A null target reports a callback or protocol error only to the global observer.
  private reportError(error: Error, target?: string | null): void {
    this.captureErrorNotification(error, target)();
  }

  private captureErrorNotification(error: Error, target?: string | null): () => void {
    const lifecycle = this.lifecycle;
    const sessionVersion = this.tokenProvider.getSessionVersion();
    const isCurrent = () => this.isCurrentLifecycle(lifecycle)
      && this.tokenProvider.getSessionVersion() === sessionVersion;
    const globalError = this.callbacks.onError;
    const subscriptions = target === undefined
      ? [...this.subscriptions.entries()]
      : target === null ? [] : [...this.subscriptions.entries()].filter(([id]) => id === target);
    return () => {
      let handled = false;
      const notify = (callback: ((error: Error) => void) | undefined) => {
        if (!callback || !isCurrent()) return;
        handled = true;
        try {
          callback(error);
        } catch (callbackError) {
          if (isCurrent()) {
            console.error('[Realtime] Error callback failed:', callbackError);
          }
        }
      };
      for (const [id, sub] of subscriptions) {
        if (this.subscriptions.get(id) === sub) notify(sub.callbacks.onError);
      }
      if (this.callbacks.onError === globalError) notify(globalError);
      if (!handled && isCurrent()) console.error('[Realtime]', error);
    };
  }

  connect(): Promise<void> {
    if (this.disposed) return Promise.reject(new Error('Realtime client has been disposed'));
    const sessionVersion = this.tokenProvider.getSessionVersion();
    this.clearReconnectTimer();
    this.reconnectEnabled = true;
    if (this.attempt?.sessionVersion === sessionVersion) return this.attempt.promise;
    if (this.attempt) this.releaseAttempt(this.attempt, new AuthSessionChangedError());
    this.lifecycle++;

    let resolve!: () => void;
    let reject!: (error: Error) => void;
    const promise = new Promise<void>((res, rej) => { resolve = res; reject = rej; });
    const attempt: ConnectionAttempt = {
      sessionVersion, socket: null, promise, resolve, reject,
      authenticated: false, authId: null, authRetryAttempted: false,
    };
    this.attempt = attempt;
    this.handshakeTimer = setTimeout(() => {
      this.fail(attempt, new Error('Realtime connection or authentication timed out'), true);
    }, this.activityTimeoutMs);
    this.setState('connecting');
    if (!this.ensureSession(attempt)) return promise;

    try {
      const socket = new WebSocket(this.wsUrl);
      attempt.socket = socket;
      socket.onopen = () => {
        if (!this.ensureSession(attempt)) return;
        this.lastMessageTime = Date.now();
        void this.authenticate(attempt, false);
      };
      socket.onmessage = (event) => {
        if (!this.ensureSession(attempt)) return;
        this.lastMessageTime = Date.now();
        let msg: BaseMessage;
        try {
          msg = JSON.parse(event.data);
          if (!msg || typeof msg.type !== 'string') throw new Error('Invalid realtime message');
          if (typeof msg.payload === 'string') msg.payload = JSON.parse(msg.payload);
        } catch (error) {
          this.reportError(this.asError(error), null);
          return;
        }
        this.handleMessage(attempt, msg);
      };
      socket.onerror = () => this.fail(attempt, new Error('WebSocket error'), true);
      socket.onclose = () => this.fail(attempt, new Error('WebSocket closed'), true);
    } catch (error) {
      this.fail(attempt, this.asError(error), true);
    }
    return promise;
  }

  private async authenticate(attempt: ConnectionAttempt, refresh: boolean): Promise<void> {
    try {
      if (!this.ensureSession(attempt)) return;
      const token = refresh
        ? await this.tokenProvider.refreshToken()
        : await this.tokenProvider.getToken();
      if (!this.ensureSession(attempt)) return;
      if (!token) {
        this.fail(attempt, new Error('Realtime authentication requires a token'), false);
        return;
      }
      attempt.authId = refresh ? 'auth-retry' : 'auth-init';
      this.sendMessage(attempt, {
        id: attempt.authId, type: MessageType.Auth,
        payload: { token, database: this.database },
      });
    } catch (error) {
      this.fail(attempt, this.asError(error), false);
    }
  }

  private ensureSession(attempt: ConnectionAttempt): boolean {
    if (this.attempt !== attempt) return false;
    if (this.tokenProvider.getSessionVersion() !== attempt.sessionVersion) {
      this.fail(attempt, new AuthSessionChangedError(), false);
      return false;
    }
    return true;
  }

  private fail(attempt: ConnectionAttempt, error: Error, retry: boolean): void {
    if (this.attempt !== attempt) return;
    if (this.tokenProvider.getSessionVersion() !== attempt.sessionVersion) {
      error = new AuthSessionChangedError();
      retry = false;
    }
    const lifecycle = this.lifecycle;
    const notificationVersion = this.tokenProvider.getSessionVersion();
    const reportError = this.captureErrorNotification(error);
    this.releaseAttempt(attempt, error);
    if (!retry) this.reconnectEnabled = false;
    this.scheduleReconnect(attempt.sessionVersion);
    this.setState('disconnected');
    reportError();
    if (this.isCurrentLifecycle(lifecycle) && !this.attempt
      && this.tokenProvider.getSessionVersion() === notificationVersion) this.invoke(this.callbacks.onDisconnect, []);
  }

  private releaseAttempt(attempt: ConnectionAttempt, error: Error): void {
    this.attempt = null;
    if (this.handshakeTimer !== null) clearTimeout(this.handshakeTimer);
    if (this.activityCheckTimer !== null) clearInterval(this.activityCheckTimer);
    this.handshakeTimer = null;
    this.activityCheckTimer = null;
    for (const sub of this.subscriptions.values()) {
      sub.sent = false;
      sub.acknowledged = false;
      sub.failed = false;
    }
    if (attempt.socket) {
      attempt.socket.onopen = null;
      attempt.socket.onmessage = null;
      attempt.socket.onerror = null;
      attempt.socket.onclose = null;
      attempt.socket.close();
    }
    attempt.reject(error);
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer !== null) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;
  }

  private scheduleReconnect(sessionVersion: number): void {
    if (!this.reconnectEnabled || this.disposed || this.reconnectAttempts >= this.maxReconnectAttempts) return;
    this.reconnectAttempts++;
    const baseDelay = this.reconnectDelay * 2 ** (this.reconnectAttempts - 1);
    const delay = Math.floor(baseDelay * (0.5 + Math.random()));
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (!this.reconnectEnabled || this.disposed || this.attempt) return;
      if (this.tokenProvider.getSessionVersion() !== sessionVersion) {
        this.reconnectEnabled = false;
        this.reportError(new AuthSessionChangedError());
        return;
      }
      // Connection failures are reported by fail, including automatic attempts.
      void this.connect().catch(() => {});
    }, delay);
  }

  disconnect(): void {
    const lifecycle = ++this.lifecycle;
    const sessionVersion = this.tokenProvider.getSessionVersion();
    this.reconnectEnabled = false;
    this.clearReconnectTimer();
    const attempt = this.attempt;
    if (attempt) this.releaseAttempt(attempt, new Error('Realtime connection disconnected'));
    this.setState('disconnected');
    if (attempt && this.isCurrentLifecycle(lifecycle) && !this.attempt
      && this.tokenProvider.getSessionVersion() === sessionVersion) this.invoke(this.callbacks.onDisconnect, []);
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.callbacks = {};
    this.subscriptions.clear();
    this.disconnect();
  }

  private startActivityCheck(attempt: ConnectionAttempt): void {
    this.activityCheckTimer = setInterval(() => {
      if (this.attempt === attempt && Date.now() - this.lastMessageTime > this.activityTimeoutMs) {
        this.fail(attempt, new Error('Realtime connection activity timed out'), true);
      }
    }, Math.min(10000, this.activityTimeoutMs / 3));
  }

  private sendMessage(attempt: ConnectionAttempt, msg: BaseMessage): boolean {
    if (!this.ensureSession(attempt) || attempt.socket?.readyState !== WebSocket.OPEN) return false;
    try {
      attempt.socket.send(JSON.stringify(msg));
      return this.attempt === attempt;
    } catch (error) {
      this.fail(attempt, this.asError(error), true);
      return false;
    }
  }

  private handleMessage(attempt: ConnectionAttempt, msg: BaseMessage): void {
    if (!this.ensureSession(attempt)) return;
    switch (msg.type) {
      case MessageType.AuthAck:
        if (attempt.authenticated || !attempt.authId || msg.id !== attempt.authId) return;
        attempt.authId = null;
        attempt.authenticated = true;
        this.reconnectAttempts = 0;
        if (this.handshakeTimer !== null) clearTimeout(this.handshakeTimer);
        this.handshakeTimer = null;
        this.startActivityCheck(attempt);
        this.setState('connected');
        if (!this.ensureSession(attempt)) return;
        for (const [id, sub] of this.subscriptions) this.sendSubscribe(attempt, id, sub);
        if (!this.ensureSession(attempt)) return;
        attempt.resolve();
        this.invoke(this.callbacks.onConnect, []);
        this.ensureSession(attempt);
        return;
      case MessageType.SubscribeAck: {
        const sub = msg.id ? this.subscriptions.get(msg.id) : undefined;
        if (!attempt.authenticated || !sub?.sent || sub.acknowledged || sub.failed) return;
        sub.acknowledged = true;
        this.invoke(sub.callbacks.onReady, []);
        this.ensureSession(attempt);
        return;
      }
      case MessageType.Event:
      case MessageType.Snapshot: {
        const subId = msg.payload?.subId;
        const sub = this.subscriptions.get(subId);
        if (!attempt.authenticated || !sub?.sent || sub.failed) return;
        if (msg.type === MessageType.Event) {
          this.invoke(sub.callbacks.onEvent, [msg.payload]);
          if (this.ensureSession(attempt) && this.subscriptions.get(subId) === sub) {
            this.invoke(this.callbacks.onEvent, [msg.payload]);
          }
        } else {
          this.invoke(sub.callbacks.onSnapshot, [msg.payload]);
          if (this.ensureSession(attempt) && this.subscriptions.get(subId) === sub) {
            this.invoke(this.callbacks.onSnapshot, [msg.payload]);
          }
        }
        return;
      }
      case MessageType.Error: {
        const error = new Error(msg.payload?.message ?? 'Unknown realtime error');
        if (attempt.authId && msg.id === attempt.authId) {
          attempt.authId = null;
          if (msg.payload?.code === 'unauthorized' && !attempt.authRetryAttempted) {
            attempt.authRetryAttempted = true;
            void this.authenticate(attempt, true);
          } else {
            this.fail(attempt, error, false);
          }
        } else if (msg.id && this.subscriptions.get(msg.id)?.sent) {
          const sub = this.subscriptions.get(msg.id)!;
          const snapshotError = sub.acknowledged
            && (msg.payload?.code === 'snapshot_failed' || msg.payload?.code === 'snapshot_limit');
          if (!snapshotError) sub.failed = true;
          this.reportError(error, msg.id);
        } else if (!msg.id) {
          this.reportError(error);
        }
        return;
      }
    }
  }

  subscribe(options: SubscribeOptions, callbacks: SubscriptionCallbacks = {}): string {
    this.assertUsable();
    const subId = `sub-${++this.subIdCounter}`;
    const sub: Subscription = { options, callbacks, sent: false, acknowledged: false, failed: false };
    this.subscriptions.set(subId, sub);
    if (this.attempt?.authenticated) this.sendSubscribe(this.attempt, subId, sub);
    return subId;
  }

  private sendSubscribe(attempt: ConnectionAttempt, subId: string, sub: Subscription): void {
    if (this.attempt !== attempt || !attempt.authenticated || sub.sent || this.subscriptions.get(subId) !== sub) return;
    sub.sent = true;
    this.sendMessage(attempt, {
      id: subId, type: MessageType.Subscribe,
      payload: {
        query: sub.options.query,
        includeData: sub.options.includeData ?? true,
        sendSnapshot: sub.options.sendSnapshot ?? false,
      },
    });
  }

  unsubscribe(subId: string): void {
    const sub = this.subscriptions.get(subId);
    this.subscriptions.delete(subId);
    if (sub?.sent && this.attempt?.authenticated) {
      this.sendMessage(this.attempt, {
        id: `unsub-${subId}`, type: MessageType.Unsubscribe, payload: { id: subId },
      });
    }
  }

  getState(): ConnectionState {
    return this.state;
  }
}

// Legacy class for backward compatibility
export class RealtimeListener {
  private client: RealtimeClient;

  constructor(wsUrl: string, tokenProvider: TokenProvider, database: string) {
    this.client = new RealtimeClient(wsUrl, tokenProvider, database);
  }

  connect() {
    void this.client.connect().catch(() => {});
  }

  disconnect() {
    this.client.disconnect();
  }

  onEvent(callback: (event: any) => void) {
    this.client.on('onEvent', callback);
  }
}
