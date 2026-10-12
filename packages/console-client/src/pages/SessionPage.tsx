import { Trans, useLingui } from '@lingui/react/macro';
import { useEffect, useRef } from 'react';
import { useSession } from '../auth';
import { AuthErrorMessage } from '../components/AuthErrorMessage';
import { Brand } from '../components/Brand';
import { LanguageSelect } from '../components/LanguageSelect';
import { PageTitle } from '../components/PageTitle';

export const SessionPage = () => {
  const { t } = useLingui();
  const { state, retry } = useSession();
  const errorRef = useRef<HTMLDivElement>(null);
  const failed = state.status === 'unavailable';
  useEffect(() => { if (failed) errorRef.current?.focus(); }, [failed]);

  return (
    <main className="login-page">
      <PageTitle title={failed ? t`Session unavailable` : t`Checking session`} />
      <section className="login-card session-card" aria-labelledby="session-heading" aria-busy={!failed}>
        <Brand />
        {!failed && <span className="spinner spinner-large" aria-hidden="true" />}
        <h1 id="session-heading">
          {failed ? <Trans>Could not confirm your session</Trans>
            : state.status === 'signing_out' ? <Trans>Signing out…</Trans>
              : state.status === 'reconciling' ? <Trans>Checking your session…</Trans>
                : <Trans>Restoring your session…</Trans>}
        </h1>
        <p role={failed ? undefined : 'status'}>
          {failed
            ? <Trans>Check your session to continue safely.</Trans>
            : state.status === 'signing_out'
              ? <Trans>Waiting for the Console service to confirm sign-out.</Trans>
              : <Trans>Please wait while we confirm your account.</Trans>}
        </p>
        {state.notice === 'logout-failed' && <p className="notice notice-error"><Trans>Sign-out was not confirmed. Your session must be checked before continuing.</Trans></p>}
        {failed && state.error && <div className="notice notice-error" role="alert" tabIndex={-1} ref={errorRef}><AuthErrorMessage error={state.error} /></div>}
        {failed && <button className="button button-primary login-submit" onClick={() => void retry()}><Trans>Check session again</Trans></button>}
        <footer className="login-footer"><LanguageSelect /></footer>
      </section>
    </main>
  );
};
