// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { emptyModel, fromTql, toTql, type BuilderModel } from './builder';

describe('toTql', () => {
  it('generates the default count query', () => {
    expect(toTql(emptyModel())).toBe('* | stats count() span auto');
  });

  it('includes source, filter, measures, group by and span', () => {
    const m: BuilderModel = {
      source: 'spans',
      filter: ' status:error ',
      measures: [
        { fn: 'p95', field: 'duration_ms' },
        { fn: 'count', field: '' },
      ],
      groupBy: ['service', ' ', 'name'],
      interval: '5m',
      viz: 'timeseries',
    };
    expect(toTql(m)).toBe(
      'source spans | status:error | stats p95(duration_ms), count() by service, name span 5m',
    );
  });

  it('omits span for non-time panels and defaults an empty measure list to count()', () => {
    expect(toTql({ ...emptyModel(), measures: [], viz: 'bar' })).toBe('* | stats count()');
  });
});

describe('fromTql', () => {
  const roundTrip: BuilderModel[] = [
    emptyModel(),
    {
      source: 'logs',
      filter: 'level:error and service:checkout*',
      measures: [{ fn: 'avg', field: 'elapsed' }],
      groupBy: ['service'],
      interval: '1m',
      viz: 'timeseries',
    },
    {
      source: 'metrics',
      filter: 'name:http.server.request.duration',
      measures: [
        { fn: 'avg', field: 'value' },
        { fn: 'max', field: 'value' },
      ],
      groupBy: ['service', '`k8s pod`'],
      interval: '15m',
      viz: 'timeseries',
    },
    {
      source: 'logs',
      filter: '',
      measures: [{ fn: 'count_distinct', field: 'user.id' }],
      groupBy: [],
      interval: 'auto',
      viz: 'table',
    },
  ];
  it.each(roundTrip)('round-trips builder → query → builder (%#)', (m) => {
    expect(fromTql(toTql(m), m.viz)).toEqual(m);
  });

  it.each([
    ['no stats stage', 'level:error'],
    ['two stages after the filter', '* | stats count() | sort count desc'],
    ['unknown function', '* | stats median(x)'],
    ['expression as group', '* | stats count() by lower(service)'],
    ['too many measures', '* | stats count(), sum(a), avg(b), max(c)'],
    ['too many groups', '* | stats count() by a, b, c, d'],
    ['unsupported interval', '* | stats count() span 7m'],
    ['empty stats', '* | stats '],
  ])('returns null for %s', (_name, q) => {
    expect(fromTql(q)).toBeNull();
  });

  it('switches a time panel without span to a bar panel', () => {
    expect(fromTql('* | stats count() by service', 'timeseries')?.viz).toBe('bar');
  });

  it('switches any panel with span to a time series', () => {
    expect(fromTql('* | stats count() span 1h', 'table')?.viz).toBe('timeseries');
  });
});
