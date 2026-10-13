// SPDX-License-Identifier: Apache-2.0
//
// Time-series chart on uPlot (SPEC §5, DESIGN §2.4, §9.3): series colours from the chart tokens
// (re-created on theme change), line / area / bars, stacking, dashed threshold lines, an
// optional shaded "condition holds" region (alert preview), crosshair tooltip and a legend.
// Also the SVG sparkline used by stat tiles.

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import uPlot from 'uplot';
import 'uplot/dist/uPlot.min.css';
import type { ConditionOp } from '../api/types';
import { cssVar, useTheme } from '../app/theme';
import { usePalette } from '../app/services';
import { cx } from '../lib/cx';
import { formatTick, formatUnit } from '../lib/format';
import { stack, type ChartData } from '../lib/series';
import { formatClock, formatDateTime, pad } from '../lib/time';
import s from './Chart.module.css';

export type ChartStyle = 'line' | 'area' | 'bars';

export interface ChartThreshold {
  value: number;
  /** Token name: accent, success, warning, danger, info or chart1…chart8. */
  color: string;
  label?: string;
}

export interface ChartProps {
  data: ChartData;
  height: number;
  ariaLabel: string;
  style?: ChartStyle;
  stacked?: boolean;
  thresholds?: readonly ChartThreshold[];
  shade?: { op: ConditionOp; value: number } | null;
  unit?: string;
  decimals?: number;
  legend?: 'bottom' | 'right' | 'hidden';
  /** Explicit colour index (1–8) per series; default: the session palette by label. */
  colors?: readonly number[];
  className?: string;
}

const DAY_MS = 86_400_000;

/** CSS variable of a threshold/series colour token name. */
export function colorVar(token: string): string {
  const m = /^chart([1-8])$/.exec(token);
  if (m) return `--tm-chart-${m[1] ?? '1'}`;
  if (['accent', 'success', 'warning', 'danger', 'info'].includes(token)) return `--tm-color-${token}`;
  return '--tm-chart-threshold';
}

/** Evaluates an alert condition. */
export function conditionHolds(v: number, op: ConditionOp, threshold: number): boolean {
  switch (op) {
    case '>':
      return v > threshold;
    case '>=':
      return v >= threshold;
    case '<':
      return v < threshold;
    case '<=':
      return v <= threshold;
    case '==':
      return v === threshold;
    case '!=':
      return v !== threshold;
  }
}

