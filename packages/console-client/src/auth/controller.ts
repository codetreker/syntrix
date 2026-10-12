import { AuthError, type AuthPhase, type AuthTransport, type ConsoleSession } from './transport';

export type SessionStatus =
  | 'restoring'
  | 'anonymous'
  | 'authenticated'
  | 'signing_out'
  | 'reconciling'
  | 'unavailable';
export type SessionNotice = 'session-ended' | 'logout-failed' | null;
export type SessionState = {
  status: SessionStatus;
  session: ConsoleSession | null;
  error: AuthError | null;
  notice: SessionNotice;
  mutation: 'login' | 'logout' | null;
};

export type SessionRuntime = {
  now: () => number;
  schedule: (callback: () => void, delay: number) => () => void;
  visible: () => boolean;
  onVisibility: (callback: () => void) => () => void;
  channel: (callback: () => void) => { notify: () => void; close: () => void } | null;
};

const browserRuntime: SessionRuntime = {
  now: () => Date.now(),
  schedule: (callback, delay) => {
    const timer = setTimeout(callback, delay);
    return () => clearTimeout(timer);
  },
  visible: () => document.visibilityState === 'visible',
  onVisibility: (callback) => {
    document.addEventListener('visibilitychange', callback);
    return () => document.removeEventListener('visibilitychange', callback);
  },
  channel: (callback) => {
    if (typeof BroadcastChannel === 'undefined') return null;
    const channel = new BroadcastChannel('syntrix.console.session');
    channel.onmessage = (event: MessageEvent<unknown>) => {
      if (event.data === 'session-may-have-changed') callback();
    };
    return {
      notify: () => channel.postMessage('session-may-have-changed'),
      close: () => channel.close(),
    };
  },
};

const asAuthError = (error: unknown, phase: AuthPhase): AuthError =>
  error instanceof AuthError ? error : new AuthError('network', phase, { cause: error });

