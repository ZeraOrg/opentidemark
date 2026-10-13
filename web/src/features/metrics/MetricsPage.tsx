// SPDX-License-Identifier: Apache-2.0
//
// Metric browser (SPEC §16.1): search metric names, chart one with a chosen aggregate and
// grouping, open the query in the explorer or add it to a dashboard.

import { useQuery } from '@tanstack/react-query';
import { BarChart3, LayoutGrid, Search } from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { search } from '../../api/endpoints';
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

export type MetricAgg = 'avg' | 'sum' | 'max' | 'min' | 'p95';
export type MetricGroup = 'none' | 'service';

/** The TQL behind the quick chart. */
export function metricQuery(name: string, agg: MetricAgg, group: MetricGroup): string {
  const by = group === 'none' ? '' : ` by ${group}`;
  return `source metrics | name:${valueLiteral(name)} | stats ${agg}(value)${by} span auto`;
}

/** Sums and counters are summed by default, everything else averaged. */
export function defaultAgg(kind: string): MetricAgg {
  return kind === 'sum' || kind === 'counter' ? 'sum' : 'avg';
}

export default function MetricsPage() {
  const api = useApi();
  const { range } = useTimeRange();
  const [params, setParams] = useSearchParams();
  const [filter, setFilter] = useState(params.get('filter') ?? '');
  const debounced = useDebounced(filter, 200);
  const names = useQuery({
    queryKey: ['metric-names', debounced],
    queryFn: ({ signal }) => search.metricNames(api, debounced, signal),
  });
  const selected = params.get('metric') ?? names.data?.[0]?.name ?? null;
  const meta = names.data?.find((m) => m.name === selected);
  const [agg, setAgg] = useState<MetricAgg | null>(null);
  const [group, setGroup] = useState<MetricGroup>('service');
  const [adding, setAdding] = useState(false);
  const effectiveAgg = agg ?? defaultAgg(meta?.kind ?? '');
  const q = selected === null ? '' : metricQuery(selected, effectiveAgg, group);
  const chart = useQuery({
    queryKey: ['query', q, range.from, range.to, ''],
    queryFn: ({ signal }) => search.query(api, { q, ...range }, signal),
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
                    options={(['avg', 'sum', 'max', 'min', 'p95'] as const).map((v) => ({
                      value: v,
                      label: v,
                    }))}
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
                    unit={meta?.unit === '1' ? '' : meta?.unit}
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
