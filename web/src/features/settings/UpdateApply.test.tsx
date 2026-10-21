// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import { ApiError } from '../../api/client';
import type { UpdateStatus } from '../../api/types';
import { ToastProvider } from '../../components/overlay';
import { STORAGE_KEYS } from '../../lib/storage';
import { jsonResponse, memoryStore } from '../../test/fakes';
import { renderSignedIn } from '../../test/session';
import {
  applyFailureText,
  applyView,
  needsTypedConfirmation,
  stepText,
  type ApplyTarget,
} from './UpdateApply';
import UpdateCard from './UpdateCard';

const CHECKED_AT = '2026-10-13T07:00:00Z';
const LATEST = {
  version: '0.5.0',
  prerelease: false,
  publishedAt: '2026-10-11T09:30:00Z',
  notesUrl: 'https://github.com/ZeraOrg/opentidemark/releases/tag/v0.5.0',
};
const SUPPORTED: UpdateStatus = {
  enabled: true,
  current: '0.4.0',
  nodeId: 'node-1',
  installKind: 'systemd',
  apply: { supported: true },
  latest: LATEST,
  available: true,
  checkedAt: CHECKED_AT,
  state: 'idle',
};
const PATCH: UpdateStatus = { ...SUPPORTED, latest: { ...LATEST, version: '0.4.1' } };
const DOWNLOADING: UpdateStatus = {
  ...SUPPORTED,
  state: 'downloading',
  progress: { bytes: 12_288_000, total: 24_576_000 },
};
const INSTALLED: UpdateStatus = {
  ...SUPPORTED,
  current: '0.5.0',
  latest: undefined,
  available: false,
  lastResult: { phase: 'installed', from: '0.4.0', to: '0.5.0', at: '2026-10-13T09:58:00Z' },
};
const ROLLED_BACK: UpdateStatus = {
  ...SUPPORTED,
  lastResult: {
    phase: 'rolled_back',
    from: '0.4.0',
    to: '0.5.0',
    at: '2026-10-13T09:58:00Z',
    message: 'the new version did not become ready within 10 min',
  },
};
const TARGET: ApplyTarget = { version: '0.5.0', from: '0.4.0', startedAt: 1 };

function conflict(reason: string): Response {
  return jsonResponse({ error: { code: 'conflict', message: 'refused', details: { reason } } }, 409);
}

/** A network failure: fetch rejects, which the client reports as `ApiError` status 0. */
function offline(): Response {
  throw new TypeError('Failed to fetch');
}

/**
 * Renders the card with a scripted status: `GET /system/update` answers the current entry of
 * `statuses` (an `offline` entry throws); `advance()` moves to the next one, so the test decides
 * when the server's state changes. `POST …/apply` answers `onApply`.
 */
function renderCard(
  statuses: (UpdateStatus | (() => Response))[],
  onApply: (body: unknown) => Response = () => jsonResponse(DOWNLOADING, 202),
  store = memoryStore(),
) {
  let i = 0;
  const result = renderSignedIn(
    <ToastProvider>
      <UpdateCard pollMs={15} />
    </ToastProvider>,
    {
      services: { store },
      respond: (url, init) => {
        if (url === '/api/v1/system/update/apply' && init.method === 'POST') {
          return onApply(JSON.parse(String(init.body)));
        }
        if (url === '/api/v1/system/update') {
          const next = statuses[Math.min(i, statuses.length - 1)];
          return typeof next === 'function' ? next() : jsonResponse(next);
        }
        return undefined;
      },
    },
  );
  const advance = (): void => {
    i++;
  };
  return { ...result, advance };
}

const card = () => screen.getByRole('region', { name: 'Updates' });
const installButton = () => screen.findByRole('button', { name: 'Install v0.5.0' });
const dialog = () => screen.getByRole('dialog', { name: 'Install v0.5.0' });
const confirmButton = () => within(dialog()).getByRole('button', { name: 'Install v0.5.0' });

