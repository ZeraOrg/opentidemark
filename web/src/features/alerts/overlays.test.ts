// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { AlertOverlay } from '../../api/types';
import { bandPointAt, overlayBreached, type ChartBand } from '../../components/Chart';
import { bandLabel, chartOverlays, chartRange } from './overlays';

const W = 300_000;
const dev = { type: 'deviation', direction: 'below', sigma: 3, baseline: '24h0m0s', minDelta: 0 } as const;

describe('alert rules on charts', () => {
  it('labels band groups like chart series', () => {
    expect(bandLabel(['checkout-api'])).toBe('checkout-api');
    expect(bandLabel(['a', ''])).toBe('a · ∅');
  });

  it('maps overlays to chart overlays and matches bands to series', () => {
    const list: AlertOverlay[] = [
      { ruleId: 'r1', name: 'errors', condition: { op: '>', threshold: 25 }, windowMs: W, bands: null },
      {
        ruleId: 'r2',
        name: 'volume',
        condition: dev,
        windowMs: W,
        bands: [
          { labels: { service: 'b' }, key: ['b'], points: [[0, 5, 10, null]] },
          { labels: { service: 'gone' }, key: ['gone'], points: [[0, 5, 10, null]] },
        ],
      },
      { name: 'draft', draft: true, condition: dev, windowMs: W, bands: [], error: 'range too long' },
    ];
    const got = chartOverlays(list, ['a', 'b']);
    expect(got[0]).toEqual({ kind: 'threshold', name: 'errors', text: '> 25', op: '>', value: 25 });
    expect(got[1]).toEqual({
      kind: 'band',
      name: 'volume',
      text: '< 3σ vs 24h',
      bands: [{ series: 1, windowMs: W, points: [[0, 5, 10, null]] }],
    });
    expect(got[2]).toMatchObject({ kind: 'band', name: 'this rule', error: 'range too long', bands: [] });
    // An ungrouped band belongs to the only series.
    const single = chartOverlays(
      [{ name: 'n', condition: dev, windowMs: W, bands: [{ labels: {}, key: [], points: [] }] }],
      ['count'],
    );
    expect(single[0]).toMatchObject({ bands: [{ series: 0 }] });
  });

  it('finds the band point of a time and the breaches', () => {
    const band: ChartBand = {
      series: 0,
      windowMs: W,
      points: [
        [0, 5, 10, 15],
        [W, null, 20, 30],
      ],
    };
    expect(bandPointAt(band, W - 1)?.[2]).toBe(10);
    expect(bandPointAt(band, W)?.[2]).toBe(20);
    expect(bandPointAt(band, 2 * W)).toBeUndefined();
    const o = { kind: 'band', name: 'v', text: '', bands: [band] } as const;
    expect(overlayBreached(o, 0, 0, 4)).toBe(true);
    expect(overlayBreached(o, 0, 0, 16)).toBe(true);
    expect(overlayBreached(o, 0, 0, 10)).toBe(false);
    expect(overlayBreached(o, 0, W, 1)).toBe(false); // no lower bound in that window
    expect(overlayBreached(o, 1, 0, 4)).toBe(false); // another series
    expect(overlayBreached({ kind: 'threshold', name: 't', text: '', op: '>', value: 3 }, 2, 0, 4)).toBe(
      true,
    );
  });

  it('covers the chart from its first bucket to the end of the last', () => {
    expect(chartRange({ x: [0, 60_000], series: [] }, 60_000)).toEqual({
      from: '1970-01-01T00:00:00.000Z',
      to: '1970-01-01T00:02:00.000Z',
    });
    expect(chartRange({ x: [], series: [] }, 60_000)).toBeNull();
  });
});