function axisTime(ms: number, spanMs: number): string {
  const d = new Date(ms);
  if (spanMs > 2 * DAY_MS) return `${pad(d.getDate())}.${pad(d.getMonth() + 1)} ${pad(d.getHours())}:00`;
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

interface Hover {
  idx: number;
  left: number;
  top: number;
}

interface Built {
  opts: uPlot.Options;
  data: uPlot.AlignedData;
}

function buildPlot(
  p: ChartProps,
  width: number,
  colorIdx: readonly number[],
  onHover: (h: Hover | null) => void,
): Built {
  const px = uPlot.pxRatio;
  const mono = cssVar('--tm-font-mono') || 'monospace';
  const axisColor = cssVar('--tm-chart-axis');
  const gridColor = cssVar('--tm-chart-grid');
  const style = p.style ?? 'line';
  const raw = p.data.series;
  const drawn = p.stacked === true ? stack(raw) : raw.map((x) => ({ ...x }));
  const order = drawn.map((_, i) => i);
  if (p.stacked === true) order.reverse();
  const colors = colorIdx.map((i) => cssVar(`--tm-chart-${i}`));
  const xs = p.data.x.map((t) => t / 1000);
  const spanMs = (p.data.x[p.data.x.length - 1] ?? 0) - (p.data.x[0] ?? 0);
  const bars = uPlot.paths.bars?.({ size: [0.72, 48], align: 0 });
  const thresholds = p.thresholds ?? [];
  const series: uPlot.Series[] = [{}];
  for (const i of order) {
    const c = colors[i] ?? axisColor;
    const fill = style === 'area' || p.stacked === true || style === 'bars';
    series.push({
      label: drawn[i]?.label ?? '',
      stroke: c,
      width: style === 'bars' ? 0 : 1.5,
      fill: fill ? (style === 'bars' ? c : `${c}${p.stacked === true ? '99' : '33'}`) : undefined,
      paths: style === 'bars' ? bars : undefined,
      points: { show: false },
      spanGaps: false,
    });
  }
  const data: uPlot.AlignedData = [xs, ...order.map((i) => drawn[i]?.values ?? [])];
  const opts: uPlot.Options = {
    width,
    height: p.height,
    legend: { show: false },
    cursor: {
      x: true,
      y: false,
      points: { show: false },
      drag: { x: false, y: false },
    },
    padding: [8, 8, 0, 0],
    scales: {
      x: { time: true },
      y: {
        range: (_u, min, max) => {
          const hi = Math.max(max, ...thresholds.map((t) => t.value), p.shade?.value ?? -Infinity);
          const lo = Math.min(0, min);
          return [lo, hi <= lo ? lo + 1 : hi * 1.1];
        },
      },
    },
    axes: [
      {
        stroke: axisColor,
        font: `${10}px ${mono}`,
        grid: { stroke: gridColor, width: 1 },
        ticks: { show: false },
        values: (_u, splits) => splits.map((v) => axisTime(v * 1000, spanMs)),
      },
      {
        stroke: axisColor,
        font: `${10}px ${mono}`,
        grid: { stroke: gridColor, width: 1 },
        ticks: { show: false },
        size: 44,
        values: (_u, splits) => splits.map((v) => formatTick(v)),
      },
    ],
    series,
    hooks: {
      draw: [
        (u) => {
          const ctx = u.ctx;
          const { left, top, width: w, height: h } = u.bbox;
          const shade = p.shade;
          if (shade && raw.length > 0) {
            ctx.save();
            ctx.fillStyle = `${cssVar('--tm-color-danger')}1f`;
            xs.forEach((_, i) => {
              // A bucket is shaded when any series (alert group) meets the condition.
              const breached = raw.some((sr) => {
                const v = sr.values[i];
                return v !== null && v !== undefined && conditionHolds(v, shade.op, shade.value);
              });
              if (!breached) return;
              const x0 = u.valToPos(xs[i] ?? 0, 'x', true);
              const x1 = u.valToPos(xs[i + 1] ?? (xs[i] ?? 0) + (xs[1] ?? 0) - (xs[0] ?? 0), 'x', true);
              ctx.fillRect(x0, top, Math.max(px, x1 - x0), h);
            });
            ctx.restore();
          }
          for (const t of thresholds) {
            const y = u.valToPos(t.value, 'y', true);
            if (y < top - 1 || y > top + h + 1) continue;
            const c = cssVar(colorVar(t.color));
            ctx.save();
            ctx.strokeStyle = c;
            ctx.lineWidth = px;
            ctx.setLineDash([4 * px, 4 * px]);
            ctx.beginPath();
            ctx.moveTo(left, y);
            ctx.lineTo(left + w, y);
            ctx.stroke();
            ctx.fillStyle = c;
            ctx.font = `${10 * px}px ${mono}`;
            // uPlot leaves the alignment of its last axis label on the context.
            ctx.textAlign = 'left';
            ctx.textBaseline = 'alphabetic';
            ctx.fillText(t.label ?? `threshold ${formatTick(t.value)}`, left + 6 * px, y - 4 * px);
            ctx.restore();
          }
        },
      ],
      setCursor: [
        (u) => {
          const idx = u.cursor.idx;
          if (idx === null || idx === undefined || u.cursor.left === undefined || u.cursor.left < 0) {
            onHover(null);
            return;
          }
          onHover({
            idx,
            left: u.over.offsetLeft + (u.cursor.left ?? 0),
            top: u.over.offsetTop + Math.max(0, (u.cursor.top ?? 0) - 20),
          });
        },
      ],
    },
  };
  return { opts, data };
}

export function Legend({
  items,
  className,
}: {
  items: readonly { label: string; color: number }[];
  className?: string;
}) {
  return (
    <ul className={cx(s.legend, className)} aria-label="Legend">
      {items.map((it) => (
        <li key={it.label}>
          <span
            className={s.swatch}
            style={{ background: `var(--tm-chart-${it.color})` }}
            aria-hidden="true"
          />
          {it.label}
        </li>
      ))}
    </ul>
  );
}

export function Chart(props: ChartProps) {
  const { data, height, ariaLabel, legend = 'bottom', colors, className, unit, decimals } = props;
  const { theme } = useTheme();
  const palette = usePalette();
  const box = useRef<HTMLDivElement>(null);
  const plot = useRef<uPlot | null>(null);
  const latest = useRef(props);
  latest.current = props;
  const [hover, setHover] = useState<Hover | null>(null);
  const colorIdx = useMemo(
    () => data.series.map((x, i) => colors?.[i] ?? palette.index(x.label)),
    [data, colors, palette],
  );
  const thresholdsKey = JSON.stringify(props.thresholds ?? []);
  const shadeKey = JSON.stringify(props.shade ?? null);

  useEffect(() => {
    const el = box.current;
    if (el === null) return undefined;
    const built = buildPlot(latest.current, el.clientWidth || 300, colorIdx, setHover);
    const u = new uPlot(built.opts, built.data, el);
    plot.current = u;
    return () => {
      u.destroy();
      plot.current = null;
      setHover(null);
    };
  }, [data, height, theme, colorIdx, thresholdsKey, shadeKey, props.style, props.stacked]);

  useLayoutEffect(() => {
    const el = box.current;
    if (el === null || typeof ResizeObserver === 'undefined') return undefined;
    const ro = new ResizeObserver(() => {
      const w = el.clientWidth;
      if (w > 0) plot.current?.setSize({ width: w, height: latest.current.height });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const spanMs = (data.x[data.x.length - 1] ?? 0) - (data.x[0] ?? 0);
  const hoverTime = hover === null ? undefined : data.x[hover.idx];
  const items = data.series.map((x, i) => ({ label: x.label, color: colorIdx[i] ?? 1 }));
  return (
    <div className={cx(s.chart, legend === 'right' && s.right, className)}>
      <div className={s.plotWrap}>
        <div
          ref={box}
          className={s.plot}
          role="img"
          aria-label={`${ariaLabel}: ${data.series.length} series, ${data.x.length} points`}
          onMouseLeave={() => setHover(null)}
        />
        {hover !== null && hoverTime !== undefined ? (
          <div className={s.tooltip} style={{ left: hover.left + 14, top: hover.top }} role="status">
            <div className={s.tipTime}>
              {spanMs > 2 * DAY_MS ? formatDateTime(hoverTime) : formatClock(hoverTime, false)}
            </div>
            {data.series.slice(0, 12).map((x, i) => (
              <div key={x.label} className={s.tipRow}>
                <span
                  className={s.swatchDot}
                  style={{ background: `var(--tm-chart-${colorIdx[i] ?? 1})` }}
                  aria-hidden="true"
                />
                <span className={s.tipLabel}>{x.label}</span>
                <span className={s.tipValue}>{formatUnit(x.values[hover.idx], unit, decimals)}</span>
              </div>
            ))}
          </div>
        ) : null}
      </div>
      {legend !== 'hidden' && items.length > 0 ? <Legend items={items} className={s.legendBlock} /> : null}
    </div>
  );
}

/** 120 × 32 sparkline (stat tiles, DESIGN §5.19). */
export function Sparkline({
  values,
  color,
  width = 120,
  height = 32,
}: {
  values: readonly number[];
  color: string;
  width?: number;
  height?: number;
}) {
  if (values.length < 2) return <svg width={width} height={height} aria-hidden="true" />;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const pts = values
    .map((v, i) => {
      const x = (i / (values.length - 1)) * (width - 2) + 1;
      const y = height - 2 - ((v - min) / span) * (height - 4);
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
      <polyline points={pts} fill="none" style={{ stroke: `var(${colorVar(color)})` }} strokeWidth={1.5} />
    </svg>
  );
}
