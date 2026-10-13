// SPDX-License-Identifier: Apache-2.0
//
// The signed-in user (GET /auth/me, SPEC §11.2) as TanStack Query state, the 401 → login hook,
// role helpers and the route guard.

import { useQuery, useQueryClient } from '@tanstack/react-query';
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { Navigate, useLocation, useNavigate } from 'react-router-dom';
import { isApiError } from '../api/client';
import { auth } from '../api/endpoints';
import type { Me, Role } from '../api/types';
import { useApi } from './services';

export const ME_KEY = ['me'] as const;

export type SessionStatus = 'loading' | 'anonymous' | 'authenticated' | 'error';

interface SessionValue {
  me: Me | null;
  status: SessionStatus;
  error: unknown;
  /** True right after a successful sign-in in this tab (drives the password prompt toast). */
  justSignedIn: boolean;
  signedIn(): Promise<void>;
  signOut(): Promise<void>;
  acknowledgeSignIn(): void;
}

const SessionContext = createContext<SessionValue | null>(null);

const ROLE_RANK: Record<Role, number> = { viewer: 0, editor: 1, admin: 2 };

/** Reports whether `role` is at least `min`. */
export function hasRole(role: Role | undefined, min: Role): boolean {
  return role !== undefined && ROLE_RANK[role] >= ROLE_RANK[min];
}

export function SessionProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [justSignedIn, setJustSignedIn] = useState(false);
  const query = useQuery({
    queryKey: ME_KEY,
    queryFn: async () => {
      try {
        return await auth.me(api);
      } catch (e) {
        if (isApiError(e) && e.status === 401) return null;
        throw e;
      }
    },
    staleTime: 60_000,
    retry: false,
  });

  useEffect(() => {
    api.setUnauthorizedHandler(() => {
      qc.setQueryData(ME_KEY, null);
    });
    return () => api.setUnauthorizedHandler(undefined);
  }, [api, qc]);

  const signedIn = useCallback(async () => {
    await qc.fetchQuery({ queryKey: ME_KEY, queryFn: () => auth.me(api), staleTime: 0 });
    setJustSignedIn(true);
  }, [api, qc]);

  const signOut = useCallback(async () => {
    try {
      await auth.logout(api);
    } finally {
      qc.clear();
      qc.setQueryData(ME_KEY, null);
      navigate('/login', { replace: true });
    }
  }, [api, qc, navigate]);

  const acknowledgeSignIn = useCallback(() => setJustSignedIn(false), []);

  const me = query.data ?? null;
  const status: SessionStatus = query.isPending
    ? 'loading'
    : query.isError
      ? 'error'
      : me === null
        ? 'anonymous'
        : 'authenticated';

  const value = useMemo<SessionValue>(
    () => ({ me, status, error: query.error, justSignedIn, signedIn, signOut, acknowledgeSignIn }),
    [me, status, query.error, justSignedIn, signedIn, signOut, acknowledgeSignIn],
  );
  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const s = useContext(SessionContext);
  if (s === null) throw new Error('SessionProvider missing');
  return s;
}

/** The signed-in user; only valid below RequireAuth. */
export function useMe(): Me {
  const { me } = useSession();
  if (me === null) throw new Error('useMe outside RequireAuth');
  return me;
}

/** Renders children only for a signed-in user, else redirects to /login?next=… */
export function RequireAuth({ children, fallback }: { children: ReactNode; fallback: ReactNode }) {
  const { status } = useSession();
  const loc = useLocation();
  if (status === 'loading' || status === 'error') return <>{fallback}</>;
  if (status === 'anonymous') {
    const next = `${loc.pathname}${loc.search}`;
    return <Navigate to={next === '/' ? '/login' : `/login?next=${encodeURIComponent(next)}`} replace />;
  }
  return <>{children}</>;
}
