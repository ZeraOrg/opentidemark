// SPDX-License-Identifier: Apache-2.0

import { act, renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ServicesProvider } from '../app/services';
import { memoryStore, testServices } from '../test/fakes';
import { useDebounced, useMediaQuery, useStoredState } from './hooks';
import { isStringArray } from './storage';

describe('useDebounced', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('updates after the value settled', () => {
    vi.useFakeTimers();
    const { result, rerender } = renderHook(({ v }) => useDebounced(v, 200), { initialProps: { v: 'a' } });
    rerender({ v: 'b' });
    expect(result.current).toBe('a');
    act(() => {
      vi.advanceTimersByTime(200);
    });
    expect(result.current).toBe('b');
  });
});

describe('useStoredState', () => {
  it('reads the stored preference and persists changes', () => {
    const store = memoryStore({ cols: '["service"]' });
    const services = testServices({ store });
    const wrapper = ({ children }: { children: ReactNode }) => (
      <ServicesProvider services={services}>{children}</ServicesProvider>
    );
    const { result } = renderHook(() => useStoredState('cols', [], isStringArray), { wrapper });
    expect(result.current[0]).toEqual(['service']);
    act(() => {
      result.current[1](['service', 'route']);
    });
    expect(result.current[0]).toEqual(['service', 'route']);
    expect(store.data.get('cols')).toBe('["service","route"]');
  });
});

describe('useMediaQuery', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('is false without matchMedia', () => {
    vi.stubGlobal('matchMedia', undefined);
    const { result } = renderHook(() => useMediaQuery('(max-width: 599px)'));
    expect(result.current).toBe(false);
  });

  it('follows change events', () => {
    let listener: (() => void) | undefined;
    const mq = {
      matches: false,
      addEventListener: (_: string, fn: () => void) => {
        listener = fn;
      },
      removeEventListener: vi.fn(),
    };
    vi.stubGlobal(
      'matchMedia',
      vi.fn(() => mq),
    );
    const { result, unmount } = renderHook(() => useMediaQuery('(max-width: 599px)'));
    expect(result.current).toBe(false);
    mq.matches = true;
    act(() => listener?.());
    expect(result.current).toBe(true);
    unmount();
    expect(mq.removeEventListener).toHaveBeenCalled();
  });
});
