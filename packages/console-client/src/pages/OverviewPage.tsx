import { Trans, useLingui } from '@lingui/react/macro';
import type { ConsoleSession } from '../auth';
import { PageTitle } from '../components/PageTitle';

export const OverviewPage = ({ session }: { session: ConsoleSession }) => {
  const { t } = useLingui();
  const username = session.developer.username;

  return (
    <>
      <PageTitle title={t`Overview`} />
      <header className="page-heading">
        <span className="page-eyebrow"><Trans>Developer Console</Trans></span>
        <h1><Trans>Overview</Trans></h1>
        <p><Trans>Welcome, {username}.</Trans></p>
      </header>
      <section className="overview-panel" aria-labelledby="account-heading">
        <div className="panel-heading">
          <span className="account-symbol" aria-hidden="true">{username.slice(0, 1).toUpperCase()}</span>
          <div>
            <h2 id="account-heading"><Trans>Developer account</Trans></h2>
            <p><Trans>You are signed in to Syntrix Console.</Trans></p>
          </div>
        </div>
        <dl className="account-details">
          <div><dt><Trans>Username</Trans></dt><dd>{username}</dd></div>
          <div><dt><Trans>Account ID</Trans></dt><dd className="identifier">{session.developer.id}</dd></div>
        </dl>
      </section>
    </>
  );
};
