// SPDX-License-Identifier: Apache-2.0
//
// Panel builder model ↔ TQL (SPEC §13.2). Builder → Query is lossless; Query → Builder succeeds
// only for queries of the builder shape `[source s |] filter | stats agg(f)… [by g…] [span x]`.

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
] as const;
export type MeasureFn = (typeof MEASURE_FUNCTIONS)[number];

export const INTERVALS = ['auto', '1m', '5m', '15m', '1h'] as const;
export type Interval = (typeof INTERVALS)[number];

export const MAX_MEASURES = 3;
export const MAX_GROUP_BY = 3;

export interface Measure {
  fn: MeasureFn;
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

function measureText(m: Measure): string {
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
    if (!a || !isMeasureFn(fn) || (field !== '' && !IDENT.test(field))) return null;
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
