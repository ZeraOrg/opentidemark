// SPDX-License-Identifier: Apache-2.0
//
// Mock-mode wiring (`vite --mode mock`): starts the MSW service worker with the /api/v1 handlers
// and provides a simulated live tail, because EventSource requests are answered by the real
// server only. Imported dynamically by main.tsx in mock mode, so production builds exclude it.

import { setupWorker } from 'msw/browser';
import type { TailSource } from '../app/services';
import { liveEvent } from './data';
import { handlers } from './handlers';

const TAIL_INTERVAL_MS = 1_500;

export async function startMockWorker(): Promise<void> {
  const worker = setupWorker(...handlers);
  await worker.start({ onUnhandledRequest: 'bypass', quiet: true });
}

/** Emits one or two new events every 1.5 s while open, within the selected streams. */
export function mockTail(): TailSource {
  return {
    open(params, h) {
      const streams = params.streams ?? [];
      const env = params.env ?? [];
      const timer = setInterval(() => {
        const evs = (Math.random() < 0.5 ? [liveEvent()] : [liveEvent(), liveEvent()]).filter(
          (ev) =>
            (streams.length === 0 || streams.includes(ev.stream)) &&
            (env.length === 0 || env.includes(ev.environment)),
        );
        if (evs.length > 0) h.onEvents(evs);
      }, TAIL_INTERVAL_MS);
      return () => clearInterval(timer);
    },
  };
}
