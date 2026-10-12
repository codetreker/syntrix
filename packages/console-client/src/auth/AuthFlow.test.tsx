import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ConsoleApp } from '../app/ConsoleApp';
import { LanguageProvider } from '../i18n/provider';
import { createLanguageRuntime } from '../i18n/runtime';
import { SessionProvider } from './SessionProvider';
import { AuthError, type AuthTransport, type ConsoleSession } from './transport';

const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

let peerMessage: ((event: MessageEvent<unknown>) => void) | undefined;
beforeEach(() => {
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  class Channel {
    set onmessage(callback: (event: MessageEvent<unknown>) => void) { peerMessage = callback; }
    postMessage() {}
    close() {}
  }
  vi.stubGlobal('BroadcastChannel', Channel);
  vi.stubGlobal('matchMedia', vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })));
});
afterEach(() => { peerMessage = undefined; vi.unstubAllGlobals(); vi.restoreAllMocks(); });

const mount = (transport: AuthTransport) => {
  const runtime = createLanguageRuntime({ environment: {
    readPreference: () => 'en', writePreference: () => {}, getLanguages: () => ['en'],
    subscribe: () => () => {}, setDocumentLanguage: () => {}, reportError: vi.fn(),
  } });
  return render(<LanguageProvider runtime={runtime}><MemoryRouter initialEntries={['/login']}><SessionProvider transport={transport}><ConsoleApp /></SessionProvider></MemoryRouter></LanguageProvider>);
};

describe('real auth provider and Console form lifecycle', () => {
  it.each(['visibility', 'peer'] as const)('keeps fields and credential denial through queued %s revalidation', async (trigger) => {
    const login = deferred<ConsoleSession>();
    const read = deferred<ConsoleSession>();
    const api = {
      me: vi.fn<AuthTransport['me']>().mockRejectedValueOnce(new AuthError('unauthorized', 'me')).mockReturnValueOnce(read.promise),
      login: vi.fn<AuthTransport['login']>().mockReturnValueOnce(login.promise),
      logout: vi.fn<AuthTransport['logout']>(),
    };
    mount(api);
    const username = await screen.findByLabelText('Username');
    const password = screen.getByLabelText('Password');
    fireEvent.change(username, { target: { value: 'developer' } });
    fireEvent.change(password, { target: { value: ' exact wrong password ' } });
    fireEvent.submit(username.closest('form')!);
    expect(api.login).toHaveBeenCalledOnce();
    const signal = api.login.mock.calls[0]![2];
    if (trigger === 'visibility') fireEvent(document, new Event('visibilitychange'));
    else act(() => peerMessage!(new MessageEvent('message', { data: 'session-may-have-changed' })));
    expect(api.me).toHaveBeenCalledOnce();
    expect(signal.aborted).toBe(false);
    await act(async () => { login.reject(new AuthError('unauthorized', 'login', { status: 401 })); });
    await waitFor(() => expect(api.me).toHaveBeenCalledTimes(2));
    expect(screen.getByLabelText('Username')).toBe(username);
    expect(screen.getByLabelText('Password')).toBe(password);
    expect(username).toHaveValue('developer');
    expect(password).toHaveValue(' exact wrong password ');
    expect(screen.queryByRole('heading', { name: 'Checking your session…' })).not.toBeInTheDocument();
    await act(async () => { read.reject(new AuthError('unauthorized', 'me')); });
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('The username or password is incorrect.');
    expect(alert).toHaveFocus();
    expect(screen.getByLabelText('Username')).toBe(username);
    expect(screen.getByLabelText('Password')).toBe(password);
    expect(username).toHaveValue('developer');
    expect(password).toHaveValue(' exact wrong password ');
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled();
    expect(api.login).toHaveBeenCalledOnce();
  });

  it('keeps known-anonymous form input through an ordinary background read but fences a failed check', async () => {
    const read = deferred<ConsoleSession>();
    const api = {
      me: vi.fn<AuthTransport['me']>().mockRejectedValueOnce(new AuthError('unauthorized', 'me')).mockReturnValueOnce(read.promise),
      login: vi.fn<AuthTransport['login']>(), logout: vi.fn<AuthTransport['logout']>(),
    };
    mount(api);
    const username = await screen.findByLabelText('Username');
    fireEvent.change(username, { target: { value: 'typed-account' } });
    fireEvent(document, new Event('visibilitychange'));
    expect(screen.getByLabelText('Username')).toBe(username);
    expect(username).toHaveValue('typed-account');
    await act(async () => { read.reject(new AuthError('unavailable', 'me', { status: 503 })); });
    expect(await screen.findByRole('heading', { name: 'Could not confirm your session' })).toBeVisible();
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Developer account' })).not.toBeInTheDocument();
  });
});
