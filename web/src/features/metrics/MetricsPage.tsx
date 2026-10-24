// SPDX-License-Identifier: Apache-2.0
//
// Metric browser (SPEC §16.1): search metric names, chart one with a chosen aggregate and
// grouping, open the query in the explorer or add it to a dashboard. Histograms, exponential
// histograms and summaries chart the mean of the recorded values by default (`hist_mean()`,
// SPEC §10.5); histograms also offer p50 / p95 / p99 (`histogram_quantile(q)`). Monotonic sums
// (counters, `monotonic` from `/metrics/names`) chart their per-second rate by default
// (`rate(value)`, SPEC §10.5): their values are running totals, so adding them up says little.
// An up-down sum is a level like a gauge, so it gets the plain aggregates and averages.

import { useQuery } from '@tanstack/react-query';
import { BarChart3, LayoutGrid, Search } from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { search } from '../../api/endpoints';
import { useDataScope } from '../../app/scope';
import { useApi } from '../../app/services';
import { useTopbarStatus } from '../../app/shellContext';
import { patchParams, useTimeRange } from '../../app/url';
import { Chart } from '../../components/Chart';
import { Button, Segmented, Spinner, Tag } from '../../components/controls';
import { Card, EmptyState, Notice, Page, PageHead } from '../../components/layout';
import { TqlText } from '../../components/TqlText';
import { cx } from '../../lib/cx';
import { useDebounced } from '../../lib/hooks';
import { timeseriesToChart } from '../../lib/series';
import { AddToDashboardDialog } from '../dashboards/AddToDashboard';
import { valueLiteral } from '../explorer/terms';
import s from './metrics.module.css';

export type MetricAgg =
  'rate' | 'avg' | 'sum' | 'max' | 'min' | 'p95' | 'hist_mean' | 'hist_p50' | 'hist_p95' | 'hist_p99';
export type MetricGroup = 'none' | 'service';

/** The stats aggregate and the button label of each quick-chart aggregate. */
const AGGREGATES: Record<MetricAgg, { stats: string; label: string }> = {
  rate: { stats: 'rate(value)', label: 'rate/s' },
  avg: { stats: 'avg(value)', label: 'avg' },
  sum: { stats: 'sum(value)', label: 'sum' },
  max: { stats: 'max(value)', label: 'max' },
  min: { stats: 'min(value)', label: 'min' },
  p95: { stats: 'p95(value)', label: 'p95' },
  hist_mean: { stats: 'hist_mean()', label: 'Mean' },
  hist_p50: { stats: 'histogram_quantile(0.5)', label: 'p50' },
  hist_p95: { stats: 'histogram_quantile(0.95)', label: 'p95' },
  hist_p99: { stats: 'histogram_quantile(0.99)', label: 'p99' },
};

const PLAIN_AGGS: readonly MetricAgg[] = ['avg', 'sum', 'max', 'min', 'p95'];
const SUM_AGGS: readonly MetricAgg[] = ['rate', ...PLAIN_AGGS];
const HISTOGRAM_AGGS: readonly MetricAgg[] = ['hist_mean', 'hist_p50', 'hist_p95', 'hist_p99'];
const SUMMARY_AGGS: readonly MetricAgg[] = ['hist_mean'];

/** A counter: a monotonic sum (OTLP kind `sum`, `counter` as a synonym), whose values are running totals. */
const isCounter = (kind: string, monotonic: boolean): boolean =>
  monotonic && (kind === 'sum' || kind === 'counter');

/** Label of an aggregate button. */
export function aggLabel(agg: MetricAgg): string {
  return AGGREGATES[agg].label;
}

/**
 * The unit the chart shows for an aggregate of a metric with `unit`: a rate is per second, the
 * unitless `1` is no unit.
 */
export function aggUnit(agg: MetricAgg, unit: string | undefined): string {
  if (unit === undefined || unit === '' || unit === '1') return '';
  return agg === 'rate' ? `${unit}/s` : unit;
}

/** The TQL behind the quick chart. */
export function metricQuery(name: string, agg: MetricAgg, group: MetricGroup): string {
  const by = group === 'none' ? '' : ` by ${group}`;
  return `source metrics | name:${valueLiteral(name)} | stats ${AGGREGATES[agg].stats}${by} span auto`;
}

/**
 * The aggregates offered for a metric kind: histogram aggregates for histograms (quantiles need
 * buckets, so a summary only has the mean), the per-second rate besides the plain aggregates of
 * `value` for counters (monotonic sums), the plain aggregates for everything else, up-down sums
 * included (their increase per second is rarely meaningful).
 */
export function aggOptions(kind: string, monotonic: boolean): readonly MetricAgg[] {
  if (kind === 'histogram' || kind === 'exp_histogram') return HISTOGRAM_AGGS;
  if (kind === 'summary') return SUMMARY_AGGS;
  if (isCounter(kind, monotonic)) return SUM_AGGS;
  return PLAIN_AGGS;
}

/**
 * Counters chart their per-second rate, histograms and summaries the mean per bucket, the rest
 * (gauges and up-down sums) the average.
 */
