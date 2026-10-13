// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  formatBytes,
  formatCount,
  formatDelta,
  formatInteger,
  formatNumber,
  formatRatePerMin,
  formatTick,
  formatUnit,
} from './format';

describe('formatCount', () => {
  it.each([
    [1_840_000_000, '1.84 B'],
    [622_000_000, '622 M'],
    [4_100_000, '4.1 M'],
    [880_000, '880 k'],
    [12_500, '12.5 k'],
    [2_000_000_000_000, '2 T'],
    [812, '812'],
    [-4_100_000, '-4.1 M'],
    [Number.POSITIVE_INFINITY, '—'],
  ])('%d → %s', (n, s) => {
    expect(formatCount(n)).toBe(s);
  });
});

describe('numbers', () => {
  it('groups integers and limits decimals', () => {
    expect(formatInteger(12_771.4)).toBe('12,771');
    expect(formatNumber(1234.5678)).toBe('1,234.57');
    expect(formatNumber(3)).toBe('3');
    expect(formatNumber(null)).toBe('—');
    expect(formatNumber(undefined)).toBe('—');
  });

  it('formats deltas with a sign', () => {
    expect(formatDelta(3)).toBe('+3');
    expect(formatDelta(-24)).toBe('-24');
    expect(formatDelta(0)).toBe('0');
  });

  it('formats ticks compactly', () => {
    expect(formatTick(1200)).toBe('1.2 k');
    expect(formatTick(0.5)).toBe('0.5');
  });
});

describe('formatBytes', () => {
  it.each([
    [0, '0 B'],
    [-5, '0 B'],
    [512, '512 B'],
    [1536, '1.5 KiB'],
    [412 * 1024 ** 3, '412 GiB'],
    [3.9 * 1024 ** 4, '3.9 TiB'],
  ])('%d → %s', (n, s) => {
    expect(formatBytes(n)).toBe(s);
  });
});

describe('formatRatePerMin', () => {
  it('converts per-second rates', () => {
    expect(formatRatePerMin(801.7)).toBe('48.1 k/min');
    expect(formatRatePerMin(0)).toBe('0');
  });
});

describe('formatUnit', () => {
  it.each([
    [12.345, '', '12.35'],
    [12.345, 'none', '12.35'],
    [1500, 'short', '1.5 k'],
    [2048, 'bytes', '2 KiB'],
    [99.5, '%', '99.5%'],
    [99.5, 'percent', '99.5%'],
    [250, 'ms', '250 ms'],
  ])('%d %s → %s', (v, unit, s) => {
    expect(formatUnit(v, unit)).toBe(s);
  });

  it('honours explicit decimals and missing values', () => {
    expect(formatUnit(1.23456, 's', 3)).toBe('1.235 s');
    expect(formatUnit(null)).toBe('—');
    expect(formatUnit(undefined, 'ms')).toBe('—');
  });
});
