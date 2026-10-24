// SPDX-License-Identifier: Apache-2.0

import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it } from 'vitest';
import type { MetricName, QueryResponse } from '../../api/types';
import { ShellProvider } from '../../app/shellContext';
import { jsonResponse } from '../../test/fakes';
import { renderSignedIn } from '../../test/session';
import MetricsPage, { aggLabel, aggOptions, aggUnit, defaultAgg, metricQuery } from './MetricsPage';

describe('metric quick-chart aggregates', () => {
  it.each([
    ['sum', true, 'rate'],
    ['counter', true, 'rate'],
    ['sum', false, 'avg'],
    ['counter', false, 'avg'],
    ['gauge', false, 'avg'],
    ['gauge', true, 'avg'],
    ['', false, 'avg'],
    ['histogram', false, 'hist_mean'],
    ['exp_histogram', false, 'hist_mean'],
    ['summary', false, 'hist_mean'],
  ])('defaults %j (monotonic %j) to %s', (kind, monotonic, want) => {
    expect(defaultAgg(kind, monotonic)).toBe(want);
  });

  it('offers quantiles for histograms, the rate for counters and plain aggregates for the rest', () => {
    expect(aggOptions('histogram', false)).toEqual(['hist_mean', 'hist_p50', 'hist_p95', 'hist_p99']);
    expect(aggOptions('exp_histogram', false)).toEqual(['hist_mean', 'hist_p50', 'hist_p95', 'hist_p99']);
    expect(aggOptions('summary', false)).toEqual(['hist_mean']);
    expect(aggOptions('sum', true)).toEqual(['rate', 'avg', 'sum', 'max', 'min', 'p95']);
    expect(aggOptions('counter', true)).toEqual(['rate', 'avg', 'sum', 'max', 'min', 'p95']);
    expect(aggOptions('sum', false)).toEqual(['avg', 'sum', 'max', 'min', 'p95']);
    expect(aggOptions('gauge', false)).toEqual(['avg', 'sum', 'max', 'min', 'p95']);
    expect(aggOptions('gauge', true)).toEqual(['avg', 'sum', 'max', 'min', 'p95']);
    expect(aggOptions('histogram', false).map(aggLabel)).toEqual(['Mean', 'p50', 'p95', 'p99']);
    expect(aggOptions('sum', true).map(aggLabel)).toEqual(['rate/s', 'avg', 'sum', 'max', 'min', 'p95']);
  });

  it('writes the rate of a sum', () => {
    expect(metricQuery('tidemark.ingest.events', 'rate', 'service')).toBe(
      'source metrics | name:tidemark.ingest.events | stats rate(value) by service span auto',
    );
  });

  it.each([
    ['rate', '{event}', '{event}/s'],
    ['sum', '{event}', '{event}'],
    ['rate', '1', ''],
    ['avg', '1', ''],
    ['rate', '', ''],
    ['avg', undefined, ''],
  ] as const)('shows %s of a metric in %j as %j', (agg, unit, want) => {
    expect(aggUnit(agg, unit)).toBe(want);
  });

  it('writes histogram aggregates without a field', () => {
    expect(metricQuery('http.server.request.duration', 'hist_mean', 'service')).toBe(
      'source metrics | name:http.server.request.duration | stats hist_mean() by service span auto',
    );
    expect(metricQuery('http.server.request.duration', 'hist_p95', 'none')).toBe(
      'source metrics | name:http.server.request.duration | stats histogram_quantile(0.95) span auto',
    );
    expect(metricQuery('x', 'hist_p50', 'none')).toBe(
      'source metrics | name:x | stats histogram_quantile(0.5) span auto',
    );
    expect(metricQuery('x', 'hist_p99', 'none')).toBe(
      'source metrics | name:x | stats histogram_quantile(0.99) span auto',
    );
    expect(metricQuery('a b', 'avg', 'service')).toBe(
      'source metrics | name:"a b" | stats avg(value) by service span auto',
    );
  });
});

const NAMES: MetricName[] = [
  {
    name: 'http.server.request.duration',
    kind: 'histogram',
    unit: 's',
    description: 'Request duration.',
    monotonic: false,
  },
  { name: 'process.cpu.utilization', kind: 'gauge', unit: '1', description: 'CPU.', monotonic: false },
  {
    name: 'tidemark.ingest.events',
    kind: 'sum',
    unit: '{event}',
    description: 'Events accepted.',
    monotonic: true,
  },
  {
    name: 'http.server.active_requests',
    kind: 'sum',
    unit: '{request}',
    description: 'Active requests.',
    monotonic: false,
  },
];

/** A timeseries answer naming its column like the server does; one bucket has no observations. */
function answer(column: string): QueryResponse {
  return {
    kind: 'timeseries',
    span: 60_000,
    columns: [
      { name: '@bucket', type: 'timestamp' },
      { name: 'service', type: 'string' },
      { name: column, type: 'number' },
    ],
    series: [
      {
        key: { service: 'checkout-api' },
        points: [
          [60_000, 0.21],
          [120_000, null],
          [180_000, 0.25],
        ],
      },
    ],
    stats: { elapsedMs: 1, rowsRead: 3, bytesRead: 30, interpretedAsText: false },
  } as QueryResponse;
}

