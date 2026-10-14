// SPDX-License-Identifier: Apache-2.0
//
// Page → shell communication: the top-bar status text (DESIGN §5.20) and the refresh label
// (dashboards show their auto-refresh interval next to the refresh button), plus a registry for
// the query bar so `/` can focus it from anywhere.

import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';

interface ShellState {
  status: string | null;
  refreshLabel: string | null;
  setStatus(s: string | null): void;
  setRefreshLabel(s: string | null): void;
  /** The current page's primary query input (focused by `/`). */
  focusTargetRef: { current: (() => void) | null };
}

const ShellContext = createContext<ShellState | null>(null);

export function ShellProvider({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<string | null>(null);
  const [refreshLabel, setRefreshLabel] = useState<string | null>(null);
  const focusTargetRef = useRef<(() => void) | null>(null);
  const value = useMemo(
    () => ({ status, refreshLabel, setStatus, setRefreshLabel, focusTargetRef }),
    [status, refreshLabel],
  );
  return <ShellContext.Provider value={value}>{children}</ShellContext.Provider>;
}

export function useShell(): ShellState {
  const s = useContext(ShellContext);
  if (s === null) throw new Error('ShellProvider missing');
  return s;
}

/** Shows `text` in the top bar while the calling page is mounted. */
export function useTopbarStatus(text: string | null): void {
  const { setStatus } = useShell();
  useEffect(() => {
    setStatus(text);
  }, [text, setStatus]);
  useEffect(() => () => setStatus(null), [setStatus]);
}

/** Shows an auto-refresh label next to the refresh button while mounted. */
export function useRefreshLabel(text: string | null): void {
  const { setRefreshLabel } = useShell();
  useEffect(() => {
    setRefreshLabel(text);
    return () => setRefreshLabel(null);
  }, [text, setRefreshLabel]);
}

/** Registers the function that focuses this page's query bar (keyboard `/`). */
export function useFocusTarget(fn: (() => void) | null): void {
  const { focusTargetRef } = useShell();
  useEffect(() => {
    focusTargetRef.current = fn;
    return () => {
      if (focusTargetRef.current === fn) focusTargetRef.current = null;
    };
  }, [fn, focusTargetRef]);
}
