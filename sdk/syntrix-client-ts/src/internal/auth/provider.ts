import axios from 'axios';
import { enableAuthOwnership, invalidateAuthOwners, authOwnersAcceptToken } from './lifecycle.js';
import { AuthSessionChangedError } from '../../api/errors';
import { AuthConfig, TokenProvider, LoginResponse, AuthService } from './types';

export class DefaultTokenProvider implements TokenProvider, AuthService {
  private token: string | null = null;
  private _refreshToken: string | null = null;
  private sessionVersion = 0;
  private desiredToken: string | null = null;
  private credentialBarrier: Promise<void> | null = null;
  private refreshOperation: { version: number; promise: Promise<string> } | null = null;
  private baseUrl: string;

  constructor(private config: AuthConfig, baseUrl?: string) {
    this.token = config.token || null;
    this._refreshToken = config.refreshToken || null;
    this.baseUrl = baseUrl || '';
    this.desiredToken = this.token;
    enableAuthOwnership(this);
  }

  getSessionVersion(): number {
    return this.sessionVersion;
  }

  async getToken(): Promise<string | null> {
    if (this.credentialBarrier) await this.credentialBarrier;
    return this.token;
  }

  setToken(token: string): void {
    this.replaceCredentials(token, null);
  }

  setRefreshToken(token: string): void {
    this.replaceCredentials(this.desiredToken, token);
  }

  isAuthenticated(): boolean {
    return this.token !== null;
  }

  async signup(username: string, password: string): Promise<LoginResponse> {
    return this.authenticate(`${this.baseUrl}/auth/v1/signup`, username, password);
  }

  async login(username: string, password: string): Promise<LoginResponse> {
    const url = this.config.refreshUrl?.replace('/refresh', '/login') || `${this.baseUrl}/auth/v1/login`;
    return this.authenticate(url, username, password);
  }

  private async authenticate(url: string, username: string, password: string): Promise<LoginResponse> {
    const version = this.clearSession();
    let data: LoginResponse;
    try {
      const response = await axios.post<LoginResponse>(url, { username, password });
      this.assertSession(version);
      data = response.data;
    } catch (error) {
      this.assertSession(version);
      throw error;
    }

    if (this.credentialBarrier) await this.credentialBarrier;
    this.assertSession(version);
    // Requests admitted while login was pending must not retry under the new identity.
    this.sessionVersion++;
    this.token = data.access_token;
    this.desiredToken = data.access_token;
    this._refreshToken = data.refresh_token;
    return data;
  }

  async logout(): Promise<void> {
    const refreshToken = this._refreshToken;
    this.clearSession();
    if (this.credentialBarrier) await this.credentialBarrier;
    if (refreshToken) {
      const url = this.config.refreshUrl?.replace('/refresh', '/logout') || `${this.baseUrl}/auth/v1/logout`;
      await axios.post(url, { refresh_token: refreshToken });
    }
  }

  async refreshToken(): Promise<string> {
    const version = this.sessionVersion;
    if (this.credentialBarrier) await this.credentialBarrier;
    this.assertSession(version);
    if (!this._refreshToken) {
      throw new Error('No refresh token available');
    }

    let operation = this.refreshOperation;
    if (!operation || operation.version !== version) {
      const refreshToken = this._refreshToken;
      const refreshUrl = this.config.refreshUrl || `${this.baseUrl}/auth/v1/refresh`;
      // Publish ownership before calling transport or application callbacks that can reenter.
      operation = {
        version,
        promise: Promise.resolve().then(() => this.performRefresh(refreshUrl, refreshToken, version)),
      };
      this.refreshOperation = operation;
    }

    try {
      const token = await operation.promise;
      this.assertSession(version);
      return token;
    } catch (error) {
      this.assertSession(version);
      throw error;
    } finally {
      if (this.refreshOperation === operation) {
        this.refreshOperation = null;
      }
    }
  }

  private async performRefresh(refreshUrl: string, refreshToken: string, version: number): Promise<string> {
    try {
      this.assertSession(version);
      const response = await axios.post(refreshUrl, { refresh_token: refreshToken });
      this.assertSession(version);

      const newToken = response.data.access_token;
      const newRefreshToken = response.data.refresh_token;
      if (!newToken) {
        throw new Error('Invalid refresh response: missing token');
      }

      if (!authOwnersAcceptToken(this, newToken, this.token)) {
        // The old HTTP request can itself belong to the drain. Reject it before waiting
        // for that drain; token readers wait for installation through the shared barrier.
        this.replaceCredentials(newToken, newRefreshToken || null);
        throw new AuthSessionChangedError();
      }
      this.token = newToken;
      this.desiredToken = newToken;
      if (newRefreshToken) {
        this._refreshToken = newRefreshToken;
      }

      this.config.onTokenRefresh?.(newToken);
      this.assertSession(version);
      return newToken;
    } catch (error) {
      this.assertSession(version);
      try {
        this.config.onAuthError?.(error as Error);
      } finally {
        this.assertSession(version);
      }
      throw error;
    }
  }

  private clearSession(): number {
    this.sessionVersion++;
    this.token = null;
    this.desiredToken = null;
    this._refreshToken = null;
    const closing = invalidateAuthOwners(this);
    if (closing) {
      const prior = this.credentialBarrier;
      this.setBarrier(Promise.allSettled(prior ? [prior, closing] : [closing]).then(results => {
        for (const result of results) if (result.status === 'rejected') throw result.reason;
      }));
    }
    return this.sessionVersion;
  }

  private setBarrier(barrier: Promise<void>): void {
    this.credentialBarrier = barrier;
    // Setters remain synchronous; readers observe a failed drain through this same promise.
    void barrier.then(() => {
      if (this.credentialBarrier === barrier) this.credentialBarrier = null;
    }, () => {});
  }

  private replaceCredentials(token: string | null, refreshToken: string | null): void {
    const version = this.clearSession();
    this.desiredToken = token;
    const install = () => {
      if (version !== this.sessionVersion) return;
      this.token = token;
      this._refreshToken = refreshToken;
    };
    if (this.credentialBarrier) this.setBarrier(this.credentialBarrier.then(install));
    else install();
  }

  private assertSession(version: number): void {
    if (version !== this.sessionVersion) {
      throw new AuthSessionChangedError();
    }
  }
}
