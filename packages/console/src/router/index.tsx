import { createBrowserRouter, Navigate } from 'react-router-dom';
import { AppLayout, AuthRoute, ProtectedRoute, SuspenseWrapper } from './RouteLayout';

// Lazy load pages for code splitting
import { lazy } from 'react';

// Lazy loaded pages
const LoginPage = lazy(() => import('../pages/auth/LoginPage'));
const DashboardPage = lazy(() => import('../pages/DashboardPage'));
const CollectionsPage = lazy(() => import('../pages/CollectionsPage'));
const TriggersPage = lazy(() => import('../pages/TriggersPage'));
const UsersPage = lazy(() => import('../pages/admin/UsersPage'));
const SecurityPage = lazy(() => import('../pages/admin/SecurityPage'));
const DatabasesPage = lazy(() => import('../pages/admin/DatabasesPage'));
const SettingsPage = lazy(() => import('../pages/SettingsPage'));

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
        element: <AppLayout basePath={BASE_PATH} />,
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
