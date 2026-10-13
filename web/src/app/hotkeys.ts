// SPDX-License-Identifier: Apache-2.0
//
// Global keyboard shortcuts (SPEC §16.5, DESIGN §8): `/` focuses the query bar, `?` opens the
// shortcut help, two-key chords `g e|t|m|d|a|s` navigate. Disabled while typing in a control.

import { useEffect, useRef } from 'react';

export const CHORDS: Readonly<Record<string, { path: string; label: string }>> = {
  e: { path: '/events', label: 'Events' },
  t: { path: '/traces', label: 'Traces' },
  m: { path: '/metrics', label: 'Metrics' },
  d: { path: '/dashboards', label: 'Dashboards' },
  a: { path: '/alerts', label: 'Alerts' },
  s: { path: '/streams', label: 'Streams' },
};

const CHORD_TIMEOUT_MS = 1_000;

/** Reports whether a key event comes from a text control (shortcuts are then ignored). */
export function isTypingTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  if (target.isContentEditable) return true;
  const tag = target.tagName;
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

export interface HotkeyHandlers {
  navigate(path: string): void;
  focusQuery(): boolean;
  help(): void;
}

export function useGlobalHotkeys(h: HotkeyHandlers): void {
  const handlers = useRef(h);
  handlers.current = h;
  useEffect(() => {
    let chordAt = 0;
    const onKey = (e: KeyboardEvent): void => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || isTypingTarget(e.target)) return;
      if (document.querySelector('[aria-modal="true"]') !== null) return;
      const now = Date.now();
      if (chordAt > 0 && now - chordAt < CHORD_TIMEOUT_MS) {
        chordAt = 0;
        const target = CHORDS[e.key];
        if (target !== undefined) {
          e.preventDefault();
          handlers.current.navigate(target.path);
        }
        return;
      }
      chordAt = 0;
      if (e.key === 'g') {
        chordAt = now;
      } else if (e.key === '/') {
        if (handlers.current.focusQuery()) e.preventDefault();
      } else if (e.key === '?') {
        e.preventDefault();
        handlers.current.help();
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, []);
}
