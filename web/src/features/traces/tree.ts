// SPDX-License-Identifier: Apache-2.0
//
// Trace layout (SPEC §16.4, DESIGN §9.4): spans ordered as a depth-first tree by start time, the
// trace bounds, and the critical path (the chain of spans that determines the end of the trace).

import type { Span } from '../../api/types';

export interface SpanRow {
  span: Span;
  depth: number;
  /** Offset from the trace start in ms. */
  offset: number;
  end: number;
  error: boolean;
  childCount: number;
}

export interface TraceLayout {
  rows: SpanRow[];
  start: number;
  duration: number;
  services: string[];
  critical: Set<string>;
  byId: Map<string, Span>;
}

const startOf = (s: Span): number => Date.parse(s.start);
export const isErrorSpan = (s: Span): boolean => s.statusCode.toLowerCase() === 'error';

/** Builds the waterfall rows; spans whose parent is missing become roots. */
export function layoutTrace(spans: readonly Span[]): TraceLayout {
  const byId = new Map(spans.map((s) => [s.spanId, s]));
  const children = new Map<string, Span[]>();
  const roots: Span[] = [];
  for (const s of spans) {
    if (s.parentSpanId !== '' && byId.has(s.parentSpanId) && s.parentSpanId !== s.spanId) {
      const list = children.get(s.parentSpanId) ?? [];
      list.push(s);
      children.set(s.parentSpanId, list);
    } else {
      roots.push(s);
    }
  }
  const byStart = (a: Span, b: Span): number => startOf(a) - startOf(b) || a.spanId.localeCompare(b.spanId);
  const start = spans.length === 0 ? 0 : Math.min(...spans.map(startOf));
  const end = spans.length === 0 ? 0 : Math.max(...spans.map((s) => startOf(s) + s.durationMs));
  const rows: SpanRow[] = [];
  const seen = new Set<string>();
  const visit = (s: Span, depth: number): void => {
    if (seen.has(s.spanId)) return;
    seen.add(s.spanId);
    const kids = (children.get(s.spanId) ?? []).sort(byStart);
    const offset = startOf(s) - start;
    rows.push({
      span: s,
      depth,
      offset,
      end: offset + s.durationMs,
      error: isErrorSpan(s),
      childCount: kids.length,
    });
    for (const c of kids) visit(c, depth + 1);
  };
  for (const r of roots.sort(byStart)) visit(r, 0);
  const services: string[] = [];
  for (const r of rows) if (!services.includes(r.span.service)) services.push(r.span.service);
  return {
    rows,
    start,
    duration: end - start,
    services,
    critical: criticalPath(spans, children, roots),
    byId,
  };
}

/**
 * Critical path: from the last-ending root, walk back through the children that finish last
 * before the parent's (remaining) end, the standard "latest-finishing child" algorithm.
 */
export function criticalPath(
  spans: readonly Span[],
  children: Map<string, Span[]>,
  roots: readonly Span[],
): Set<string> {
  const out = new Set<string>();
  const endOf = (s: Span): number => startOf(s) + s.durationMs;
  const walk = (s: Span, until: number): void => {
    out.add(s.spanId);
    let t = Math.min(endOf(s), until);
    const kids = [...(children.get(s.spanId) ?? [])].sort((a, b) => endOf(b) - endOf(a));
    for (const c of kids) {
      if (startOf(c) >= t || out.has(c.spanId)) continue;
      walk(c, t);
      t = startOf(c);
    }
  };
  const root = [...roots].sort((a, b) => endOf(b) - endOf(a))[0];
  if (root !== undefined && spans.length > 0) walk(root, endOf(root));
  return out;
}

/** Four evenly spaced tick values for the time axis (0 … duration). */
export function ticks(duration: number): number[] {
  return [0, 0.25, 0.5, 0.75, 1].map((f) => Math.round(duration * f));
}
