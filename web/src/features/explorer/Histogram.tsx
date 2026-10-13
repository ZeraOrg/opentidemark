// SPDX-License-Identifier: Apache-2.0
//
// Volume histogram (DESIGN §5.12): bars stacked by level in the level colours, legend, hover
// tooltip, and a brush whose release zooms the global time range.

import { useMemo, useRef, useState, type PointerEvent } from 'react';
import type { HistogramResponse } from '../../api/types';
import { cx } from '../../lib/cx';
import { formatInteger } from '../../lib/format';
import { formatClock, formatSpan } from '../../lib/time';
import s from './Histogram.module.css';

/** Bottom-to-top stacking order of the known count keys. */
export const STACK_ORDER = [
  'Verbose',
  'Debug',
  'Information',
  'Warning',
  'Error',
  'Fatal',
  'unset',
  'ok',
  'error',
  'count',
];

const KEY_CLASS: Record<string, string> = {
  Verbose: 'verbose',
  Debug: 'debug',
  Information: 'information',
  Warning: 'warning',
  Error: 'error',
  Fatal: 'fatal',
  unset: 'unset',
  ok: 'ok',
  error: 'spanError',
  count: 'count',
};

export interface Bucket {
  t: number;
  total: number;
  parts: { key: string; count: number }[];
}

/** Normalizes the API buckets: time in ms, stacked parts in STACK_ORDER, totals. */
export function toBuckets(h: HistogramResponse): Bucket[] {
  return h.buckets.map((b) => {
    const keys = Object.keys(b.counts).sort((x, y) => {
      const a = STACK_ORDER.indexOf(x);
      const c = STACK_ORDER.indexOf(y);
      return (a < 0 ? 99 : a) - (c < 0 ? 99 : c);
    });
    const parts = keys.map((key) => ({ key, count: b.counts[key] ?? 0 })).filter((p) => p.count > 0);
    return { t: Date.parse(b.t), total: parts.reduce((n, p) => n + p.count, 0), parts };
  });
}

/** Total of all buckets. */
export function histogramTotal(h: HistogramResponse | undefined): number {
  if (h === undefined) return 0;
  return h.buckets.reduce((n, b) => n + Object.values(b.counts).reduce((m, c) => m + c, 0), 0);
}

interface Brush {
  a: number;
  b: number;
}

/** Header (20 px + 8 px gap) and time axis heights inside the total height (DESIGN §5.12). */
const HEADER_PX = 28;
const AXIS_PX = 20;
const MIN_BARS_PX = 24;