describe('one-click update helpers', () => {
  it.each([
    ['0.4.0', '0.4.1', false],
    ['0.4.0', '0.5.0', true],
    ['0.4.0', '1.0.0', true],
    ['1.2.3', '1.3.0', false],
    ['1.2.3', '2.0.0', true],
    ['dev', '0.5.0', true],
  ])('%s → %s needs a typed confirmation: %s', (current, target, want) => {
    expect(needsTypedConfirmation(current, target)).toBe(want);
  });

  it('explains every refused apply', () => {
    expect(applyFailureText(new ApiError(409, 'conflict', 'x', { reason: 'draining' }))).toMatch(
      /shutting down/,
    );
    expect(applyFailureText(new ApiError(409, 'conflict', 'x', { reason: 'wal-failed' }))).toMatch(
      /write-ahead log/,
    );
    expect(applyFailureText(new ApiError(409, 'conflict', 'x', { reason: 'in-progress' }))).toMatch(
      /already/,
    );
    expect(applyFailureText(new ApiError(409, 'conflict', 'x', { reason: 'helper-missing' }))).toMatch(
      /helper/,
    );
    expect(applyFailureText(new ApiError(409, 'conflict', 'x', { reason: 'container' }))).toMatch(/image/);
    expect(applyFailureText(new ApiError(400, 'bad_request', 'x', { field: 'version' }))).toMatch(/changed/);
    expect(applyFailureText(new ApiError(500, 'internal', 'boom'))).toBe('boom');
  });

  it('derives the view from the target, the status and reachability', () => {
    expect(applyView(null, SUPPORTED, false)).toEqual({ kind: 'none' });
    expect(applyView(TARGET, undefined, false)).toEqual({
      kind: 'progress',
      step: 'unknown',
      unreachable: false,
    });
    expect(applyView(TARGET, DOWNLOADING, false)).toEqual({
      kind: 'progress',
      step: 'downloading',
      progress: DOWNLOADING.progress,
      unreachable: false,
    });
    expect(applyView(TARGET, DOWNLOADING, true)).toEqual({
      kind: 'progress',
      step: 'restarting',
      unreachable: true,
    });
    expect(applyView(TARGET, INSTALLED, false)).toEqual({ kind: 'installed', from: '0.4.0', to: '0.5.0' });
    expect(applyView(TARGET, ROLLED_BACK, false)).toEqual({ kind: 'ended' });
    // The same outcome seen before the attempt started is not this attempt's.
    const seen = { ...TARGET, seenResultAt: '2026-10-13T09:58:00Z' };
    expect(applyView(seen, ROLLED_BACK, false)).toEqual({
      kind: 'progress',
      step: 'unknown',
      unreachable: false,
    });
  });

  it('names each step', () => {
    expect(stepText('downloading', { bytes: 12_288_000, total: 24_576_000 }, false)).toBe(
      'Downloading 11.7 MiB of 23.4 MiB',
    );
    expect(stepText('downloading', undefined, false)).toMatch(/Downloading/);
    expect(stepText('verifying', undefined, false)).toMatch(/signature/);
    expect(stepText('staged', undefined, false)).toMatch(/helper/);
    expect(stepText('installing', undefined, false)).toMatch(/Installing/);
    expect(stepText('restarting', undefined, false)).toMatch(/Restarting/);
    expect(stepText('unknown', undefined, false)).toMatch(/Waiting/);
    expect(stepText('installing', undefined, true)).toMatch(/coming back/);
  });
});

