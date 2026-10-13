// SPDX-License-Identifier: Apache-2.0
//
// Small shared hooks: debouncing, per-browser preferences and media queries.

import { useCallback, useEffect, useState } from 'react';
import { useServices } from '../app/services';
import { readJSON } from './storage';

/** `value`, updated only after it stopped changing for `ms` milliseconds. */
export function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

/** A JSON preference in the injected key-value store (localStorage in the browser). */
export function useStoredState<T>(
  key: string,
  fallback: T,
  guard: (v: unknown) => v is T,
): [T, (v: T) => void] {
  const { store } = useServices();
  const [value, setValue] = useState<T>(() => readJSON(store, key, fallback, guard));
  const set = useCallback(
    (v: T) => {
      store.set(key, JSON.stringify(v));
      setValue(v);
    },
    [store, key],
  );
  return [value, set];
}

/** Tracks a CSS media query. */
export function useMediaQuery(query: string): boolean {
  const get = (): boolean => typeof window.matchMedia === 'function' && window.matchMedia(query).matches;
  const [matches, setMatches] = useState(get);
  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return undefined;
    const mq = window.matchMedia(query);
    const on = (): void => setMatches(mq.matches);
    on();
    mq.addEventListener('change', on);
    return () => mq.removeEventListener('change', on);
  }, [query]);
  return matches;
}

export const PHONE_QUERY = '(max-width: 599px)';
