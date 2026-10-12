import { act, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthError, useSession, type ConsoleSession } from '../auth';
import { LanguageProvider } from '../i18n/provider';
import { createLanguageRuntime } from '../i18n/runtime';
import type { LanguageEnvironment } from '../i18n/environment';
import { ConsoleApp, safeReturnTarget } from './ConsoleApp';

vi.mock('../auth', async (importOriginal) => ({
  ...await importOriginal<typeof import('../auth')>(),
  useSession: vi.fn(),
}));

const session: ConsoleSession = {
  developer: { id: 'dev-123', username: 'developer' },
  expires_at: '2099-01-01T00:00:00Z',
  csrf_token: 'secret-token',
};
type SessionValue = ReturnType<typeof useSession>;
const anonymous = (): SessionValue => ({
  state: { status: 'anonymous', session: null, error: null, notice: null, mutation: null },
  login: vi.fn().mockResolvedValue(undefined),
  logout: vi.fn().mockResolvedValue(undefined),
  retry: vi.fn().mockResolvedValue(undefined),
});
let auth: SessionValue;
let mobile = false;
let mediaChange: (() => void) | undefined;

beforeEach(() => {
  auth = anonymous();
  vi.mocked(useSession).mockImplementation(() => auth);
  mobile = false;
  mediaChange = undefined;
  vi.stubGlobal('matchMedia', vi.fn(() => ({
    get matches() { return mobile; },
    media: '(max-width: 767px)',
    addEventListener: (_event: string, listener: () => void) => { mediaChange = listener; },
    removeEventListener: (_event: string, listener: () => void) => { if (mediaChange === listener) mediaChange = undefined; },
  })));
});
afterEach(() => vi.unstubAllGlobals());

const Location = () => <span data-testid="location">{useLocation().pathname}{useLocation().search}</span>;
const mount = (route = '/login', overrides: Partial<LanguageEnvironment> = {}) => {
  let preference = 'en';
  const runtime = createLanguageRuntime({ environment: {
    readPreference: () => preference,
    writePreference: (value) => { preference = value; },
    getLanguages: () => ['en'],
    setDocumentLanguage: (locale) => { document.documentElement.lang = locale; },
    subscribe: () => () => {},
    reportError: vi.fn(),
    ...overrides,
  } });
  const tree = <LanguageProvider runtime={runtime}><MemoryRouter initialEntries={[route]}><ConsoleApp /><Location /></MemoryRouter></LanguageProvider>;
  const view = render(tree);
  return { ...view, runtime, update: () => view.rerender(tree) };
};