export function Histogram({
  data,
  height = 120,
  onZoom,
  compact,
}: {
  data: HistogramResponse;
  height?: number;
  onZoom: (fromMs: number, toMs: number) => void;
  compact?: boolean;
}) {
  const buckets = useMemo(() => toBuckets(data), [data]);
  const area = useRef<HTMLDivElement>(null);
  const [brush, setBrush] = useState<Brush | null>(null);
  const [hover, setHover] = useState<number | null>(null);
  const dragging = useRef(false);
  const max = Math.max(1, ...buckets.map((b) => b.total));
  const n = Math.max(1, buckets.length);
  const start = buckets[0]?.t ?? 0;
  const end = (buckets[buckets.length - 1]?.t ?? 0) + data.span;
  const keys = useMemo(() => {
    const present = new Set<string>();
    for (const b of buckets) for (const p of b.parts) present.add(p.key);
    return STACK_ORDER.filter((k) => present.has(k));
  }, [buckets]);
  const total = buckets.reduce((acc, b) => acc + b.total, 0);

  const fraction = (e: PointerEvent): number => {
    const r = area.current?.getBoundingClientRect();
    if (r === undefined || r.width === 0) return 0;
    return Math.min(1, Math.max(0, (e.clientX - r.left) / r.width));
  };
  const timeAt = (f: number): number => start + f * (end - start);

  const onDown = (e: PointerEvent): void => {
    if (e.button !== 0) return;
    dragging.current = true;
    (e.target as Element).setPointerCapture?.(e.pointerId);
    const f = fraction(e);
    setBrush({ a: f, b: f });
  };
  const onMove = (e: PointerEvent): void => {
    const f = fraction(e);
    setHover(Math.min(n - 1, Math.floor(f * n)));
    if (dragging.current) setBrush((b) => (b === null ? null : { a: b.a, b: f }));
  };
  const onUp = (): void => {
    dragging.current = false;
    if (brush !== null && Math.abs(brush.b - brush.a) > 0.005) {
      const lo = Math.min(brush.a, brush.b);
      const hi = Math.max(brush.a, brush.b);
      onZoom(Math.round(timeAt(lo)), Math.round(timeAt(hi)));
    }
    setBrush(null);
  };

  const ticks = compact === true ? [] : [0, 0.25, 0.5, 0.75, 1];
  // `height` is the whole histogram; compact (phone) mode has neither header nor axis.
  const barsHeight = Math.max(MIN_BARS_PX, compact === true ? height : height - HEADER_PX - AXIS_PX);
  const hovered = hover === null ? undefined : buckets[hover];
  const brushLo = brush === null ? 0 : Math.min(brush.a, brush.b);
  const brushHi = brush === null ? 0 : Math.max(brush.a, brush.b);
  const peak = Math.max(0, ...buckets.map((b) => b.total));
  return (
    <div className={cx(s.histogram, compact === true && s.compact)}>
      {compact === true ? null : (
        <div className={s.head}>
          <span className={s.caps}>{formatSpan(data.span)} buckets</span>
          <ul className={s.legend}>
            {keys.map((k) => (
              <li key={k}>
                <span className={cx(s.square, s[KEY_CLASS[k] ?? 'count'])} aria-hidden="true" />
                {k.toLowerCase()}
              </li>
            ))}
          </ul>
          {brush !== null && brushHi - brushLo > 0.005 ? (
            <span className={s.readout}>
              Brushed {formatClock(timeAt(brushLo), false)} – {formatClock(timeAt(brushHi), false)} · release
              to zoom
            </span>
          ) : null}
        </div>
      )}
      <div
        ref={area}
        className={s.area}
        style={{ height: barsHeight }}
        role="img"
        aria-label={`Event volume: ${formatInteger(total)} events in ${buckets.length} buckets of ${formatSpan(data.span)}, peak ${formatInteger(peak)}`}
        onPointerDown={onDown}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerLeave={() => {
          if (!dragging.current) setHover(null);
        }}
      >
        {buckets.map((b, i) => (
          <div
            key={b.t}
            className={cx(s.col, hover === i && s.hoverCol)}
            style={{ left: `${(i / n) * 100}%`, width: `${100 / n}%` }}
          >
            <div className={s.stack} style={{ height: `${(b.total / max) * 100}%` }}>
              {b.parts.map((p) => (
                <span
                  key={p.key}
                  className={cx(s.seg, s[KEY_CLASS[p.key] ?? 'count'])}
                  style={{ height: `${(p.count / b.total) * 100}%` }}
                />
              ))}
            </div>
          </div>
        ))}
        {brush !== null ? (
          <div
            className={s.brush}
            style={{ left: `${brushLo * 100}%`, width: `${(brushHi - brushLo) * 100}%` }}
          />
        ) : null}
        {hovered !== undefined && brush === null && compact !== true ? (
          <div
            className={s.tooltip}
            style={{ left: `${Math.min(80, ((hover ?? 0) / n) * 100)}%` }}
            role="status"
          >
            <div className={s.tipTime}>
              {formatClock(hovered.t, false)} – {formatClock(hovered.t + data.span, false)}
            </div>
            {hovered.parts.length === 0 ? <div className={s.tipRow}>no events</div> : null}
            {[...hovered.parts].reverse().map((p) => (
              <div key={p.key} className={s.tipRow}>
                <span className={cx(s.square, s[KEY_CLASS[p.key] ?? 'count'])} aria-hidden="true" />
                <span>{p.key}</span>
                <span className={s.tipCount}>{formatInteger(p.count)}</span>
              </div>
            ))}
          </div>
        ) : null}
      </div>
      {ticks.length > 0 ? (
        <div className={s.axis} aria-hidden="true">
          {ticks.map((f) => (
            <span
              key={f}
              style={{ left: `${f * 100}%` }}
              className={cx(f === 0 && s.first, f === 1 && s.last)}
            >
              {formatClock(timeAt(f), false)}
            </span>
          ))}
        </div>
      ) : null}
    </div>
  );
}
