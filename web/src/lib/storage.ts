// SPDX-License-Identifier: Apache-2.0
//
// Per-browser preferences in localStorage (DESIGN §10: tm.theme, tm.density, tm.columns.*,
// query history, tm.scope). Storage can be unavailable (private mode, blocked site data), so
// every access is guarded and falls back to the default.

export interface KeyValueStore {
  get(key: string): string | null;
  set(key: string, value: string): void;
  remove(key: string): void;
}

/** localStorage-backed store that never throws. */
export function browserStore(): KeyValueStore {
  return {
    get(key) {
      try {
        return window.localStorage.getItem(key);
      } catch {
        return null;
      }
    },
    set(key, value) {
      try {
        window.localStorage.setItem(key, value);
      } catch {
        // Storage full or disabled: preferences are best effort.
      }
    },
    remove(key) {
      try {
        window.localStorage.removeItem(key);
      } catch {
        // Storage disabled: nothing to remove.
      }
    },
  };
}

/** Reads a JSON value, returning `fallback` when missing or invalid. */
export function readJSON<T>(
  store: KeyValueStore,
  key: string,
  fallback: T,
  guard: (v: unknown) => v is T,
): T {
  const raw = store.get(key);
  if (raw === null) return fallback;
  try {
    const v: unknown = JSON.parse(raw);
    return guard(v) ? v : fallback;
  } catch {
    return fallback;
  }
}

export function isStringArray(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((x) => typeof x === 'string');
}

export const STORAGE_KEYS = {
  theme: 'tm.theme',
  density: 'tm.density',
  history: 'tm.history',
  columns: 'tm.columns.logs',
  nav: 'tm.nav.collapsed',
  passwordPromptDismissed: 'tm.passwordPrompt',
  scope: 'tm.scope',
  updateTarget: 'tm.update.target',
} as const;
