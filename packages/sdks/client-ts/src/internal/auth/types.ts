export interface AuthConfig {
  token?: string;
  refreshToken?: string;
  refreshUrl?: string;
  database?: string;
  onTokenRefresh?: (newToken: string) => void;
  onAuthError?: (error: Error) => void;
}

export interface TokenProvider {
  /** Changes on explicit credential replacement, but not same-session refresh. */
  getSessionVersion(): number;
  getToken(): Promise<string | null>;
  setToken(token: string): void;
  setRefreshToken(token: string): void;
  refreshToken(): Promise<string>;
}

export interface LoginResponse {
  access_token: string;
  refresh_token: string;
  expires_in: number;
}

export interface AuthService {
  signup(username: string, password: string): Promise<LoginResponse>;
  login(username: string, password: string): Promise<LoginResponse>;
  logout(): Promise<void>;
  isAuthenticated(): boolean;
}
