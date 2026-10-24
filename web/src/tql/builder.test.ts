// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  builderProblem,
  emptyModel,
  fromTql,
  isQuantile,
  nextMeasure,
  toTql,
  withFn,
  type BuilderModel,
  type Measure,
} from './builder';

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

  it('renders histogram aggregates without a field', () => {
    const m: BuilderModel = {
      ...emptyModel(),
      source: 'metrics',
      filter: 'name:http.server.request.duration',
      measures: [
        { fn: 'hist_mean', field: '' },
        { fn: 'histogram_quantile', field: '0.95' },
      ],
      groupBy: ['route'],
      interval: '5m',
    };
    expect(toTql(m)).toBe(
      'source metrics | name:http.server.request.duration | stats hist_mean(), histogram_quantile(0.95) by route span 5m',
    );
  });
});

describe('histogram measures', () => {
  it.each([
    ['0.95', true],
    ['0.5', true],
    [' 0.999 ', true],
    ['1', false],
    ['0', false],
    ['95', false],
    ['.5', false],
    ['0.5s', false],
    ['', false],
    ['abc', false],
  ])('isQuantile(%j) = %s', (s, want) => {
    expect(isQuantile(s)).toBe(want);
  });

  it('changes the function and keeps or replaces the argument', () => {
    const avg: Measure = { fn: 'avg', field: 'elapsed' };
    expect(withFn(avg, 'histogram_quantile')).toEqual({ fn: 'histogram_quantile', field: '0.95' });
    expect(withFn(avg, 'hist_mean')).toEqual({ fn: 'hist_mean', field: '' });
    expect(withFn(avg, 'max')).toEqual({ fn: 'max', field: 'elapsed' });
    const q: Measure = { fn: 'histogram_quantile', field: '0.5' };
    expect(withFn(q, 'histogram_quantile')).toEqual(q);
    expect(withFn(q, 'avg')).toEqual({ fn: 'avg', field: '' });
    expect(withFn({ fn: 'hist_mean', field: '' }, 'histogram_quantile').field).toBe('0.95');
  });

  it('adds a measure that fits the existing ones', () => {
    expect(nextMeasure([{ fn: 'count', field: '' }])).toEqual({ fn: 'count', field: '' });
    expect(nextMeasure([{ fn: 'histogram_quantile', field: '0.95' }])).toEqual({
      fn: 'hist_mean',
      field: '',
    });
    expect(nextMeasure([])).toEqual({ fn: 'count', field: '' });
  });

  const metrics = (...measures: Measure[]): BuilderModel => ({
    ...emptyModel(),
    source: 'metrics',
    measures,
  });
  it.each<[string, BuilderModel, string | null]>([
    ['plain measures', emptyModel(), null],
    [
      'histogram measures on metrics',
      metrics({ fn: 'hist_mean', field: '' }, { fn: 'histogram_quantile', field: '0.5' }),
      null,
    ],
    [
      'mixed with count',
      metrics({ fn: 'hist_mean', field: '' }, { fn: 'count', field: '' }),
      'hist_mean() and histogram_quantile() can only be combined with each other, not with other measures.',
    ],
    [
      'mixed with rate',
      metrics({ fn: 'rate', field: 'value' }, { fn: 'histogram_quantile', field: '0.95' }),
      'hist_mean() and histogram_quantile() can only be combined with each other, not with other measures.',
    ],
    [
      'on logs',
      { ...emptyModel(), measures: [{ fn: 'hist_mean', field: '' }] },
      'hist_mean() and histogram_quantile() need the source metrics.',
    ],
    [
      'quantile out of range',
      metrics({ fn: 'histogram_quantile', field: '95' }),
      'histogram_quantile() needs a quantile between 0 and 1, for example 0.95.',
    ],
    [
      'empty quantile',
      metrics({ fn: 'histogram_quantile', field: '' }),
      'histogram_quantile() needs a quantile between 0 and 1, for example 0.95.',
    ],
  ])('builderProblem: %s', (_name, m, want) => {
    expect(builderProblem(m)).toBe(want);
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
    {
      source: 'metrics',
      filter: 'name:http.server.request.duration',
      measures: [
        { fn: 'histogram_quantile', field: '0.95' },
        { fn: 'hist_mean', field: '' },
      ],
      groupBy: ['route'],
      interval: 'auto',
      viz: 'timeseries',
    },
    {
      source: 'metrics',
      filter: 'name:x',
      measures: [{ fn: 'histogram_quantile', field: '0.5' }],
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
    ['hist_mean with a field', 'source metrics | * | stats hist_mean(value)'],
    ['histogram_quantile without a quantile', 'source metrics | * | stats histogram_quantile()'],
    ['histogram_quantile with a field', 'source metrics | * | stats histogram_quantile(value)'],
    ['histogram_quantile out of range', 'source metrics | * | stats histogram_quantile(95)'],
    ['histogram_quantile with an alias', 'source metrics | * | stats histogram_quantile(0.95) as p95'],
    ['arithmetic with an alias', '* | stats sum(elapsed) / count() as mean_ms by service span 1m'],
    ['arithmetic without an alias', '* | stats sum(elapsed) / count() by service'],
    ['arithmetic without spaces', '* | stats sum(a)/sum(b)'],
    ['a number times an aggregate', '* | stats 2*count()'],
    ['an aggregate times a number', 'source metrics | name:x | stats rate(value) * 60 as per_minute span 1m'],
    ['parenthesized arithmetic', '* | stats (max(elapsed) - min(elapsed)) * 1000 as spread'],
    ['arithmetic next to a plain measure', '* | stats count(), sum(a) / count() by service'],
    [
      'arithmetic between histogram quantiles',
      'source metrics | name:x | stats histogram_quantile(0.99) / histogram_quantile(0.5) as tail by route',
    ],
  ])('returns null for %s', (_name, q) => {
    expect(fromTql(q)).toBeNull();
  });

  it('keeps the quantile as written and accepts upper-case names', () => {
    expect(fromTql('SOURCE METRICS | name:x | STATS Histogram_Quantile( 0.50 ) BY route SPAN 1m')).toEqual({
      source: 'metrics',
      filter: 'name:x',
      measures: [{ fn: 'histogram_quantile', field: '0.50' }],
      groupBy: ['route'],
      interval: '1m',
      viz: 'timeseries',
    });
  });

  it('switches a time panel without span to a bar panel', () => {
    expect(fromTql('* | stats count() by service', 'timeseries')?.viz).toBe('bar');
  });

  it('switches any panel with span to a time series', () => {
    expect(fromTql('* | stats count() span 1h', 'table')?.viz).toBe('timeseries');
  });
});
