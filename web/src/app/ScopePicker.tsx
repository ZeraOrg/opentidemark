// SPDX-License-Identifier: Apache-2.0
//
// Scope picker (SPEC §16.1, §16.6): the top-bar control that narrows the Events, Traces, Metrics
// and dashboard views to some of the caller's visible streams and, for logs, to environments.
// Desktop: a popover with two fieldsets (streams, searchable above SEARCH_THRESHOLD; environments
// from the `environment` facet of the last 24 hours) and Apply. Phone: an icon trigger with a
// count badge that opens the same panel as a bottom sheet. A caller who can see exactly one
// stream gets a static chip instead; a caller who can see none gets nothing (the page shows the
// empty state).

import { useQuery } from '@tanstack/react-query';
import { ChevronDown, Layers, Search } from 'lucide-react';
import { useEffect, useId, useMemo, useRef, useState, type FormEvent } from 'react';
import { useLocation, useSearchParams } from 'react-router-dom';
import { search } from '../api/endpoints';
import { Button } from '../components/controls';
import { moveFocus, SEARCH_THRESHOLD } from '../components/MultiSelect';
import { Modal, Popover } from '../components/overlay';
import { cx } from '../lib/cx';
import {
  isEmptyScope,
  rememberedScope,
  scopeDescription,
  scopeLabel,
  scopePatch,
  useDataScope,
  useVisibleStreams,
  type Scope,
} from './scope';
import { useServices } from './services';
import { patchParams } from './url';
import s from './ScopePicker.module.css';

/** Environments are listed from the last day so the list is stable while browsing. */
const ENV_RANGE = { from: 'now-24h', to: 'now' } as const;
const ENV_TOP = 50;

function useEnvironments(streams: readonly string[], enabled: boolean) {
  const { api } = useServices();
  return useQuery({
    queryKey: ['facets', 'environment', streams.join(',')],
    queryFn: ({ signal }) =>
      search.facets(
        api,
        {
          q: '',
          ...ENV_RANGE,
          fields: ['environment'],
          top: ENV_TOP,
          ...(streams.length > 0 ? { streams } : {}),
        },
        signal,
      ),
    enabled,
    staleTime: 60_000,
    select: (facets) => (facets[0]?.values ?? []).map((v) => v.value),
  });
}

function CheckRow({
  checked,
  onChange,
  children,
  mono,
  muted,
}: {
  checked: boolean;
  onChange: (on: boolean) => void;
  children: string;
  mono?: boolean;
  muted?: boolean;
}) {
  return (
    <label className={cx(s.row, muted === true && s.rowMuted)}>
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)} />
      <span className={cx(s.rowLabel, mono === true && s.mono)} title={children}>
        {children}
      </span>
    </label>
  );
}