function setup() {
  const queries: string[] = [];
  const r = renderSignedIn(
    <ShellProvider>
      <MetricsPage />
    </ShellProvider>,
    {
      path: '/metrics',
      respond: (url, init) => {
        if (url.startsWith('/api/v1/metrics/names')) return jsonResponse(NAMES);
        if (url.startsWith('/api/v1/query')) {
          const q = (JSON.parse(String(init.body)) as { q: string }).q;
          queries.push(q);
          return jsonResponse(
            answer(/histogram_quantile\(0\.95\)/.test(q) ? 'histogram_quantile_0_95' : 'hist_mean'),
          );
        }
        return undefined;
      },
    },
  );
  return { ...r, queries };
}

describe('MetricsPage', () => {
  it('charts a histogram as its mean by default and as p95 on request', async () => {
    const { queries } = setup();
    const aggregate = await screen.findByRole('group', { name: 'Aggregate' });
    expect(within(aggregate).getByRole('button', { name: 'Mean' })).toHaveAttribute('aria-pressed', 'true');
    expect(
      within(aggregate)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual(['Mean', 'p50', 'p95', 'p99']);
    expect(queries).toContain(
      'source metrics | name:http.server.request.duration | stats hist_mean() by service span auto',
    );
    expect(await screen.findByRole('img', { name: /http.server.request.duration/ })).toHaveAttribute(
      'aria-label',
      'http.server.request.duration: 1 series, 3 points',
    );
    await userEvent.click(within(aggregate).getByRole('button', { name: 'p95' }));
    expect(within(aggregate).getByRole('button', { name: 'p95' })).toHaveAttribute('aria-pressed', 'true');
    expect(queries).toContain(
      'source metrics | name:http.server.request.duration | stats histogram_quantile(0.95) by service span auto',
    );
    expect(screen.getByRole('link', { name: 'Open in explorer' })).toHaveAttribute(
      'href',
      `/events?q=${encodeURIComponent('source metrics | name:http.server.request.duration | stats histogram_quantile(0.95) by service span auto')}`,
    );
  });

  it('offers plain aggregates for a gauge and resets the choice when the metric changes', async () => {
    const { queries } = setup();
    const aggregate = await screen.findByRole('group', { name: 'Aggregate' });
    await userEvent.click(within(aggregate).getByRole('button', { name: 'p95' }));
    await userEvent.click(screen.getByRole('button', { name: /process\.cpu\.utilization/ }));
    expect(within(aggregate).getByRole('button', { name: 'avg' })).toHaveAttribute('aria-pressed', 'true');
    expect(
      within(aggregate)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual(['avg', 'sum', 'max', 'min', 'p95']);
    expect(queries).toContain(
      'source metrics | name:process.cpu.utilization | stats avg(value) by service span auto',
    );
  });

  it('charts an up-down sum as its average without a rate button', async () => {
    const { queries } = setup();
    await screen.findByRole('group', { name: 'Aggregate' });
    await userEvent.click(screen.getByRole('button', { name: /http\.server\.active_requests/ }));
    const aggregate = screen.getByRole('group', { name: 'Aggregate' });
    expect(within(aggregate).getByRole('button', { name: 'avg' })).toHaveAttribute('aria-pressed', 'true');
    expect(
      within(aggregate)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual(['avg', 'sum', 'max', 'min', 'p95']);
    expect(queries).toContain(
      'source metrics | name:http.server.active_requests | stats avg(value) by service span auto',
    );
  });

  it('charts a counter as its per-second rate by default and as a sum on request', async () => {
    const { queries } = setup();
    await screen.findByRole('group', { name: 'Aggregate' });
    await userEvent.click(screen.getByRole('button', { name: /tidemark\.ingest\.events/ }));
    const aggregate = screen.getByRole('group', { name: 'Aggregate' });
    expect(within(aggregate).getByRole('button', { name: 'rate/s' })).toHaveAttribute('aria-pressed', 'true');
    expect(
      within(aggregate)
        .getAllByRole('button')
        .map((b) => b.textContent),
    ).toEqual(['rate/s', 'avg', 'sum', 'max', 'min', 'p95']);
    expect(queries).toContain(
      'source metrics | name:tidemark.ingest.events | stats rate(value) by service span auto',
    );
    await userEvent.click(within(aggregate).getByRole('button', { name: 'sum' }));
    expect(within(aggregate).getByRole('button', { name: 'sum' })).toHaveAttribute('aria-pressed', 'true');
    expect(queries).toContain(
      'source metrics | name:tidemark.ingest.events | stats sum(value) by service span auto',
    );
  });
});
