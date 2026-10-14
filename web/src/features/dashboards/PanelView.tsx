// SPDX-License-Identifier: Apache-2.0
//
// Dashboard panel (SPEC §13.1, DESIGN §5.19, §9.5): resolves the panel query (server-side AST
// substitution for saved panels, preview substitution while editing), runs it and renders the
// visualization: time series, bar, stat, gauge, table, pie, logs, traces or markdown.

import { useQuery } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { dashboards as dashApi, search } from '../../api/endpoints';
import type { LogEvent, Panel, QueryResponse } from '../../api/types';
import { useDataScope } from '../../app/scope';
import { useApi, usePalette } from '../../app/services';
import { Chart, colorVar, Legend, Sparkline } from '../../components/Chart';
import { LevelChip, Spinner } from '../../components/controls';
import { layoutStyles as l } from '../../components/layout';
import { Markdown } from '../../components/Markdown';
import { cx } from '../../lib/cx';
import { formatNumber, formatUnit } from '../../lib/format';
import type { TimeRange } from '../../lib/time';
import { formatClock, formatDuration } from '../../lib/time';
import { statValue, tableCategories, timeseriesToChart } from '../../lib/series';
import { substitutePreview } from '../../tql/substitute';
import { TableResult } from '../explorer/ResultsView';
import { Message } from '../explorer/EventList';
import s from './dashboards.module.css';

export const ROW_HEIGHT = 40;
export const GRID_GAP = 12;
const PANEL_CHROME = 34 + 18;
const LEGEND_HEIGHT = 26;
const LOG_LIMIT = 50;
const TRACE_LIMIT = 20;

/** Pixel height of a panel body for a grid height `h`. */
export function bodyHeight(h: number): number {
  return Math.max(60, h * ROW_HEIGHT + (h - 1) * GRID_GAP - PANEL_CHROME);
}

/** The query of `panel` with variables applied (undefined until resolved). */
export function usePanelQuery(
  dashboardId: string | undefined,
  panel: Panel,
  vars: Record<string, string[]>,
  preview: boolean,
): { q: string | undefined; error: unknown } {
  const api = useApi();
  const raw = panel.query ?? '';
  const server = useQuery({
    queryKey: ['panel-q', dashboardId, panel.id, raw, vars],
    queryFn: () => dashApi.resolvePanel(api, dashboardId ?? '', panel.id, vars),
    enabled: !preview && dashboardId !== undefined && raw !== '',
    staleTime: Infinity,
  });
  if (raw === '') return { q: '', error: null };
  if (preview || dashboardId === undefined) return { q: substitutePreview(raw, vars), error: null };
  return { q: server.data?.q, error: server.error };
}

function thresholdsOf(panel: Panel) {
  return (panel.options?.thresholds ?? []).map((t) => ({
    value: t.value,
    color: t.color,
    label: `threshold ${formatNumber(t.value)}`,
  }));
}

function TimeseriesBody({ panel, resp, height }: { panel: Panel; resp: QueryResponse; height: number }) {
  const palette = usePalette();
  const data = timeseriesToChart(resp);
  const legend = panel.options?.legend ?? 'bottom';
  const colors = data.series.map((x) => palette.index(x.label));
  if (data.x.length === 0) return <div className={s.noData}>No data</div>;
  return (
    <Chart
      data={data}
      height={height - (legend === 'bottom' ? LEGEND_HEIGHT : 0)}
      ariaLabel={panel.title}
      style={panel.options?.style ?? 'line'}
      stacked={panel.options?.stack === true}
      thresholds={thresholdsOf(panel)}
      unit={panel.options?.unit}
      decimals={panel.options?.decimals}
      legend={legend}
      colors={colors}
    />
  );
}

/** Colour token of the highest panel threshold that `v` reaches, or null. */
export function thresholdToken(panel: Panel, v: number | null): string | null {
  if (v === null) return null;
  const hit = [...(panel.options?.thresholds ?? [])]
    .sort((a, b) => b.value - a.value)
    .find((t) => v >= t.value);
  return hit === undefined ? null : hit.color;
}

/** CSS colour of a value against the panel thresholds (highest threshold reached wins). */
export function thresholdColor(panel: Panel, v: number | null): string | null {
  const token = thresholdToken(panel, v);
  return token === null ? null : `var(${colorVar(token)})`;
}

