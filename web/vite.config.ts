// SPDX-License-Identifier: Apache-2.0
//
// Vite + Vitest configuration (SPEC §5, §16). `--mode mock` builds the MSW-backed demo into
// dist-mock/ (CI e2e and screenshots only); the production build in dist/ never contains mocks.
import { readFileSync } from 'node:fs';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vitest/config';

// Same policy as internal/httpx (SPEC §15.7); the preview server enforces it so e2e tests catch
// any CSP violation of the built bundle.
const contentSecurityPolicy =
  "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
  "font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'";

const backend = 'http://localhost:8080';

// The demo reports the release in the repository's VERSION file, so screenshots show it.
const demoVersion = (): string => readFileSync(new URL('../VERSION', import.meta.url), 'utf8').trim();

export default defineConfig(({ mode }) => {
  const mock = mode === 'mock';
  return {
    plugins: [react()],
    publicDir: mock ? 'mock-public' : 'public',
    define: mock ? { __TM_DEMO_VERSION__: JSON.stringify(demoVersion()) } : {},
    build: {
      outDir: mock ? 'dist-mock' : 'dist',
      emptyOutDir: true,
      // Fonts and images must stay files: data: URIs would violate font-src 'self'.
      assetsInlineLimit: 0,
      target: 'es2022',
      sourcemap: false,
      chunkSizeWarningLimit: 1600,
    },
    server: {
      proxy: {
        '/api': { target: backend, changeOrigin: false },
        '/readyz': backend,
        '/healthz': backend,
      },
    },
    preview: {
      headers: {
        'Content-Security-Policy': contentSecurityPolicy,
        'X-Content-Type-Options': 'nosniff',
        'Referrer-Policy': 'no-referrer',
      },
    },
    test: {
      environment: 'jsdom',
      setupFiles: ['src/test/setup.ts'],
      include: ['src/**/*.test.{ts,tsx}'],
      css: { modules: { classNameStrategy: 'non-scoped' } },
      coverage: {
        provider: 'v8',
        reporter: ['text-summary', 'html', 'json-summary'],
        reportsDirectory: 'coverage',
        include: ['src/**/*.{ts,tsx}'],
        exclude: [
          'src/mocks/**',
          'src/main.tsx',
          'src/test/**',
          'src/**/*.test.{ts,tsx}',
          'src/vite-env.d.ts',
        ],
        thresholds: {
          'src/tql/**': { statements: 80, branches: 70, functions: 80, lines: 80 },
          'src/lib/**': { statements: 80, branches: 70, functions: 80, lines: 80 },
          'src/api/client.ts': { statements: 80, branches: 70, functions: 80, lines: 80 },
        },
      },
    },
  };
});