describe('Console login and routing', () => {
  it('guards a protected route and submits exact credentials once with native form semantics', async () => {
    let finish!: () => void;
    auth.login = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    const user = userEvent.setup();
    mount('/overview');
    expect(screen.getByTestId('location')).toHaveTextContent('/login?returnTo=%2Foverview');
    const username = screen.getByLabelText('Username');
    const password = screen.getByLabelText('Password');
    expect(username).toHaveAttribute('autocomplete', 'username');
    expect(password).toHaveAttribute('autocomplete', 'current-password');
    await user.type(username, 'developer');
    await user.type(password, '  exact password  ');
    await user.keyboard('{Enter}');
    expect(auth.login).toHaveBeenCalledWith('developer', '  exact password  ');
    expect(screen.getByRole('button', { name: 'Signing in…' })).toBeDisabled();
    fireEvent.submit(password.closest('form')!);
    expect(auth.login).toHaveBeenCalledTimes(1);
    await act(async () => finish());
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeEnabled();
  });

  it('leaves an empty form unsubmitted and preserves password input while toggling visibility', async () => {
    const user = userEvent.setup();
    mount();
    expect(screen.getByRole('button', { name: 'Sign in' })).toBeDisabled();
    fireEvent.submit(screen.getByLabelText('Password').closest('form')!);
    expect(auth.login).not.toHaveBeenCalled();
    const password = screen.getByLabelText('Password');
    await user.type(password, 'secret');
    await user.click(screen.getByRole('button', { name: 'Show' }));
    expect(password).toHaveAttribute('type', 'text');
    expect(password).toHaveValue('secret');
    expect(screen.getByRole('button', { name: 'Hide' })).toHaveAttribute('aria-pressed', 'true');
    await user.click(screen.getByRole('button', { name: 'Hide' }));
    expect(password).toHaveAttribute('type', 'password');
  });

  it('focuses credential errors and translates them without replacing fields, focus, or route', async () => {
    const failure = new AuthError('unauthorized', 'login');
    auth.login = vi.fn().mockRejectedValue(failure);
    const user = userEvent.setup();
    const view = mount();
    const username = screen.getByLabelText('Username');
    const password = screen.getByLabelText('Password');
    await user.type(username, 'developer');
    await user.type(password, 'secret');
    await user.click(screen.getByRole('button', { name: 'Sign in' }));
    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent('The username or password is incorrect.');
    expect(alert).toHaveFocus();
    password.focus();
    act(() => view.runtime.setPreference('zh-CN'));
    expect(screen.getByLabelText('用户名')).toBe(username);
    expect(screen.getByLabelText('密码')).toBe(password);
    expect(password).toHaveValue('secret');
    expect(password).toHaveFocus();
    expect(alert).not.toHaveTextContent('The username or password is incorrect.');
    expect(document.title).not.toContain('Sign in');
    expect(screen.getByTestId('location')).toHaveTextContent('/login');
    act(() => view.runtime.setPreference('en'));
    expect(alert).toHaveTextContent('The username or password is incorrect.');
  });

  it('shows a reconciled anonymous mutation error and session-expiry notice', () => {
    auth.state.error = new AuthError('network', 'login', { requestId: 'request-42' });
    auth.state.notice = 'session-ended';
    mount();
    expect(screen.getByRole('alert')).toHaveTextContent('Could not reach the Console service.');
    expect(screen.getByRole('alert')).toHaveTextContent('Request ID: request-42');
    expect(screen.getByRole('status')).toHaveTextContent('Your session has ended.');
  });

  it.each(['/overview', 'https://evil.example/steal', '//evil.example/steal', '/unknown', '/overview?token=secret', ''])('only permits the app return route for %s', (target) => {
    expect(safeReturnTarget(`?returnTo=${encodeURIComponent(target)}`)).toBe('/overview');
  });

  it('redirects an authenticated login to overview without exposing CSRF data', () => {
    auth.state.status = 'authenticated';
    auth.state.session = session;
    mount('/login?returnTo=https%3A%2F%2Fevil.example');
    expect(screen.getByTestId('location')).toHaveTextContent('/overview');
    expect(screen.getByRole('heading', { name: 'Developer account' })).toBeVisible();
    expect(document.body).not.toHaveTextContent('secret-token');
    expect(document.title).toBe('Overview · Syntrix Console');
  });

  it('routes the root entry through the guarded overview', () => {
    mount('/');
    expect(screen.getByTestId('location')).toHaveTextContent('/login?returnTo=%2Foverview');
    expect(safeReturnTarget('')).toBe('/overview');
  });

  it.each(['anonymous', 'authenticated'] as const)('offers the appropriate destination for an unknown route while %s', async (status) => {
    auth.state.status = status;
    auth.state.session = status === 'authenticated' ? session : null;
    const user = userEvent.setup();
    mount('/does-not-exist');
    expect(screen.getByRole('heading', { name: 'Page not found' })).toBeVisible();
    await user.click(screen.getByRole('link', { name: status === 'authenticated' ? 'Go to overview' : 'Go to sign in' }));
    expect(screen.getByTestId('location')).toHaveTextContent(status === 'authenticated' ? '/overview' : '/login');
  });
});

describe('session authority screens', () => {
  it.each(['restoring', 'reconciling', 'signing_out'] as const)('hides retained private content while %s', (status) => {
    auth.state.status = status;
    auth.state.session = session;
    mount('/overview');
    expect(screen.queryByRole('heading', { name: 'Developer account' })).not.toBeInTheDocument();
    expect(screen.getByRole('status')).toBeVisible();
    expect(screen.getByRole('combobox', { name: 'Language' })).toBeEnabled();
    expect(screen.queryByText('developer')).not.toBeInTheDocument();
  });

  it('makes failed session checks visible, focused, and retryable without granting snapshot authority', async () => {
    auth.state.status = 'unavailable';
    auth.state.session = session;
    auth.state.error = new AuthError('unavailable', 'me');
    auth.state.notice = 'logout-failed';
    const user = userEvent.setup();
    mount('/overview');
    expect(screen.getByRole('alert')).toHaveFocus();
    expect(screen.getByRole('alert')).toHaveTextContent('The Console service is unavailable.');
    expect(screen.getByText('Sign-out was not confirmed. Your session must be checked before continuing.')).toBeVisible();
    await user.click(screen.getByRole('button', { name: 'Check session again' }));
    expect(auth.retry).toHaveBeenCalledOnce();
    expect(screen.queryByRole('button', { name: 'Sign out' })).not.toBeInTheDocument();
  });
});