describe('UpdateCard one-click update (SPEC §16.4, §17.7)', () => {
  it('opens a dialog that explains the restart and needs the typed version for a minor bump', async () => {
    renderCard([SUPPORTED]);
    await userEvent.click(await installButton());
    expect(dialog()).toBeVisible();
    expect(within(dialog()).getByText(/15–30 seconds/)).toBeVisible();
    expect(within(dialog()).getByText(/within 10 minutes/)).toBeVisible();
    expect(within(dialog()).getByRole('link', { name: /^Release notes for v0\.5\.0/ })).toHaveAttribute(
      'href',
      LATEST.notesUrl,
    );
    expect(within(dialog()).getByText(/breaking changes/)).toBeVisible();
    expect(confirmButton()).toBeDisabled();
    const input = within(dialog()).getByLabelText('Type 0.5.0 to confirm');
    expect(input).toHaveFocus();
    await userEvent.type(input, '0.5');
    expect(confirmButton()).toBeDisabled();
    expect(within(dialog()).getByText('Confirmation does not match yet.')).toBeVisible();
    await userEvent.type(input, '.0');
    expect(confirmButton()).toBeEnabled();
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(screen.getByRole('button', { name: 'Install v0.5.0' })).toHaveFocus();
  });

  it('needs no typed confirmation for a patch release and sends the version', async () => {
    const bodies: unknown[] = [];
    renderCard([PATCH], (b) => {
      bodies.push(b);
      return jsonResponse({ ...PATCH, state: 'downloading' }, 202);
    });
    await userEvent.click(await screen.findByRole('button', { name: 'Install v0.4.1' }));
    const d = screen.getByRole('dialog', { name: 'Install v0.4.1' });
    expect(within(d).queryByLabelText(/to confirm/)).toBeNull();
    await userEvent.click(within(d).getByRole('button', { name: 'Install v0.4.1' }));
    await waitFor(() => expect(bodies).toEqual([{ version: '0.4.1' }]));
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('follows the states, tolerates the restart and ends up to date with a toast', async () => {
    const { advance } = renderCard([
      SUPPORTED,
      DOWNLOADING,
      { ...SUPPORTED, state: 'verifying' },
      { ...SUPPORTED, state: 'restarting' },
      offline,
      INSTALLED,
    ]);
    await userEvent.click(await installButton());
    await userEvent.type(within(dialog()).getByLabelText('Type 0.5.0 to confirm'), '0.5.0');
    advance();
    await userEvent.click(confirmButton());
    const progress = await screen.findByRole('status', { name: 'Installing v0.5.0' });
    expect(within(card()).getByText('Updating')).toBeVisible();
    expect(screen.queryByRole('button', { name: /^Install/ })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull();
    expect(await within(progress).findByText('Downloading 11.7 MiB of 23.4 MiB')).toBeVisible();
    expect(within(progress).getByRole('progressbar', { name: 'Download progress' })).toHaveAttribute(
      'aria-valuenow',
      '50',
    );
    advance();
    expect(await within(progress).findByText(/Verifying the signature/)).toBeVisible();
    expect(within(progress).queryByRole('progressbar')).toBeNull();
    advance();
    expect(await within(progress).findByText(/^Restarting the service/)).toBeVisible();
    advance();
    expect(await within(progress).findByText(/coming back/)).toBeVisible();
    advance();
    expect(await screen.findByText('Up to date')).toBeVisible();
    expect(screen.getByText('Installed v0.5.0')).toBeVisible();
    expect(within(card()).getByText(/Last update/).nextElementSibling).toHaveTextContent(
      /installed v0\.5\.0 \(from v0\.4\.0\)/,
    );
    expect(screen.queryByRole('status', { name: /^Installing/ })).toBeNull();
    expect(screen.getByRole('button', { name: 'Check now' })).toBeEnabled();
  });

  it('shows a rolled-back attempt with the message and both versions', async () => {
    const { advance } = renderCard([SUPPORTED, offline, ROLLED_BACK]);
    await userEvent.click(await installButton());
    await userEvent.type(within(dialog()).getByLabelText('Type 0.5.0 to confirm'), '0.5.0');
    advance();
    await userEvent.click(confirmButton());
    expect(await screen.findByText(/coming back/)).toBeVisible();
    advance();
    const panel = await screen.findByRole('alert');
    expect(panel).toHaveTextContent('The update to v0.5.0 was rolled back; v0.4.0 is running again.');
    expect(panel).toHaveTextContent('The new version did not become ready within 10 min. The helper');
    expect(panel).toHaveTextContent(/journalctl -u tidemark-update/);
    expect(within(card()).getByText(/Last update/).nextElementSibling).toHaveTextContent(
      /rolled back to v0\.4\.0 after installing v0\.5\.0/,
    );
    expect(screen.getByText('Update available')).toBeVisible();
    expect(screen.getByRole('button', { name: 'Install v0.5.0' })).toBeEnabled();
  });

  it('shows the progress for a 409 in-progress', async () => {
    const { advance } = renderCard([SUPPORTED, { ...SUPPORTED, state: 'installing' }], () =>
      conflict('in-progress'),
    );
    await userEvent.click(await installButton());
    await userEvent.type(within(dialog()).getByLabelText('Type 0.5.0 to confirm'), '0.5.0');
    advance();
    await userEvent.click(confirmButton());
    const progress = await screen.findByRole('status', { name: 'Installing v0.5.0' });
    expect(await within(progress).findByText(/Installing the new binary/)).toBeVisible();
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('keeps the dialog open with the reason for draining and wal-failed', async () => {
    let answer = () => conflict('draining');
    renderCard([SUPPORTED], () => answer());
    await userEvent.click(await installButton());
    await userEvent.type(within(dialog()).getByLabelText('Type 0.5.0 to confirm'), '0.5.0');
    await userEvent.click(confirmButton());
    expect(await within(dialog()).findByText(/shutting down/)).toBeVisible();
    answer = () => conflict('wal-failed');
    await userEvent.click(confirmButton());
    expect(await within(dialog()).findByText(/write-ahead log/)).toBeVisible();
  });

  it('refreshes the status when the version is no longer the latest', async () => {
    const newer: UpdateStatus = { ...SUPPORTED, latest: { ...LATEST, version: '0.5.1' } };
    const { advance } = renderCard([SUPPORTED, newer], () =>
      jsonResponse({ error: { code: 'bad_request', message: 'x', details: { field: 'version' } } }, 400),
    );
    await userEvent.click(await installButton());
    await userEvent.type(within(dialog()).getByLabelText('Type 0.5.0 to confirm'), '0.5.0');
    advance();
    await userEvent.click(confirmButton());
    expect(await screen.findByText(/The latest release changed/)).toBeVisible();
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(await screen.findByRole('button', { name: 'Install v0.5.1' })).toBeVisible();
  });

  it('resumes a remembered update after a reload or a new sign-in', async () => {
    const store = memoryStore({ [STORAGE_KEYS.updateTarget]: JSON.stringify(TARGET) });
    const { advance } = renderCard([{ ...SUPPORTED, state: 'restarting' }, INSTALLED], undefined, store);
    expect(await screen.findByRole('status', { name: 'Installing v0.5.0' })).toHaveTextContent(/Restarting/);
    advance();
    expect(await screen.findByText('Installed v0.5.0')).toBeVisible();
    await waitFor(() => expect(store.get(STORAGE_KEYS.updateTarget)).toBeNull());
  });

  it('offers no install where the helper is missing and says how to get it', async () => {
    renderCard([{ ...SUPPORTED, apply: { supported: false, reason: 'helper-missing' } }]);
    expect(await screen.findByText('How to update')).toBeVisible();
    expect(screen.queryByRole('button', { name: /^Install/ })).toBeNull();
    expect(screen.getByText(/needs the update helper/)).toBeVisible();
    expect(screen.getByText(/adds the helper/)).toBeVisible();
    expect(screen.getByRole('link', { name: /^Upgrade guide/ })).toBeVisible();
  });
});