export function defaultAgg(kind: string, monotonic: boolean): MetricAgg {
  if (isCounter(kind, monotonic)) return 'rate';
  if (aggOptions(kind, monotonic) !== PLAIN_AGGS) return 'hist_mean';
  return 'avg';
}

export default function MetricsPage() {
  const api = useApi();
  const { range } = useTimeRange();
  const scope = useDataScope();
  const [params, setParams] = useSearchParams();
  const [filter, setFilter] = useState(params.get('filter') ?? '');
  const debounced = useDebounced(filter, 200);
  const names = useQuery({
    queryKey: ['metric-names', debounced, scope.key],
    queryFn: ({ signal }) => search.metricNames(api, { q: debounced, ...scope.params }, signal),
  });
  const selected = params.get('metric') ?? names.data?.[0]?.name ?? null;
  const meta = names.data?.find((m) => m.name === selected);
  const [agg, setAgg] = useState<MetricAgg | null>(null);
  const [group, setGroup] = useState<MetricGroup>('service');
  const [adding, setAdding] = useState(false);
  const kind = meta?.kind ?? '';
  const monotonic = meta?.monotonic ?? false;
  const options = aggOptions(kind, monotonic);
  const effectiveAgg = agg !== null && options.includes(agg) ? agg : defaultAgg(kind, monotonic);
  const q = selected === null ? '' : metricQuery(selected, effectiveAgg, group);
  const chart = useQuery({
    queryKey: ['query', q, range.from, range.to, '', scope.key],
    queryFn: ({ signal }) => search.query(api, { q, ...range, ...scope.body }, signal),
    enabled: q !== '',
  });
  const data = useMemo(
    () => (chart.data?.kind === 'timeseries' ? timeseriesToChart(chart.data) : null),
    [chart.data],
  );
  useTopbarStatus(names.data === undefined ? null : `${names.data.length} metrics`);
  return (
    <Page className={s.page}>
      <PageHead title="Metrics" note="names from metric points in the selected range" />
      <div className={s.layout}>
        <aside className={s.list} aria-label="Metric names">
          <label className={s.search}>
            <Search size={14} aria-hidden="true" />
            <input
              type="search"
              placeholder="Search metric names"
              aria-label="Search metric names"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </label>
          {names.isPending ? <Spinner /> : null}
          <ul>
            {(names.data ?? []).map((m) => (
              <li key={m.name}>
                <button
                  type="button"
                  className={cx(s.item, m.name === selected && s.active)}
                  aria-pressed={m.name === selected}
                  onClick={() => {
                    setAgg(null);
                    setParams((p) => patchParams(p, { metric: m.name }), { replace: true });
                  }}
                >
                  <span className={s.name}>{m.name}</span>
                  <span className={s.tags}>
                    <Tag>{m.kind}</Tag>
                    {m.unit !== '' ? <Tag>{m.unit}</Tag> : null}
                  </span>
                </button>
              </li>
            ))}
          </ul>
          {names.data?.length === 0 ? <div className={s.none}>No metrics match.</div> : null}
        </aside>
        {selected === null ? (
          <EmptyState
            icon={<BarChart3 size={20} aria-hidden="true" />}
            title="No metrics yet"
            hint="Send OTLP metrics to a connection with the otlp protocol."
          />
        ) : (
          <div className={s.detail}>
            <Card
              title={selected}
              subtitle={meta?.description}
              actions={
                <>
                  <Segmented<MetricAgg>
                    label="Aggregate"
                    value={effectiveAgg}
                    onChange={setAgg}
                    options={options.map((v) => ({ value: v, label: aggLabel(v) }))}
                  />
                  <Segmented<MetricGroup>
                    label="Group by"
                    value={group}
                    onChange={setGroup}
                    options={[
                      { value: 'service', label: 'by service' },
                      { value: 'none', label: 'total' },
                    ]}
                  />
                </>
              }
            >
              {chart.isError ? <Notice kind="danger">{errorMessage(chart.error)}</Notice> : null}
              {chart.isPending ? <Spinner /> : null}
              {data !== null ? (
                data.x.length === 0 ? (
                  <EmptyState title="No points in this range" />
                ) : (
                  <Chart
                    data={data}
                    height={300}
                    ariaLabel={selected}
                    unit={aggUnit(effectiveAgg, meta?.unit)}
                  />
                )
              ) : null}
            </Card>
            <div className={s.queryLine}>
              <TqlText q={q} />
              <span className={s.queryActions}>
                <Link to={`/events?q=${encodeURIComponent(q)}`}>Open in explorer</Link>
                <Button variant="highlight" icon={<LayoutGrid size={14} />} onClick={() => setAdding(true)}>
                  Add to dashboard
                </Button>
              </span>
            </div>
          </div>
        )}
      </div>
      {adding ? (
        <AddToDashboardDialog
          q={q}
          kind="timeseries"
          title={selected ?? 'Metric'}
          onClose={() => setAdding(false)}
        />
      ) : null}
    </Page>
  );
}
