// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { aliasOf, baseFor, complete } from './handlers';

describe('aliasOf', () => {
  it.each([
    ['count()', 'count'],
    ['sum(elapsed)', 'sum_elapsed'],
    ['histogram_quantile(0.95)', 'histogram_quantile_0_95'],
    ['sum(elapsed) / count() as mean_ms', 'mean_ms'],
    ['sum(elapsed) / count()', 'sum(elapsed) / count()'],
    ['sum(a)/sum(b)', 'sum(a) / sum(b)'],
    ['2*count()', '2 * count()'],
    ['( max(elapsed) - min(elapsed) ) * 1000', '(max(elapsed) - min(elapsed)) * 1000'],
    ['count() * -1', 'count() * -1'],
    ['sum(response-time)', 'sum_response-time'],
  ])('%s → %s', (agg, want) => {
    expect(aliasOf(agg)).toBe(want);
  });
});

describe('baseFor', () => {
  const q = 'source metrics | name:x | stats count()';

  it('evaluates arithmetic over the bases of its calls', () => {
    const mean = baseFor('sum(elapsed) / count() as mean_ms', 'checkout-api', q);
    expect(mean).toBe(140);
    expect(baseFor('rate(value) * 60', 'checkout-api', q)).toBe(
      baseFor('rate(value)', 'checkout-api', q) * 60,
    );
    expect(baseFor('histogram_quantile(0.99) / histogram_quantile(0.5)', '', q)).toBeCloseTo(520 / 140);
    expect(baseFor('(max(elapsed) - min(elapsed)) * 1000', '', q)).toBe(508_000);
    expect(baseFor('count() * -1', 'checkout-api', q)).toBe(-baseFor('count()', 'checkout-api', q));
  });

  it('respects precedence and division by zero', () => {
    expect(baseFor('count() + count() * 2', 'checkout-api', q)).toBe(
      3 * baseFor('count()', 'checkout-api', q),
    );
    expect(baseFor('count() / (count() - count())', 'checkout-api', q)).toBeNaN();
  });
});

/** `kind:label` of each suggestion at the end of q, like the server's completion tests. */
function keys(q: string): string[] {
  return complete(q, q.length).suggestions.map((s) => `${s.kind}:${s.label}`);
}

describe('complete in a stats stage', () => {
  it.each([
    ['x | stats count() ', ['keyword:by', 'keyword:span', 'keyword:as', 'operator:|'], []],
    ['x | stats count() ', ['operator:+', 'operator:-', 'operator:*', 'operator:/'], []],
    ['x | stats sum(a) / ', ['function:count', 'function:sum'], ['field:level', 'function:hist_mean']],
    ['x | stats sum(a) * ', ['function:avg'], []],
    ['x | stats sum(a) - ', ['function:max'], []],
    ['x | stats sum(a)/', ['function:count'], ['operator:/']],
    ['x | stats (', ['function:sum'], ['field:level']],
    ['x | stats sum(a) / (', ['function:count'], []],
    ['x | stats sum(a) / count(', ['field:level'], ['function:count']],
    ['x | stats ', ['function:count', 'function:rate'], ['function:hist_mean']],
    ['x | stats count(), ', ['function:sum'], []],
    ['x | stats count() by a ', ['keyword:span', 'keyword:as', 'operator:|'], ['operator:/', 'keyword:by']],
    ['x | stats count() by ', ['field:level'], ['function:count']],
    ['x | stats count() by a, ', ['field:level'], []],
    ['x | stats count() span ', ['keyword:auto', 'keyword:5m'], ['operator:+']],
    ['x | stats count() span 5m ', [], []],
    ['source metrics | x | stats ', ['function:hist_mean', 'function:histogram_quantile'], []],
    ['source metrics | x | stats hist_mean(', [], ['field:level']],
    ['source metrics | x | stats rate(value) ', ['operator:*', 'keyword:span'], []],
  ])('%j offers %j but not %j', (q, present, absent) => {
    const got = keys(q);
    for (const p of present) expect(got).toContain(p);
    for (const a of absent) expect(got).not.toContain(a);
  });

  it('filters by the typed prefix and replaces the word', () => {
    const q = 'x | stats count() / su';
    expect(keys(q)).toEqual(['function:sum']);
    expect(complete(q, q.length)).toMatchObject({ from: q.length - 2, to: q.length });
    expect(complete('source metrics | x | stats hist', 31).suggestions).toEqual([
      { kind: 'function', label: 'hist_mean', insertText: 'hist_mean()', detail: 'histogram aggregate' },
      {
        kind: 'function',
        label: 'histogram_quantile',
        insertText: 'histogram_quantile(0.95)',
        detail: 'histogram aggregate',
      },
    ]);
    expect(complete('x | stats count() ', 18).suggestions).toContainEqual({
      kind: 'operator',
      label: '/',
      insertText: '/ ',
      detail: 'arithmetic between aggregates',
    });
  });

  it('leaves filters to the term completion', () => {
    expect(keys('level ')).toContain('keyword:and');
    expect(keys('x | stats count() | where level:')).toContain('value:error');
  });
});
