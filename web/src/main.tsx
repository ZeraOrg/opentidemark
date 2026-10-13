// SPDX-License-Identifier: Apache-2.0
//
// Composition root of the SPA (SPEC §6.2, §16): builds the concrete services once (API client,
// live-tail transport, preference store, series palette, clock), routes CodeMirror's styles
// through a constructable stylesheet (lib/adoptedStyles.ts) and renders the app. In
// `--mode mock` (CI e2e and screenshots only) the MSW worker and a simulated tail are installed
// first; the production build contains neither.

import '@fontsource/ibm-plex-sans/400.css';
import '@fontsource/ibm-plex-sans/500.css';
import '@fontsource/ibm-plex-sans/600.css';
import '@fontsource/jetbrains-mono/400.css';
import '@fontsource/jetbrains-mono/500.css';
import '@fontsource/jetbrains-mono/600.css';
import './styles/tokens.css';
import './styles/global.css';

import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { StyleModule } from 'style-mod';
import { ApiClient } from './api/client';
import { App } from './app/App';
import { createPalette, eventSourceTail, type Services, type TailSource } from './app/services';
import { applyStoredTheme } from './app/theme';
import { installAdoptedStyles } from './lib/adoptedStyles';
import { browserStore } from './lib/storage';

async function tailSource(api: ApiClient): Promise<TailSource> {
  if (import.meta.env.MODE === 'mock') {
    const mock = await import('./mocks/browser');
    await mock.startMockWorker();
    return mock.mockTail();
  }
  return eventSourceTail(api);
}

async function main(): Promise<void> {
  const root = document.getElementById('root');
  if (root === null) return;
  installAdoptedStyles(StyleModule, document);
  const store = browserStore();
  applyStoredTheme(store);
  const api = new ApiClient();
  const services: Services = {
    api,
    tail: await tailSource(api),
    store,
    palette: createPalette(),
    now: () => Date.now(),
  };
  createRoot(root).render(
    <StrictMode>
      <App services={services} />
    </StrictMode>,
  );
}

void main();
