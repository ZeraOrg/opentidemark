// SPDX-License-Identifier: Apache-2.0

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  formatAge,
  formatClock,
  formatDateTime,
  formatDuration,
  formatSpan,
  isValidTime,
  pad,
  PRESETS,
  rangeLabel,
  rangeShortLabel,
  resolveTime,
} from './time';

const NOW = Date.parse('2026-10-13T10:00:00Z');

describe('isValidTime / resolveTime', () => {
  it.each([
    ['now', true],
    ['now-15m', true],
    ['now-7d', true],
    ['2026-10-13T08:00:00Z', true],
    ['now-15x', false],
    ['yesterday', false],
    ['2026-13-45T99:00:00Z', false],
  ])('%s valid=%s', (s, ok) => {
    expect(isValidTime(s)).toBe(ok);
  });

  it.each([
    ['now', NOW],
    ['now-30s', NOW - 30_000],
    ['now-15m', NOW - 900_000],
    ['now-2h', NOW - 7_200_000],
    ['now-1d', NOW - 86_400_000],
    ['now-1w', NOW - 604_800_000],
    ['2026-10-13T08:00:00Z', Date.parse('2026-10-13T08:00:00Z')],
    ['garbage', NOW],
  ])('resolves %s', (s, ms) => {
    expect(resolveTime(s, NOW)).toBe(ms);
  });
});

describe('labels', () => {
  it.each([
    [{ from: 'now-15m', to: 'now' }, 'Last 15 minutes', '15m'],
    [{ from: 'now-1h', to: 'now' }, 'Last 1 hour', '1h'],
    [{ from: 'now-24h', to: 'now' }, 'Last 24 hours', '24h'],
    [{ from: 'now-7d', to: 'now' }, 'Last 7 days', '7d'],
  ])('%j', (range, label, short) => {
    expect(rangeLabel(range)).toBe(label);
    expect(rangeShortLabel(range)).toBe(short);
  });

  it('labels absolute ranges', () => {
    const r = { from: '2026-10-13T08:00:00Z', to: 'now' };
    expect(rangeLabel(r)).toMatch(/ – now$/);
    expect(rangeShortLabel(r)).toBe('range');
    expect(rangeLabel({ from: 'bad', to: 'now-5m' })).toBe('bad – now-5m');
  });
});

describe('presets', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(NOW);
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it('are valid ranges', () => {
    for (const p of PRESETS) {
      const r = p.range(new Date(NOW));
      expect(isValidTime(r.from), p.label).toBe(true);
      expect(isValidTime(r.to), p.label).toBe(true);
    }
  });

  it('yesterday ends where today starts', () => {
    const today = PRESETS.find((p) => p.short === 'today')?.range(new Date(NOW));
    const yday = PRESETS.find((p) => p.short === 'yday')?.range(new Date(NOW));
    expect(yday?.to).toBe(today?.from);
    expect(Date.parse(yday?.to ?? '') - Date.parse(yday?.from ?? '')).toBeGreaterThanOrEqual(23 * 3_600_000);
  });
});

describe('formatting', () => {
  it('pads', () => {
    expect(pad(7)).toBe('07');
    expect(pad(7, 3)).toBe('007');
  });

  it('formats clock and date in local time', () => {
    const d = new Date(2026, 9, 13, 9, 5, 7, 42).getTime();
    expect(formatClock(d)).toBe('09:05:07.042');
    expect(formatClock(d, false)).toBe('09:05:07');
    expect(formatDateTime(d)).toBe('2026-10-13 09:05');
  });

  it.each([
    [NOW - 500, 'now'],
    [NOW - 5_000, '5 s ago'],
    [NOW - 14 * 60_000, '14 min ago'],
    [NOW - 5 * 3_600_000, '5 h ago'],
    [NOW - 41 * 86_400_000, '41 d ago'],
    [NOW + 60_000, 'now'],
  ])('age of %d', (ms, s) => {
    expect(formatAge(ms, NOW)).toBe(s);
  });

  it.each([
    [0.25, '0.25 ms'],
    [38, '38 ms'],
    [9_999, '9,999 ms'],
    [12_300, '12.3 s'],
    [240_000, '4 min'],
  ])('duration %d', (ms, s) => {
    expect(formatDuration(ms)).toBe(s);
  });

  it.each([
    [86_400_000, '1d'],
    [7_200_000, '2h'],
    [300_000, '5m'],
    [30_000, '30s'],
  ])('span %d', (ms, s) => {
    expect(formatSpan(ms)).toBe(s);
  });
});
