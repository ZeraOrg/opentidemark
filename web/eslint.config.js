// SPDX-License-Identifier: Apache-2.0
//
// ESLint flat config (SPEC §18): typescript-eslint strict, React hooks rules, zero warnings.
import js from '@eslint/js';
import reactHooks from 'eslint-plugin-react-hooks';
import globals from 'globals';
import tseslint from 'typescript-eslint';

export default tseslint.config(
  {
    ignores: [
      'dist',
      'dist-mock',
      'mock-public',
      'coverage',
      'playwright-report',
      'test-results',
      'node_modules',
      'e2e/screenshots',
    ],
  },
  js.configs.recommended,
  ...tseslint.configs.strict,
  {
    files: ['**/*.{ts,tsx}'],
    languageOptions: { globals: { ...globals.browser } },
    plugins: { 'react-hooks': reactHooks },
    rules: {
      ...reactHooks.configs.recommended.rules,
      // eslint-plugin-react-hooks 7 ships the React Compiler diagnostics in `recommended`. This app
      // does not use the compiler; three of them only flag deliberate patterns and stay off:
      // the "latest value" ref written during render and read from editor/keyboard callbacks,
      // drafts that follow the URL or the server from an effect, and the @tanstack/react-virtual
      // "incompatible library" notice.
      'react-hooks/refs': 'off',
      'react-hooks/set-state-in-effect': 'off',
      'react-hooks/incompatible-library': 'off',
      'no-restricted-syntax': [
        'error',
        {
          selector: "JSXAttribute[name.name='dangerouslySetInnerHTML']",
          message: 'dangerouslySetInnerHTML is forbidden (SPEC §15.7).',
        },
      ],
      '@typescript-eslint/no-unused-vars': ['error', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      eqeqeq: ['error', 'always'],
    },
  },
  {
    files: ['**/*.{js,mjs}', 'vite.config.ts', 'playwright.config.ts', 'e2e/**/*.ts'],
    languageOptions: { globals: { ...globals.node } },
  },
);