function StatBody({ panel, resp }: { panel: Panel; resp: QueryResponse }) {
  const v = statValue(resp);
  const unit = panel.options?.unit ?? '';
  const delta =
    v.value !== null && v.previous !== null && v.previous !== 0
      ? ((v.value - v.previous) / Math.abs(v.previous)) * 100
      : null;
  const color = thresholdColor(panel, v.value);
  // Sparklines do not take a session series colour (§2.4 colours belong to series labels): the
  // reached threshold's token, else chart colour 1.
  const series = thresholdToken(panel, v.value) ?? 'chart1';
  const text = formatUnit(v.value, '', panel.options?.decimals);
  return (
    <div className={s.stat}>
      <div className={s.statValue} style={color === null ? undefined : { color }}>
        {text}
        {unit !== '' ? <span className={s.statUnit}>{unit}</span> : null}
      </div>
      {delta !== null ? (
        <span className={cx(s.delta, delta > 0 ? s.up : delta < 0 ? s.down : s.flat)}>
          {delta > 0 ? '▲' : delta < 0 ? '▼' : '—'} {formatNumber(Math.abs(delta), 1)}%
        </span>
      ) : null}
      <span className={s.spark}>
        <Sparkline values={v.spark} color={series} />
      </span>
    </div>
  );
}

const GAUGE_RADIUS = 52;

function GaugeBody({ panel, resp }: { panel: Panel; resp: QueryResponse }) {
  const v = statValue(resp).value ?? 0;
  const max = Math.max(v, ...(panel.options?.thresholds ?? []).map((t) => t.value * 1.25), 100);
  const frac = Math.min(1, Math.max(0, v / max));
  const len = Math.PI * GAUGE_RADIUS;
  const color = thresholdColor(panel, v) ?? 'var(--tm-chart-1)';
  return (
    <div className={s.gauge}>
      <svg
        viewBox="0 0 120 70"
        role="img"
        aria-label={`${panel.title}: ${formatUnit(v, panel.options?.unit)}`}
      >
        <path d="M8 62 A52 52 0 0 1 112 62" className={s.gaugeTrack} />
        <path
          d="M8 62 A52 52 0 0 1 112 62"
          className={s.gaugeValue}
          style={{ stroke: color, strokeDasharray: `${frac * len} ${len}` }}
        />
      </svg>
      <div className={s.gaugeText}>{formatUnit(v, panel.options?.unit, panel.options?.decimals)}</div>
    </div>
  );
}

function BarBody({ resp }: { resp: QueryResponse }) {
  const palette = usePalette();
  const cats = tableCategories(resp);
  const max = Math.max(1, ...cats.map((c) => c.value));
  return (
    <ul className={s.bars}>
      {cats.slice(0, 20).map((c) => (
        <li key={c.label}>
          <span className={s.barLabel}>{c.label}</span>
          <span className={s.barTrack}>
            <span
              className={s.barFill}
              style={{
                width: `${(c.value / max) * 100}%`,
                background: `var(--tm-chart-${palette.index(c.label)})`,
              }}
            />
          </span>
          <span className={s.barValue}>{formatNumber(c.value)}</span>
        </li>
      ))}
    </ul>
  );
}

function PieBody({ resp, height }: { resp: QueryResponse; height: number }) {
  const palette = usePalette();
  const cats = tableCategories(resp)
    .filter((c) => c.value > 0)
    .slice(0, 8);
  const total = cats.reduce((a, c) => a + c.value, 0) || 1;
  // Each slice starts where the previous ones end (percent of the circumference).
  const starts = cats.map((_, i) => cats.slice(0, i).reduce((a, c) => a + (c.value / total) * 100, 0));
  const r = 15.915;
  return (
    <div className={s.pie}>
      <svg
        viewBox="0 0 42 42"
        style={{ height: Math.min(height - 8, 220) }}
        role="img"
        aria-label={`Pie of ${cats.length} categories`}
      >
        {cats.map((c, i) => {
          const frac = (c.value / total) * 100;
          return (
            <circle
              key={c.label}
              cx="21"
              cy="21"
              r={r}
              className={s.pieSlice}
              style={{
                stroke: `var(--tm-chart-${palette.index(c.label)})`,
                strokeDasharray: `${frac} ${100 - frac}`,
                strokeDashoffset: `${25 - (starts[i] ?? 0)}`,
              }}
            />
          );
        })}
      </svg>
      <Legend
        items={cats.map((c) => ({
          label: `${c.label} · ${Math.round((c.value / total) * 100)}%`,
          color: palette.index(c.label),
        }))}
      />
    </div>
  );
}

