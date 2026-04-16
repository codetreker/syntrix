import { createBrowserRouter, Navigate, Outlet } from 'react-router-dom';
import { Layout, AuthLayout } from '../components/layout';
import { useAuthStore } from '../stores';

// Lazy load pages for code splitting
import { lazy, Suspense, type ReactNode } from 'react';
import { PageLoading } from '../components/ui';

// Lazy loaded pages
const LoginPage = lazy(() => import('../pages/auth/LoginPage'));
const DashboardPage = lazy(() => import('../pages/DashboardPage'));
const CollectionsPage = lazy(() => import('../pages/CollectionsPage'));
const TriggersPage = lazy(() => import('../pages/TriggersPage'));
const UsersPage = lazy(() => import('../pages/admin/UsersPage'));
const SecurityPage = lazy(() => import('../pages/admin/SecurityPage'));
const DatabasesPage = lazy(() => import('../pages/admin/DatabasesPage'));
const SettingsPage = lazy(() => import('../pages/SettingsPage'));

// Suspense wrapper for lazy loaded components
function SuspenseWrapper({ children }: { children: ReactNode }) {
  return <Suspense fallback={<PageLoading />}>{children}</Suspense>;
}

// Protected route wrapper
function ProtectedRoute() {
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  
  if (!isAuthenticated) {
    return <Navigate to="/login" replace />;
  }
  
  return <Outlet />;
}

// Auth route wrapper - redirect to dashboard if already logged in
function AuthRoute() {
  const isAuthenticated = useAuthStore((state) => state.isAuthenticated);
  
  if (isAuthenticated) {
    return <Navigate to="/" replace />;
  }
  
  return (
    <AuthLayout>
      <Outlet />
    </AuthLayout>
  );
}

// Main app layout wrapper
function AppLayout() {
  const { user, logout } = useAuthStore();
  const username = user?.username || 'User';
  const isAdmin = user?.role === 'admin';
  
  const handleLogout = () => {
    logout();
    window.location.href = `${BASE_PATH}/login`;
  };
  
  return (
    <Layout username={username} isAdmin={isAdmin} onLogout={handleLogout}>
      <Outlet />
    </Layout>
  );
}

const BASE_PATH = '/console';

export const router = createBrowserRouter([
  // Auth routes
  {
    path: '/login',
    element: <AuthRoute />,
    children: [
      {
        index: true,
        element: (
          <SuspenseWrapper>
            <LoginPage />
          </SuspenseWrapper>
        ),
      },
    ],
  },
  // Protected routes
  {
    path: '/',
    element: <ProtectedRoute />,
    children: [
      {
        element: <AppLayout />,
        children: [
          {
            index: true,
            element: (
              <SuspenseWrapper>
                <DashboardPage />
              </SuspenseWrapper>
            ),
          },
          {
            path: 'databases',
            element: (
              <SuspenseWrapper>
                <DatabasesPage />
              </SuspenseWrapper>
            ),
          },
          {
            path: 'collections',
            element: (
              <SuspenseWrapper>
                <CollectionsPage />
              </SuspenseWrapper>
            ),
          },
          {
            path: 'triggers',
            element: (
              <SuspenseWrapper>
                <TriggersPage />
              </SuspenseWrapper>
            ),
          },
          {
            path: 'users',
            element: (
              <SuspenseWrapper>
                <UsersPage />
              </SuspenseWrapper>
            ),
          },
          {
            path: 'security',
            element: (
              <SuspenseWrapper>
                <SecurityPage />
              </SuspenseWrapper>
            ),
          },
          {
            path: 'settings',
            element: (
              <SuspenseWrapper>
                <SettingsPage />
              </SuspenseWrapper>
            ),
          },
        ],
      },
    ],
  },
  // Catch-all redirect
  {
    path: '*',
    element: <Navigate to="/" replace />,
  },
], { basename: BASE_PATH });
