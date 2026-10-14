// SPDX-License-Identifier: Apache-2.0
//
// Data scope (SPEC §16.1, §16.6, §15.10): the stream and environment selection of the Events,
// Traces, Metrics and dashboard views. The selection lives in the URL (`streams`, `env`) so links
// are shareable; the last choice is remembered per browser in localStorage `tm.scope`. Streams a
// user cannot see are never selected: the picker offers only the visible streams and a selection
// from a link is narrowed to them.

import { useQuery } from '@tanstack/react-query';
import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import { streams as streamsApi } from '../api/endpoints';
import type { ScopeParams } from '../api/types';
import { isStringArray, readJSON, STORAGE_KEYS, type KeyValueStore } from '../lib/storage';
import { useApi, useServices } from './services';
import { useMe } from './session';
import { listParam, patchParams, type ParamPatch } from './url';

export interface Scope {
  streams: string[];
  env: string[];
}

export const EMPTY_SCOPE: Scope = { streams: [], env: [] };

/** Server limits of the selection (SPEC §11.3). */
export const MAX_SCOPE_STREAMS = 100;
export const MAX_SCOPE_ENVIRONMENTS = 20;

/** Routes whose data is read through the scope (the same as the ones with a time range). */
export function usesScope(pathname: string): boolean {
  return (
    /^\/(events|traces|metrics)(\/|$)/.test(pathname) ||
    (/^\/dashboards\/[^/]+/.test(pathname) && pathname !== '/dashboards/')
  );
}

export function isScope(v: unknown): v is Scope {
  return (
    typeof v === 'object' &&
    v !== null &&
    isStringArray((v as { streams?: unknown }).streams) &&
    isStringArray((v as { env?: unknown }).env)
  );
}

const unique = (xs: readonly string[]): string[] => [...new Set(xs.filter((x) => x !== ''))];

/** Reads `streams` and `env` from the URL. */
export function scopeFromParams(params: URLSearchParams): Scope {
  return {
    streams: unique(listParam(params, 'streams')).slice(0, MAX_SCOPE_STREAMS),
    env: unique(listParam(params, 'env')).slice(0, MAX_SCOPE_ENVIRONMENTS),
  };
}

/** The URL patch for a scope (empty lists remove the parameter). */
export function scopePatch(scope: Scope): ParamPatch {
  return {
    streams: scope.streams.length === 0 ? null : scope.streams.join(','),
    env: scope.env.length === 0 ? null : scope.env.join(','),
  };
}

/** Keeps only the streams the caller can see (`visible` null = every stream). */
export function narrowScope(scope: Scope, visible: readonly string[] | null): Scope {
  if (visible === null) return scope;
  return { ...scope, streams: scope.streams.filter((s) => visible.includes(s)) };
}

export const isEmptyScope = (s: Scope): boolean => s.streams.length === 0 && s.env.length === 0;

/** The short trigger label: "All streams", "payments", "3 streams", with " · production" for one environment. */
export function scopeLabel(scope: Scope): string {
  const streams =
    scope.streams.length === 0
      ? 'All streams'
      : scope.streams.length === 1
        ? (scope.streams[0] ?? '')
        : `${scope.streams.length} streams`;
  if (scope.env.length === 0) return streams;
  return `${streams} · ${scope.env.length === 1 ? (scope.env[0] ?? '') : `${scope.env.length} environments`}`;
}

/** The accessible description of the selection for the trigger's label. */
export function scopeDescription(scope: Scope, visible: readonly string[] | null): string {
  const streams =
    scope.streams.length === 0
      ? visible === null
        ? 'all streams'
        : `all my streams (${visible.length})`
      : `streams ${scope.streams.join(', ')}`;
  const env = scope.env.length === 0 ? 'all environments' : `environments ${scope.env.join(', ')}`;
  return `${streams}, ${env}`;
}

export interface DataScope {
  /** The selected streams (only visible ones) and environments. */
  streams: string[];
  env: string[];
  /** Streams the caller may see; `null` = every stream (names come from `GET /streams`). */
  visible: string[] | null;
  /** True when the caller cannot see any stream: views show an empty state and send no query. */
  none: boolean;
  /** Query parameters of the GET search routes. */
  params: ScopeParams;
  /** Body fields of `POST /query`. */
  body: { streams?: string[]; environments?: string[] };
  /** A stable fragment for query keys. */
  key: string;
  set(next: Scope): void;
}

/** The visible streams of the signed-in user: `me.streams`, or every stream name for `allStreams`. */
export function useVisibleStreams(): { names: string[] | null; all: boolean; loading: boolean } {
  const me = useMe();
  const api = useApi();
  const all = me.allStreams;
  const list = useQuery({
    queryKey: ['streams'],
    queryFn: () => streamsApi.list(api),
    enabled: all,
    staleTime: 60_000,
  });
  const names = useMemo(
    () => (all ? (list.data?.streams.map((s) => s.name).sort() ?? null) : [...me.streams].sort()),
    [all, list.data, me.streams],
  );
  return { names, all, loading: all && list.isPending };
}

/** The current data scope from the URL, narrowed to the caller's visible streams. */
export function useDataScope(): DataScope {
  const me = useMe();
  const { store } = useServices();
  const [params, setParams] = useSearchParams();
  const streamsParam = params.get('streams') ?? '';
  const envParam = params.get('env') ?? '';
  const visibleKey = me.allStreams ? null : me.streams.join(',');
  const scope = useMemo(() => {
    const raw = scopeFromParams(new URLSearchParams({ streams: streamsParam, env: envParam }));
    return narrowScope(raw, visibleKey === null ? null : visibleKey.split(',').filter((s) => s !== ''));
  }, [streamsParam, envParam, visibleKey]);
  const set = useCallback(
    (next: Scope) => {
      const clean = { streams: unique(next.streams), env: unique(next.env) };
      store.set(STORAGE_KEYS.scope, JSON.stringify(clean));
      setParams((p) => patchParams(p, scopePatch(clean)));
    },
    [store, setParams],
  );
  return useMemo(() => {
    const none = !me.allStreams && me.streams.length === 0;
    return {
      streams: scope.streams,
      env: scope.env,
      visible: me.allStreams ? null : me.streams,
      none,
      params: {
        ...(scope.streams.length > 0 ? { streams: scope.streams } : {}),
        ...(scope.env.length > 0 ? { env: scope.env } : {}),
      },
      body: {
        ...(scope.streams.length > 0 ? { streams: scope.streams } : {}),
        ...(scope.env.length > 0 ? { environments: scope.env } : {}),
      },
      key: `${scope.streams.join(',')}|${scope.env.join(',')}`,
      set,
    };
  }, [scope, me.allStreams, me.streams, set]);
}

/** The scope remembered in this browser (`tm.scope`), or the empty scope. */
export function rememberedScope(store: KeyValueStore): Scope {
  return readJSON(store, STORAGE_KEYS.scope, EMPTY_SCOPE, isScope);
}
