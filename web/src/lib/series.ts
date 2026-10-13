// SPDX-License-Identifier: Apache-2.0
//
// Shapes POST /query results (SPEC §11.3: table / timeseries) into what charts, stat tiles and
// result tables need. Pure functions, unit tested.

import type { Column, QueryResponse } from '../api/types';

export interface ChartSeries {
  label: string;
  values: (number | null)[];
}

/** Column-aligned chart data: `x` in epoch milliseconds, one value per x per series. */
export interface ChartData {
  x: number[];
  series: ChartSeries[];
}

export const BUCKET_COLUMN = '@bucket';

function toNumber(v: unknown): number | null {
  if (typeof v === 'number') return Number.isFinite(v) ? v : null;
  if (typeof v === 'string' && v.trim() !== '') {
    const n = Number(v);
    return Number.isFinite(n) ? n : null;
  }
  return null;
}

/** Label of a series key ({service: "a", route: "/x"} → "a · /x"). */
export function keyLabel(key: Record<string, unknown>): string {
  const parts = Object.values(key).map((v) => (v === null || v === undefined || v === '' ? '∅' : String(v)));
  return parts.join(' · ');
}

/** Aggregate (measure) columns of a timeseries result: everything but @bucket and by-fields. */
export function measureColumns(resp: QueryResponse): Column[] {
  const keyFields = new Set(Object.keys(resp.series?.[0]?.key ?? {}));
  return (resp.columns ?? []).filter((c) => c.name !== BUCKET_COLUMN && !keyFields.has(c.name));
}

/** Converts a timeseries result to aligned chart data (missing points are null). */
export function timeseriesToChart(resp: QueryResponse): ChartData {
  const measures = measureColumns(resp);
  const series = resp.series ?? [];
  const xs = new Set<number>();
  for (const s of series) {
    for (const p of s.points) {
      const t = p[0];
      if (typeof t === 'number') xs.add(t);
    }
  }
  const x = [...xs].sort((a, b) => a - b);
  const index = new Map(x.map((t, i) => [t, i]));
  const out: ChartSeries[] = [];
  const multi = measures.length > 1;
  for (const s of series) {
    const base = keyLabel(s.key);
    measures.forEach((m, j) => {
      const values: (number | null)[] = new Array<number | null>(x.length).fill(null);
      for (const p of s.points) {
        const i = index.get(p[0] ?? Number.NaN);
        if (i !== undefined) values[i] = toNumber(p[j + 1]);
      }
      const label = base === '' ? m.name : multi ? `${base} · ${m.name}` : base;
      out.push({ label, values });
    });
  }
  return { x, series: out };
}

/** Turns per-series values into cumulative sums (stacked charts). */
export function stack(series: readonly ChartSeries[]): ChartSeries[] {
  const out: ChartSeries[] = [];
  let acc: (number | null)[] = [];
  for (const s of series) {
    const values = s.values.map((v, i) => {
      const prev = acc[i] ?? 0;
      return v === null ? prev : prev + v;
    });
    out.push({ label: s.label, values });
    acc = values;
  }
  return out;
}

export interface ResultRow {
  bucket: number;
  key: Record<string, unknown>;
  series: number;
  values: (number | null)[];
  delta: (number | null)[];
}

/** Rows of a timeseries result, newest bucket first, with the change vs the previous bucket. */
export function timeseriesRows(resp: QueryResponse): ResultRow[] {
  const measures = measureColumns(resp);
  const rows: ResultRow[] = [];
  (resp.series ?? []).forEach((s, si) => {
    const sorted = [...s.points].sort((a, b) => (a[0] ?? 0) - (b[0] ?? 0));
    let prev: (number | null)[] | null = null;
    for (const p of sorted) {
      const values = measures.map((_, j) => toNumber(p[j + 1]));
      const before = prev;
      const delta = values.map((v, j) => {
        const pv = before?.[j];
        return v === null || pv === null || pv === undefined ? null : v - pv;
      });
      rows.push({ bucket: p[0] ?? 0, key: s.key, series: si, values, delta });
      prev = values;
    }
  });
  return rows.sort((a, b) => b.bucket - a.bucket || a.series - b.series);
}

export interface Category {
  label: string;
  value: number;
}

/** Categories of a table result: label = non-numeric columns, value = first numeric column. */
export function tableCategories(resp: QueryResponse): Category[] {
  const cols = resp.columns ?? [];
  const valueIdx = cols.findIndex((c) => c.type === 'number' || c.type === 'duration_ms');
  if (valueIdx < 0) return [];
  const labelIdx = cols.map((_, i) => i).filter((i) => i !== valueIdx && cols[i]?.type !== 'number');
  return (resp.rows ?? []).map((r) => ({
    label: labelIdx.map((i) => (r[i] === null || r[i] === undefined ? '∅' : String(r[i]))).join(' · '),
    value: toNumber(r[valueIdx]) ?? 0,
  }));
}

export interface StatValue {
  value: number | null;
  previous: number | null;
  spark: number[];
}

/**
 * Single value for stat/gauge panels: a table's first numeric cell, or for a timeseries the sum
 * of the first series' last bucket (previous = the bucket before) with the series as sparkline.
 */
export function statValue(resp: QueryResponse): StatValue {
  if (resp.kind === 'timeseries') {
    const data = timeseriesToChart(resp);
    const totals = data.x.map((_, i) =>
      data.series.reduce<number | null>((acc, s) => {
        const v = s.values[i];
        return v === null || v === undefined ? acc : (acc ?? 0) + v;
      }, null),
    );
    const spark = totals.filter((v): v is number => v !== null);
    return {
      value: spark.length > 0 ? (spark[spark.length - 1] ?? null) : null,
      previous: spark.length > 1 ? (spark[spark.length - 2] ?? null) : null,
      spark,
    };
  }
  const cats = tableCategories(resp);
  const first = cats[0];
  return { value: first === undefined ? null : first.value, previous: null, spark: [] };
}

/** Text of a table cell. */
export function cellText(v: unknown): string {
  if (v === null || v === undefined) return '';
  if (typeof v === 'object') return JSON.stringify(v);
  return String(v);
}
