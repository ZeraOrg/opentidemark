// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { splitPipes, tokenize, variablesOf, type TokenKind } from './tokenize';

/** Non-space tokens as `kind:text` pairs. */
function kinds(q: string): string[] {
  return tokenize(q)
    .filter((t) => t.kind !== 'space')
    .map((t) => `${t.kind}:${t.text}`);
}

describe('tokenize', () => {
  it('covers every character exactly once', () => {
    const q = "level:error and not service:'a b' | stats count() by service span 5m";
    const toks = tokenize(q);
    expect(toks.map((t) => t.text).join('')).toBe(q);
    toks.forEach((t, i) => {
      expect(t.from).toBe(i === 0 ? 0 : toks[i - 1]?.to);
    });
  });

  const cases: { name: string; q: string; want: string[] }[] = [
    { name: 'free text', q: 'timeout', want: ['value:timeout'] },
    { name: 'phrase', q: '"connection refused"', want: ['string:"connection refused"'] },
    { name: 'field:value', q: 'level:error', want: ['field:level', 'operator::', 'value:error'] },
    {
      name: 'comparison with number',
      q: 'status_code >= 500',
      want: ['field:status_code', 'operator:>=', 'number:500'],
    },
    {
      name: 'boolean logic and wildcard',
      q: 'service:checkout* and not env:dev',
      want: [
        'field:service',
        'operator::',
        'value:checkout*',
        'keyword:and',
        'keyword:not',
        'field:env',
        'operator::',
        'value:dev',
      ],
    },
    {
      name: 'regex value',
      q: 'path:/\\/api\\/v[12]/',
      want: ['field:path', 'operator::', 'regex:/\\/api\\/v[12]/'],
    },
    {
      name: 'stats stage with function and by',
      q: '* | stats p95(elapsed) by service span 1m',
      want: [
        'value:*',
        'pipe:|',
        'keyword:stats',
        'function:p95',
        'punct:(',
        'field:elapsed',
        'punct:)',
        'keyword:by',
        'field:service',
        'keyword:span',
        'number:1m',
      ],
    },
    {
      name: 'source clause',
      q: 'source spans | status:error',
      want: ['keyword:source', 'value:spans', 'pipe:|', 'field:status', 'operator::', 'value:error'],
    },
    { name: 'variable', q: 'service:$svc', want: ['field:service', 'operator::', 'variable:$svc'] },
    { name: 'backtick field', q: '`my field`:x', want: ['field:`my field`', 'operator::', 'value:x'] },
    {
      name: 'seq-style operators',
      q: "Service = 'payments' && StatusCode != 200",
      want: [
        'field:Service',
        'operator:=',
        "string:'payments'",
        'operator:&&',
        'field:StatusCode',
        'operator:!=',
        'number:200',
      ],
    },
    {
      name: 'value groups',
      q: 'status_code:(500 or 502)',
      want: [
        'field:status_code',
        'operator::',
        'punct:(',
        'number:500',
        'keyword:or',
        'number:502',
        'punct:)',
      ],
    },
    {
      name: 'sort and limit stages',
      q: 'x | sort count desc | limit 10',
      want: [
        'value:x',
        'pipe:|',
        'keyword:sort',
        'field:count',
        'keyword:desc',
        'pipe:|',
        'keyword:limit',
        'number:10',
      ],
    },
    {
      name: 'unterminated string runs to the end',
      q: 'msg:"open',
      want: ['field:msg', 'operator::', 'string:"open'],
    },
    {
      name: 'escaped quote inside string',
      q: '"a\\"b"',
      want: ['string:"a\\"b"'],
    },
    {
      name: 'slash that is not a regex',
      q: 'path:/api/x',
      want: ['field:path', 'operator::', 'value:/api/x'],
    },
    { name: 'lone dollar', q: '$', want: ['value:$'] },
  ];
  it.each(cases)('$name', ({ q, want }) => {
    expect(kinds(q)).toEqual(want);
  });

  it('classifies a keyword used as a field name as a field', () => {
    const k = tokenize('source:x').find((t) => t.text === 'source')?.kind;
    expect(k).toBe<TokenKind>('field');
  });
});

describe('splitPipes', () => {
  it('splits outside quotes and keeps offsets', () => {
    expect(splitPipes("a:'x|y' | stats count()")).toEqual([
      { text: "a:'x|y' ", from: 0 },
      { text: ' stats count()', from: 9 },
    ]);
  });
  it('does not split on ||', () => {
    expect(splitPipes('a || b')).toHaveLength(1);
  });
});

describe('variablesOf', () => {
  it('returns each variable once', () => {
    expect(variablesOf('service:$svc and env:$env or host:$svc')).toEqual(['svc', 'env']);
  });
});
