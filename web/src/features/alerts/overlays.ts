// SPDX-License-Identifier: Apache-2.0
//
// Alert rules drawn on charts (SPEC §14.4): POST /alerts/overlays for the chart's query and time
// range (rules whose query matches, rules a panel names, or the editor's draft), turned into
// chart overlays whose deviation bands are matched to the chart series by their by-field values.

import { useQuery } from '@tanstack/react-query';
import { alerts as alertsApi } from '../../api/endpoints';
import type { AlertOverlay, RuleDoc } from '../../api/types';
import { useApi } from '../../app/services';
import type { ChartBand, ChartOverlay } from '../../components/Chart';
import type { ChartData } from '../../lib/series';
import { conditionLabel, isDeviation } from './rules';

/** The chart label of a band's by-field values (as `keyLabel` labels series). */
export function bandLabel(key: readonly string[]): string {
  return key.map((v) => (v === '' ? '∅' : v)).join(' · ');
}

/** Chart overlays for the series labels; bands of groups without a chart series are dropped. */
export function chartOverlays(list: readonly AlertOverlay[], labels: readonly string[]): ChartOverlay[] {
  return list.map((o): ChartOverlay => {
    const name = o.draft === true ? 'this rule' : o.name;
    const text = conditionLabel(o.condition);
    const c = o.condition;
    if (!isDeviation(c)) return { kind: 'threshold', name, text, op: c.op, value: c.threshold };
    const bands: ChartBand[] = [];
    for (const b of o.bands ?? []) {
      const series = b.key.length === 0 && labels.length === 1 ? 0 : labels.indexOf(bandLabel(b.key));
      if (series >= 0) bands.push({ series, windowMs: o.windowMs, points: b.points });
    }
    return { kind: 'band', name, text, bands, ...(o.error !== undefined ? { error: o.error } : {}) };
  });
}

/** The absolute range a chart covers: from its first bucket to the end of its last one. */
export function chartRange(data: ChartData, spanMs: number): { from: string; to: string } | null {
  const first = data.x[0];
  const last = data.x[data.x.length - 1];
  if (first === undefined || last === undefined) return null;
  return { from: new Date(first).toISOString(), to: new Date(last + Math.max(spanMs, 1)).toISOString() };
}

/**
 * The alert rules to draw on a chart of `q`: rules whose query matches, the rules in `rules`
 * and the `draft` rule. Empty until loaded, and when the chart has no data.
 */
export function useAlertOverlays(opts: {
  q: string;
  data: ChartData | null;
  spanMs: number;
  rules?: readonly string[];
  draft?: { doc: RuleDoc; streams: string[] | null };
  enabled?: boolean;
}): ChartOverlay[] {
  const api = useApi();
  const range = opts.data === null ? null : chartRange(opts.data, opts.spanMs);
  const rules = opts.rules ?? [];
  const res = useQuery({
    queryKey: [
      'alert-overlays',
      opts.q,
      range?.from,
      range?.to,
      rules.join(','),
      JSON.stringify(opts.draft ?? null),
    ],
    queryFn: ({ signal }) =>
      alertsApi.overlays(
        api,
        {
          q: opts.q,
          from: range?.from ?? '',
          to: range?.to ?? '',
          ...(rules.length > 0 ? { rules: [...rules] } : {}),
          ...(opts.draft !== undefined ? { doc: opts.draft.doc, streams: opts.draft.streams } : {}),
        },
        signal,
      ),
    enabled: (opts.enabled ?? true) && range !== null,
    staleTime: 60_000,
  });
  if (res.data === undefined || opts.data === null) return [];
  return chartOverlays(
    res.data.overlays,
    opts.data.series.map((x) => x.label),
  );
}