export const createSessionController = (transport: AuthTransport, runtime: SessionRuntime = browserRuntime) => {
  let state: SessionState = { status: 'restoring', session: null, error: null, notice: null, mutation: null };
  const listeners = new Set<() => void>();
  let connected = false;
  let epoch = 0;
  let read: AbortController | null = null;
  let pending: { controller: AbortController; epoch: number } | null = null;
  let dirty = false;
  let stopExpiry: (() => void) | null = null;
  let stopVisibility: (() => void) | null = null;
  let channel: ReturnType<SessionRuntime['channel']> = null;

  const publish = (next: SessionState) => {
    state = next;
    stopExpiry?.();
    stopExpiry = null;
    if (connected && state.status === 'authenticated' && state.session !== null) {
      // Browser clock skew must not cause immediate revalidation loops.
      const delay = Math.min(2_147_483_647, Math.max(1000, Date.parse(state.session.expires_at) - runtime.now()));
      stopExpiry = runtime.schedule(() => { void revalidate(); }, delay);
    }
    listeners.forEach((listener) => listener());
  };

  const revalidate = async (
    options: { notice?: SessionNotice; error?: AuthError | null } = {},
  ): Promise<void> => {
    if (!connected) return;
    if (pending !== null) {
      dirty = true;
      return;
    }
    read?.abort();
    const controller = new AbortController();
    read = controller;
    const generation = ++epoch;
    const previous = state.session;
    const notice = options.notice === undefined ? state.notice : options.notice;
    const retainedError = options.error === undefined ? state.error : options.error;
    const publicForm = state.status === 'anonymous' && previous === null && retainedError === null;
    publish({ ...state, status: state.status === 'restoring' ? 'restoring' : publicForm ? 'anonymous' : 'reconciling', notice });
    try {
      const session = await transport.me(controller.signal);
      if (!connected || epoch !== generation) return;
      publish({ status: 'authenticated', session, error: notice === 'logout-failed' ? retainedError : null, notice: notice === 'session-ended' ? null : notice, mutation: null });
    } catch (cause) {
      if (!connected || epoch !== generation) return;
      const error = asAuthError(cause, 'me');
      if (error.category === 'unauthorized') {
        const ended = options.notice === undefined && previous !== null ? 'session-ended' : notice;
        publish({ status: 'anonymous', session: null, error: options.error ?? null, notice: ended, mutation: null });
      } else {
        publish({ status: 'unavailable', session: previous, error, notice, mutation: null });
      }
    } finally {
      if (read === controller) read = null;
    }
  };

  const mutate = async (phase: 'login' | 'logout', username?: string, password?: string): Promise<void> => {
    if (!connected || pending !== null) throw new AuthError('busy', phase);
    if (phase === 'logout' && state.session === null) return;
    if (phase === 'logout' && (state.status === 'unavailable' || (state.status === 'reconciling' && state.error !== null))) {
      throw new AuthError('busy', phase);
    }
    const interruptedRead = read !== null;
    read?.abort();
    const operation = { controller: new AbortController(), epoch: ++epoch };
    pending = operation;
    dirty = interruptedRead;
    const previous = state;
    publish({ ...state, status: phase === 'logout' ? 'signing_out' : state.session === null ? 'anonymous' : 'reconciling', error: null, notice: null, mutation: phase });
    let error: AuthError | null = null;
    let success = false;
    try {
      if (phase === 'login') {
        const session = await transport.login(username as string, password as string, operation.controller.signal);
        if (!connected || pending !== operation) return;
        publish({ ...state, status: 'reconciling', session });
      } else {
        await transport.logout(previous.session!.csrf_token, operation.controller.signal);
        if (!connected || pending !== operation) return;
        publish({ ...state, session: null });
      }
      success = true;
    } catch (cause) {
      if (!connected || pending !== operation) return;
      error = asAuthError(cause, phase);
    } finally {
      if (pending === operation) pending = null;
    }
    if (!connected || epoch !== operation.epoch) return;

    const denial = error !== null && ['unauthorized', 'forbidden', 'invalid-request', 'not-found', 'conflict', 'busy'].includes(error.category);
    const uncertain = error !== null && !denial;
    const logoutExpired = phase === 'logout' && error?.category === 'unauthorized';
    const logoutDenied = phase === 'logout' && error?.category === 'forbidden';
    if (success || uncertain || logoutExpired) channel?.notify();

    if (logoutExpired) publish({ status: 'anonymous', session: null, error: null, notice: null, mutation: null });
    else if (error !== null) publish({ ...previous, status: uncertain || logoutDenied ? 'reconciling' : previous.status, mutation: null, error: phase === 'logout' || uncertain ? error : null, notice: phase === 'logout' ? 'logout-failed' : previous.notice });
    else publish({ ...state, mutation: null });

    const reconcile = success || uncertain || logoutDenied || dirty;
    dirty = false;
    if (reconcile) {
      await revalidate({
        ...(phase === 'logout' ? { notice: error === null || logoutExpired ? null : 'logout-failed' } : {}),
        error: error !== null && (phase === 'logout' || uncertain) ? error : null,
      });
    }
    if (phase === 'login' && error !== null && denial) throw error;
  };

  return {
    getSnapshot: () => state,
    subscribe: (listener: () => void) => {
      listeners.add(listener);
      return () => { listeners.delete(listener); };
    },
    connect: () => {
      if (connected) return () => {};
      connected = true;
      publish({ ...state, status: 'restoring', error: null, mutation: null });
      stopVisibility = runtime.onVisibility(() => { if (runtime.visible()) void revalidate(); });
      channel = runtime.channel(() => { if (runtime.visible()) void revalidate(); });
      void revalidate();
      return () => {
        connected = false;
        ++epoch;
        read?.abort();
        read = null;
        pending?.controller.abort();
        pending = null;
        dirty = false;
        stopExpiry?.();
        stopExpiry = null;
        stopVisibility?.();
        stopVisibility = null;
        channel?.close();
        channel = null;
      };
    },
    login: (username: string, password: string) => mutate('login', username, password),
    logout: () => mutate('logout'),
    retry: () => revalidate(),
  };
};

export type SessionController = ReturnType<typeof createSessionController>;
