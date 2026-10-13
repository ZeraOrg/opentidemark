// SPDX-License-Identifier: Apache-2.0
//
// Inline event detail (SPEC §16.3, DESIGN §5.10): Properties (filter / exclude / pin / copy
// actions, template-sourced values highlighted, trace/span links), Exception with collapsible
// framework frames, Context (same service & host, or same trace) and Raw JSON; permalink and
// "Open trace".

import { useQuery } from '@tanstack/react-query';
import { Activity, ChevronUp, Copy, Link2, Pin, PinOff } from 'lucide-react';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { search } from '../../api/endpoints';
import type { LogEvent } from '../../api/types';
import { useApi } from '../../app/services';
import { LevelChip, Segmented, Spinner, Tag } from '../../components/controls';
import { JsonView, Tabs } from '../../components/layout';
import { useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { copyText } from '../../lib/download';
import { renderValue, templateProperties } from '../../lib/template';
import { formatClock } from '../../lib/time';
import { parseException } from './exception';
import s from './EventDetail.module.css';

type DetailTab = 'properties' | 'exception' | 'context' | 'raw';

const HOUR_MS = 3_600_000;

export interface EventDetailProps {
  event: LogEvent;
  pinned: readonly string[];
  onTerm: (field: string, value: string, exclude: boolean) => void;
  onPin: (key: string) => void;
  onClose: () => void;
  permalink: string;
}

interface PropRow {
  key: string;
  field: string;
  value: unknown;
  builtin: boolean;
}

function propRows(ev: LogEvent): PropRow[] {
  const rows: PropRow[] = [
    { key: '@timestamp', field: '@timestamp', value: ev.timestamp, builtin: true },
    { key: 'level', field: 'level', value: ev.level, builtin: true },
    { key: 'service', field: 'service', value: ev.service, builtin: true },
    { key: 'environment', field: 'environment', value: ev.environment, builtin: true },
    { key: 'host', field: 'host', value: ev.host, builtin: true },
    { key: 'template', field: 'template', value: ev.messageTemplate, builtin: true },
  ];
  for (const [k, v] of Object.entries(ev.properties))
    rows.push({ key: `p.${k}`, field: k, value: v, builtin: false });
  rows.push(
    { key: 'trace_id', field: 'trace_id', value: ev.traceId, builtin: true },
    { key: 'span_id', field: 'span_id', value: ev.spanId, builtin: true },
    { key: 'event_type', field: 'event_type', value: ev.eventType, builtin: true },
  );
  return rows.filter((r) => !(r.builtin && (r.value === '' || r.value === null || r.value === undefined)));
}

function PropertiesCard({
  event: ev,
  pinned,
  onTerm,
  onPin,
}: Omit<EventDetailProps, 'onClose' | 'permalink'>) {
  const toast = useToast();
  const fromTemplate = templateProperties(ev.messageTemplate);
  const rows = propRows(ev);
  return (
    <section className={s.card} aria-label="Properties">
      <header className={s.cardHead}>
        <h3>Properties</h3>
        <Tag>{rows.length}</Tag>
        <span className={s.hint}>hover a row for filter · exclude · pin · copy</span>
      </header>
      <dl className={s.props}>
        {rows.map((r) => {
          const text = renderValue(r.value);
          const isPinned = pinned.includes(r.field);
          return (
            <div key={r.key} className={s.propRow}>
              <dt>{r.field}</dt>
              <dd>
                {r.field === 'level' ? (
                  <LevelChip level={String(r.value)} />
                ) : r.field === 'trace_id' ? (
                  <Link to={`/traces/${encodeURIComponent(text)}`} className={s.mono}>
                    {text}
                  </Link>
                ) : r.field === 'span_id' && ev.traceId !== '' ? (
                  <Link
                    to={`/traces/${encodeURIComponent(ev.traceId)}?span=${encodeURIComponent(text)}`}
                    className={s.mono}
                  >
                    {text}
                  </Link>
                ) : (
                  <span
                    className={cx(
                      s.value,
                      !r.builtin && fromTemplate.has(r.field) && s.pv,
                      r.field === 'template' && s.template,
                    )}
                  >
                    {text}
                  </span>
                )}
                {typeof r.value === 'number' ? <span className={s.type}>number</span> : null}
                {r.field !== '@timestamp' && r.field !== 'template' ? (
                  <span className={s.actions}>
                    <button
                      type="button"
                      aria-label={`Filter ${r.field} = ${text}`}
                      title="Filter ="
                      onClick={() => onTerm(r.field, text, false)}
                    >
                      =
                    </button>
                    <button
                      type="button"
                      aria-label={`Exclude ${r.field} = ${text}`}
                      title="Exclude"
                      onClick={() => onTerm(r.field, text, true)}
                    >
                      ≠
                    </button>
                    {!r.builtin ? (
                      <button
                        type="button"
                        aria-label={isPinned ? `Unpin column ${r.field}` : `Pin column ${r.field}`}
                        title={isPinned ? 'Unpin column' : 'Pin column'}
                        onClick={() => onPin(r.field)}
                      >
                        {isPinned ? <PinOff size={13} /> : <Pin size={13} />}
                      </button>
                    ) : null}
                    <button
                      type="button"
                      aria-label={`Copy ${r.field}`}
                      title="Copy"
                      onClick={() =>
                        void copyText(text).then((ok) =>
                          toast.push({
                            kind: ok ? 'success' : 'warning',
                            title: ok ? 'Copied' : 'Copy failed',
                          }),
                        )
                      }
                    >
                      <Copy size={13} />
                    </button>
                  </span>
                ) : null}
              </dd>
            </div>
          );
        })}
      </dl>
    </section>
  );
}

function ExceptionCard({ text, full }: { text: string; full?: boolean }) {
  const [collapse, setCollapse] = useState(true);
  const toast = useToast();
  const ex = parseException(text);
  const shortType = ex.type.split('.').pop() ?? ex.type;
  let hidden = 0;
  const lines: { text: string; location: string | null }[] = [];
  for (const f of ex.frames) {
    if (collapse && f.framework) {
      hidden++;
      continue;
    }
    lines.push({ text: f.text, location: f.location });
  }
  return (
    <section className={cx(s.card, full === true && s.full)} aria-label="Exception">
      <header className={s.cardHead}>
        <h3 className={s.exTitle}>Exception</h3>
        {shortType !== '' ? <Tag>{shortType}</Tag> : null}
        <span className={s.cardActions}>
          <button type="button" className={s.textButton} onClick={() => setCollapse(!collapse)}>
            {collapse ? 'Expand frames' : 'Collapse frames'}
          </button>
          <button
            type="button"
            className={s.textButton}
            onClick={() =>
              void copyText(text).then((ok) =>
                toast.push({
                  kind: ok ? 'success' : 'warning',
                  title: ok ? 'Exception copied' : 'Copy failed',
                }),
              )
            }
          >
            Copy
          </button>
        </span>
      </header>
      <pre className={s.stack} tabIndex={0}>
        <span className={s.exHead}>{ex.headline}</span>
        {lines.map((l, i) => {
          const loc = l.location;
          const at = loc === null ? -1 : l.text.indexOf(loc);
          return (
            <span key={i} className={s.frame}>
              {'\n'}
              {at < 0 || loc === null ? (
                l.text
              ) : (
                <>
                  {l.text.slice(0, at)}
                  <span className={s.loc}>{loc}</span>
                  {l.text.slice(at + loc.length)}
                </>
              )}
            </span>
          );
        })}
        {hidden > 0 ? (
          <span className={s.collapsed}>{`\n   … ${hidden} framework frames collapsed`}</span>
        ) : null}
      </pre>
    </section>
  );
}

type ContextScope = 'service' | 'trace';

function ContextCard({ ev, full }: { ev: LogEvent; full?: boolean }) {
  const api = useApi();
  const [scope, setScope] = useState<ContextScope>(ev.traceId !== '' ? 'trace' : 'service');
  const t = Date.parse(ev.timestamp);
  const ctx = useQuery({
    queryKey: ['context', ev.id, scope],
    queryFn: async (): Promise<LogEvent[]> => {
      if (scope === 'service') {
        const c = await search.context(api, ev.id, 5, 5);
        return [...c.before, c.event, ...c.after];
      }
      const r = await search.query(api, {
        q: `trace_id:${ev.traceId}`,
        from: new Date(t - HOUR_MS).toISOString(),
        to: new Date(t + HOUR_MS).toISOString(),
        limit: 50,
      });
      const list = (r.events ?? []) as LogEvent[];
      return [...list].sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp));
    },
  });
  const all = ctx.data ?? [];
  const idx = all.findIndex((e) => e.id === ev.id);
  const rows = idx < 0 ? all.slice(0, 11) : all.slice(Math.max(0, idx - 5), idx + 6);
  return (
    <section className={cx(s.card, full === true && s.full)} aria-label="Context">
      <header className={s.cardHead}>
        <h3>Context</h3>
        <span className={s.hint}>±5 events · {scope === 'trace' ? 'same trace' : 'same service + host'}</span>
        <span className={s.cardActions}>
          <Segmented<ContextScope>
            label="Context scope"
            value={scope}
            onChange={setScope}
            options={[
              ...(ev.traceId !== '' ? [{ value: 'trace' as const, label: 'Trace' }] : []),
              { value: 'service', label: 'Service · Host' },
            ]}
          />
        </span>
      </header>
      {ctx.isPending ? (
        <div className={s.loading}>
          <Spinner size={14} />
        </div>
      ) : ctx.isError ? (
        <div className={s.hintBlock}>Context is not available.</div>
      ) : (
        <ul className={s.context}>
          {rows.map((e) => (
            <li key={e.id} className={cx(e.id === ev.id && s.current)}>
              <span className={s.ctxTime}>{formatClock(Date.parse(e.timestamp))}</span>
              <LevelChip level={e.level} />
              <span className={s.ctxMsg}>{e.message}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

export function EventDetail(props: EventDetailProps) {
  const { onClose, permalink } = props;
  const api = useApi();
  const full = useQuery({
    queryKey: ['event', props.event.id],
    queryFn: () => search.event(api, props.event.id),
    placeholderData: props.event,
    staleTime: 60_000,
  });
  const ev = full.data ?? props.event;
  const [tab, setTab] = useState<DetailTab>('properties');
  const tabs = [
    { value: 'properties' as const, label: 'Properties' },
    ...(ev.exception !== '' ? [{ value: 'exception' as const, label: 'Exception' }] : []),
    { value: 'context' as const, label: 'Context' },
    { value: 'raw' as const, label: 'Raw JSON' },
  ];
  return (
    <div className={s.detail} role="region" aria-label={`Event ${ev.id}`}>
      <div className={s.head}>
        <Tabs<DetailTab> items={tabs} value={tab} onChange={setTab} label="Event detail" className={s.tabs} />
        <span className={s.meta}>
          {ev.timestamp} · stream <strong>{ev.stream}</strong>
          {ev.protocol !== '' ? ` · ${ev.protocol} protocol` : ''}
        </span>
        <span className={s.headActions}>
          <Link to={permalink} className={s.headLink}>
            <Link2 size={14} aria-hidden="true" /> Permalink
          </Link>
          {ev.traceId !== '' ? (
            <Link to={`/traces/${encodeURIComponent(ev.traceId)}`} className={s.headLink}>
              <Activity size={14} aria-hidden="true" /> Open trace
            </Link>
          ) : null}
          <button type="button" className={s.collapse} aria-label="Collapse event" onClick={onClose}>
            <ChevronUp size={16} />
          </button>
        </span>
      </div>
      {tab === 'properties' ? (
        <div className={s.grid}>
          <PropertiesCard event={ev} pinned={props.pinned} onTerm={props.onTerm} onPin={props.onPin} />
          <div className={s.side}>
            {ev.exception !== '' ? <ExceptionCard text={ev.exception} /> : null}
            <ContextCard ev={ev} />
          </div>
        </div>
      ) : null}
      {tab === 'exception' ? <ExceptionCard text={ev.exception} full /> : null}
      {tab === 'context' ? <ContextCard ev={ev} full /> : null}
      {tab === 'raw' ? <JsonView value={ev} className={s.raw} /> : null}
    </div>
  );
}
