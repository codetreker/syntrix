import { Trans, useLingui } from '@lingui/react/macro';
import { Link } from 'react-router-dom';
import { useSession } from '../auth';
import { Brand } from '../components/Brand';
import { LanguageSelect } from '../components/LanguageSelect';
import { PageTitle } from '../components/PageTitle';

export const NotFoundPage = () => {
  const { t } = useLingui();
  const { state } = useSession();
  const authenticated = state.status === 'authenticated';
  return (
    <main className="login-page">
      <PageTitle title={t`Page not found`} />
      <section className="login-card session-card">
        <Brand />
        <h1><Trans>Page not found</Trans></h1>
        <p><Trans>This page does not exist in the Console.</Trans></p>
        <Link className="button button-primary login-submit" to={authenticated ? '/overview' : '/login'}>
          {authenticated ? <Trans>Go to overview</Trans> : <Trans>Go to sign in</Trans>}
        </Link>
        <footer className="login-footer"><LanguageSelect /></footer>
      </section>
    </main>
  );
};
