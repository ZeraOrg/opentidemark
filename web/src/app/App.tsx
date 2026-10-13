// SPDX-License-Identifier: Apache-2.0
//
// Providers and routes (SPEC §16.1). Feature pages are split into their own chunks.

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { lazy, Suspense, useState, type ReactNode } from 'react';
import { BrowserRouter, Navigate, Route, Routes } from 'react-router-dom';
import { isApiError } from '../api/client';
import { Spinner } from '../components/controls';
import { EmptyState } from '../components/layout';
import { ToastProvider } from '../components/overlay';
import { LoginPage } from '../features/auth/LoginPage';
import s from './App.module.css';
import { ServicesProvider, type Services } from './services';
import { RequireAuth, SessionProvider } from './session';
import { Shell } from './Shell';
import { ThemeProvider } from './theme';

const ExplorerPage = lazy(() => import('../features/explorer/ExplorerPage'));
const TracesPage = lazy(() => import('../features/traces/TracesPage'));
const TracePage = lazy(() => import('../features/traces/TracePage'));
const MetricsPage = lazy(() => import('../features/metrics/MetricsPage'));
const DashboardsPage = lazy(() => import('../features/dashboards/DashboardsPage'));
const DashboardPage = lazy(() => import('../features/dashboards/DashboardPage'));
const AlertsPage = lazy(() => import('../features/alerts/AlertsPage'));
const StreamsPage = lazy(() => import('../features/streams/StreamsPage'));
const SettingsLayout = lazy(() => import('../features/settings/SettingsLayout'));
const ConnectionsPage = lazy(() => import('../features/settings/ConnectionsPage'));
const UsersPage = lazy(() => import('../features/settings/UsersPage'));
const TokensPage = lazy(() => import('../features/settings/TokensPage'));
const ChannelsPage = lazy(() => import('../features/settings/ChannelsPage'));
const AuditPage = lazy(() => import('../features/settings/AuditPage'));
const SystemPage = lazy(() => import('../features/settings/SystemPage'));
const ImportPage = lazy(() => import('../features/settings/ImportPage'));
const SettingsIndex = lazy(() => import('../features/settings/SettingsIndex'));

const MAX_RETRIES = 2;

/** Retries transient failures only: never 4xx (bad queries, permissions, missing objects). */
export function shouldRetry(count: number, err: unknown): boolean {
  if (count >= MAX_RETRIES) return false;
  return !(isApiError(err) && err.status >= 400 && err.status < 500);
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: shouldRetry, refetchOnWindowFocus: false, staleTime: 15_000 },
      mutations: { retry: false },
    },
  });
}

function Loading() {
  return (
    <div className={s.loading}>
      <Spinner size={20} />
    </div>
  );
}

function Lazy({ children }: { children: ReactNode }) {
  return <Suspense fallback={<Loading />}>{children}</Suspense>;
}

function NotFound() {
  return <EmptyState title="Page not found" hint="The address does not match any screen." />;
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        element={
          <RequireAuth fallback={<Loading />}>
            <Shell />
          </RequireAuth>
        }
      >
        <Route index element={<Navigate to="/events" replace />} />
        <Route
          path="events"
          element={
            <Lazy>
              <ExplorerPage />
            </Lazy>
          }
        />
        <Route
          path="events/:id"
          element={
            <Lazy>
              <ExplorerPage />
            </Lazy>
          }
        />
        <Route
          path="traces"
          element={
            <Lazy>
              <TracesPage />
            </Lazy>
          }
        />
        <Route
          path="traces/:traceId"
          element={
            <Lazy>
              <TracePage />
            </Lazy>
          }
        />
        <Route
          path="metrics"
          element={
            <Lazy>
              <MetricsPage />
            </Lazy>
          }
        />
        <Route
          path="dashboards"
          element={
            <Lazy>
              <DashboardsPage />
            </Lazy>
          }
        />
        <Route
          path="dashboards/:id"
          element={
            <Lazy>
              <DashboardPage />
            </Lazy>
          }
        />
        <Route
          path="dashboards/:id/edit"
          element={
            <Lazy>
              <DashboardPage edit />
            </Lazy>
          }
        />
        <Route
          path="alerts"
          element={
            <Lazy>
              <AlertsPage />
            </Lazy>
          }
        />
        <Route
          path="alerts/:id"
          element={
            <Lazy>
              <AlertsPage />
            </Lazy>
          }
        />
        <Route
          path="streams"
          element={
            <Lazy>
              <StreamsPage />
            </Lazy>
          }
        />
        <Route
          path="streams/:name"
          element={
            <Lazy>
              <StreamsPage />
            </Lazy>
          }
        />
        <Route
          path="settings"
          element={
            <Lazy>
              <SettingsLayout />
            </Lazy>
          }
        >
          <Route
            index
            element={
              <Lazy>
                <SettingsIndex />
              </Lazy>
            }
          />
          <Route
            path="connections"
            element={
              <Lazy>
                <ConnectionsPage />
              </Lazy>
            }
          />
          <Route
            path="users"
            element={
              <Lazy>
                <UsersPage />
              </Lazy>
            }
          />
          <Route
            path="tokens"
            element={
              <Lazy>
                <TokensPage />
              </Lazy>
            }
          />
          <Route
            path="channels"
            element={
              <Lazy>
                <ChannelsPage />
              </Lazy>
            }
          />
          <Route
            path="audit"
            element={
              <Lazy>
                <AuditPage />
              </Lazy>
            }
          />
          <Route
            path="system"
            element={
              <Lazy>
                <SystemPage />
              </Lazy>
            }
          />
          <Route
            path="import"
            element={
              <Lazy>
                <ImportPage />
              </Lazy>
            }
          />
        </Route>
        <Route path="*" element={<NotFound />} />
      </Route>
    </Routes>
  );
}

export function App({ services }: { services: Services }) {
  const [client] = useState(createQueryClient);
  return (
    <ServicesProvider services={services}>
      <QueryClientProvider client={client}>
        <ThemeProvider store={services.store}>
          <ToastProvider>
            <BrowserRouter>
              <SessionProvider>
                <AppRoutes />
              </SessionProvider>
            </BrowserRouter>
          </ToastProvider>
        </ThemeProvider>
      </QueryClientProvider>
    </ServicesProvider>
  );
}
