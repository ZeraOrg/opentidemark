// SPDX-License-Identifier: Apache-2.0
//
// Pure helpers of the streams screen (SPEC §12): rows per stream and source, retention summary,
// the TTL preview (same shape as internal/retention, §12.3), purge confirmation and estimates.

import type { LevelName, LevelRule, Source, SourceStats, Stream } from '../../api/types';
import { LEVELS } from '../../api/types';

export const MAX_INDEXED = 50;
export const MAX_LEVEL_RULES = 6;
export const DEFAULT_DAYS: Record<Source, number> = { logs: 30, spans: 7, metrics: 30 };

export interface StreamRow {
  stream: Stream;
  source: Source | null;
  stats: SourceStats | null;
}

/** One table row per stream and source with data; streams without data get one empty row. */
export function streamRows(streams: readonly Stream[]): StreamRow[] {
  const out: StreamRow[] = [];
  for (const st of streams) {
    const entries = (['logs', 'spans', 'metrics'] as const).flatMap((src) => {
      const v = st.stats?.[src];
      return v === undefined ? [] : [{ stream: st, source: src, stats: v }];
    });
    if (entries.length === 0) out.push({ stream: st, source: null, stats: null });
    else out.push(...entries);
  }
  return out;
}

/** Retention override of a stream for a source (null = default). */
export function retentionFor(st: Stream, source: Source): number | null {
  if (source === 'spans') return st.spansRetentionDays;
  if (source === 'metrics') return st.metricsRetentionDays;
  return st.retentionDays;
}

export function retentionPatchKey(
  source: Source,
): 'retentionDays' | 'spansRetentionDays' | 'metricsRetentionDays' {
  return source === 'spans'
    ? 'spansRetentionDays'
    : source === 'metrics'
      ? 'metricsRetentionDays'
      : 'retentionDays';
}

const SHORT: Record<string, string> = {
  Verbose: 'verbose',
  Debug: 'debug',
  Information: 'info',
  Warning: 'warning',
  Error: 'error',
  Fatal: 'fatal',
};

/** "verbose/debug 3 d" summary of level rules. */
export function levelRuleSummary(rules: readonly LevelRule[]): string {
  return rules
    .map((r) => `${r.levels.map((l) => SHORT[l] ?? l.toLowerCase()).join('/')} ${r.days} d`)
    .join(' · ');
}

export function levelNum(level: string): number {
  return LEVELS.indexOf(level as LevelName);
}

function quote(s: string): string {
  return `'${s.replace(/\\/g, '\\\\').replace(/'/g, "\\'")}'`;
}

/**
 * Preview of the TTL clauses for one stream (logs table): level rules first, then the stream
 * retention. Rendering only; the server builds and validates the real DDL.
 */
export function ttlPreview(stream: string, days: number, rules: readonly LevelRule[]): string {
  const name = quote(stream);
  const clauses = [...rules]
    .filter((r) => r.levels.length > 0)
    .sort((a, b) => a.days - b.days)
    .map((r) => {
      const nums = r.levels
        .map(levelNum)
        .filter((n) => n >= 0)
        .sort((a, b) => a - b);
      const cond = nums.length === 1 ? `level_num = ${nums[0]}` : `level_num IN (${nums.join(',')})`;
      return `toDateTime(timestamp) + toIntervalDay(${r.days}) DELETE\n    WHERE stream = ${name} AND ${cond}`;
    });
  clauses.push(`toDateTime(timestamp) + toIntervalDay(${days}) DELETE\n    WHERE stream = ${name}`);
  return `-- generated TTL (preview)\nTTL ${clauses.join(',\n    ')}`;
}

/** The typed confirmation for purging a stream (or all streams). */
export function purgeConfirmation(stream: string): string {
  return `purge ${stream === '' ? 'all' : stream}`;
}

/** Estimated compressed bytes of `rows` using the stream's average row size. */
export function estimateBytes(rows: number, stats: SourceStats | null): number | null {
  if (stats === null || stats.rows <= 0) return null;
  return Math.round((stats.compressedBytes / stats.rows) * rows);
}

/** Whether a purge drops whole partitions (no stream, no filter, ≥ one full UTC day, §12.5). */
export function dropsPartitions(stream: string, q: string, fromMs: number, toMs: number): boolean {
  if (stream !== '' || q.trim() !== '') return false;
  const day = 86_400_000;
  const firstFull = Math.ceil(fromMs / day) * day;
  return firstFull + day <= toMs;
}
