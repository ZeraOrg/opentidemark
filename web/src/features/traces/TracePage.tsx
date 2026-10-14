// SPDX-License-Identifier: Apache-2.0
//
// Trace view (SPEC §16.4, DESIGN §9.4): header with the root span, waterfall (or flame graph)
// coloured by service with the critical path and errored spans marked, and a span drawer with
// attributes, events, linked logs and raw JSON.

import { useQuery } from '@tanstack/react-query';
import { Copy, EyeOff, List, X } from 'lucide-react';
import { useMemo, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { search } from '../../api/endpoints';
import type { LogEvent } from '../../api/types';
import { useApi, usePalette } from '../../app/services';
import { useTopbarStatus } from '../../app/shellContext';
import { patchParams } from '../../app/url';
import { Button, LevelChip, Segmented, Spinner, StatusChip, Tag } from '../../components/controls';
import { EmptyState, JsonView, layoutStyles as l, Notice, Tabs } from '../../components/layout';
import { useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { copyText } from '../../lib/download';
import { renderValue } from '../../lib/template';
import { formatClock, formatDuration } from '../../lib/time';
import { layoutTrace, ticks, type SpanRow, type TraceLayout } from './tree';
import s from './traces.module.css';

type View = 'waterfall' | 'flame';
type SpanTab = 'attributes' | 'events' | 'logs' | 'raw';

const INDENT_PX = 16;
/** Bars at least this share of the trace hold their label at the end. */
const WIDE_BAR = 0.15;
const LABEL_FIT = 0.82;

function pct(v: number, total: number): string {
  return `${total <= 0 ? 0 : (v / total) * 100}%`;
}

function ServiceDot({ service }: { service: string }) {
  const palette = usePalette();
  return (
    <span
      className={s.dot}
      style={{ background: `var(--tm-chart-${palette.index(service)})` }}
      aria-hidden="true"
    />
  );
}

function Waterfall({
  layout,
  selected,
  onSelect,
}: {
  layout: TraceLayout;
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  const palette = usePalette();
  const total = layout.duration;
  return (
    <div className={s.waterfall} role="treegrid" aria-label="Waterfall">
      <div className={s.wfHead} role="row">
        <span role="columnheader">Service · span</span>
        <span className={s.axis} role="columnheader">
          {ticks(total).map((t, i) => (
            <span key={i} style={{ left: pct(t, total) }} className={cx(s.tick, i === 4 && s.tickLast)}>
              {i === 4 ? `${formatDuration(t)}` : i === 0 ? '0 ms' : t.toLocaleString('en-US')}
            </span>
          ))}
        </span>
      </div>
      {layout.rows.map((r) => {
        const critical = layout.critical.has(r.span.spanId);
        const left = r.offset / Math.max(1, total);
        const width = r.span.durationMs / Math.max(1, total);
        // Labels go right of the bar; near the end they go inside a wide bar, else left of it.
        const nearEnd = left + width > LABEL_FIT;
        const labelInside = nearEnd && width >= WIDE_BAR;
        const labelStyle = !nearEnd
          ? { left: `calc(${(left + width) * 100}% + 6px)` }
          : labelInside
            ? undefined
            : { right: `calc(${(1 - left) * 100}% + 6px)` };
        return (
          <button
            type="button"
            key={r.span.spanId}
            role="row"
            aria-selected={selected === r.span.spanId}
            aria-level={r.depth + 1}
            className={s.wfRow}
            onClick={() => onSelect(r.span.spanId)}
          >
            <span className={s.wfName} style={{ paddingLeft: 16 + r.depth * INDENT_PX }}>
              <ServiceDot service={r.span.service} />
              <span className={s.wfService}>{r.span.service}</span>
              <span className={s.wfSpan}>{r.span.name}</span>
              {r.depth === 0 && r.span.parentSpanId !== '' ? (
                <span className={s.orphan} title="The parent span is in a stream you cannot see">
                  <EyeOff size={11} aria-hidden="true" />
                  <span>parent span not visible</span>
                </span>
              ) : null}
            </span>
            <span className={s.track}>
              {[0.25, 0.5, 0.75].map((f) => (
                <span key={f} className={s.gridLine} style={{ left: `${f * 100}%` }} aria-hidden="true" />
              ))}
              <span
                className={cx(s.bar, critical && s.critical, r.error && s.errorBar)}
                style={{
                  left: `${left * 100}%`,
                  width: `max(3px, ${width * 100}%)`,
                  background: `var(--tm-chart-${palette.index(r.span.service)})`,
                }}
                title={`${r.span.name} · ${formatDuration(r.span.durationMs)}${critical ? ' · critical path' : ''}${r.error ? ' · error' : ''}`}
              />
              <span className={cx(s.barLabel, labelInside && s.barLabelEnd)} style={labelStyle}>
                {formatDuration(r.span.durationMs)}
              </span>
            </span>
          </button>
        );
      })}
    </div>
  );
}

function Flame({
  layout,
  selected,
  onSelect,
}: {
  layout: TraceLayout;
  selected: string | null;
  onSelect: (id: string) => void;
}) {
  const palette = usePalette();
  const depth = Math.max(0, ...layout.rows.map((r) => r.depth)) + 1;
  return (
    <div className={s.flame} style={{ height: depth * 26 + 8 }} role="group" aria-label="Flame graph">
      {layout.rows.map((r) => (
        <button
          type="button"
          key={r.span.spanId}
          aria-pressed={selected === r.span.spanId}
          className={cx(s.flameBlock, r.error && s.errorBar)}
          style={{
            left: pct(r.offset, layout.duration),
            width: `max(2px, ${pct(r.span.durationMs, layout.duration)})`,
            top: r.depth * 26 + 4,
            background: `var(--tm-chart-${palette.index(r.span.service)})`,
          }}
          title={`${r.span.service} ${r.span.name} · ${formatDuration(r.span.durationMs)}`}
          onClick={() => onSelect(r.span.spanId)}
        >
          {r.span.name}
        </button>
      ))}
    </div>
  );
}

function LinkedLogs({ events }: { events: readonly LogEvent[] }) {
  if (events.length === 0) return <div className={l.subtle}>No log events with this span id.</div>;
  return (
    <ul className={s.logs}>
      {events.map((e) => (
        <li key={e.id}>
          <Link to={`/events/${encodeURIComponent(e.id)}`} className={s.logRow}>
            <span className={s.logTime}>{formatClock(Date.parse(e.timestamp)).slice(6)}</span>
            <LevelChip level={e.level} />
            <span className={s.logMsg}>{e.message}</span>
          </Link>
        </li>
      ))}
    </ul>
  );
}

function SpanDrawer({
  row,
  layout,
  events,
  onClose,
  traceId,
}: {
  row: SpanRow;
  layout: TraceLayout;
  events: LogEvent[];
  onClose: () => void;
  traceId: string;
}) {
  const [tab, setTab] = useState<SpanTab>('attributes');
  const navigate = useNavigate();
  const span = row.span;
  const parent = layout.byId.get(span.parentSpanId);
  const linked = events.filter((e) => e.spanId === span.spanId);
  const attrs: [string, unknown][] = [
    ['status', span.statusCode.toUpperCase()],
    ...(span.statusMessage !== '' ? [['status_message', span.statusMessage] as [string, unknown]] : []),
    ['service', span.service],
    ['span_id', span.spanId],
    ...(span.parentSpanId !== '' ? [['parent_span_id', span.parentSpanId] as [string, unknown]] : []),
    ...Object.entries(span.attributes),
  ];
  return (
    <aside className={s.drawer} aria-label={`Span ${span.name}`}>
      <header className={s.drawerHead}>
        <ServiceDot service={span.service} />
        <span className={s.drawerTitle}>{span.name}</span>
        {row.error ? <StatusChip variant="firing">error</StatusChip> : null}
        <Button
          variant="ghost"
          size="sm"
          iconOnly
          icon={<X size={16} />}
          aria-label="Close span"
          onClick={onClose}
          className={s.drawerClose}
        />
      </header>
      <div className={s.drawerBody}>
        <div className={s.statCards}>
          <div className={s.statCard}>
            <span className={l.sectionLabel}>Duration</span>
            <strong>{formatDuration(span.durationMs)}</strong>
            <span className={cx(s.statNote, layout.critical.has(span.spanId) && s.statDanger)}>
              {Math.round((span.durationMs / Math.max(1, layout.duration)) * 100)}% of trace
            </span>
          </div>
          <div className={s.statCard}>
            <span className={l.sectionLabel}>Started</span>
            <strong>+{Math.round(row.offset).toLocaleString('en-US')} ms</strong>
            <span className={s.statNote}>{formatClock(Date.parse(span.start))}</span>
          </div>
          <div className={s.statCard}>
            <span className={l.sectionLabel}>Kind</span>
            <strong>{span.kind === '' ? '—' : span.kind}</strong>
            <span className={s.statNote}>
              {parent !== undefined
                ? `parent: ${parent.name}`
                : span.parentSpanId !== ''
                  ? 'parent span not visible'
                  : 'root span'}
            </span>
          </div>
        </div>
        <Tabs<SpanTab>
          label="Span detail"
          value={tab}
          onChange={setTab}
          items={[
            { value: 'attributes', label: 'Attributes' },
            { value: 'events', label: 'Events', count: span.events.length },
            { value: 'logs', label: 'Logs', count: linked.length },
            { value: 'raw', label: 'Raw' },
          ]}
        />
        {tab === 'attributes' ? (
          <>
            <dl className={l.kv}>
              {attrs.map(([k, v]) => (
                <div key={k} className={s.kvRow}>
                  <dt>{k}</dt>
                  <dd className={cx(k === 'status' && row.error && s.statDanger)}>{renderValue(v)}</dd>
                </div>
              ))}
            </dl>
            <div className={l.sectionLabel}>Linked logs · same span</div>
            <LinkedLogs events={linked} />
          </>
        ) : null}
        {tab === 'events' ? (
          span.events.length === 0 ? (
            <div className={l.subtle}>No span events.</div>
          ) : (
            <ul className={s.logs}>
              {span.events.map((e, i) => (
                <li key={i} className={s.spanEvent}>
                  <span className={s.logTime}>{formatClock(Date.parse(e.time))}</span>
                  <strong>{e.name}</strong>
                  <span className={l.muted}>
                    {Object.entries(e.attributes ?? {})
                      .map(([k, v]) => `${k}=${renderValue(v)}`)
                      .join(' ')}
                  </span>
                </li>
              ))}
            </ul>
          )
        ) : null}
        {tab === 'logs' ? <LinkedLogs events={linked} /> : null}
        {tab === 'raw' ? <JsonView value={span} /> : null}
      </div>
      <footer className={s.drawerFoot}>
        <Button
          variant="highlight"
          onClick={() => navigate(`/events?q=${encodeURIComponent(`trace_id:${traceId}`)}`)}
        >
          Open logs in explorer
        </Button>
        <Button
          variant="ghost"
          onClick={() => navigate(`/traces?q=${encodeURIComponent(`service:${span.service}`)}`)}
        >
          Filter spans by service
        </Button>
      </footer>
    </aside>
  );
}

export default function TracePage() {
  const { traceId = '' } = useParams();
  const api = useApi();
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const [view, setView] = useState<View>('waterfall');
  const trace = useQuery({ queryKey: ['trace', traceId], queryFn: () => search.trace(api, traceId) });
  const layout = useMemo(() => layoutTrace(trace.data?.spans ?? []), [trace.data]);
  const selected = params.get('span');
  const row = layout.rows.find((r) => r.span.spanId === selected);
  const root = layout.rows[0]?.span;
  const errors = layout.rows.filter((r) => r.error).length;
  useTopbarStatus(
    trace.data === undefined ? null : `${layout.rows.length} spans · ${formatDuration(layout.duration)}`,
  );
  const select = (id: string | null): void =>
    setParams((p) => patchParams(p, { span: id }), { replace: true });

  if (trace.isPending) return <Spinner />;
  if (trace.isError) {
    return (
      <div className={s.tracePage}>
        <Notice kind="danger">{errorMessage(trace.error)}</Notice>
      </div>
    );
  }
  if (root === undefined)
    return <EmptyState title="Trace has no spans" hint="Only log events reference this trace id." />;
  const copy = async (): Promise<void> => {
    const ok = await copyText(traceId);
    toast.push({ kind: ok ? 'success' : 'warning', title: ok ? 'Trace id copied' : 'Copy failed' });
  };
  return (
    <div className={s.traceLayout}>
      <div className={s.tracePage}>
        <nav className={l.breadcrumb} aria-label="Breadcrumb">
          <Link to="/traces">Traces</Link> / <span className={s.traceId}>{traceId}</span>
          <Button
            variant="ghost"
            size="sm"
            iconOnly
            icon={<Copy size={14} />}
            aria-label="Copy trace id"
            onClick={() => void copy()}
          />
        </nav>
        <div className={s.traceHead}>
          <h1 className={s.traceTitle}>{root.name}</h1>
          {errors > 0 ? (
            <StatusChip variant="firing">error</StatusChip>
          ) : (
            <StatusChip variant="ok">ok</StatusChip>
          )}
          <span className={s.traceMeta}>
            {formatDuration(layout.duration)} · {layout.rows.length} spans · {layout.services.length} services
            · {formatClock(layout.start)}
          </span>
          <span className={s.traceActions}>
            <Segmented<View>
              label="Trace view"
              value={view}
              onChange={setView}
              options={[
                { value: 'waterfall', label: 'Waterfall' },
                { value: 'flame', label: 'Flame' },
              ]}
            />
            <Link className={s.logsLink} to={`/events?q=${encodeURIComponent(`trace_id:${traceId}`)}`}>
              <List size={14} aria-hidden="true" /> Logs for trace <Tag>{trace.data.events.length}</Tag>
            </Link>
          </span>
        </div>
        <div className={s.legendRow}>
          {layout.services.map((svc) => (
            <span key={svc} className={s.service}>
              <ServiceDot service={svc} />
              {svc}
            </span>
          ))}
          <span className={s.criticalKey}>
            <span className={s.criticalSwatch} aria-hidden="true" /> critical path
          </span>
        </div>
        {view === 'waterfall' ? (
          <Waterfall
            layout={layout}
            selected={selected}
            onSelect={(id) => select(id === selected ? null : id)}
          />
        ) : (
          <Flame layout={layout} selected={selected} onSelect={(id) => select(id)} />
        )}
      </div>
      {row !== undefined ? (
        <SpanDrawer
          row={row}
          layout={layout}
          events={trace.data.events}
          onClose={() => select(null)}
          traceId={traceId}
        />
      ) : null}
    </div>
  );
}
