// SPDX-License-Identifier: Apache-2.0
//
// Log explorer (SPEC §16.3, DESIGN §9.1–§9.3, §9.11, §9.12): query bar with validation and
// autocomplete, volume histogram with brush zoom, facet rail with signals, a virtualized event
// stream with inline detail, live tail over SSE, and table / timeseries results with "Add to
// dashboard". Query, range, signals, live and the selected event are all in the URL.

import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { Bookmark, Download, LayoutGrid, Link2, MoreHorizontal, Columns3, Filter, Play } from 'lucide-react';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { search, signals as signalsApi } from '../../api/endpoints';
import type { LogEvent, QueryResponse } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { useFocusTarget, useTopbarStatus } from '../../app/shellContext';
import { listParam, patchParams, useTimeRange } from '../../app/url';
import { Button, Kbd, Segmented, SignalChip, StatusChip } from '../../components/controls';
import { EmptyState, Notice } from '../../components/layout';
import { MenuItem, MenuSeparator, Modal, Popover, useToast } from '../../components/overlay';
import { QueryEditor } from '../../components/QueryEditor';
import { copyText, downloadUrl } from '../../lib/download';
import { formatInteger } from '../../lib/format';
import { PHONE_QUERY, useMediaQuery, useStoredState } from '../../lib/hooks';
import { isStringArray, STORAGE_KEYS } from '../../lib/storage';
import { formatClock, rangeLabel, resolveTime } from '../../lib/time';
import { isTypingTarget } from '../../app/hotkeys';
import { AddToDashboardDialog } from '../dashboards/AddToDashboard';
import { errorFromApi, useQueryCheck } from '../query/useQueryCheck';
import { EventDetail } from './EventDetail';
import { EventList, type Density } from './EventList';
import { FacetRail } from './FacetRail';
import { Histogram, histogramTotal } from './Histogram';
import { chartTitle, ResultsView } from './ResultsView';
import { SaveSignalDialog } from './SaveSignalDialog';
import { appendTerm, hasStages, term } from './terms';
import s from './ExplorerPage.module.css';

export const PAGE_SIZE = 200;
export const DEFAULT_FACETS = ['service', 'level', 'environment', 'host'];
const MAX_HISTORY = 50;
const MAX_LIVE = 5_000;
const LIVE_REFRESH_MS = 5_000;
const DAY_MS = 86_400_000;

const isDensity = (v: unknown): v is Density => v === 'compact' || v === 'comfortable';

/** Adds `q` to the front of the history (deduplicated, bounded). */
export function pushHistory(history: readonly string[], q: string): string[] {
  const t = q.trim();
  if (t === '') return [...history];
  return [t, ...history.filter((h) => h !== t)].slice(0, MAX_HISTORY);
}

/** Merges live events in front of query pages, newest first and without duplicates. */
export function mergeEvents(live: readonly LogEvent[], pages: readonly LogEvent[]): LogEvent[] {
  const seen = new Set<string>();
  const out: LogEvent[] = [];
  for (const ev of [...live, ...pages]) {
    if (seen.has(ev.id)) continue;
    seen.add(ev.id);
    out.push(ev);
  }
  return out;
}

function useExplorerUrl() {
  const [params, setParams] = useSearchParams();
  const { id } = useParams();
  const navigate = useNavigate();
  const q = params.get('q') ?? '';
  const activeSignals = listParam(params, 'signals');
  const live = params.get('live') === '1';
  const go = useCallback(
    (patch: Record<string, string | null>, eventId: string | null | undefined = id, replace = false) => {
      const next = patchParams(params, patch);
      const qs = next.toString();
      const path =
        eventId === null || eventId === undefined ? '/events' : `/events/${encodeURIComponent(eventId)}`;
      navigate(`${path}${qs === '' ? '' : `?${qs}`}`, { replace });
    },
    [params, id, navigate],
  );
  return { params, setParams, q, activeSignals, live, selectedId: id ?? null, go };
}

