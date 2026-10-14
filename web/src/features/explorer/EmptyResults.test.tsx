// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import type { Stream } from '../../api/types';
import { jsonResponse } from '../../test/fakes';
import { ADMIN_ME, renderSignedIn, restrictedMe } from '../../test/session';
import { EmptyResults, hasNoLogData, INGESTION_DOCS_URL } from './EmptyResults';

function stream(name: string, rows: number | null): Stream {
  return {
    id: `s-${name}`,
    name,
    description: '',
    retentionDays: null,
    spansRetentionDays: null,
    metricsRetentionDays: null,
    levelRules: [],
    paused: false,
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-01T00:00:00Z',
    ...(rows === null
      ? {}
      : {
          stats: {
            logs: {
              rows,
              oldest: '',
              newest: '',
              ratePer5Min: 0,
              ratePer24h: 0,
              compressedBytes: 0,
              uncompressedBytes: 0,
              compressionRatio: 0,
              estimated: false,
              topServices: [],
            },
          },
        }),
  };
}

function setup(streams: Stream[], me = ADMIN_ME) {
  const onLast24h = vi.fn();
  const rendered = renderSignedIn(<EmptyResults onLast24h={onLast24h} />, {
    me,
    path: '/events',
    respond: (url) => {
      if (url.startsWith('/api/v1/streams')) return jsonResponse({ streams, statsUpdatedAt: null });
      return undefined;
    },
  });
  return { ...rendered, onLast24h };
}

describe('hasNoLogData', () => {
  it('is false while loading and true only when no stream holds log rows', () => {
    expect(hasNoLogData(undefined)).toBe(false);
    expect(hasNoLogData({ streams: [], statsUpdatedAt: null })).toBe(true);
    expect(hasNoLogData({ streams: [stream('api', null), stream('w', 0)], statsUpdatedAt: null })).toBe(true);
    expect(hasNoLogData({ streams: [stream('api', 0), stream('w', 3)], statsUpdatedAt: null })).toBe(false);
  });
});

describe('EmptyResults', () => {
  it('tells an admin how to connect the first source when no stream has data', async () => {
    setup([stream('api', 0), stream('workers', null)]);
    expect(await screen.findByText('No data yet')).toBeInTheDocument();
    expect(screen.getByText('bootstrap')).toBeInTheDocument();
    expect(screen.getByText('TIDEMARK_BOOTSTRAP_INGEST_KEY')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open connections' })).toHaveAttribute(
      'href',
      '/settings/connections',
    );
    const docs = screen.getByRole('link', { name: 'Ingestion docs (opens in a new tab)' });
    expect(docs).toHaveAttribute('href', INGESTION_DOCS_URL);
    expect(docs).toHaveAttribute('target', '_blank');
    expect(docs).toHaveAttribute('rel', 'noopener noreferrer');
    expect(screen.queryByText('No events match')).toBeNull();
  });

  it('asks a non-admin to contact an administrator, without links', async () => {
    setup([stream('payments', 0)], restrictedMe(['payments']));
    expect(await screen.findByText('No data yet')).toBeInTheDocument();
    expect(screen.getByText('Ask an administrator to connect a log source.')).toBeInTheDocument();
    expect(screen.queryByRole('link')).toBeNull();
    expect(screen.queryByText(/bootstrap/)).toBeNull();
  });

  it('keeps "No events match" when any visible stream has log rows', async () => {
    const { calls, onLast24h } = setup([stream('api', 0), stream('workers', 12)]);
    expect(await screen.findByText('No events match')).toBeInTheDocument();
    await waitFor(() => expect(calls.some((c) => c.url.startsWith('/api/v1/streams'))).toBe(true));
    await waitFor(() => expect(screen.getByText('No events match')).toBeInTheDocument());
    expect(screen.queryByText('No data yet')).toBeNull();
    screen.getByRole('button', { name: 'Last 24 hours' }).click();
    expect(onLast24h).toHaveBeenCalledTimes(1);
  });
});
