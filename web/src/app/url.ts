// SPDX-License-Identifier: Apache-2.0
//
// URL-backed UI state (SPEC §16.1: time range, query, signals and selection live in the URL so
// links are shareable).

import { useCallback, useMemo } from 'react';
import { useSearchParams } from 'react-router-dom';
import { DEFAULT_RANGE, isValidTime, type TimeRange } from '../lib/time';

export type ParamPatch = Record<string, string | null | undefined>;

/** Applies a patch to search params (`null`/`undefined`/'' removes a key). */
export function patchParams(params: URLSearchParams, patch: ParamPatch): URLSearchParams {
  const next = new URLSearchParams(params);
  for (const [k, v] of Object.entries(patch)) {
    if (v === null || v === undefined || v === '') next.delete(k);
    else next.set(k, v);
  }
  return next;
}

/** Reads the global time range from `from`/`to` (invalid values fall back to `fallback`). */
export function rangeFromParams(params: URLSearchParams, fallback: TimeRange = DEFAULT_RANGE): TimeRange {
  const from = params.get('from');
  const to = params.get('to');
  return {
    from: from !== null && isValidTime(from) ? from : fallback.from,
    to: to !== null && isValidTime(to) ? to : fallback.to,
  };
}

export function useParamPatch(): (patch: ParamPatch, replace?: boolean) => void {
  const [, setParams] = useSearchParams();
  return useCallback(
    (patch: ParamPatch, replace = false) => setParams((p) => patchParams(p, patch), { replace }),
    [setParams],
  );
}

export function useTimeRange(fallback: TimeRange = DEFAULT_RANGE): {
  range: TimeRange;
  setRange: (r: TimeRange) => void;
} {
  const [params] = useSearchParams();
  const patch = useParamPatch();
  const from = params.get('from');
  const to = params.get('to');
  const range = useMemo(
    () =>
      rangeFromParams(
        new URLSearchParams({ ...(from === null ? {} : { from }), ...(to === null ? {} : { to }) }),
        fallback,
      ),
    [from, to, fallback],
  );
  const setRange = useCallback((r: TimeRange) => patch({ from: r.from, to: r.to }), [patch]);
  return { range, setRange };
}

/** Comma-separated list parameter (e.g. `signals`). */
export function listParam(params: URLSearchParams, name: string): string[] {
  const v = params.get(name);
  return v === null || v === '' ? [] : v.split(',').filter((x) => x !== '');
}
