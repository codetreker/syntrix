import axios from 'axios';
import { AuthConfig, LoginResponse, AuthService } from '../internal/auth/types';
import { DefaultTokenProvider } from '../internal/auth/provider';
import { setupAuthInterceptor } from '../internal/auth/interceptor';
import { RestTransport } from '../internal/transport/rest-transport';
import { StorageClient } from '../internal/storage-client';
import { CollectionReference, DocumentReference, PullOptions, PullPage } from '../api/types';
import { PullTransport } from '../internal/pull';
import { CollectionReferenceImpl, DocumentReferenceImpl } from '../api/reference';
import { RealtimeClient, SubscriptionCallbacks, SubscribeOptions } from '../replication/realtime';
import { RealtimeSSEClient, RealtimeSSEOptions } from '../replication/realtime-sse';

export interface SyntrixClientConfig {
  database: string;
  auth?: AuthConfig;
}

export class SyntrixClient implements AuthService {
  private storage: StorageClient;
  private tokenProvider: DefaultTokenProvider;
  private pullTransport: PullTransport;
  private realtimeClient: RealtimeClient | null = null;
  private realtimeSseClient: RealtimeSSEClient | null = null;
  private baseUrl: string;
  private database: string;

  constructor(baseUrl: string, config: SyntrixClientConfig) {
    this.baseUrl = baseUrl;
    this.database = config.database;
    const axiosInstance = axios.create({ baseURL: baseUrl });
    this.tokenProvider = new DefaultTokenProvider(config.auth || {}, baseUrl);
    setupAuthInterceptor(axiosInstance, this.tokenProvider);
    this.storage = new RestTransport(axiosInstance, config.database);
    this.pullTransport = new PullTransport(axiosInstance, this.tokenProvider, config.database);
  }

  getDatabase(): string {
    return this.database;
  }

  // Auth methods
  async signup(username: string, password: string): Promise<LoginResponse> {
    const operation = this.tokenProvider.signup(username, password);
    this.clearRealtimeClients();
    return operation;
  }

  async login(username: string, password: string): Promise<LoginResponse> {
    const operation = this.tokenProvider.login(username, password);
    this.clearRealtimeClients();
    return operation;
  }

  async logout(): Promise<void> {
    const operation = this.tokenProvider.logout();
    this.clearRealtimeClients();
    return operation;
  }

  private clearRealtimeClients(): void {
    const realtime = this.realtimeClient;
    const sse = this.realtimeSseClient;
    // Detach both owners before teardown callbacks can create replacements.
    this.realtimeClient = null;
    this.realtimeSseClient = null;
    realtime?.dispose();
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

  // Realtime methods
  realtime(): RealtimeClient {
    if (!this.realtimeClient) {
      const wsUrl = this.baseUrl.replace(/^http/, 'ws') + '/realtime/ws';
      this.realtimeClient = new RealtimeClient(wsUrl, this.tokenProvider, this.database);
    }
    return this.realtimeClient;
  }

  realtimeSSE(): RealtimeSSEClient {
    if (!this.realtimeSseClient) {
      this.realtimeSseClient = new RealtimeSSEClient(this.baseUrl, this.tokenProvider, this.database);
    }
    return this.realtimeSseClient;
  }

  // Convenience method for subscribing to a collection
  subscribe(
    collection: string,
    callbacks: SubscriptionCallbacks,
    options?: Partial<SubscribeOptions>
  ): { subId: string; unsubscribe: () => void } {
    const rt = this.realtime();

    const subId = rt.subscribe({
      query: { collection, filters: options?.query?.filters || [] },
      includeData: options?.includeData ?? true,
      sendSnapshot: options?.sendSnapshot ?? false,
    }, callbacks);

    // The realtime client reports connection failures to the registered callbacks.
    void rt.connect().catch(() => {});

    return {
      subId,
      unsubscribe: () => rt.unsubscribe(subId),
    };
  }
}
