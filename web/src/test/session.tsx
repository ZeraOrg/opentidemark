// SPDX-License-Identifier: Apache-2.0
//
// Test helper: providers with a signed-in user. The scripted fetch answers `GET /auth/me` with
// `me` and every other request through `respond` (default: an empty JSON object).

import { render, type RenderResult } from '@testing-library/react';
import type { ReactNode } from 'react';
import { ApiClient } from '../api/client';
import type { Me } from '../api/types';
import { RequireAuth, SessionProvider } from '../app/session';
import { jsonResponse, Providers, scriptedFetch, testServices, type RecordedRequest } from './fakes';
import type { Services } from '../app/services';

export const ADMIN_ME: Me = {
  id: 'u-admin',
  username: 'admin',
  displayName: 'Administrator',
  role: 'admin',
  mustChangePassword: false,
  defaultCredentials: false,
  allStreams: true,
  streams: [],
  teams: [],
  streamAccess: 'admin',
};

/** A viewer limited to `streams` through one team. */
export function restrictedMe(streams: string[]): Me {
  return {
    ...ADMIN_ME,
    id: 'u-sam',
    username: 'sam',
    displayName: 'Sam Lindqvist',
    role: 'viewer',
    allStreams: false,
    streams,
    teams: [{ id: 'tm-payments', name: 'Payments' }],
    streamAccess: 'teams',
  };
}

export function renderSignedIn(
  ui: ReactNode,
  opts: {
    me?: Me;
    path?: string;
    respond?: (url: string, init: RequestInit) => Response | undefined;
    services?: Partial<Services>;
  } = {},
): RenderResult & { calls: RecordedRequest[]; services: Services } {
  const me = opts.me ?? ADMIN_ME;
  const { fetch, calls } = scriptedFetch((url, init) => {
    if (url.startsWith('/api/v1/auth/me')) return jsonResponse(me);
    return opts.respond?.(url, init) ?? jsonResponse({});
  });
  const services = testServices({ api: new ApiClient({ fetch, cookies: () => '' }), ...opts.services });
  const result = render(
    <Providers services={services} path={opts.path ?? '/'}>
      <SessionProvider>
        <RequireAuth fallback={<span>Signing in…</span>}>{ui}</RequireAuth>
      </SessionProvider>
    </Providers>,
  );
  return { ...result, calls, services };
}
