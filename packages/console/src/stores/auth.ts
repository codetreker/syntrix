import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { authApi, type LoginRequest, type User } from '../lib/api';

interface AuthState {
  token: string | null;
  refreshToken: string | null;
  user: User | null;
  isAuthenticated: boolean;
  isLoading: boolean;
  error: string | null;
  tokenExpiry: number | null; // Unix timestamp in seconds
  refreshTimerId: ReturnType<typeof setTimeout> | null;
  sessionWarningShown: boolean;

  // Actions
  login: (credentials: LoginRequest) => Promise<void>;
  logout: () => void;
  clearError: () => void;
  setUser: (user: User | null) => void;
  refreshSession: () => Promise<void>;
  setupTokenRefresh: () => void;
  getTimeUntilExpiry: () => number; // Returns seconds until expiry
}

// Parse JWT to get expiry time
function parseJwtExpiry(token: string): number | null {
  try {
    const payload = JSON.parse(atob(token.split('.')[1]));
    return payload.exp || null;
  } catch {
    return null;
  }
}

// Parse user info from JWT
function parseJwtUser(token: string, username: string): User {
  try {
    const payload = JSON.parse(atob(token.split('.')[1]));
    return {
      id: payload.oid || payload.sub || '',
      username: payload.username || username,
      role: payload.roles?.[0] || 'user',
      database: payload.tid || 'default',
    };
  } catch {
    return {
      id: '',
      username,
      role: 'user',
      database: 'default',
    };
  }
}

export const useAuthStore = create<AuthState>()(
  persist(
    (set, get) => ({
      token: null,
      refreshToken: null,
      user: null,
      isAuthenticated: false,
      isLoading: false,
      error: null,
      tokenExpiry: null,
      refreshTimerId: null,
      sessionWarningShown: false,

      login: async (credentials: LoginRequest) => {
        set({ isLoading: true, error: null });
        try {
          const response = await authApi.login(credentials);
          const token = response.token;
          const refreshToken = response.refreshToken || null;

          // Store token in localStorage for API interceptor
          localStorage.setItem('token', token);
          if (refreshToken) {
            localStorage.setItem('refreshToken', refreshToken);
          }

          // Parse user and expiry from JWT
          const user = parseJwtUser(token, credentials.username);
          const tokenExpiry = parseJwtExpiry(token);

          set({
            token,
            refreshToken,
            user,
            isAuthenticated: true,
            isLoading: false,
            tokenExpiry,
            sessionWarningShown: false,
          });

          // Setup automatic token refresh
          get().setupTokenRefresh();
        } catch (error: unknown) {
          const message = error instanceof Error ? error.message : 'Login failed';
          localStorage.removeItem('token');
          localStorage.removeItem('refreshToken');
          set({
            token: null,
            refreshToken: null,
            user: null,
            isAuthenticated: false,
            isLoading: false,
            error: message,
            tokenExpiry: null,
          });
          throw error;
        }
      },

      logout: () => {
        const { refreshTimerId, refreshToken } = get();
        if (refreshTimerId) {
          clearTimeout(refreshTimerId);
        }
        // Best-effort server-side logout (don't block on failure)
        if (refreshToken) {
          authApi.logout(refreshToken).catch(() => {});
        }
        localStorage.removeItem('token');
        localStorage.removeItem('refreshToken');
        set({
          token: null,
          refreshToken: null,
          user: null,
          isAuthenticated: false,
          error: null,
          tokenExpiry: null,
          refreshTimerId: null,
          sessionWarningShown: false,
        });
      },

      clearError: () => {
        set({ error: null });
      },

      setUser: (user: User | null) => {
        set({ user });
      },

      getTimeUntilExpiry: () => {
        const { tokenExpiry } = get();
        if (!tokenExpiry) return Infinity;
        return tokenExpiry - Math.floor(Date.now() / 1000);
      },

      refreshSession: async () => {
        const { refreshToken, user } = get();
        if (!refreshToken) {
          console.log('[Auth] No refresh token available');
          return;
        }

        try {
          console.log('[Auth] Refreshing token...');
          const response = await authApi.refresh(refreshToken);
          const newToken = response.token;
          const newRefreshToken = response.refreshToken || refreshToken;

          localStorage.setItem('token', newToken);
          if (newRefreshToken) {
            localStorage.setItem('refreshToken', newRefreshToken);
          }

          const newExpiry = parseJwtExpiry(newToken);
          const newUser = user ? parseJwtUser(newToken, user.username) : null;

          set({
            token: newToken,
            refreshToken: newRefreshToken,
            tokenExpiry: newExpiry,
            user: newUser || user,
            sessionWarningShown: false,
          });

          console.log('[Auth] Token refreshed successfully');
          get().setupTokenRefresh();
        } catch (error) {
          console.error('[Auth] Token refresh failed:', error);
          // If refresh fails, logout user
          get().logout();
        }
      },

      setupTokenRefresh: () => {
        const { tokenExpiry, refreshTimerId } = get();

        // Clear existing timer
        if (refreshTimerId) {
          clearTimeout(refreshTimerId);
        }

        if (!tokenExpiry) {
          console.log('[Auth] No token expiry, skipping refresh setup');
          return;
        }

        const now = Math.floor(Date.now() / 1000);
        const timeUntilExpiry = tokenExpiry - now;

        // Refresh 1 minute before expiry (or at 80% of lifetime)
        const refreshIn = Math.max(timeUntilExpiry - 60, timeUntilExpiry * 0.8);

        // Show warning 5 minutes before expiry
        const warnIn = Math.max(timeUntilExpiry - 300, 0);

        console.log(`[Auth] Token expires in ${timeUntilExpiry}s, will refresh in ${refreshIn}s`);

        // Setup warning timer (only if we haven't shown it yet)
        if (warnIn > 0 && warnIn < timeUntilExpiry) {
          setTimeout(() => {
            const state = get();
            if (state.isAuthenticated && !state.sessionWarningShown) {
              set({ sessionWarningShown: true });
              // Dispatch custom event for toast notification
              window.dispatchEvent(new CustomEvent('session-expiring', {
                detail: { expiresIn: 300 }
              }));
            }
          }, warnIn * 1000);
        }

        // Setup refresh timer
        if (refreshIn > 0) {
          const timerId = setTimeout(() => {
            get().refreshSession();
          }, refreshIn * 1000);
          set({ refreshTimerId: timerId });
        } else {
          // Token is about to expire, refresh immediately
          get().refreshSession();
        }
      },
    }),
    {
      name: 'auth-storage',
      partialize: (state) => ({
        token: state.token,
        refreshToken: state.refreshToken,
        user: state.user,
        isAuthenticated: state.isAuthenticated,
        tokenExpiry: state.tokenExpiry,
      }),
    }
  )
);

// Initialize token refresh on page load
const initAuth = () => {
  const state = useAuthStore.getState();
  const storedToken = localStorage.getItem('token');
  const storedRefreshToken = localStorage.getItem('refreshToken');

  if (storedToken && state.isAuthenticated) {
    const expiry = parseJwtExpiry(storedToken);
    const now = Math.floor(Date.now() / 1000);

    if (expiry && expiry > now) {
      // Token still valid, setup refresh
      useAuthStore.setState({
        token: storedToken,
        refreshToken: storedRefreshToken,
        tokenExpiry: expiry,
      });
      state.setupTokenRefresh();
    } else if (storedRefreshToken) {
      // Token expired but we have refresh token
      state.refreshSession();
    } else {
      // Token expired and no refresh token, logout
      state.logout();
    }
  }
};

// Run init after a short delay to ensure store is ready
setTimeout(initAuth, 100);
