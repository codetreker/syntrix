import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { Link, NavLink } from 'react-router-dom';
import { useSession, type ConsoleSession } from '../auth';
import { AuthErrorMessage } from './AuthErrorMessage';
import { Brand } from './Brand';
import { LanguageSelect } from './LanguageSelect';

const useMobileViewport = () => {
  const [mobile, setMobile] = useState(() => window.matchMedia('(max-width: 767px)').matches);
  useEffect(() => {
    const query = window.matchMedia('(max-width: 767px)');
    const update = () => setMobile(query.matches);
    query.addEventListener('change', update);
    update();
    return () => query.removeEventListener('change', update);
  }, []);
  return mobile;
};

const Sidebar = ({ session, close }: { session: ConsoleSession; close?: () => void }) => {
  const { t } = useLingui();
  const { logout } = useSession();
  return (
    <>
      <div className="sidebar-brand"><Link to="/overview" onClick={close}><Brand /></Link></div>
      <nav aria-label={t`Main navigation`} className="sidebar-navigation">
        <NavLink to="/overview" onClick={close} className={({ isActive }) => `navigation-link${isActive ? ' active' : ''}`}>
          <svg viewBox="0 0 20 20" aria-hidden="true"><rect x="3" y="3" width="5" height="5" rx="1" /><rect x="12" y="3" width="5" height="5" rx="1" /><rect x="3" y="12" width="5" height="5" rx="1" /><rect x="12" y="12" width="5" height="5" rx="1" /></svg>
          <Trans>Overview</Trans>
        </NavLink>
      </nav>
      <footer className="sidebar-footer">
        <LanguageSelect />
        <div className="sidebar-account"><span><Trans>Signed in as</Trans></span><strong>{session.developer.username}</strong></div>
        <button className="button logout-button" onClick={() => { close?.(); void logout(); }}><Trans>Sign out</Trans></button>
      </footer>
    </>
  );
};

export const ConsoleShell = ({ session, children }: { session: ConsoleSession; children: ReactNode }) => {
  const { t } = useLingui();
  const { state, logout, retry } = useSession();
  const mobile = useMobileViewport();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const drawerRef = useRef<HTMLDivElement>(null);
  const noticeRef = useRef<HTMLDivElement>(null);

  useEffect(() => { if (!mobile) setDrawerOpen(false); }, [mobile]);
  useEffect(() => {
    if (!drawerOpen) return;
    const originalOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    drawerRef.current?.querySelector<HTMLButtonElement>('button')?.focus();
    return () => {
      document.body.style.overflow = originalOverflow;
      triggerRef.current?.focus();
    };
  }, [drawerOpen]);
  useEffect(() => { if (state.notice === 'logout-failed') noticeRef.current?.focus(); }, [state.notice]);

  const drawerKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (event.key === 'Escape') {
      event.preventDefault();
      setDrawerOpen(false);
      return;
    }
    if (event.key !== 'Tab') return;
    const controls = drawerRef.current!.querySelectorAll<HTMLElement>('a[href], button:not([disabled]), select:not([disabled]), [tabindex="0"]');
    const first = controls[0]!;
    const last = controls[controls.length - 1]!;
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  };

  return (
    <>
      <div className="console-layout" inert={drawerOpen}>
        <a className="skip-link" href="#console-main"><Trans>Skip to content</Trans></a>
        {mobile ? (
          <header className="mobile-header">
            <Link to="/overview"><Brand /></Link>
            <button className="button navigation-toggle" ref={triggerRef} aria-label={t`Open navigation`} aria-controls="console-navigation-drawer" aria-expanded={drawerOpen} onClick={() => setDrawerOpen(true)}>
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 6h14M5 12h14M5 18h14" /></svg>
            </button>
          </header>
        ) : <aside className="console-sidebar"><Sidebar session={session} /></aside>}
        <main className="console-main" id="console-main" tabIndex={-1}>
          {(state.notice === 'logout-failed' || state.error) && (
            <div className="notice notice-error shell-notice" role="alert" tabIndex={-1} ref={noticeRef}>
              {state.notice === 'logout-failed' && <span><Trans>Sign-out was not confirmed. You are still signed in.</Trans></span>}
              {state.error && <AuthErrorMessage error={state.error} />}
              <button className="button" onClick={() => void (state.notice === 'logout-failed' ? logout() : retry())}>
                {state.notice === 'logout-failed' ? <Trans>Try sign-out again</Trans> : <Trans>Check session again</Trans>}
              </button>
            </div>
          )}
          {children}
        </main>
      </div>
      {mobile && drawerOpen && (
        <div className="drawer-layer">
          <button className="drawer-backdrop" aria-label={t`Close navigation`} tabIndex={-1} onClick={() => setDrawerOpen(false)} />
          <div className="navigation-drawer" id="console-navigation-drawer" role="dialog" aria-modal="true" aria-label={t`Navigation`} ref={drawerRef} onKeyDown={drawerKeyDown}>
            <button className="button drawer-close" aria-label={t`Close navigation`} onClick={() => setDrawerOpen(false)}>×</button>
            <Sidebar session={session} close={() => setDrawerOpen(false)} />
          </div>
        </div>
      )}
    </>
  );
};
