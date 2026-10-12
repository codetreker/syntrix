import { Navigate, Route, Routes, useLocation } from 'react-router-dom';
import { useSession } from '../auth';
import { ConsoleShell } from '../components/ConsoleShell';
import { LoginPage } from '../pages/LoginPage';
import { NotFoundPage } from '../pages/NotFoundPage';
import { OverviewPage } from '../pages/OverviewPage';
import { SessionPage } from '../pages/SessionPage';

export const safeReturnTarget = (search: string) => {
  const target = new URLSearchParams(search).get('returnTo');
  return target === '/overview' ? target : '/overview';
};

const LoginRoute = () => {
  const { state } = useSession();
  const location = useLocation();
  return state.status === 'authenticated'
    ? <Navigate to={safeReturnTarget(location.search)} replace />
    : <LoginPage />;
};

const OverviewRoute = () => {
  const { state } = useSession();
  return state.status === 'authenticated' && state.session
    ? <ConsoleShell session={state.session}><OverviewPage session={state.session} /></ConsoleShell>
    : <Navigate to="/login?returnTo=%2Foverview" replace />;
};

export const ConsoleApp = () => {
  const { state } = useSession();
  if (state.status !== 'anonymous' && state.status !== 'authenticated') return <SessionPage />;
  return (
    <Routes>
      <Route path="/" element={<Navigate to="/overview" replace />} />
      <Route path="/login" element={<LoginRoute />} />
      <Route path="/overview" element={<OverviewRoute />} />
      <Route path="*" element={<NotFoundPage />} />
    </Routes>
  );
};
