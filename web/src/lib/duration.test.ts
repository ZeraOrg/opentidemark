// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { formatGoDuration, parseGoDuration, shortDuration } from './duration';

describe('parseGoDuration', () => {
  it.each([
    ['5m0s', 300_000],
    ['1h30m', 5_400_000],
    ['250ms', 250],
    ['1.5s', 1_500],
    ['10us', 0.01],
    ['0', 0],
    ['0s', 0],
  ])('%s', (s, ms) => {
    expect(parseGoDuration(s)).toBeCloseTo(ms);
  });

  it.each(['', '5', 'm5', '5x', '5m junk', ' 5m 3s'])('rejects %j', (s) => {
    expect(parseGoDuration(s)).toBeNull();
  });
});

describe('formatGoDuration', () => {
  it.each([
    [300_000, '5m'],
    [5_400_000, '1h30m'],
    [3_723_250, '1h2m3s250ms'],
    [0, '0s'],
    [-1, '0s'],
    [Number.NaN, '0s'],
  ])('%d → %s', (ms, s) => {
    expect(formatGoDuration(ms)).toBe(s);
  });
});

describe('shortDuration', () => {
  it('shortens canonical durations and keeps unparsable input', () => {
    expect(shortDuration('5m0s')).toBe('5m');
    expect(shortDuration('soon')).toBe('soon');
  });
});
