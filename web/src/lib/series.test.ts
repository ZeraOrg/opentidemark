// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { QueryResponse } from '../api/types';
import {
  cellText,
  keyLabel,
  measureColumns,
  stack,
  statValue,
  tableCategories,
  timeseriesRows,
  timeseriesToChart,
} from './series';

const stats = { elapsedMs: 3, rowsRead: 10, bytesRead: 100 } as QueryResponse['stats'];

const ts: QueryResponse = {
  kind: 'timeseries',
  span: 60_000,
  columns: [
    { name: '@bucket', type: 'timestamp' },
    { name: 'service', type: 'string' },
    { name: 'count', type: 'number' },
  ],
  series: [
    {
      key: { service: 'a' },
      points: [
        [120_000, 5],
        [60_000, 3],
      ],
    },
    {
      key: { service: 'b' },
      points: [
        [60_000, '2'],
        [180_000, null],
      ],
    },
  ],
  stats,
} as QueryResponse;

describe('timeseries', () => {
  it('finds the measure columns', () => {
    expect(measureColumns(ts).map((c) => c.name)).toEqual(['count']);
  });

  it('aligns series on the union of buckets', () => {
    expect(timeseriesToChart(ts)).toEqual({
      x: [60_000, 120_000, 180_000],
      series: [
        { label: 'a', values: [3, 5, null] },
        { label: 'b', values: [2, null, null] },
      ],
    });
  });

  it('labels multi-measure and key-less series', () => {
    const multi = {
      ...ts,
      columns: [
        { name: '@bucket', type: 'timestamp' },
        { name: 'count', type: 'number' },
        { name: 'p95', type: 'duration_ms' },
      ],
      series: [{ key: {}, points: [[60_000, 1, 'x']] }],
    } as QueryResponse;
    expect(timeseriesToChart(multi).series).toEqual([
      { label: 'count', values: [1] },
      { label: 'p95', values: [null] },
    ]);
    const keyed = { ...multi, series: [{ key: { s: 'a' }, points: [[60_000, 1, 2]] }] } as QueryResponse;
    expect(timeseriesToChart(keyed).series.map((s) => s.label)).toEqual(['a · count', 'a · p95']);
  });

  it('stacks values cumulatively', () => {
    expect(
      stack([
        { label: 'a', values: [1, null, 3] },
        { label: 'b', values: [2, 2, null] },
      ]),
    ).toEqual([
      { label: 'a', values: [1, 0, 3] },
      { label: 'b', values: [3, 2, 3] },
    ]);
  });

  it('builds rows newest first with deltas', () => {
    expect(timeseriesRows(ts)).toEqual([
      { bucket: 180_000, key: { service: 'b' }, series: 1, values: [null], delta: [null] },
      { bucket: 120_000, key: { service: 'a' }, series: 0, values: [5], delta: [2] },
      { bucket: 60_000, key: { service: 'a' }, series: 0, values: [3], delta: [null] },
      { bucket: 60_000, key: { service: 'b' }, series: 1, values: [2], delta: [null] },
    ]);
  });

  it('computes the stat value from the last two buckets', () => {
    expect(statValue(ts)).toEqual({ value: 5, previous: 5, spark: [5, 5] });
    expect(statValue({ ...ts, series: [] } as QueryResponse)).toEqual({
      value: null,
      previous: null,
      spark: [],
    });
  });
});

describe('tables', () => {
  const table = {
    kind: 'table',
    columns: [
      { name: 'service', type: 'string' },
      { name: 'route', type: 'string' },
      { name: 'count', type: 'number' },
    ],
    rows: [
      ['a', '/x', 12],
      [null, '/y', 'bad'],
    ],
    stats,
  } as QueryResponse;

  it('turns rows into categories', () => {
    expect(tableCategories(table)).toEqual([
      { label: 'a · /x', value: 12 },
      { label: '∅ · /y', value: 0 },
    ]);
    expect(statValue(table)).toEqual({ value: 12, previous: null, spark: [] });
  });

  it('has no categories without a numeric column', () => {
    const none = { ...table, columns: [{ name: 's', type: 'string' }], rows: [['a']] } as QueryResponse;
    expect(tableCategories(none)).toEqual([]);
    expect(statValue(none).value).toBeNull();
  });
});

describe('text helpers', () => {
  it('labels keys and cells', () => {
    expect(keyLabel({ service: 'a', route: '', x: null })).toBe('a · ∅ · ∅');
    expect(cellText(null)).toBe('');
    expect(cellText({ a: 1 })).toBe('{"a":1}');
    expect(cellText(3)).toBe('3');
  });
});
