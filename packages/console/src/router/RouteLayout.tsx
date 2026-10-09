import { Suspense, type ReactNode } from 'react';
import { Navigate, Outlet } from 'react-router-dom';
import { Layout, AuthLayout } from '../components/layout';
import { PageLoading } from '../components/ui';
import { useAuthStore } from '../stores';

export const SuspenseWrapper = ({ children }: { children: ReactNode }) => {
  return <Suspense fallback={<PageLoading />}>{children}</Suspense>;
};

export const ProtectedRoute = () => {
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);

  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }

  return <Outlet />;
};

export const AuthRoute = () => {
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);

  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }

  return (
    <AuthLayout>
      <Outlet />
    </AuthLayout>
  );
};

export const AppLayout = ({ basePath }: { basePath: string }) => {
  const { user, logout } = useAuthStore();
  const username = user?.username || 'User';
  const isAdmin = user?.role === 'admin';

  const handleLogout = () => {
    logout();
    window.location.href = `${basePath}/login`;
  };

  return (
    <Layout username={username} isAdmin={isAdmin} onLogout={handleLogout}>
      <Outlet />
    </Layout>
  );
};
