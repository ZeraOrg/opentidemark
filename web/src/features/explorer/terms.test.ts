// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { appendTerm, fieldRef, hasStages, term, valueLiteral } from './terms';
import { parseException } from './exception';

describe('facet terms', () => {
  it.each([
    ['service', 'service'],
    ['http.status_code', 'http.status_code'],
    ['my field', '`my field`'],
    ['we`ird', '`weird`'],
  ])('fieldRef(%j)', (name, want) => {
    expect(fieldRef(name)).toBe(want);
  });

  it.each([
    ['checkout-api', 'checkout-api'],
    ['/api/orders', '"/api/orders"'],
    ['two words', '"two words"'],
    ['and', '"and"'],
    ['', '""'],
    ['a"b\\c*', '"a\\"b\\\\c\\*"'],
  ])('valueLiteral(%j)', (v, want) => {
    expect(valueLiteral(v)).toBe(want);
  });

  it('builds include and exclude terms', () => {
    expect(term('service', 'api')).toBe('service:api');
    expect(term('service', 'api', true)).toBe('not service:api');
  });

  it.each([
    ['', 'level:error'],
    ['*', 'level:error'],
    ['timeout', 'timeout and level:error'],
    ['timeout | stats count() by service', 'timeout and level:error | stats count() by service'],
    ['source spans', 'source spans | level:error'],
    ['source spans | status:error', 'source spans | status:error and level:error'],
  ])('appendTerm(%j)', (q, want) => {
    expect(appendTerm(q, 'level:error')).toBe(want);
  });

  it('detects pipe stages', () => {
    expect(hasStages('a | stats count()')).toBe(true);
    expect(hasStages('source spans | a')).toBe(false);
    expect(hasStages('a')).toBe(false);
  });
});

describe('parseException', () => {
  it('splits headline, type and frames and marks framework frames', () => {
    const p = parseException(
      [
        'System.InvalidOperationException: Sequence contains no elements',
        '   at Shop.Orders.Place(Order o) in /src/Orders.cs:line 42',
        '   at System.Linq.Enumerable.First[T](IEnumerable`1 source)',
        '    at handler (/app/node_modules/express/lib/router.js:12:3)',
        '  File "/usr/lib/python3/site-packages/x.py", line 9, in f',
        '  main.go:17',
        '',
      ].join('\r\n'),
    );
    expect(p.type).toBe('System.InvalidOperationException');
    expect(p.headline).toBe('System.InvalidOperationException: Sequence contains no elements');
    expect(p.frames.map((f) => [f.framework, f.location])).toEqual([
      [false, '/src/Orders.cs:line 42'],
      [true, null],
      [true, '/app/node_modules/express/lib/router.js:12:3'],
      [true, '/usr/lib/python3/site-packages/x.py:9'],
      [false, 'main.go:17'],
    ]);
  });

  it('has no type for free-text headlines', () => {
    expect(parseException('something failed: badly').type).toBe('');
  });
});
