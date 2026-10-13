// SPDX-License-Identifier: Apache-2.0
//
// Pure helpers for alert rules (SPEC §14): display of conditions and states, the preview query
// (the rule query bucketed by `every` over the last 3 hours) and the default new rule.

import type { Alert, AlertState, RuleDoc } from '../../api/types';
import type { StatusVariant } from '../../components/controls';
import { parseGoDuration, shortDuration } from '../../lib/duration';
import { splitPipes } from '../../tql/tokenize';

export const PREVIEW_RANGE = { from: 'now-3h', to: 'now' };
export const WINDOWS = ['1m', '5m', '10m', '15m', '30m', '1h', '6h', '24h'];
export const EVERY = ['30s', '1m', '5m', '15m', '1h'];
export const FOR = ['0s', '1m', '2m', '5m', '10m', '15m', '30m', '1h'];

export type RuleFilter = 'all' | 'firing' | 'muted';

/** Whether the rule is muted now. */
export function isMuted(a: Alert, now: number): boolean {
  return a.doc.muteUntil !== null && Date.parse(a.doc.muteUntil) > now;
}

/** Status chip of a rule: muted beats ok, the state otherwise. */
export function stateChip(a: Alert, now: number): { variant: StatusVariant; label: string } {
  if (isMuted(a, now) && a.state === 'ok') return { variant: 'muted', label: 'Muted' };
  return stateVariant(a.state);
}

export function stateVariant(state: string): { variant: StatusVariant; label: string } {
  switch (state as AlertState | 'resolved') {
    case 'firing':
      return { variant: 'firing', label: 'Firing' };
    case 'pending':
      return { variant: 'pending', label: 'Pending' };
    case 'error':
      return { variant: 'error', label: 'Error' };
    default:
      return { variant: 'ok', label: 'OK' };
  }
}

/** "> 25 for 2m" */
export function conditionText(doc: RuleDoc): string {
  const forMs = parseGoDuration(doc.for) ?? 0;
  return `${doc.condition.op} ${doc.condition.threshold}${forMs > 0 ? ` for ${shortDuration(doc.for)}` : ''}`;
}

/** "service=checkout-api" from group labels. */
export function groupText(labels: Record<string, string>): string {
  return Object.entries(labels)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ');
}

/**
 * The preview query: the rule query with `span <every>` appended to its stats stage, so the
 * aggregate becomes a time series over the preview range.
 */
export function previewQuery(query: string, every: string): string {
  const parts = splitPipes(query).map((p) => p.text.trim());
  const i = parts.findIndex((p) => /^stats\s/i.test(p));
  if (i < 0) return query;
  const stage = parts[i] ?? '';
  if (/\sspan\s+\S+\s*$/i.test(stage)) return query;
  parts[i] = `${stage} span ${shortDuration(every)}`;
  return parts.join(' | ');
}

export function newRule(): { name: string; enabled: boolean; doc: RuleDoc } {
  return {
    name: '',
    enabled: true,
    doc: {
      description: '',
      query: 'level >= error | stats count()',
      condition: { op: '>', threshold: 0 },
      window: '5m',
      every: '1m',
      for: '0s',
      severity: 'warning',
      channels: [],
      noData: 'ok',
      groupBy: [],
      muteUntil: null,
    },
  };
}

/** Keeps the `by` fields of the stats stage in sync with groupBy (SPEC §14.1: they must match). */
export function withGroupBy(query: string, groupBy: readonly string[]): string {
  const parts = splitPipes(query).map((p) => p.text.trim());
  const i = parts.findIndex((p) => /^stats\s/i.test(p));
  if (i < 0) return query;
  const stage = (parts[i] ?? '').replace(/\s+by\s+.+$/i, '');
  parts[i] = groupBy.length === 0 ? stage : `${stage} by ${groupBy.join(', ')}`;
  return parts.join(' | ');
}