function ToolbarMenu({
  onExport,
  onShare,
  onColumns,
}: {
  onExport: (f: 'csv' | 'ndjson') => void;
  onShare: () => void;
  onColumns: () => void;
}) {
  return (
    <Popover
      align="right"
      trigger={({ open, toggle }) => (
        <Button
          variant="ghost"
          iconOnly
          icon={<MoreHorizontal size={16} />}
          aria-label="More actions"
          aria-expanded={open}
          onClick={toggle}
        />
      )}
    >
      {(close) => (
        <div role="menu" aria-label="More actions">
          <MenuItem
            icon={<Download size={14} aria-hidden="true" />}
            onClick={() => {
              close();
              onExport('csv');
            }}
          >
            Export CSV
          </MenuItem>
          <MenuItem
            icon={<Download size={14} aria-hidden="true" />}
            onClick={() => {
              close();
              onExport('ndjson');
            }}
          >
            Export JSON (NDJSON)
          </MenuItem>
          <MenuSeparator />
          <MenuItem
            icon={<Link2 size={14} aria-hidden="true" />}
            onClick={() => {
              close();
              onShare();
            }}
          >
            Share link
          </MenuItem>
          <MenuItem
            icon={<Columns3 size={14} aria-hidden="true" />}
            onClick={() => {
              close();
              onColumns();
            }}
          >
            Column settings
          </MenuItem>
        </div>
      )}
    </Popover>
  );
}

function ColumnsDialog({
  pinned,
  onChange,
  onClose,
  candidates,
}: {
  pinned: readonly string[];
  onChange: (p: string[]) => void;
  onClose: () => void;
  candidates: readonly string[];
}) {
  return (
    <Modal
      title="Columns"
      onClose={onClose}
      footer={
        <Button variant="primary" onClick={onClose}>
          Done
        </Button>
      }
    >
      <p className={s.dialogNote}>
        Pinned property columns appear before the message. Saved in this browser.
      </p>
      <ul className={s.columnList}>
        {candidates.map((c) => {
          const on = pinned.includes(c);
          return (
            <li key={c}>
              <label>
                <input
                  type="checkbox"
                  checked={on}
                  onChange={() => onChange(on ? pinned.filter((p) => p !== c) : [...pinned, c])}
                />
                <span>{c}</span>
              </label>
            </li>
          );
        })}
      </ul>
    </Modal>
  );
}

