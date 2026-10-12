import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { AuthError, useSession } from '../auth';
import { AuthErrorMessage } from '../components/AuthErrorMessage';
import { Brand } from '../components/Brand';
import { LanguageSelect } from '../components/LanguageSelect';
import { PageTitle } from '../components/PageTitle';

export const LoginPage = () => {
  const { t } = useLingui();
  const { state, login } = useSession();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [error, setError] = useState<AuthError | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const pending = useRef(false);
  const errorRef = useRef<HTMLDivElement>(null);
  const displayedError = error ?? state.error;

  useEffect(() => { if (displayedError) errorRef.current?.focus(); }, [displayedError]);

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (pending.current || !username || !password) return;
    pending.current = true;
    setSubmitting(true);
    setError(null);
    try {
      await login(username, password);
    } catch (failure) {
      if (!(failure instanceof AuthError)) throw failure;
      setError(failure);
    } finally {
      pending.current = false;
      setSubmitting(false);
    }
  };

  return (
    <main className="login-page">
      <PageTitle title={t`Sign in`} />
      <section className="login-card" aria-labelledby="login-heading">
        <Brand />
        <header className="login-heading">
          <h1 id="login-heading"><Trans>Sign in</Trans></h1>
          <p><Trans>Use your developer account to continue.</Trans></p>
        </header>
        {state.notice === 'session-ended' && (
          <p className="notice notice-info" role="status"><Trans>Your session has ended. Sign in again to continue.</Trans></p>
        )}
        <form onSubmit={submit} aria-busy={submitting}>
          <div className="form-field">
            <label htmlFor="console-username"><Trans>Username</Trans></label>
            <input id="console-username" name="username" autoComplete="username" autoCapitalize="none" spellCheck={false} value={username} onChange={(event) => setUsername(event.target.value)} required disabled={submitting} />
          </div>
          <div className="form-field">
            <label htmlFor="console-password"><Trans>Password</Trans></label>
            <div className="password-control">
              <input id="console-password" name="password" type={passwordVisible ? 'text' : 'password'} autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required disabled={submitting} />
              <button className="password-toggle" type="button" aria-controls="console-password" aria-pressed={passwordVisible} disabled={submitting} onMouseDown={(event) => event.preventDefault()} onClick={() => setPasswordVisible((visible) => !visible)}>
                {passwordVisible ? <Trans>Hide</Trans> : <Trans>Show</Trans>}
              </button>
            </div>
          </div>
          {displayedError && <div className="notice notice-error" role="alert" tabIndex={-1} ref={errorRef}><AuthErrorMessage error={displayedError} /></div>}
          <button className="button button-primary login-submit" type="submit" disabled={submitting || !username || !password}>
            {submitting && <span className="spinner" aria-hidden="true" />}
            {submitting ? <Trans>Signing in…</Trans> : <Trans>Sign in</Trans>}
          </button>
        </form>
        <footer className="login-footer"><LanguageSelect /></footer>
      </section>
    </main>
  );
};
