import { AuthError, type AuthTransport, type ConsoleSession } from '../auth';

export type MockupScenario =
  | 'login'
  | 'login-pending'
  | 'invalid-credentials'
  | 'login-unavailable'
  | 'restoring'
  | 'restore-unavailable'
  | 'overview'
  | 'session-expired'
  | 'denied'
  | 'logout-pending'
  | 'logout-failed'
  | 'uncertain-login'
  | 'uncertain-session'
  | 'language-storage';

const pause = (milliseconds: number, signal: AbortSignal) => new Promise<void>((resolve, reject) => {
  if (signal.aborted) {
    reject(new AuthError('canceled', 'me'));
    return;
  }
  const abort = () => {
    clearTimeout(timer);
    reject(new AuthError('canceled', 'me'));
  };
  const timer = setTimeout(() => { signal.removeEventListener('abort', abort); resolve(); }, milliseconds);
  signal.addEventListener('abort', abort, { once: true });
});

export const createMockupTransport = (scenario: MockupScenario): AuthTransport => {
  const initiallySignedIn = ['overview', 'session-expired', 'logout-pending', 'logout-failed', 'uncertain-session'].includes(scenario);
  let session: ConsoleSession | null = initiallySignedIn ? {
    developer: { id: 'developer-preview', username: 'developer' },
    expires_at: new Date(Date.now() + (scenario === 'session-expired' ? 4_000 : 3_600_000)).toISOString(),
    csrf_token: 'preview-only-csrf',
  } : null;
  let reads = 0;
  let mutationAttempted = false;

  return {
    me: async (signal) => {
      await pause(scenario === 'restoring' ? 30_000 : scenario === 'uncertain-login' && mutationAttempted ? 3_000 : 150, signal);
      reads++;
      if (scenario === 'restore-unavailable' || (scenario === 'uncertain-session' && mutationAttempted)) {
        throw new AuthError('unavailable', 'me', { status: 503, requestId: 'preview-request' });
      }
      if (scenario === 'session-expired' && reads > 1) session = null;
      if (!session) throw new AuthError('unauthorized', 'me', { status: 401 });
      return session;
    },
    login: async (username, _password, signal) => {
      await pause(scenario === 'login-pending' ? 30_000 : 700, signal);
      mutationAttempted = true;
      if (scenario === 'invalid-credentials') throw new AuthError('unauthorized', 'login', { status: 401 });
      if (scenario === 'denied') throw new AuthError('forbidden', 'login', { status: 403, requestId: 'preview-request' });
      if (scenario === 'login-unavailable') throw new AuthError('unavailable', 'login', { status: 503, requestId: 'preview-request' });
      session = { developer: { id: 'developer-preview', username }, expires_at: new Date(Date.now() + 3_600_000).toISOString(), csrf_token: 'preview-only-csrf' };
      if (scenario === 'uncertain-login') throw new AuthError('invalid-response', 'login', { status: 200 });
      return session;
    },
    logout: async (_csrfToken, signal) => {
      await pause(scenario === 'logout-pending' ? 30_000 : 700, signal);
      mutationAttempted = true;
      if (scenario === 'logout-failed') throw new AuthError('unavailable', 'logout', { status: 503, requestId: 'preview-request' });
      session = null;
      if (scenario === 'uncertain-session') throw new AuthError('network', 'logout');
    },
  };
};
