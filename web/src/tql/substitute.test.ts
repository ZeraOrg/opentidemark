// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { quoteValue, substitutePreview } from './substitute';

describe('quoteValue', () => {
  it('escapes backslashes and quotes', () => {
    expect(quoteValue('a"b\\c')).toBe('"a\\"b\\\\c"');
  });
});

describe('substitutePreview', () => {
  it.each([
    ['single string', { svc: ['checkout-api'] }, 'service:"checkout-api" | stats count()'],
    ['single number', { svc: ['42'] }, 'service:42 | stats count()'],
    ['several values', { svc: ['a', '7'] }, 'service:("a" or 7) | stats count()'],
    ['all', { svc: ['*'] }, 'service:* | stats count()'],
    ['empty list', { svc: [] }, 'service:* | stats count()'],
    ['unknown variable left alone', { other: ['x'] }, 'service:$svc | stats count()'],
  ])('%s', (_name, vars, want) => {
    expect(substitutePreview('service:$svc | stats count()', vars)).toBe(want);
  });

  it('never inserts raw TQL', () => {
    expect(substitutePreview('a:$v', { v: ['x or level:*'] })).toBe('a:"x or level:*"');
  });
});
