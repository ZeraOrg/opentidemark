// SPDX-License-Identifier: Apache-2.0
//
// Panel builder model ↔ TQL (SPEC §13.2). Builder → Query is lossless; Query → Builder succeeds
// only for queries of the builder shape `[source s |] filter | stats agg(f)… [by g…] [span x]`.
// The histogram aggregates (SPEC §10.5) take no field: `hist_mean()` has no argument and
// `histogram_quantile(q)` a quantile `0 < q < 1`, which the model keeps in `Measure.field`.

import type { PanelType, Source } from '../api/types';
import { splitPipes, tokenize } from './tokenize';

export const MEASURE_FUNCTIONS = [
  'count',
  'count_distinct',
  'sum',
  'avg',
  'min',
  'max',
  'p50',
  'p75',
  'p90',
  'p95',
  'p99',
  'rate',
  'hist_mean',
  'histogram_quantile',
] as const;
export type MeasureFn = (typeof MEASURE_FUNCTIONS)[number];

/** Aggregates over histogram buckets: metrics only, and only combined with each other. */
export const HISTOGRAM_FUNCTIONS: ReadonlySet<MeasureFn> = new Set<MeasureFn>([
  'hist_mean',
  'histogram_quantile',
]);
export const DEFAULT_QUANTILE = '0.95';

export const INTERVALS = ['auto', '1m', '5m', '15m', '1h'] as const;
export type Interval = (typeof INTERVALS)[number];

export const MAX_MEASURES = 3;
export const MAX_GROUP_BY = 3;

export interface Measure {
  fn: MeasureFn;
  /** The field, or for `histogram_quantile` the quantile as written (`0.95`). */
  field: string;
}

export interface BuilderModel {
  source: Source;
  filter: string;
  measures: Measure[];
  groupBy: string[];
  interval: Interval;
  viz: PanelType;
}

/** Panel types whose query is a time series (`span` present). */
export const TIME_PANELS: ReadonlySet<PanelType> = new Set<PanelType>(['timeseries']);

export function emptyModel(): BuilderModel {
  return {
    source: 'logs',
    filter: '',
    measures: [{ fn: 'count', field: '' }],
    groupBy: [],
    interval: 'auto',
    viz: 'timeseries',
  };
}

export function isHistogramFn(fn: MeasureFn): boolean {
  return HISTOGRAM_FUNCTIONS.has(fn);
}

const QUANTILE = /^\d+(\.\d+)?$/;

/** Whether `s` is a number literal strictly between 0 and 1 (SPEC §10.5). */
export function isQuantile(s: string): boolean {
  const t = s.trim();
  if (!QUANTILE.test(t)) return false;
  const n = Number(t);
  return n > 0 && n < 1;
}

/** The measure after changing its function: histogram measures drop the field, a quantile gets a default. */
export function withFn(m: Measure, fn: MeasureFn): Measure {
  if (fn === 'histogram_quantile') return { fn, field: isQuantile(m.field) ? m.field : DEFAULT_QUANTILE };
  if (fn === 'hist_mean') return { fn, field: '' };
  return { fn, field: isHistogramFn(m.fn) ? '' : m.field };
}

/** The measure the "+ measure" button adds: one that can share the stage with the others. */
export function nextMeasure(measures: readonly Measure[]): Measure {
  const first = measures[0];
  if (first !== undefined && isHistogramFn(first.fn)) return { fn: 'hist_mean', field: '' };
  return { fn: 'count', field: '' };
}

/**
 * Why the measures cannot be queried as they are, or null: histogram aggregates need source
 * metrics, a quantile between 0 and 1, and cannot share a stats stage with other measures.
 */
export function builderProblem(m: BuilderModel): string | null {
  const hist = m.measures.filter((x) => isHistogramFn(x.fn));
  if (hist.length === 0) return null;
  if (hist.length !== m.measures.length) {
    return 'hist_mean() and histogram_quantile() can only be combined with each other, not with other measures.';
  }
  if (m.source !== 'metrics') return 'hist_mean() and histogram_quantile() need the source metrics.';
  if (hist.some((x) => x.fn === 'histogram_quantile' && !isQuantile(x.field))) {
    return 'histogram_quantile() needs a quantile between 0 and 1, for example 0.95.';
  }
  return null;
}

function measureText(m: Measure): string {
  if (m.fn === 'hist_mean') return 'hist_mean()';
  return `${m.fn}(${m.field.trim()})`;
}

