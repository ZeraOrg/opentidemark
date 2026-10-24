// SPDX-License-Identifier: Apache-2.0
//
// Vitest setup: DOM matchers, a clean DOM after every test, the matchMedia that jsdom lacks
// (uPlot reads it when its module loads) and a no-op canvas 2D context so charts can mount.

import '@testing-library/jest-dom/vitest';
import { cleanup } from '@testing-library/react';
import { afterEach } from 'vitest';

/** jsdom has no canvas: every drawing call is a no-op and text measures 10 px per character. */
function noopContext(): CanvasRenderingContext2D {
  const target: Record<string | symbol, unknown> = {};
  return new Proxy(target, {
    get: (t, key) => {
      if (key in t) return t[key];
      if (key === 'measureText') return (text: string) => ({ width: text.length * 10 });
      return () => undefined;
    },
    set: (t, key, value) => {
      t[key] = value;
      return true;
    },
  }) as unknown as CanvasRenderingContext2D;
}

Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
  value: (): CanvasRenderingContext2D => noopContext(),
  configurable: true,
  writable: true,
});

/** uPlot draws series through Path2D, which jsdom lacks as well. */
if (typeof globalThis.Path2D === 'undefined') {
  Object.defineProperty(globalThis, 'Path2D', {
    value: function Path2D(): object {
      return new Proxy({}, { get: () => () => undefined });
    },
    configurable: true,
    writable: true,
  });
}

if (typeof window.matchMedia !== 'function') {
  window.matchMedia = (query: string): MediaQueryList => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  });
}

afterEach(() => {
  cleanup();
});
