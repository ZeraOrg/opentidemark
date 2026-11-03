// SPDX-License-Identifier: Apache-2.0
//
// Pure helpers for alert rules (SPEC §14): display of conditions and states, the preview query
// (the rule query bucketed by `every` over the last 3 hours) and the default new rule.

import type {
  Alert,
  AlertState,
  DeviationCondition,
  RuleCondition,
  RuleDoc,
  ThresholdCondition,
} from '../../api/types';
import type { StatusVariant } from '../../components/controls';
import { parseGoDuration, shortDuration } from '../../lib/duration';
import { formatNumber } from '../../lib/format';
import { splitPipes } from '../../tql/tokenize';

export const PREVIEW_RANGE = { from: 'now-3h', to: 'now' };
export const WINDOWS = ['1m', '5m', '10m', '15m', '30m', '1h', '6h', '24h'];
export const EVERY = ['30s', '1m', '5m', '15m', '1h'];
export const FOR = ['0s', '1m', '2m', '5m', '10m', '15m', '30m', '1h'];

export type RuleFilter = 'all' | 'firing' | 'muted';

/** Baseline choices of deviation rules: value (Go duration) and label. */
export const BASELINES: readonly { value: string; label: string }[] = [
  { value: '1h', label: '1h' },
  { value: '6h', label: '6h' },
  { value: '24h', label: '24h' },
  { value: '168h', label: '7d' },
];
/** Baseline windows a deviation rule may have (SPEC §14.1). */
export const MIN_BASELINE_WINDOWS = 5;
export const MAX_BASELINE_WINDOWS = 1000;
const DAY_MS = 86_400_000;

export const isDeviation = (c: RuleCondition): c is DeviationCondition => c.type === 'deviation';

/** Whether a window can be used by a deviation rule: whole seconds that divide 24 h. */
export function windowFitsDeviation(window: string): boolean {
  const ms = parseGoDuration(window);
  return ms !== null && ms > 0 && ms % 1000 === 0 && DAY_MS % ms === 0;
}

/** The baseline choices that give 5–1,000 windows of `window`. */
export function baselineChoices(window: string): { value: string; label: string }[] {
  const w = parseGoDuration(window);
  if (w === null || w <= 0) return [];
  return BASELINES.filter((b) => {
    const ms = parseGoDuration(b.value) ?? 0;
    const n = ms / w;
    return Number.isInteger(n) && n >= MIN_BASELINE_WINDOWS && n <= MAX_BASELINE_WINDOWS;
  });
}

/** A deviation condition for the window: 3σ either side of the longest fitting baseline up to 24 h. */
export function defaultDeviation(window: string): DeviationCondition {
  const choices = baselineChoices(window).filter((b) => b.value !== '168h');
  return {
    type: 'deviation',
    direction: 'both',
    sigma: 3,
    baseline: choices[choices.length - 1]?.value ?? '24h',
    minDelta: 0,
  };
}

export const defaultThreshold = (): ThresholdCondition => ({ op: '>', threshold: 0 });

/** Why the condition cannot be saved with this window, or null. */
export function conditionProblem(c: RuleCondition, window: string): string | null {
  if (!isDeviation(c)) return Number.isFinite(c.threshold) ? null : 'Threshold must be a number.';
  if (!windowFitsDeviation(window)) return 'Deviation rules need a window that divides 24h.';
  if (!Number.isFinite(c.sigma) || c.sigma <= 0 || c.sigma > 100) return 'σ must be between 0 and 100.';
  if (!Number.isFinite(c.minDelta) || c.minDelta < 0) return 'Min delta must be 0 or more.';
  const b = shortDuration(c.baseline);
  if (!baselineChoices(window).some((x) => x.value === b)) {
    return `Choose a baseline of ${MIN_BASELINE_WINDOWS}–${MAX_BASELINE_WINDOWS} windows.`;
  }
  return null;
}

/** Label of a baseline duration ("168h0m0s" → "7d"). */
export function baselineLabel(baseline: string): string {
  const b = shortDuration(baseline);
  return BASELINES.find((x) => x.value === b)?.label ?? b;
}

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

/** "> 25", or for deviation conditions "< 3σ vs 24h". */
export function conditionLabel(c: RuleCondition): string {
  if (isDeviation(c)) {
    const sign = c.direction === 'above' ? '>' : c.direction === 'below' ? '<' : '±';
    return `${sign} ${c.sigma}σ vs ${baselineLabel(c.baseline)}`;
  }
  return `${c.op} ${c.threshold}`;
}

/** "> 25 for 2m", or for deviation rules "< 3σ vs 24h". */
export function conditionText(doc: RuleDoc): string {
  const forMs = parseGoDuration(doc.for) ?? 0;
  return `${conditionLabel(doc.condition)}${forMs > 0 ? ` for ${shortDuration(doc.for)}` : ''}`;
}

/**
 * The value of a history entry with the condition: "value 31 > 25" ("≤" style for resolved
 * threshold rules); deviation rules show the value only (the message carries the baseline).
 */
export function historyValueText(c: RuleCondition, h: { value: number | null; toState: string }): string {
  if (h.value === null) return '';
  const v = `value ${formatNumber(h.value)}`;
  if (isDeviation(c)) return v;
  return `${v} ${h.toState === 'ok' ? '≤' : c.op} ${formatNumber(c.threshold)}`;
}

/** "mean 80 · σ 16.6 · z −4.1" */
export function baselineText(
  b: { mean: number; stdDev: number; z?: number },
  fmt: (v: number) => string,
): string {
  return `mean ${fmt(b.mean)} · σ ${fmt(b.stdDev)}${b.z !== undefined ? ` · z ${fmt(b.z)}` : ''}`;
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