/** Generates TQL for the model. */
export function toTql(m: BuilderModel): string {
  const parts: string[] = [];
  if (m.source !== 'logs') parts.push(`source ${m.source}`);
  parts.push(m.filter.trim() === '' ? '*' : m.filter.trim());
  const measures = (m.measures.length === 0 ? [{ fn: 'count', field: '' } as Measure] : m.measures).map(
    measureText,
  );
  let stats = `stats ${measures.join(', ')}`;
  const groups = m.groupBy.map((g) => g.trim()).filter((g) => g !== '');
  if (groups.length > 0) stats += ` by ${groups.join(', ')}`;
  if (TIME_PANELS.has(m.viz)) stats += ` span ${m.interval}`;
  parts.push(stats);
  return parts.join(' | ');
}

const IDENT = /^(`[^`]+`|[A-Za-z_@][A-Za-z0-9_.\-@]*)$/;
const AGG = /^([a-z_0-9]+)\(\s*([^()]*?)\s*\)$/i;

function isMeasureFn(s: string): s is MeasureFn {
  return (MEASURE_FUNCTIONS as readonly string[]).includes(s);
}

function isInterval(s: string): s is Interval {
  return (INTERVALS as readonly string[]).includes(s);
}

/** Whether `arg` is a valid argument of `fn` as the builder writes it. */
function argumentFits(fn: MeasureFn, arg: string): boolean {
  if (fn === 'hist_mean') return arg === '';
  if (fn === 'histogram_quantile') return isQuantile(arg);
  return arg === '' || IDENT.test(arg);
}

/** Splits on top-level commas (outside parentheses and quotes). */
function splitCommas(s: string): string[] {
  const out: string[] = [];
  let depth = 0;
  let start = 0;
  for (const t of tokenize(s)) {
    if (t.text === '(') depth++;
    else if (t.text === ')') depth--;
    else if (t.text === ',' && depth === 0) {
      out.push(s.slice(start, t.from).trim());
      start = t.to;
    }
  }
  out.push(s.slice(start).trim());
  return out;
}

interface ParsedStats {
  measures: Measure[];
  groupBy: string[];
  span: string | null;
}

function parseStats(stage: string): ParsedStats | null {
  const m = /^stats\s+(.*)$/is.exec(stage.trim());
  if (!m) return null;
  let rest = m[1] ?? '';
  let span: string | null = null;
  const spanM = /\s+span\s+(\S+)\s*$/i.exec(rest);
  if (spanM) {
    span = (spanM[1] ?? '').toLowerCase();
    rest = rest.slice(0, spanM.index);
  }
  let groupBy: string[] = [];
  const byM = /\s+by\s+(.+)$/is.exec(rest);
  if (byM) {
    groupBy = splitCommas(byM[1] ?? '');
    rest = rest.slice(0, byM.index);
    if (groupBy.some((g) => !IDENT.test(g))) return null;
  }
  const measures: Measure[] = [];
  for (const part of splitCommas(rest)) {
    const a = AGG.exec(part);
    const fn = (a?.[1] ?? '').toLowerCase();
    const field = a?.[2] ?? '';
    if (!a || !isMeasureFn(fn) || !argumentFits(fn, field)) return null;
    measures.push({ fn, field });
  }
  return { measures, groupBy, span };
}

/**
 * Parses TQL into a builder model, or returns null when the query does not fit the builder
 * (the Builder tab is then read-only). `viz` is kept from the current panel when compatible.
 */
export function fromTql(q: string, viz: PanelType = 'timeseries'): BuilderModel | null {
  const parts = splitPipes(q).map((p) => p.text.trim());
  let source: Source = 'logs';
  const first = parts[0] ?? '';
  const src = /^source\s+(logs|spans|metrics)$/i.exec(first);
  if (src) {
    source = (src[1] ?? 'logs').toLowerCase() as Source;
    parts.shift();
  }
  if (parts.length !== 2) return null;
  const filter = parts[0] === '*' ? '' : (parts[0] ?? '');
  const stats = parseStats(parts[1] ?? '');
  if (
    !stats ||
    stats.measures.length === 0 ||
    stats.measures.length > MAX_MEASURES ||
    stats.groupBy.length > MAX_GROUP_BY
  )
    return null;
  let interval: Interval = 'auto';
  let nextViz = viz;
  if (stats.span !== null) {
    if (!isInterval(stats.span)) return null;
    interval = stats.span;
    nextViz = 'timeseries';
  } else if (TIME_PANELS.has(viz)) {
    nextViz = 'bar';
  }
  return { source, filter, measures: stats.measures, groupBy: stats.groupBy, interval, viz: nextViz };
}
