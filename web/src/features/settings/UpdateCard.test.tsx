// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { ApiError } from '../../api/client';
import type { UpdateStatus } from '../../api/types';
import { jsonResponse } from '../../test/fakes';
import { renderSignedIn } from '../../test/session';
import UpdateCard, {
  checkFailureText,
  containerCommands,
  ROLLING_UPGRADE_DOCS_URL,
  updateErrorText,
  UPGRADE_DOCS_URL,
} from './UpdateCard';

// FIXED_NOW is 2026-10-13T10:00Z, so these read "3 h ago" and "2 d ago" in any time zone.
const CHECKED_AT = '2026-10-13T07:00:00Z';
const PUBLISHED_AT = '2026-10-11T09:30:00Z';

const LATEST = {
  version: '0.5.0',
  prerelease: false,
  publishedAt: PUBLISHED_AT,
  notesUrl: 'https://github.com/ZeraOrg/opentidemark/releases/tag/v0.5.0',
};
const AVAILABLE: UpdateStatus = {
  enabled: true,
  current: '0.4.0',
  nodeId: 'node-1',
  installKind: 'systemd',
  apply: { supported: false, reason: 'helper-missing' },
  latest: LATEST,
  available: true,
  checkedAt: CHECKED_AT,
  state: 'idle',
};
const UP_TO_DATE: UpdateStatus = { ...AVAILABLE, latest: undefined, available: false };

function rateLimited(retryAfter: string): Response {
  return new Response(JSON.stringify({ error: { code: 'rate_limited', message: 'too many requests' } }), {
    status: 429,
    headers: { 'Content-Type': 'application/json', 'Retry-After': retryAfter },
  });
}

function conflict(reason: string): Response {
  return jsonResponse(
    { error: { code: 'conflict', message: 'the update check is unavailable', details: { reason } } },
    409,
  );
}

function renderCard(status: UpdateStatus, onCheck: () => Response = () => jsonResponse(status)) {
  return renderSignedIn(<UpdateCard />, {
    respond: (url, init) => {
      if (url === '/api/v1/system/update/check' && init.method === 'POST') return onCheck();
      if (url === '/api/v1/system/update') return jsonResponse(status);
      return undefined;
    },
  });
}

const card = () => screen.getByRole('region', { name: 'Updates' });

describe('UpdateCard texts', () => {
  it('explains every failed check and the refused manual checks', () => {
    expect(updateErrorText('unreachable')).toMatch(/api\.github\.com.*TIDEMARK_UPDATE_PROXY/);
    expect(updateErrorText('rate_limited')).toMatch(/rate-limited/);
    expect(updateErrorText('invalid_response')).toMatch(/unexpected/);
    expect(updateErrorText('not_found')).toMatch(/no releases/);
    expect(checkFailureText(new ApiError(429, 'rate_limited', 'x', { retryAfter: 17 }))).toBe(
      'Checked less than a minute ago. Try again in 17 s.',
    );
    expect(checkFailureText(new ApiError(429, 'rate_limited', 'x'))).toMatch(/Try again shortly/);
    expect(checkFailureText(new ApiError(409, 'conflict', 'x', { reason: 'disabled' }))).toMatch(
      /TIDEMARK_UPDATE_CHECK=false/,
    );
    expect(checkFailureText(new ApiError(409, 'conflict', 'x', { reason: 'dev' }))).toMatch(
      /development build/,
    );
    expect(checkFailureText(new ApiError(500, 'internal', 'internal error'))).toBe('internal error');
  });

  it('builds the container commands for the new version', () => {
    expect(containerCommands('0.5.0')).toEqual({
      image: 'TM_IMAGE=ghcr.io/zeraorg/tidemark:0.5.0',
      pull: 'docker compose pull tidemark && docker compose up -d --no-build tidemark',
      source: 'git fetch --tags && git checkout v0.5.0 && docker compose up -d --build',
    });
  });
});

