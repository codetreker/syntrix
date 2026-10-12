import { StrictMode } from 'react';
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { SessionProvider, useSession } from './SessionProvider';
import { AuthError, type AuthTransport, type ConsoleSession } from './transport';

const session: ConsoleSession = { developer: { id: 'alice', username: 'Alice' }, expires_at: new Date(Date.now() + 60_000).toISOString(), csrf_token: 'csrf' };
const Probe = () => {
  const { state, login, logout, retry } = useSession();
  return <>
    <output>{state.status}:{state.session?.developer.username}</output>
    <button onClick={() => void login('alice', 'password')}>Login</button>
    <button onClick={() => void logout()}>Logout</button>
    <button onClick={() => void retry()}>Retry</button>
  </>;
};
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('SessionProvider ownership', () => {
  it('restores through StrictMode cleanup and exposes login/logout/retry', async () => {
    const api: AuthTransport = {
      me: vi.fn().mockRejectedValue(new AuthError('unauthorized', 'me')),
      login: vi.fn().mockResolvedValue(session),
      logout: vi.fn().mockResolvedValue(undefined),
    };
    render(<StrictMode><SessionProvider transport={api}><Probe /></SessionProvider></StrictMode>);
    await screen.findByText('anonymous:');
    expect(api.me).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.me).mock.calls[0]![0].aborted).toBe(true);
    vi.mocked(api.me).mockResolvedValueOnce(session);
    fireEvent.click(screen.getByRole('button', { name: 'Login' }));
    await screen.findByText('authenticated:Alice');
    expect(api.login).toHaveBeenCalledWith('alice', 'password', expect.any(AbortSignal));
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByText('anonymous:');
    vi.mocked(api.me).mockResolvedValueOnce(session);
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await screen.findByText('authenticated:Alice');
    fireEvent.click(screen.getByRole('button', { name: 'Logout' }));
    await screen.findByText('anonymous:');
    expect(api.logout).toHaveBeenCalledWith('csrf', expect.any(AbortSignal));
  });

  it('does not rebuild session ownership when ordinary child state changes', async () => {
    const api = { me: vi.fn<AuthTransport['me']>().mockResolvedValue(session), login: vi.fn<AuthTransport['login']>(), logout: vi.fn<AuthTransport['logout']>() };
    const view = render(<SessionProvider transport={api}><Probe /><p>English</p></SessionProvider>);
    await screen.findByText('authenticated:Alice');
    view.rerender(<SessionProvider transport={api}><Probe /><p>简体中文</p></SessionProvider>);
    expect(screen.getByText('authenticated:Alice')).toBeDefined();
    expect(api.me).toHaveBeenCalledOnce();
  });

  it('requires the provider instead of silently granting a default session', () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(() => render(<Probe />)).toThrow('useSession requires a SessionProvider');
    consoleError.mockRestore();
  });

  it('defaults to the real same-origin transport', async () => {
    const fetch = vi.fn<typeof globalThis.fetch>().mockResolvedValue(Response.json(session));
    vi.stubGlobal('fetch', fetch);
    render(<SessionProvider><Probe /></SessionProvider>);
    await screen.findByText('authenticated:Alice');
    expect(fetch).toHaveBeenCalledWith('/auth/v1/me', expect.objectContaining({ credentials: 'same-origin' }));
  });

  it('revalidates visibility without BroadcastChannel and cleans the subscription', async () => {
    vi.stubGlobal('BroadcastChannel', undefined);
    const api = { me: vi.fn<AuthTransport['me']>().mockResolvedValue(session), login: vi.fn<AuthTransport['login']>(), logout: vi.fn<AuthTransport['logout']>() };
    const remove = vi.spyOn(document, 'removeEventListener');
    const view = render(<SessionProvider transport={api}><Probe /></SessionProvider>);
    await screen.findByText('authenticated:Alice');
    await act(async () => { document.dispatchEvent(new Event('visibilitychange')); });
    await waitFor(() => expect(api.me).toHaveBeenCalledTimes(2));
    view.unmount();
    expect(remove).toHaveBeenCalledWith('visibilitychange', expect.any(Function));
  });

  it('uses a secret-free channel and ignores unrelated messages', async () => {
    let message: ((event: MessageEvent<unknown>) => void) | null = null;
    const notify = vi.fn();
    const close = vi.fn();
    class Channel {
      set onmessage(callback: (event: MessageEvent<unknown>) => void) { message = callback; }
      postMessage = notify;
      close = close;
    }
    vi.stubGlobal('BroadcastChannel', Channel);
    const api = { me: vi.fn<AuthTransport['me']>().mockResolvedValue(session), login: vi.fn<AuthTransport['login']>().mockResolvedValue(session), logout: vi.fn<AuthTransport['logout']>() };
    const view = render(<SessionProvider transport={api}><Probe /></SessionProvider>);
    await screen.findByText('authenticated:Alice');
    await act(async () => { message!(new MessageEvent('message', { data: 'unrelated' })); });
    expect(api.me).toHaveBeenCalledOnce();
    await act(async () => { message!(new MessageEvent('message', { data: 'session-may-have-changed' })); });
    expect(api.me).toHaveBeenCalledTimes(2);
    fireEvent.click(screen.getByRole('button', { name: 'Login' }));
    await waitFor(() => expect(notify).toHaveBeenCalledWith('session-may-have-changed'));
    expect(notify.mock.calls).toEqual([['session-may-have-changed']]);
    view.unmount(); expect(close).toHaveBeenCalledOnce();
  });
});
