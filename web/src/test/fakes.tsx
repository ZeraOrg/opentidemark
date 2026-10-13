// SPDX-License-Identifier: Apache-2.0
//
// Test fakes: an in-memory key-value store, a scripted fetch and the services provider wrapper
// used by component and hook tests.

import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { ApiClient } from '../api/client';
import { createPalette, ServicesProvider, type Services, type TailSource } from '../app/services';
import type { KeyValueStore } from '../lib/storage';

export const FIXED_NOW = Date.parse('2026-10-13T10:00:00Z');

export function memoryStore(
  initial: Record<string, string> = {},
): KeyValueStore & { data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    get: (k) => data.get(k) ?? null,
    set: (k, v) => {
      data.set(k, v);
    },
    remove: (k) => {
      data.delete(k);
    },
  };
}

export interface RecordedRequest {
  url: string;
  init: RequestInit;
}

/** A fetch that answers from `respond` and records every request. */
export function scriptedFetch(respond: (url: string, init: RequestInit) => Response | Promise<Response>): {
  fetch: typeof fetch;
  calls: RecordedRequest[];
} {
  const calls: RecordedRequest[] = [];
  const fn = (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
    calls.push({ url, init });
    return Promise.resolve(respond(url, init));
  };
  return { fetch: fn as typeof fetch, calls };
}

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const silentTail: TailSource = { open: () => () => undefined };

export function testServices(overrides: Partial<Services> = {}): Services {
  return {
    api: new ApiClient({ fetch: scriptedFetch(() => jsonResponse({})).fetch, cookies: () => '' }),
    tail: silentTail,
    store: memoryStore(),
    palette: createPalette(),
    now: () => FIXED_NOW,
    ...overrides,
  };
}

/** Wraps children with services, a fresh query client and a memory router. */
export function Providers({
  services,
  children,
  path = '/',
}: {
  services: Services;
  children: ReactNode;
  path?: string;
}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <ServicesProvider services={services}>
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[path]}>{children}</MemoryRouter>
      </QueryClientProvider>
    </ServicesProvider>
  );
}