export default function ExplorerPage() {
  const api = useApi();
  const { tail, now } = useServices();
  const toast = useToast();
  const { range, setRange } = useTimeRange();
  const { setParams, q, activeSignals, live, selectedId, go } = useExplorerUrl();
  const phone = useMediaQuery(PHONE_QUERY);
  const [draft, setDraft] = useState(q);
  useEffect(() => setDraft(q), [q]);
  const check = useQueryCheck(draft);
  const [history, setHistory] = useStoredState<string[]>(STORAGE_KEYS.history, [], isStringArray);
  const [density, setDensity] = useStoredState<Density>(STORAGE_KEYS.density, 'compact', isDensity);
  const [pinned, setPinned] = useStoredState<string[]>(STORAGE_KEYS.columns, [], isStringArray);
  const [openFields, setOpenFields] = useState<string[]>([]);
  const [cursor, setCursor] = useState(-1);
  const [dialog, setDialog] = useState<'signal' | 'dashboard' | 'columns' | 'fields' | null>(null);
  const [actionsFor, setActionsFor] = useState<string | null>(null);
  const focusRef = useRef<(() => void) | null>(null);
  const focusQuery = useCallback(() => focusRef.current?.(), []);
  useFocusTarget(focusQuery);

  const signalsQ = useQuery({ queryKey: ['signals'], queryFn: () => signalsApi.list(api) });
  const logSignals = useMemo(() => (signalsQ.data ?? []).filter((x) => x.source === 'logs'), [signalsQ.data]);
  const fieldsQ = useQuery({
    queryKey: ['fields', 'logs', range.from, range.to],
    queryFn: ({ signal }) => search.fields(api, { source: 'logs', ...range }, signal),
    staleTime: 60_000,
  });

  const results = useInfiniteQuery({
    queryKey: ['query', q, range.from, range.to, activeSignals.join(',')],
    queryFn: ({ pageParam, signal }) =>
      search.query(api, { q, ...range, limit: PAGE_SIZE, cursor: pageParam, signals: activeSignals }, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: QueryResponse) => (last.kind === 'events' ? last.nextCursor : undefined),
  });
  const first = results.data?.pages[0];
  const kind = first?.kind ?? null;
  const isEvents = kind === null || kind === 'events';

  const histQ = useQuery({
    queryKey: ['histogram', q, range.from, range.to, activeSignals.join(',')],
    queryFn: ({ signal }) =>
      search.histogram(api, { q, ...range, buckets: phone ? 40 : 80, signals: activeSignals }, signal),
    enabled: isEvents && !hasStages(q),
    refetchInterval: live ? LIVE_REFRESH_MS : false,
  });
  const facetFields = useMemo(
    () => [...DEFAULT_FACETS, ...openFields.filter((f) => !DEFAULT_FACETS.includes(f))],
    [openFields],
  );
  const facetsQ = useQuery({
    queryKey: ['facets', q, range.from, range.to, activeSignals.join(','), facetFields.join(',')],
    queryFn: ({ signal }) =>
      search.facets(api, { q, ...range, fields: facetFields, top: 10, signals: activeSignals }, signal),
    enabled: isEvents && !hasStages(q),
  });

  // ---- live tail -----------------------------------------------------------------------------
  const [liveEvents, setLiveEvents] = useState<LogEvent[]>([]);
  const [newCount, setNewCount] = useState(0);
  const atTop = useRef(true);
  const [tailError, setTailError] = useState<string | null>(null);
  useEffect(() => {
    setLiveEvents([]);
    setNewCount(0);
    setTailError(null);
    if (!live || hasStages(q)) return undefined;
    return tail.open(
      { q, signals: activeSignals },
      {
        onEvents: (evs) => {
          const sorted = [...evs].sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
          setLiveEvents((cur) => [...sorted, ...cur].slice(0, MAX_LIVE));
          if (!atTop.current) setNewCount((n) => n + evs.length);
        },
        onDropped: (n) =>
          toast.push({
            kind: 'warning',
            title: `${n} events skipped`,
            body: 'The live tail is limited to 50 events per second.',
          }),
        onError: (m) => setTailError(m),
      },
    );
    // activeSignals is derived from the URL; its joined form is the dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [live, q, activeSignals.join(','), tail, toast]);

  const pageEvents = useMemo(
    () =>
      (results.data?.pages ?? []).flatMap((p) =>
        p.kind === 'events' ? ((p.events ?? []) as LogEvent[]) : [],
      ),
    [results.data],
  );
  const selectedQ = useQuery({
    queryKey: ['event', selectedId],
    queryFn: () => search.event(api, selectedId ?? ''),
    enabled: selectedId !== null && !pageEvents.some((e) => e.id === selectedId),
  });
  const events = useMemo(() => {
    const merged = mergeEvents(liveEvents, pageEvents);
    const sel = selectedQ.data;
    return sel !== undefined && !merged.some((e) => e.id === sel.id) ? [sel, ...merged] : merged;
  }, [liveEvents, pageEvents, selectedQ.data]);

  // ---- actions -------------------------------------------------------------------------------
  const run = useCallback(
    (text: string) => {
      setHistory(pushHistory(history, text));
      setCursor(-1);
      go({ q: text.trim() === '' ? null : text.trim(), live: null }, null);
    },
    [go, history, setHistory],
  );
  const onDraft = (v: string): void => {
    setDraft(v);
    if (live && v !== q) setParams((p) => patchParams(p, { live: null }), { replace: true });
  };
  const addTerm = useCallback(
    (field: string, value: string, exclude: boolean) => run(appendTerm(q, term(field, value, exclude))),
    [q, run],
  );
  const toggleSignal = (sid: string): void => {
    const next = activeSignals.includes(sid)
      ? activeSignals.filter((x) => x !== sid)
      : [...activeSignals, sid];
    go({ signals: next.length === 0 ? null : next.join(',') });
  };
  const toggleEvent = useCallback(
    (ev: LogEvent) => {
      if (phone && selectedId !== ev.id && actionsFor !== ev.id) {
        setActionsFor(ev.id);
        return;
      }
      setActionsFor(null);
      go({}, selectedId === ev.id ? null : ev.id, true);
    },
    [go, selectedId, phone, actionsFor],
  );
  const exportAs = (format: 'csv' | 'ndjson'): void =>
    downloadUrl(search.exportUrl(api, { q, ...range, format, signals: activeSignals }));
  const share = async (): Promise<void> => {
    const ok = await copyText(window.location.href);
    toast.push({
      kind: ok ? 'success' : 'warning',
      title: ok ? 'Link copied' : 'Copy failed — select the address bar instead',
    });
  };
  const togglePin = (key: string): void =>
    setPinned(pinned.includes(key) ? pinned.filter((p) => p !== key) : [...pinned, key]);
  const zoom = (fromMs: number, toMs: number): void => {
    setRange({ from: new Date(fromMs).toISOString(), to: new Date(toMs).toISOString() });
  };

  // ---- keyboard: j / k / Enter (DESIGN §8) ---------------------------------------------------
  const latest = useRef({ events, cursor, toggleEvent });
  latest.current = { events, cursor, toggleEvent };
  useEffect(() => {
    const onKey = (e: KeyboardEvent): void => {
      if (e.defaultPrevented || e.ctrlKey || e.metaKey || e.altKey || isTypingTarget(e.target)) return;
      if (document.querySelector('[aria-modal="true"]') !== null) return;
      const { events: evs, cursor: c, toggleEvent: toggle } = latest.current;
      if (e.key === 'j' || e.key === 'k') {
        e.preventDefault();
        setCursor(Math.max(0, Math.min(evs.length - 1, c + (e.key === 'j' ? 1 : -1))));
      } else if (e.key === 'Enter' && c >= 0) {
        const ev = evs[c];
        if (
          ev !== undefined &&
          !(e.target instanceof HTMLButtonElement || e.target instanceof HTMLAnchorElement)
        ) {
          e.preventDefault();
          toggle(ev);
        }
      } else if (e.key === 'Escape' && selectedId !== null) {
        go({}, null, true);
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [go, selectedId]);

  // ---- status line ---------------------------------------------------------------------------
  const total = histogramTotal(histQ.data);
  const status = useMemo(() => {
    if (first === undefined) return null;
    if (selectedId !== null && !live) return `paused · event ${selectedId.slice(0, 13)} selected`;
    if (first.kind === 'timeseries') {
      const buckets = new Set((first.series ?? []).flatMap((x) => x.points.map((p) => p[0]))).size;
      return `${buckets} buckets · ${(first.series ?? []).length} series · ${first.stats.elapsedMs} ms`;
    }
    if (first.kind === 'table') return `${(first.rows ?? []).length} rows · ${first.stats.elapsedMs} ms`;
    return `${formatClock(now(), false)} · ${formatInteger(total || events.length)} events · ${first.stats.elapsedMs} ms`;
  }, [first, total, events.length, selectedId, live, now]);
  useTopbarStatus(status);

  const queryError = check.error ?? errorFromApi(q, results.error);
  const showDate = resolveTime(range.to, now()) - resolveTime(range.from, now()) > DAY_MS;
  const candidates = (fieldsQ.data ?? []).filter((f) => !f.builtin).map((f) => f.name);

  const rail = (
    <FacetRail
      signals={logSignals}
      activeSignals={activeSignals}
      onToggleSignal={toggleSignal}
      facets={facetsQ.data}
      facetsLoading={facetsQ.isFetching}
      fields={fieldsQ.data ?? []}
      openFields={openFields}
      onToggleField={(f) => setOpenFields((xs) => (xs.includes(f) ? xs.filter((x) => x !== f) : [...xs, f]))}
      onTerm={addTerm}
      className={s.rail}
    />
  );

  const kindChip =
    check.kind !== null && check.kind !== 'events' ? (
      <StatusChip variant="info">{check.kind}</StatusChip>
    ) : null;
  return (
    <div className={s.page}>
      <div className={s.queryRow}>
        <QueryEditor
          value={draft}
          onChange={onDraft}
          onSubmit={run}
          ariaLabel="Query"
          error={queryError}
          tone={check.interpretedAsText ? 'text' : null}
          history={history}
          placeholder="Search events — try level:error or press ?"
          className={s.editor}
          onView={(v) => {
            focusRef.current = v === null ? null : () => v.focus();
          }}
          trailing={
            <>
              {check.interpretedAsText ? (
                <span className={s.textNote}>interpreted as free text</span>
              ) : (
                kindChip
              )}
              <Kbd>?</Kbd>
              <Kbd>Ctrl ↵</Kbd>
            </>
          }
        />
        <div className={s.queryActions}>
          <Button variant="ghost" icon={<Play size={14} />} onClick={() => run(draft)}>
            Run
          </Button>
          <Button
            variant="ghost"
            icon={<Bookmark size={14} />}
            disabled={hasStages(q) || q.trim() === ''}
            onClick={() => setDialog('signal')}
            title={hasStages(q) ? 'Signals are filters without pipe stages' : undefined}
          >
            Save as signal
          </Button>
          <Button
            variant={kind === 'events' || kind === null ? 'ghost' : 'highlight'}
            icon={<LayoutGrid size={14} />}
            disabled={q.trim() === ''}
            onClick={() => setDialog('dashboard')}
          >
            Add to dashboard
          </Button>
          <ToolbarMenu
            onExport={exportAs}
            onShare={() => void share()}
            onColumns={() => setDialog('columns')}
          />
        </div>
      </div>
      {phone ? (
        <div className={s.mobileStrip}>
          <Button size="sm" variant="ghost" icon={<Filter size={14} />} onClick={() => setDialog('fields')}>
            Fields <span className={s.count}>{activeSignals.length}</span>
          </Button>
          {logSignals.map((sig) => (
            <SignalChip
              key={sig.id}
              active={activeSignals.includes(sig.id)}
              onToggle={() => toggleSignal(sig.id)}
            >
              {sig.name}
            </SignalChip>
          ))}
        </div>
      ) : null}
      <div className={s.body}>
        {!phone && isEvents ? rail : null}
        <section className={s.stream} aria-label="Results">
          {results.isError && queryError === null ? (
            <Notice kind="danger" className={s.notice}>
              {errorMessage(results.error)}
            </Notice>
          ) : null}
          {tailError !== null ? (
            <Notice kind="warning" className={s.notice}>
              {tailError}
            </Notice>
          ) : null}
          {!isEvents && first !== undefined ? (
            <ResultsView resp={first} q={q} rangeText={rangeLabel(range)} />
          ) : (
            <>
              {histQ.data !== undefined && (selectedId === null || live) ? (
                <Histogram data={histQ.data} onZoom={zoom} height={phone ? 64 : 120} compact={phone} />
              ) : null}
              <div className={s.toolbar}>
                <span className={s.total}>
                  <strong>{formatInteger(total || events.length)}</strong> events
                  {selectedId !== null && !live ? ' · histogram collapsed' : ''}
                </span>
                {!phone ? (
                  <>
                    <Segmented<Density>
                      label="Row density"
                      value={density}
                      onChange={setDensity}
                      options={[
                        { value: 'compact', label: 'Compact' },
                        { value: 'comfortable', label: 'Comfortable' },
                      ]}
                    />
                    <Button size="sm" variant="ghost" onClick={() => setDialog('columns')}>
                      Columns
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => exportAs('csv')}>
                      Export
                    </Button>
                  </>
                ) : (
                  <span className={s.rangeNote}>{rangeLabel(range).toLowerCase()}</span>
                )}
              </div>
              {results.isPending ? null : events.length === 0 ? (
                <EmptyState
                  title="No events match"
                  hint="Widen the time range or remove a filter."
                  action={
                    <Button size="sm" onClick={() => setRange({ from: 'now-24h', to: 'now' })}>
                      Last 24 hours
                    </Button>
                  }
                />
              ) : (
                <EventList
                  events={events}
                  selectedId={selectedId}
                  cursor={cursor}
                  onCursor={setCursor}
                  onToggle={toggleEvent}
                  density={density}
                  pinned={pinned}
                  showDate={showDate}
                  hasMore={results.hasNextPage}
                  loadingMore={results.isFetchingNextPage}
                  onLoadMore={() => void results.fetchNextPage()}
                  live={live}
                  newCount={newCount}
                  onTopChange={(top) => {
                    atTop.current = top;
                    if (top) setNewCount(0);
                  }}
                  onJumpTop={() => {
                    setCursor(0);
                    setNewCount(0);
                  }}
                  mobile={phone}
                  actionsFor={actionsFor}
                  renderActions={(ev) => (
                    <div className={s.cardActions}>
                      <Button size="sm" variant="ghost" onClick={() => go({}, ev.id, true)}>
                        Properties
                      </Button>
                      {ev.exception !== '' ? (
                        <Button size="sm" variant="ghost" onClick={() => go({}, ev.id, true)}>
                          Exception
                        </Button>
                      ) : null}
                      {ev.traceId !== '' ? (
                        <Link className={s.traceLink} to={`/traces/${encodeURIComponent(ev.traceId)}`}>
                          Trace
                        </Link>
                      ) : null}
                    </div>
                  )}
                  renderDetail={(ev) => (
                    <EventDetail
                      event={ev}
                      pinned={pinned}
                      onTerm={addTerm}
                      onPin={togglePin}
                      onClose={() => go({}, null, true)}
                      permalink={`/events/${encodeURIComponent(ev.id)}?from=${encodeURIComponent(new Date(Date.parse(ev.timestamp) - 300_000).toISOString())}&to=${encodeURIComponent(new Date(Date.parse(ev.timestamp) + 300_000).toISOString())}`}
                    />
                  )}
                />
              )}
            </>
          )}
        </section>
      </div>
      {dialog === 'signal' ? (
        <SaveSignalDialog
          tql={q}
          onClose={() => setDialog(null)}
          onSaved={(sig) => go({ signals: [...activeSignals, sig.id].join(',') })}
        />
      ) : null}
      {dialog === 'dashboard' ? (
        <AddToDashboardDialog q={q} kind={kind} title={chartTitle(q)} onClose={() => setDialog(null)} />
      ) : null}
      {dialog === 'columns' ? (
        <ColumnsDialog
          pinned={pinned}
          onChange={setPinned}
          candidates={candidates}
          onClose={() => setDialog(null)}
        />
      ) : null}
      {dialog === 'fields' ? (
        <Modal title="Fields" onClose={() => setDialog(null)}>
          {rail}
        </Modal>
      ) : null}
    </div>
  );
}