function LogsBody({ resp }: { resp: QueryResponse }) {
  const events = (resp.events ?? []) as LogEvent[];
  if (events.length === 0) return <div className={s.noData}>No events</div>;
  return (
    <ul className={s.logList}>
      {events.map((e) => (
        <li key={e.id}>
          <Link to={`/events/${encodeURIComponent(e.id)}`} className={s.logRow}>
            <span className={l.subtle}>{formatClock(Date.parse(e.timestamp))}</span>
            <LevelChip level={e.level} />
            <span className={s.logMsg}>
              <Message ev={e} />
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function TracesBody({ q, range }: { q: string; range: TimeRange }) {
  const api = useApi();
  const scope = useDataScope();
  const traces = useQuery({
    queryKey: ['traces', q, range.from, range.to, TRACE_LIMIT, scope.key],
    queryFn: ({ signal }) => search.traces(api, { q, ...range, limit: TRACE_LIMIT, ...scope.params }, signal),
  });
  if (traces.isPending) return <Spinner />;
  if (traces.isError) return <div className={s.panelError}>{errorMessage(traces.error)}</div>;
  return (
    <ul className={s.logList}>
      {traces.data.map((t) => (
        <li key={t.traceId}>
          <Link to={`/traces/${encodeURIComponent(t.traceId)}`} className={s.logRow}>
            <span className={l.subtle}>{formatClock(Date.parse(t.start))}</span>
            <span className={cx(t.errorCount > 0 && s.errorText)}>{formatDuration(t.durationMs)}</span>
            <span className={s.logMsg}>
              {t.rootService} · {t.rootName}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function QueryBody({
  panel,
  q,
  range,
  height,
  refetchMs,
}: {
  panel: Panel;
  q: string;
  range: TimeRange;
  height: number;
  refetchMs: number | null;
}) {
  const api = useApi();
  const scope = useDataScope();
  const res = useQuery({
    queryKey: ['query', q, range.from, range.to, panel.type, scope.key],
    queryFn: ({ signal }) =>
      search.query(
        api,
        { q, ...range, limit: panel.type === 'logs' ? LOG_LIMIT : undefined, ...scope.body },
        signal,
      ),
    refetchInterval: refetchMs ?? false,
  });
  if (res.isPending) return <Spinner />;
  if (res.isError) return <div className={s.panelError}>{errorMessage(res.error)}</div>;
  const resp = res.data;
  switch (panel.type) {
    case 'timeseries':
      return resp.kind === 'timeseries' ? (
        <TimeseriesBody panel={panel} resp={resp} height={height} />
      ) : (
        <TableResult resp={resp} />
      );
    case 'stat':
      return <StatBody panel={panel} resp={resp} />;
    case 'gauge':
      return <GaugeBody panel={panel} resp={resp} />;
    case 'bar':
      return <BarBody resp={resp} />;
    case 'pie':
      return <PieBody resp={resp} height={height} />;
    case 'logs':
      return <LogsBody resp={resp} />;
    default:
      return <TableResult resp={resp} />;
  }
}

export function PanelBody({
  dashboardId,
  panel,
  vars,
  range,
  preview,
  height,
  refetchMs = null,
}: {
  dashboardId: string | undefined;
  panel: Panel;
  vars: Record<string, string[]>;
  range: TimeRange;
  preview: boolean;
  height: number;
  refetchMs?: number | null;
}) {
  const { q, error } = usePanelQuery(dashboardId, panel, vars, preview);
  if (panel.type === 'markdown') return <Markdown source={panel.options?.content ?? ''} />;
  if (error !== null && error !== undefined) return <div className={s.panelError}>{errorMessage(error)}</div>;
  if (q === undefined) return <Spinner />;
  if (panel.type === 'traces') return <TracesBody q={q} range={range} />;
  return <QueryBody panel={panel} q={q} range={range} height={height} refetchMs={refetchMs} />;
}

/** One-line subtitle shown next to the panel title. */
export function panelSubtitle(panel: Panel): string {
  const unit = panel.options?.unit;
  if (panel.type === 'timeseries' && panel.options?.stack === true)
    return `stacked${unit ? ` · ${unit}` : ''}`;
  return unit ?? '';
}

export function PanelFrame({
  panel,
  actions,
  children,
  className,
}: {
  panel: Panel;
  actions?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  const stat = panel.type === 'stat';
  return (
    <section className={cx(s.panel, stat && s.statPanel, className)} aria-label={panel.title}>
      <header className={s.panelHead}>
        <h3 className={s.panelTitle}>{panel.title}</h3>
        {!stat && panelSubtitle(panel) !== '' ? (
          <span className={s.panelSub}>{panelSubtitle(panel)}</span>
        ) : null}
        {actions !== undefined ? <span className={s.panelActions}>{actions}</span> : null}
      </header>
      <div className={s.panelBody}>{children}</div>
    </section>
  );
}
