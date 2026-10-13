// SPDX-License-Identifier: Apache-2.0

import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { ApiClient } from '../../api/client';
import { SessionProvider } from '../../app/session';
import { jsonResponse, Providers, scriptedFetch, testServices } from '../../test/fakes';
import { LoginPage, safeNext } from './LoginPage';

describe('safeNext', () => {
  it.each([
    [null, '/events'],
    ['/dashboards/1?x=1', '/dashboards/1?x=1'],
    ['//evil.example', '/events'],
    ['/\\evil.example', '/events'],
    ['https://evil.example', '/events'],
  ])('%j', (next, want) => {
    expect(safeNext(next)).toBe(want);
  });
});

function setup(login: (body: { username: string; password: string }) => Response) {
  const { fetch, calls } = scriptedFetch((url, init) => {
    if (url === '/api/v1/auth/me')
      return jsonResponse({ error: { code: 'unauthorized', message: 'no' } }, 401);
    if (url === '/api/v1/auth/login')
      return login(JSON.parse(String(init.body)) as { username: string; password: string });
    return jsonResponse({});
  });
  const services = testServices({ api: new ApiClient({ fetch, cookies: () => '' }) });
  render(
    <Providers services={services} path="/login">
      <SessionProvider>
        <LoginPage />
      </SessionProvider>
    </Providers>,
  );
  return calls;
}

describe('LoginPage', () => {
  it('asks for both fields before calling the server', async () => {
    const calls = setup(() => jsonResponse({}));
    await userEvent.click(await screen.findByRole('button', { name: 'Sign in' }));
    expect(screen.getByRole('alert')).toHaveTextContent('Enter your username and password.');
    expect(calls.some((c) => c.url.endsWith('/auth/login'))).toBe(false);
  });

  it('shows one message for unknown users and wrong passwords', async () => {
    setup(() => jsonResponse({ error: { code: 'unauthorized', message: 'invalid credentials' } }, 401));
    await userEvent.type(await screen.findByLabelText('Username'), 'admin');
    await userEvent.type(screen.getByLabelText('Password', { exact: true }), 'wrong');
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Incorrect username or password.');
  });

  it('offers the password change when the server asks for it', async () => {
    setup(() => jsonResponse({ mustChangePassword: true }));
    await userEvent.type(await screen.findByLabelText('Username'), 'admin');
    await userEvent.type(screen.getByLabelText('Password', { exact: true }), 'admin');
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('heading', { name: 'Set a new password' })).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('New password'), 'short');
    expect(screen.getByText('Use at least 12 characters.')).toBeInTheDocument();
    await userEvent.type(screen.getByLabelText('Confirm password'), 'other');
    expect(screen.getByText('The passwords do not match.')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Not now' })).toBeInTheDocument();
  });
});
