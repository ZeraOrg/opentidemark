// SPDX-License-Identifier: Apache-2.0
//
// Theme preference (DESIGN §2, §10): dark (default), light or system, persisted as tm.theme and
// applied as data-theme on <html>.

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { STORAGE_KEYS, type KeyValueStore } from '../lib/storage';

export type ThemePref = 'dark' | 'light' | 'system';
export type Theme = 'dark' | 'light';

export function isThemePref(v: string | null): v is ThemePref {
  return v === 'dark' || v === 'light' || v === 'system';
}

export function resolveTheme(pref: ThemePref, prefersDark: boolean): Theme {
  if (pref === 'system') return prefersDark ? 'dark' : 'light';
  return pref;
}

function prefersDark(): boolean {
  return typeof window.matchMedia === 'function' && window.matchMedia('(prefers-color-scheme: dark)').matches;
}

/** Applies the stored preference to <html> before the first render (no theme flash). */
export function applyStoredTheme(store: KeyValueStore): void {
  const raw = store.get(STORAGE_KEYS.theme);
  const pref = isThemePref(raw) ? raw : 'dark';
  document.documentElement.dataset.theme = resolveTheme(pref, prefersDark());
}

interface ThemeValue {
  pref: ThemePref;
  theme: Theme;
  setPref(p: ThemePref): void;
}

const ThemeContext = createContext<ThemeValue>({ pref: 'dark', theme: 'dark', setPref: () => undefined });

export function ThemeProvider({ store, children }: { store: KeyValueStore; children: ReactNode }) {
  const [pref, setPrefState] = useState<ThemePref>(() => {
    const raw = store.get(STORAGE_KEYS.theme);
    return isThemePref(raw) ? raw : 'dark';
  });
  const [dark, setDark] = useState(prefersDark);

  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return undefined;
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = (): void => setDark(mq.matches);
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);

  const theme = resolveTheme(pref, dark);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  const setPref = useCallback(
    (p: ThemePref) => {
      store.set(STORAGE_KEYS.theme, p);
      setPrefState(p);
    },
    [store],
  );

  const value = useMemo(() => ({ pref, theme, setPref }), [pref, theme, setPref]);
  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme(): ThemeValue {
  return useContext(ThemeContext);
}

/** Reads a design token from the document (charts need concrete colours). */
export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}
