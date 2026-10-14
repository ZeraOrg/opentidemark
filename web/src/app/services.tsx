// SPDX-License-Identifier: Apache-2.0
//
// Dependency injection for the UI: the composition root (src/main.tsx) builds the concrete
// services once and provides them through context; components and hooks only see interfaces,
// so tests and the mock-mode build substitute fakes.

import { createContext, useContext, type ReactNode } from 'react';
import type { ApiClient } from '../api/client';
import type { LogEvent } from '../api/types';
import type { KeyValueStore } from '../lib/storage';

export interface TailHandlers {
  onEvents(events: LogEvent[]): void;
  onDropped(count: number): void;
  onError(message: string): void;
}

/** Live tail transport (SPEC §11.3 GET /tail, Server-Sent Events). */
export interface TailParams {
  q: string;
  signals: readonly string[];
  /** The scope selection (SPEC §11.3). */
  streams?: readonly string[];
  env?: readonly string[];
}

export interface TailSource {
  open(params: TailParams, handlers: TailHandlers): () => void;
}

/**
 * Session-wide series colours (DESIGN §2.4): a label keeps the chart colour (1–8) it was first
 * given, so a service has the same colour in every chart and in the trace waterfall.
 */
export interface SeriesPalette {
  index(label: string): number;
}

export const CHART_COLORS = 8;

export function createPalette(): SeriesPalette {
  const assigned = new Map<string, number>();
  return {
    index(label) {
      let i = assigned.get(label);
      if (i === undefined) {
        i = (assigned.size % CHART_COLORS) + 1;
        assigned.set(label, i);
      }
      return i;
    },
  };
}

export interface Services {
  api: ApiClient;
  tail: TailSource;
  store: KeyValueStore;
  palette: SeriesPalette;
  now: () => number;
}

const ServicesContext = createContext<Services | null>(null);

export function ServicesProvider({ services, children }: { services: Services; children: ReactNode }) {
  return <ServicesContext.Provider value={services}>{children}</ServicesContext.Provider>;
}

export function useServices(): Services {
  const s = useContext(ServicesContext);
  if (s === null) throw new Error('ServicesProvider missing');
  return s;
}

export function useApi(): ApiClient {
  return useServices().api;
}

export function usePalette(): SeriesPalette {
  return useServices().palette;
}

function parseJSON(data: unknown): unknown {
  if (typeof data !== 'string') return undefined;
  try {
    return JSON.parse(data) as unknown;
  } catch {
    return undefined;
  }
}

/** EventSource-based tail: `event: events` (JSON array), `dropped` (count), `error` (message). */
export function eventSourceTail(api: ApiClient): TailSource {
  return {
    open({ q, signals, streams, env }, h) {
      const es = new EventSource(api.url('/tail', { q, signals, streams, env }));
      es.addEventListener('events', (e) => {
        const data = parseJSON((e as MessageEvent).data);
        if (Array.isArray(data)) h.onEvents(data as LogEvent[]);
      });
      es.addEventListener('dropped', (e) => {
        const n = Number((e as MessageEvent).data);
        if (Number.isFinite(n)) h.onDropped(n);
      });
      es.addEventListener('error', (e) => {
        const data = e instanceof MessageEvent ? e.data : undefined;
        es.close();
        h.onError(typeof data === 'string' && data !== '' ? data : 'Live tail disconnected.');
      });
      return () => es.close();
    },
  };
}