describe('authenticated shell', () => {
  beforeEach(() => { auth.state.status = 'authenticated'; auth.state.session = session; });

  it('reports a failed logout and offers another sign-out without losing the developer', async () => {
    auth.state.notice = 'logout-failed';
    auth.state.error = new AuthError('unavailable', 'logout');
    const user = userEvent.setup();
    mount('/overview');
    expect(screen.getByRole('alert')).toHaveFocus();
    expect(screen.getByRole('alert')).toHaveTextContent('You are still signed in.');
    await user.click(screen.getByRole('button', { name: 'Try sign-out again' }));
    expect(auth.logout).toHaveBeenCalledOnce();
    await user.click(screen.getByRole('button', { name: 'Sign out' }));
    expect(auth.logout).toHaveBeenCalledTimes(2);
    expect(screen.getByText('dev-123')).toBeVisible();
  });

  it('resynchronizes a denied authenticated request without claiming logout', async () => {
    auth.state.error = new AuthError('forbidden', 'me');
    const user = userEvent.setup();
    mount('/overview');
    expect(screen.getByRole('alert')).toHaveTextContent('This request was denied.');
    expect(screen.queryByText('Sign-out was not confirmed. You are still signed in.')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Check session again' }));
    expect(auth.retry).toHaveBeenCalledOnce();
  });

  it('traps mobile drawer focus, closes with Escape/backdrop, and restores the trigger', async () => {
    mobile = true;
    const user = userEvent.setup();
    mount('/overview');
    const trigger = screen.getByRole('button', { name: 'Open navigation' });
    await user.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Navigation' });
    const close = within(dialog).getByRole('button', { name: 'Close navigation' });
    expect(close).toHaveFocus();
    expect(trigger.closest('.console-layout')).toHaveAttribute('inert');
    await user.keyboard('{ArrowDown}');
    expect(close).toHaveFocus();
    await user.tab();
    expect(within(dialog).getByRole('link', { name: 'Syntrix Console' })).toHaveFocus();
    await user.tab({ shift: true });
    expect(close).toHaveFocus();
    await user.keyboard('{Shift>}{Tab}{/Shift}');
    expect(within(dialog).getByRole('button', { name: 'Sign out' })).toHaveFocus();
    await user.tab();
    expect(close).toHaveFocus();
    await user.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
    expect(document.body.style.overflow).toBe('');
    await user.click(trigger);
    fireEvent.click(screen.getAllByRole('button', { name: 'Close navigation' })[0]!);
    expect(trigger).toHaveFocus();
    await user.click(trigger);
    await user.click(within(screen.getByRole('dialog')).getByRole('link', { name: 'Overview' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    await user.click(trigger);
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Close navigation' }));
    expect(trigger).toHaveFocus();
  });

  it('closes an open mobile drawer when the viewport becomes desktop and removes listeners', async () => {
    mobile = true;
    const user = userEvent.setup();
    const view = mount('/overview');
    await user.click(screen.getByRole('button', { name: 'Open navigation' }));
    act(() => { mobile = false; mediaChange!(); });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Open navigation' })).not.toBeInTheDocument();
    expect(screen.getByRole('navigation', { name: 'Main navigation' })).toBeVisible();
    view.unmount();
    expect(mediaChange).toBeUndefined();
  });

  it('signs out from mobile navigation and releases scroll locking', async () => {
    mobile = true;
    const user = userEvent.setup();
    mount('/overview');
    await user.click(screen.getByRole('button', { name: 'Open navigation' }));
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Sign out' }));
    expect(auth.logout).toHaveBeenCalledOnce();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(document.body.style.overflow).toBe('');
  });

  it('changes language without replacing the authenticated page or session', async () => {
    const user = userEvent.setup();
    mount('/overview');
    const main = screen.getByRole('main');
    const selector = screen.getByRole('combobox', { name: 'Language' });
    await user.selectOptions(selector, 'zh-CN');
    expect(screen.getByRole('main')).toBe(main);
    expect(screen.getByText('dev-123')).toBeVisible();
    expect(auth.login).not.toHaveBeenCalled();
    expect(auth.logout).not.toHaveBeenCalled();
    expect(screen.getByTestId('location')).toHaveTextContent('/overview');
    expect(selector).toHaveFocus();
  });
});

describe('language controls', () => {
  it('applies a chosen language and associates a persistence warning with the selector', async () => {
    const user = userEvent.setup();
    mount('/login', { writePreference: () => { throw new DOMException('Denied', 'SecurityError'); } });
    const selector = screen.getByRole('combobox', { name: 'Language' });
    await user.selectOptions(selector, 'zh-CN');
    expect(selector).toHaveValue('zh-CN');
    expect(selector).toHaveFocus();
    expect(document.documentElement.lang).toBe('zh-CN');
    expect(screen.getByRole('status')).toBeVisible();
    expect(selector).toHaveAttribute('aria-describedby', screen.getByRole('status').id);
    await user.selectOptions(selector, 'en');
    expect(screen.getByRole('status')).toHaveTextContent('Your language choice could not be saved in this browser.');
  });

  it('reports unreadable saved preference while keeping the system language usable', () => {
    mount('/login', { readPreference: () => { throw new DOMException('Denied', 'SecurityError'); } });
    expect(screen.getByRole('combobox', { name: 'Language' })).toHaveValue('system');
    expect(screen.getByRole('status')).toHaveTextContent('Your saved language could not be read.');
  });
});
