// SPDX-License-Identifier: Apache-2.0
//
// Analytics results (SPEC §16.3, DESIGN §9.3): for `timeseries` a chart card (Line / Area / Bars /
// Stacked, legend, crosshair tooltip) and a results table with Δ vs the previous bucket and an
// inline proportional bar; for `table` the table alone. Export CSV and Copy TQL.

import { useMemo, useState, type ReactNode } from 'react';
import type { QueryResponse } from '../../api/types';
import { usePalette } from '../../app/services';
import { Chart, Legend, type ChartStyle } from '../../components/Chart';
import { Button, Segmented, Tag } from '../../components/controls';
import { Card, layoutStyles as l } from '../../components/layout';
import { useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { copyText, downloadText } from '../../lib/download';
import { formatDelta, formatNumber } from '../../lib/format';
import { cellText, measureColumns, timeseriesRows, timeseriesToChart } from '../../lib/series';
import { formatDateTime, formatSpan } from '../../lib/time';
import { splitPipes } from '../../tql/tokenize';
import s from './ResultsView.module.css';

type ViewStyle = 'line' | 'area' | 'bars' | 'stacked';

const MAX_TABLE_ROWS = 500;

/** "count() by service" from the stats stage of a query (chart title). */
export function chartTitle(q: string): string {
  const stage = splitPipes(q)
    .map((p) => p.text.trim())
    .find((p) => /^(stats|top)\s/i.test(p));
  if (stage === undefined) return 'Results';
  return stage
    .replace(/^stats\s+/i, '')
    .replace(/\s+span\s+\S+$/i, '')
    .trim();
}

function csvCell(v: string): string {
  const safe = /^[=+\-@\t\r]/.test(v) ? `'${v}` : v;
  return /[",\n]/.test(safe) ? `"${safe.replace(/"/g, '""')}"` : safe;
}

/** CSV of a result's columns/rows (formula-injection guarded like GET /export). */
export function resultCsv(resp: QueryResponse): string {
  const cols = resp.columns ?? [];
  const lines = [cols.map((c) => csvCell(c.name)).join(',')];
  if (resp.kind === 'timeseries') {
    for (const r of timeseriesRows(resp)) {
      const key = Object.values(r.key).map((v) => cellText(v));
      lines.push(
        [new Date(r.bucket).toISOString(), ...key, ...r.values.map((v) => (v === null ? '' : String(v)))]
          .map(csvCell)
          .join(','),
      );
    }
  } else {
    for (const row of resp.rows ?? []) lines.push(row.map((v) => csvCell(cellText(v))).join(','));
  }
  return `${lines.join('\n')}\n`;
}

function ResultActions({ resp, q }: { resp: QueryResponse; q: string }) {
  const toast = useToast();
  return (
    <>
      <Button
        size="sm"
        variant="ghost"
        onClick={() => downloadText('tidemark-results.csv', resultCsv(resp), 'text/csv')}
      >
        Export CSV
      </Button>
      <Button
        size="sm"
        variant="ghost"
        onClick={() =>
          void copyText(q).then((ok) =>
            toast.push({ kind: ok ? 'success' : 'warning', title: ok ? 'TQL copied' : 'Copy failed' }),
          )
        }
      >
        Copy TQL
      </Button>
    </>
  );
}

function TimeseriesTable({ resp, colors }: { resp: QueryResponse; colors: readonly number[] }) {
  const rows = useMemo(() => timeseriesRows(resp), [resp]);
  const measures = measureColumns(resp);
  const keyFields = Object.keys(resp.series?.[0]?.key ?? {});
  const max = Math.max(1, ...rows.map((r) => Math.abs(r.values[0] ?? 0)));
  const shown = rows.slice(0, MAX_TABLE_ROWS);
  return (
    <div className={l.tableWrap}>
      <table className={cx(l.table, l.compact)}>
        <thead>
          <tr>
            <th>@bucket</th>
            {keyFields.map((k) => (
              <th key={k}>{k}</th>
            ))}
            {measures.map((m) => (
              <th key={m.name} className={l.num}>
                {m.name}
              </th>
            ))}
            <th className={l.num}>Δ vs prev</th>
            <th className={s.barHead} aria-label="Proportion" />
          </tr>
        </thead>
        <tbody>
          {shown.map((r) => {
            const d = r.delta[0] ?? null;
            return (
              <tr key={`${r.bucket}.${r.series}`}>
                <td className={l.monoCell}>{formatDateTime(r.bucket)}</td>
                {keyFields.map((k) => (
                  <td key={k} className={l.monoCell}>
                    {cellText(r.key[k])}
                  </td>
                ))}
                {r.values.map((v, j) => (
                  <td key={measures[j]?.name ?? j} className={l.num}>
                    {formatNumber(v)}
                  </td>
                ))}
                <td className={cx(l.num, d !== null && d > 0 && s.up, d !== null && d < 0 && s.down)}>
                  {d === null ? '' : formatDelta(d)}
                </td>
                <td className={s.barCell}>
                  <span
                    className={s.bar}
                    style={{
                      width: `${(Math.abs(r.values[0] ?? 0) / max) * 100}%`,
                      background: `var(--tm-chart-${colors[r.series] ?? 1})`,
                    }}
                  />
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {rows.length > MAX_TABLE_ROWS ? (
        <div className={s.more}>
          Showing {MAX_TABLE_ROWS} of {rows.length} rows · export for all
        </div>
      ) : null}
    </div>
  );
}

export function TableResult({ resp }: { resp: QueryResponse }) {
  const cols = resp.columns ?? [];
  const rows = resp.rows ?? [];
  const numIdx = cols.findIndex((c) => c.type === 'number' || c.type === 'duration_ms');
  const max = Math.max(1, ...rows.map((r) => Math.abs(Number(r[numIdx] ?? 0)) || 0));
  return (
    <div className={l.tableWrap}>
      <table className={cx(l.table, l.compact)}>
        <thead>
          <tr>
            {cols.map((c) => (
              <th key={c.name} className={cx(c.type === 'number' && l.num)}>
                {c.name}
              </th>
            ))}
            {numIdx >= 0 ? <th className={s.barHead} aria-label="Proportion" /> : null}
          </tr>
        </thead>
        <tbody>
          {rows.slice(0, MAX_TABLE_ROWS).map((r, i) => (
            <tr key={i}>
              {cols.map((c, j) => (
                <td
                  key={c.name}
                  className={c.type === 'number' || c.type === 'duration_ms' ? l.num : l.monoCell}
                >
                  {typeof r[j] === 'number' ? formatNumber(r[j] as number) : cellText(r[j])}
                </td>
              ))}
              {numIdx >= 0 ? (
                <td className={s.barCell}>
                  <span
                    className={s.bar}
                    style={{
                      width: `${(Math.abs(Number(r[numIdx] ?? 0)) / max) * 100}%`,
                      background: 'var(--tm-chart-1)',
                    }}
                  />
                </td>
              ) : null}
            </tr>
          ))}
        </tbody>
      </table>
      {rows.length === 0 ? <div className={s.more}>No rows.</div> : null}
    </div>
  );
}

export function ResultsView({
  resp,
  q,
  rangeText,
  actions,
}: {
  resp: QueryResponse;
  q: string;
  rangeText: string;
  actions?: ReactNode;
}) {
  const palette = usePalette();
  const [view, setView] = useState<ViewStyle>('line');
  const data = useMemo(() => (resp.kind === 'timeseries' ? timeseriesToChart(resp) : null), [resp]);
  const colors = useMemo(
    () => (data === null ? [] : data.series.map((x) => palette.index(x.label))),
    [data, palette],
  );
  const rowCount = resp.kind === 'timeseries' ? timeseriesRows(resp).length : (resp.rows ?? []).length;
  const measures = measureColumns(resp)
    .map((m) => m.name)
    .join(', ');
  const style: ChartStyle = view === 'stacked' ? 'area' : view;
  return (
    <div className={s.page}>
      {data !== null ? (
        <Card
          title={chartTitle(q)}
          subtitle={`${measures} · span ${formatSpan(resp.span ?? 0)} · ${rangeText}`}
          actions={
            <>
              <Segmented<ViewStyle>
                label="Chart style"
                value={view}
                onChange={setView}
                options={[
                  { value: 'line', label: 'Line' },
                  { value: 'area', label: 'Area' },
                  { value: 'bars', label: 'Bars' },
                  { value: 'stacked', label: 'Stacked' },
                ]}
              />
              {actions}
            </>
          }
        >
          <Legend
            items={data.series.map((x, i) => ({ label: x.label, color: colors[i] ?? 1 }))}
            className={s.legend}
          />
          {data.x.length === 0 ? (
            <div className={s.empty}>No data in this range.</div>
          ) : (
            <Chart
              data={data}
              height={280}
              style={style}
              stacked={view === 'stacked'}
              legend="hidden"
              colors={colors}
              ariaLabel={chartTitle(q)}
            />
          )}
        </Card>
      ) : null}
      <Card
        title="Results"
        headExtra={<Tag>{rowCount} rows</Tag>}
        actions={
          <>
            <span className={s.sortNote}>{resp.kind === 'timeseries' ? 'sorted by @bucket desc' : ''}</span>
            <ResultActions resp={resp} q={q} />
            {data === null ? actions : null}
          </>
        }
        flush
      >
        {resp.kind === 'timeseries' ? (
          <TimeseriesTable resp={resp} colors={colors} />
        ) : (
          <TableResult resp={resp} />
        )}
      </Card>
    </div>
  );
}