export function ScopePanel({
  scope,
  streams,
  loadingStreams,
  onApply,
  onCancel,
}: {
  scope: Scope;
  streams: readonly string[];
  loadingStreams: boolean;
  onApply: (next: Scope) => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState<Scope>(scope);
  const [filter, setFilter] = useState('');
  const envs = useEnvironments(scope.streams, true);
  const searchable = streams.length > SEARCH_THRESHOLD;
  const shown = useMemo(() => {
    const t = filter.trim().toLowerCase();
    return t === '' ? streams : streams.filter((x) => x.toLowerCase().includes(t));
  }, [streams, filter]);
  // Keep environments that are selected but missing from the facet (older than a day).
  const envOptions = useMemo(() => {
    const list = envs.data ?? [];
    return [...list, ...draft.env.filter((e) => !list.includes(e))];
  }, [envs.data, draft.env]);
  const toggleStream = (name: string, on: boolean): void =>
    setDraft((d) => ({ ...d, streams: on ? [...d.streams, name] : d.streams.filter((x) => x !== name) }));
  const toggleEnv = (name: string, on: boolean): void =>
    setDraft((d) => ({ ...d, env: on ? [...d.env, name] : d.env.filter((x) => x !== name) }));
  const submit = (e: FormEvent): void => {
    e.preventDefault();
    onApply(draft);
  };
  const listId = useId();
  return (
    <form className={s.panel} onSubmit={submit}>
      <div className={s.columns}>
        <fieldset className={s.group} onKeyDown={(e) => moveFocus(e, 'input[type="checkbox"]')}>
          <legend className={s.legend}>
            Streams
            <span className={s.count}>
              {draft.streams.length === 0 ? 'all' : `${draft.streams.length} of ${streams.length}`}
            </span>
          </legend>
          {searchable ? (
            <label className={s.search}>
              <Search size={14} aria-hidden="true" />
              <input
                type="search"
                value={filter}
                placeholder="Filter streams"
                aria-label="Filter streams"
                aria-controls={listId}
                onChange={(e) => setFilter(e.target.value)}
              />
            </label>
          ) : null}
          <div id={listId} className={s.list}>
            {filter === '' ? (
              <CheckRow
                checked={draft.streams.length === 0}
                onChange={() => setDraft((d) => ({ ...d, streams: [] }))}
              >
                All my streams
              </CheckRow>
            ) : null}
            {loadingStreams ? <div className={s.empty}>Loading streams…</div> : null}
            {shown.map((name) => (
              <CheckRow
                key={name}
                mono
                checked={draft.streams.includes(name)}
                onChange={(on) => toggleStream(name, on)}
              >
                {name}
              </CheckRow>
            ))}
            {!loadingStreams && shown.length === 0 ? (
              <div className={s.empty}>{filter === '' ? 'No streams yet.' : 'No stream matches.'}</div>
            ) : null}
          </div>
        </fieldset>
        <fieldset
          className={cx(s.group, s.envGroup)}
          onKeyDown={(e) => moveFocus(e, 'input[type="checkbox"]')}
        >
          <legend className={s.legend}>
            Environment
            <span className={s.count}>logs only</span>
          </legend>
          <div className={s.list}>
            <CheckRow checked={draft.env.length === 0} onChange={() => setDraft((d) => ({ ...d, env: [] }))}>
              All environments
            </CheckRow>
            {envs.isPending ? <div className={s.empty}>Loading…</div> : null}
            {envs.isError ? <div className={s.empty}>Environments are unavailable.</div> : null}
            {envOptions.map((name) => (
              <CheckRow
                key={name}
                mono
                checked={draft.env.includes(name)}
                onChange={(on) => toggleEnv(name, on)}
              >
                {name}
              </CheckRow>
            ))}
            {envs.isSuccess && envOptions.length === 0 ? (
              <div className={s.empty}>No environment values in the last 24 hours.</div>
            ) : null}
          </div>
          <p className={s.hint}>
            Spans and metric points have no environment; this filter applies to log events.
          </p>
        </fieldset>
      </div>
      <div className={s.foot}>
        <Button
          size="sm"
          variant="ghost"
          disabled={isEmptyScope(draft)}
          onClick={() => setDraft({ streams: [], env: [] })}
        >
          Reset
        </Button>
        <span className={s.footRight}>
          <Button size="sm" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button size="sm" variant="primary" type="submit">
            Apply
          </Button>
        </span>
      </div>
    </form>
  );
}

/** Re-applies the remembered scope (`tm.scope`) when a scoped route is opened without one. */
export function useRememberedScope(active: boolean): void {
  const { store } = useServices();
  const { pathname } = useLocation();
  const [params, setParams] = useSearchParams();
  const hasScope = params.has('streams') || params.has('env');
  useEffect(() => {
    if (!active || hasScope) return;
    const remembered = rememberedScope(store);
    if (isEmptyScope(remembered)) return;
    setParams((p) => patchParams(p, scopePatch(remembered)), { replace: true });
    // Only when the route changes: a user who clears the scope must not get it back.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, pathname]);
}

export function ScopePicker({ compact }: { compact?: boolean }) {
  const scope = useDataScope();
  const { names, loading } = useVisibleStreams();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  if (scope.none) return null;
  const current: Scope = { streams: scope.streams, env: scope.env };
  const visible = names ?? [];
  if (scope.visible !== null && scope.visible.length === 1) {
    const only = scope.visible[0] ?? '';
    return (
      <span className={cx(s.chip, compact === true && s.chipCompact)} title="The only stream you can see">
        <Layers size={14} aria-hidden="true" />
        <span className={s.mono}>{only}</span>
      </span>
    );
  }
  const close = (): void => {
    setOpen(false);
    trigger.current?.focus();
  };
  const apply = (next: Scope): void => {
    scope.set(next);
    close();
  };
  const label = `Data scope: ${scopeDescription(current, scope.visible)}`;
  const selectedCount = current.streams.length + current.env.length;
  if (compact === true) {
    return (
      <>
        <button
          ref={trigger}
          type="button"
          className={cx(s.compact, selectedCount > 0 && s.compactOn)}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-label={label}
          onClick={() => setOpen(true)}
        >
          <Layers size={18} aria-hidden="true" />
          {selectedCount > 0 ? <span className={s.badge}>{selectedCount}</span> : null}
        </button>
        {open ? (
          <Modal title="Data scope" onClose={close} sheet>
            <ScopePanel
              scope={current}
              streams={visible}
              loadingStreams={loading}
              onApply={apply}
              onCancel={close}
            />
          </Modal>
        ) : null}
      </>
    );
  }
  return (
    <Popover
      open={open}
      onOpenChange={(o) => (o ? setOpen(true) : close())}
      className={s.popover}
      trigger={({ open: isOpen, toggle }) => (
        <button
          ref={trigger}
          type="button"
          className={cx(s.trigger, !isEmptyScope(current) && s.triggerOn)}
          aria-haspopup="dialog"
          aria-expanded={isOpen}
          aria-label={label}
          onClick={toggle}
        >
          <Layers size={16} aria-hidden="true" />
          <span className={s.triggerText}>{scopeLabel(current)}</span>
          <ChevronDown size={14} aria-hidden="true" />
        </button>
      )}
    >
      <div role="dialog" aria-label="Data scope">
        <ScopePanel
          scope={current}
          streams={visible}
          loadingStreams={loading}
          onApply={apply}
          onCancel={close}
        />
      </div>
    </Popover>
  );
}
