// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { UpdateStatus } from '../api/types';
import { jsonResponse } from '../test/fakes';
import { ADMIN_ME, renderSignedIn, restrictedMe } from '../test/session';
import { UPDATE_CARD_PATH, UpdateIndicator } from './UpdateIndicator';

const AVAILABLE: UpdateStatus = {
  enabled: true,
  current: '0.4.0',
  nodeId: 'node-1',
  installKind: 'systemd',
  apply: { supported: false, reason: 'helper-missing' },
  latest: {
    version: '0.5.0',
    prerelease: false,
    publishedAt: '2026-10-19T08:00:00Z',
    notesUrl: 'https://github.com/ZeraOrg/opentidemark/releases/tag/v0.5.0',
  },
  available: true,
  checkedAt: '2026-10-21T06:00:00Z',
  state: 'idle',
};

const UP_TO_DATE: UpdateStatus = { ...AVAILABLE, latest: undefined, available: false };

function respondWith(status: UpdateStatus) {
  return (url: string) => (url.startsWith('/api/v1/system/update') ? jsonResponse(status) : undefined);
}

async function signedIn(): Promise<void> {
  await waitFor(() => expect(screen.queryByText('Signing in…')).toBeNull());
}

describe('UpdateIndicator (SPEC §16.1)', () => {
  it('shows admins a chip linking to the update card once a newer release is known', async () => {
    renderSignedIn(<UpdateIndicator />, { respond: respondWith(AVAILABLE) });
    const chip = await screen.findByRole('link', { name: 'Update available · v0.5.0' });
    expect(chip).toHaveAttribute('href', UPDATE_CARD_PATH);
    expect(chip).toHaveAttribute('title', expect.stringContaining('v0.5.0 is available'));
    expect(chip).toHaveAttribute('title', expect.stringContaining('runs v0.4.0'));
  });

  it('keeps the accessible name in the compact (icon-only) variant', async () => {
    renderSignedIn(<UpdateIndicator compact />, { respond: respondWith(AVAILABLE) });
    const chip = await screen.findByRole('link', { name: 'Update available · v0.5.0' });
    expect(chip).toHaveAttribute('aria-label', 'Update available · v0.5.0');
  });

  it('says "Updating…" while an update is being applied', async () => {
    renderSignedIn(<UpdateIndicator />, { respond: respondWith({ ...AVAILABLE, state: 'installing' }) });
    const chip = await screen.findByRole('link', { name: 'Updating…' });
    expect(chip).toHaveAttribute('href', UPDATE_CARD_PATH);
    expect(chip).toHaveAttribute('title', expect.stringContaining('being updated to v0.5.0'));
  });

  it('renders nothing while up to date', async () => {
    const { calls } = renderSignedIn(<UpdateIndicator />, { respond: respondWith(UP_TO_DATE) });
    await signedIn();
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/system/update')).toBe(true));
    expect(screen.queryByRole('link')).toBeNull();
  });

  it.each([
    ['viewer', restrictedMe(['payments'])],
    ['editor', { ...ADMIN_ME, role: 'editor' as const }],
  ])('makes no request and renders nothing for a %s', async (_role, me) => {
    const { calls } = renderSignedIn(<UpdateIndicator />, { me, respond: respondWith(AVAILABLE) });
    await signedIn();
    expect(screen.queryByRole('link')).toBeNull();
    expect(calls.filter((c) => c.url.startsWith('/api/v1/system/update'))).toEqual([]);
  });
});
