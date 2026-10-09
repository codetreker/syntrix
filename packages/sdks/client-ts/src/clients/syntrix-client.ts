import axios, { type AxiosInstance } from 'axios';
import { AuthConfig, LoginResponse, AuthService } from '../internal/auth/types';
import { DefaultTokenProvider } from '../internal/auth/provider';
import { setupAuthInterceptor } from '../internal/auth/interceptor';
import { RestTransport } from '../internal/transport/rest-transport';
import { StorageClient } from '../internal/storage-client';
import { CollectionReference, DocumentReference, PullOptions, PullPage } from '../api/types';
import { PullTransport } from '../internal/pull';
import { CollectionReferenceImpl, DocumentReferenceImpl } from '../api/reference';
import { RealtimeSSEClient } from '../replication/realtime-sse';
import type { OpenReplicaOptions, ReplicaDatabase, ReplicaSource } from '../api/replica-types.js';
import type { QueryValue } from '../api/value.js';
import { createReplicaSource, snapshotReplicaOptions } from '../api/replica-reference.js';
import { createReplicaSession, type ReplicaSession } from '../internal/replica/session.js';
import { loadReplicaRuntime } from '../internal/replica/loader.js';
import { ReplicaStorageError } from '../internal/replica/storage-types.js';

export interface SyntrixClientConfig {
  database: string;
  auth?: AuthConfig;
}

export class SyntrixClient implements AuthService {
  private storage: StorageClient;
  private tokenProvider: DefaultTokenProvider;
  private pullTransport: PullTransport;
  private axios: AxiosInstance;
  private realtimeSseClient: RealtimeSSEClient | null = null;
  private baseUrl: string;
  private database: string;

  constructor(baseUrl: string, config: SyntrixClientConfig) {
    this.baseUrl = baseUrl;
    this.database = config.database;
    const axiosInstance = axios.create({ baseURL: baseUrl });
    this.axios = axiosInstance;
    this.tokenProvider = new DefaultTokenProvider(config.auth || {}, baseUrl);
    setupAuthInterceptor(axiosInstance, this.tokenProvider);
    this.storage = new RestTransport(axiosInstance, config.database);
    this.pullTransport = new PullTransport(axiosInstance, this.tokenProvider, config.database);
  }

  getDatabase(): string {
    return this.database;
  }

  replicate<T = Record<string, QueryValue>>(path: string): ReplicaSource<T> {
    return createReplicaSource<T>(this, path);
  }

  async openReplica(options: OpenReplicaOptions): Promise<ReplicaDatabase> {
    const snapshot = snapshotReplicaOptions(this, options);
    if (typeof window === 'undefined' || typeof document === 'undefined' || typeof indexedDB === 'undefined' ||
        typeof navigator === 'undefined' || typeof navigator.locks?.request !== 'function' ||
        typeof crypto === 'undefined' || typeof crypto.subtle?.digest !== 'function' || typeof crypto.randomUUID !== 'function') {
      throw new ReplicaStorageError('ReplicaUnsupportedEnvironment', 'Replica storage requires a browser with IndexedDB, Web Locks, and Web Crypto');
    }
    // Registration occurs synchronously, before importing the storage runtime.
    // A credential refresh during import must still respect this account owner.
    const opening = createReplicaSession(this.tokenProvider);
    let session: ReplicaSession | undefined;
    try {
      session = await opening;
      session.assertCurrent();
      const runtime = await loadReplicaRuntime();
      session.assertCurrent();
      const replica = await session.track(() => runtime.openReplicaDatabase({
        session: session!, axios: this.axios, provider: this.tokenProvider,
        endpoint: this.baseUrl, database: this.database, options: snapshot,
      }));
      session.assertCurrent();
      return replica;
    } catch (error) {
      if (session) {
        try { await session.close(); }
        catch (cleanupError) {
          throw Object.assign(new ReplicaStorageError('ReplicaStorageCleanupFailed', 'Replica open cleanup failed', { cause: error }), { cleanupErrors: [cleanupError] });
        }
      }
      throw error;
    }
  }

  // Auth methods
  async signup(username: string, password: string): Promise<LoginResponse> {
    const operation = this.tokenProvider.signup(username, password);
    this.clearRealtimeSSE();
    return operation;
  }

  async login(username: string, password: string): Promise<LoginResponse> {
    const operation = this.tokenProvider.login(username, password);
    this.clearRealtimeSSE();
    return operation;
  }

  async logout(): Promise<void> {
    const operation = this.tokenProvider.logout();
    this.clearRealtimeSSE();
    return operation;
  }

  private clearRealtimeSSE(): void {
    const sse = this.realtimeSseClient;
    // Detach the old owner before teardown callbacks can create a replacement.
    this.realtimeSseClient = null;
    sse?.disconnect();
  }

  isAuthenticated(): boolean {
    return this.tokenProvider.isAuthenticated();
  }

  // Data methods
  collection<T>(path: string): CollectionReference<T> {
    return new CollectionReferenceImpl<T>(this.storage, path);
  }

  doc<T>(path: string): DocumentReference<T> {
    const parts = path.split('/');
    const id = parts[parts.length - 1];
    return new DocumentReferenceImpl<T>(this.storage, path, id);
  }

  /** Reads one replication page; the caller owns atomic local application and checkpoint persistence. */
  pull<T>(collection: string, options?: PullOptions): Promise<PullPage<T>> {
    return this.pullTransport.pull<T>(collection, options);
  }

  realtimeSSE(): RealtimeSSEClient {
    if (!this.realtimeSseClient) {
      this.realtimeSseClient = new RealtimeSSEClient(this.baseUrl, this.tokenProvider, this.database);
    }
    return this.realtimeSseClient;
  }

}