describe('UpdateCard (SPEC §16.4, §17.7)', () => {
  it('shows an available update with its notes, dates and the installer guide for a service', async () => {
    renderCard(AVAILABLE);
    expect(await screen.findByText('Update available')).toBeVisible();
    expect(card()).toHaveAttribute('id', 'update');
    const notes = within(card()).getByRole('link', { name: /^Release notes/ });
    expect(notes).toHaveAttribute('href', 'https://github.com/ZeraOrg/opentidemark/releases/tag/v0.5.0');
    expect(notes).toHaveAttribute('target', '_blank');
    expect(notes).toHaveAttribute('rel', 'noopener noreferrer');
    expect(within(card()).getByText(/Last checked/).nextElementSibling).toHaveTextContent(/3 h ago/);
    expect(within(card()).getByText(/Latest release/).nextElementSibling).toHaveTextContent(
      /published 2026-10-11/,
    );
    expect(screen.getByText('How to update')).toBeVisible();
    expect(screen.getByRole('link', { name: /^Upgrade guide/ })).toHaveAttribute('href', UPGRADE_DOCS_URL);
    expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled();
  });

  it('gives container installs copyable Compose commands', async () => {
    renderCard({ ...AVAILABLE, installKind: 'container', apply: { supported: false, reason: 'container' } });
    expect(await screen.findByText('How to update')).toBeVisible();
    expect(screen.getByLabelText('TM_IMAGE line for .env')).toHaveTextContent(
      'TM_IMAGE=ghcr.io/zeraorg/tidemark:0.5.0',
    );
    expect(screen.getByLabelText('pull and restart')).toHaveTextContent('docker compose pull tidemark');
    expect(screen.getByLabelText('rebuild from source')).toHaveTextContent('git checkout v0.5.0');
    expect(screen.getAllByRole('button', { name: /^Copy: / })).toHaveLength(3);
  });

  it('points HA nodes at the rolling upgrade', async () => {
    renderCard({ ...AVAILABLE, apply: { supported: false, reason: 'ha' } });
    expect(await screen.findByRole('link', { name: /^Rolling upgrade/ })).toHaveAttribute(
      'href',
      ROLLING_UPGRADE_DOCS_URL,
    );
    expect(screen.getByText(/one node at a time/)).toBeVisible();
  });

  it('marks a pre-release and an unsupported platform', async () => {
    renderCard({
      ...AVAILABLE,
      apply: { supported: false, reason: 'unsupported-platform' },
      latest: { ...LATEST, version: '0.5.0-rc.1', prerelease: true },
    });
    expect(await screen.findByText('pre-release')).toBeVisible();
    expect(screen.getByText(/no release archive for this platform/)).toBeVisible();
  });

  it('says up to date, and "Check now" refreshes the status', async () => {
    const after: UpdateStatus = { ...UP_TO_DATE, checkedAt: '2026-10-13T09:59:30Z' };
    renderCard(UP_TO_DATE, () => jsonResponse(after));
    expect(await screen.findByText('Up to date')).toBeVisible();
    expect(screen.queryByText(/How to update/)).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Check now' }));
    await waitFor(() =>
      expect(within(card()).getByText(/Last checked/).nextElementSibling).toHaveTextContent(/30 s ago/),
    );
  });

  it('explains a 429 with the remaining time and a 409 with its reason', async () => {
    let answer: () => Response = () => rateLimited('42');
    renderCard(UP_TO_DATE, () => answer());
    const button = await screen.findByRole('button', { name: 'Check now' });
    await userEvent.click(button);
    expect(await screen.findByText('Checked less than a minute ago. Try again in 42 s.')).toBeVisible();
    answer = () => conflict('disabled');
    await userEvent.click(button);
    expect(await screen.findByText(/TIDEMARK_UPDATE_CHECK=false/)).toBeVisible();
    expect(screen.queryByText(/Try again in 42 s/)).toBeNull();
  });

  it('shows the last failed check with a hint and keeps the known release', async () => {
    renderCard({ ...AVAILABLE, error: 'unreachable' });
    expect(await screen.findByText(/GitHub could not be reached/)).toHaveTextContent(
      /TIDEMARK_UPDATE_PROXY.*The last known release is shown\./,
    );
    expect(screen.getByText('Update available')).toBeVisible();
    renderCard({ ...UP_TO_DATE, error: 'not_found' });
    expect(await screen.findByText('Check failed')).toBeVisible();
  });

  it('has no check button when disabled or a development build, and says why', async () => {
    const { unmount } = renderCard({
      ...UP_TO_DATE,
      enabled: false,
      apply: { supported: false, reason: 'disabled' },
      checkedAt: undefined,
    });
    expect(await screen.findByText(/Update checks are turned off/)).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull();
    expect(within(card()).getByText(/Last checked/).nextElementSibling).toHaveTextContent('never');
    unmount();
    renderCard({
      ...UP_TO_DATE,
      current: 'dev',
      installKind: 'dev',
      apply: { supported: false, reason: 'dev' },
      checkedAt: undefined,
    });
    expect(await screen.findByText(/Development build/)).toBeVisible();
    expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull();
  });

  it('tells a fresh node that the first check is still to come', async () => {
    renderCard({ ...UP_TO_DATE, checkedAt: undefined });
    expect(await screen.findByText('Not checked yet')).toBeVisible();
    expect(screen.getByText(/first check runs within 15 minutes/)).toBeVisible();
  });
});
