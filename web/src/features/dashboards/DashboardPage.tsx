// SPDX-License-Identifier: Apache-2.0
//
// Dashboard view and edit mode (SPEC §13, §16.4, DESIGN §9.5, §9.6): variables bar, 12-column
// grid (react-grid-layout), auto refresh, share / TV mode / export, and in edit mode drag and
// resize, add / duplicate / remove panels, the panel editor and save with optimistic concurrency.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Copy,
  ExternalLink,
  LayoutGrid,
  Monitor,
  Pencil,
  Plus,
  Share2,
  Trash2,
  Download,
} from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import RGL, { WidthProvider, type Layout } from 'react-grid-layout';
import 'react-grid-layout/css/styles.css';
import 'react-resizable/css/styles.css';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { errorMessage, isApiError } from '../../api/client';
import { dashboards as dashApi } from '../../api/endpoints';
import type { Dashboard, DashboardDoc, Panel } from '../../api/types';
import { useApi } from '../../app/services';
import { hasRole, useMe } from '../../app/session';
import { useRefreshLabel, useTopbarStatus } from '../../app/shellContext';
import { patchParams, useTimeRange } from '../../app/url';
import { Button, Spinner, StatusChip } from '../../components/controls';
import { EmptyState, layoutStyles as l, Notice } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { copyText, downloadUrl } from '../../lib/download';
import { PHONE_QUERY, useMediaQuery } from '../../lib/hooks';
import { substitutePreview } from '../../tql/substitute';
import { nextPanelId } from './AddToDashboard';
import { PanelEditor } from './PanelEditor';
import { bodyHeight, GRID_GAP, PanelBody, PanelFrame, ROW_HEIGHT } from './PanelView';
import { VariablesBar, VariablesEditor } from './Variables';
import { refreshMs, variableValues } from './vars';
import s from './dashboards.module.css';

const Grid = WidthProvider(RGL);
const COLUMNS = 12;
const TV_CYCLE_MS = 60_000;
const DEFAULT_DASH_RANGE = { from: 'now-6h', to: 'now' };

/** Applies a react-grid-layout layout to the panels of `doc`. */
export function applyLayout(doc: DashboardDoc, layout: readonly Layout[]): DashboardDoc {
  const byId = new Map(layout.map((x) => [x.i, x]));
  return {
    ...doc,
    panels: doc.panels.map((p) => {
      const g = byId.get(p.id);
      return g === undefined ? p : { ...p, grid: { x: g.x, y: g.y, w: g.w, h: g.h } };
    }),
  };
}

/** Explorer link for a panel query with variables applied for preview. */
export function explorerLink(
  panel: Panel,
  vars: Record<string, string[]>,
  range: { from: string; to: string },
): string {
  const p = new URLSearchParams({
    q: substitutePreview(panel.query ?? '', vars),
    from: range.from,
    to: range.to,
  });
  return `/events?${p.toString()}`;
}

function TvCycle({ id }: { id: string }) {
  const api = useApi();
  const navigate = useNavigate();
  const list = useQuery({ queryKey: ['dashboards'], queryFn: () => dashApi.list(api) });
  useEffect(() => {
    const all = list.data ?? [];
    if (all.length < 2) return undefined;
    const t = setTimeout(() => {
      const i = all.findIndex((d) => d.id === id);
      const next = all[(i + 1) % all.length];
      if (next !== undefined) navigate(`/dashboards/${encodeURIComponent(next.id)}?tv=1`);
    }, TV_CYCLE_MS);
    return () => clearTimeout(t);
  }, [list.data, id, navigate]);
  return null;
}

export default function DashboardPage({ edit = false }: { edit?: boolean }) {
  const { id = '' } = useParams();
  const api = useApi();
  const me = useMe();
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const phone = useMediaQuery(PHONE_QUERY);
  const dash = useQuery({ queryKey: ['dashboard', id], queryFn: () => dashApi.get(api, id) });
  const [draft, setDraft] = useState<DashboardDoc | null>(null);
  const [varsOpen, setVarsOpen] = useState(false);
  const doc = edit ? (draft ?? dash.data?.doc) : dash.data?.doc;
  useEffect(() => {
    if (!edit) setDraft(null);
  }, [edit]);
  const { range } = useTimeRange(dash.data?.doc?.timeRange ?? DEFAULT_DASH_RANGE);
  const vars = useMemo(() => variableValues(doc, params), [doc, params]);
  const refresh = refreshMs(doc?.refresh);
  useRefreshLabel(refresh === null ? null : (doc?.refresh ?? null));
  useTopbarStatus(null);
  const tv = params.get('tv') === '1';
  const editingPanel = params.get('panel');
  const canEdit = hasRole(me.role, 'editor');

  const save = useMutation({
    mutationFn: (d: Dashboard & { doc: DashboardDoc }) =>
      dashApi.update(api, d.id, {
        name: d.name,
        description: d.description,
        shared: d.shared,
        doc: d.doc,
        version: d.version,
      }),
    onSuccess: (d) => {
      qc.setQueryData(['dashboard', d.id], d);
      void qc.invalidateQueries({ queryKey: ['panel-q', d.id] });
      void qc.invalidateQueries({ queryKey: ['dashboards'] });
      setDraft(null);
      toast.push({ kind: 'success', title: 'Dashboard saved' });
      navigate(`/dashboards/${encodeURIComponent(d.id)}`);
    },
  });

  if (dash.isPending) return <Spinner />;
  if (dash.isError) {
    return (
      <div className={l.page}>
        <Notice kind="danger">{errorMessage(dash.error)}</Notice>
      </div>
    );
  }
  const d = dash.data;
  const current = doc ?? { version: 1 as const, panels: [] };
  const update = (next: DashboardDoc): void => setDraft(next);
  const share = async (): Promise<void> => {
    const ok = await copyText(window.location.href);
    toast.push({ kind: ok ? 'success' : 'warning', title: ok ? 'Link copied' : 'Copy failed' });
  };
  const addPanel = (): void => {
    const pid = nextPanelId(current);
    const bottom = current.panels.reduce((m, p) => Math.max(m, p.grid.y + p.grid.h), 0);
    update({
      ...current,
      panels: [
        ...current.panels,
        {
          id: pid,
          title: 'New panel',
          type: 'timeseries',
          grid: { x: 0, y: bottom, w: 6, h: 6 },
          query: 'level >= error | stats count() by service span auto',
          options: { legend: 'bottom' },
        },
      ],
    });
    setParams((p) => patchParams(p, { panel: pid }));
  };
  const duplicate = (p: Panel): void => {
    const pid = nextPanelId(current);
    update({
      ...current,
      panels: [
        ...current.panels,
        { ...p, id: pid, title: `${p.title} (copy)`, grid: { ...p.grid, y: p.grid.y + p.grid.h } },
      ],
    });
  };
  const remove = (p: Panel): void =>
    update({ ...current, panels: current.panels.filter((x) => x.id !== p.id) });

  if (edit && editingPanel !== null) {
    const panel = current.panels.find((p) => p.id === editingPanel);
    if (panel !== undefined) {
      return (
        <PanelEditor
          dashboard={d}
          panel={panel}
          doc={current}
          vars={vars}
          range={range}
          onApply={(p) => {
            update({ ...current, panels: current.panels.map((x) => (x.id === p.id ? p : x)) });
            setParams((q) => patchParams(q, { panel: null }));
          }}
          onDiscard={() => setParams((q) => patchParams(q, { panel: null }))}
        />
      );
    }
  }

  const layout: Layout[] = current.panels.map((p) => ({
    i: p.id,
    x: p.grid.x,
    y: p.grid.y,
    w: p.grid.w,
    h: p.grid.h,
    minW: 2,
    minH: 2,
  }));
  const panelActions = (p: Panel) => (
    <>
      {p.query !== undefined ? (
        <Link
          to={explorerLink(p, vars, range)}
          className={s.panelAction}
          title="Open in explorer"
          aria-label={`Open ${p.title} in explorer`}
        >
          <ExternalLink size={14} aria-hidden="true" />
        </Link>
      ) : null}
      {edit ? (
        <>
          <button
            type="button"
            className={s.panelAction}
            aria-label={`Edit ${p.title}`}
            title="Edit"
            onClick={() => setParams((q) => patchParams(q, { panel: p.id }))}
          >
            <Pencil size={14} aria-hidden="true" />
          </button>
          <button
            type="button"
            className={s.panelAction}
            aria-label={`Duplicate ${p.title}`}
            title="Duplicate"
            onClick={() => duplicate(p)}
          >
            <Copy size={14} aria-hidden="true" />
          </button>
          <button
            type="button"
            className={s.panelAction}
            aria-label={`Remove ${p.title}`}
            title="Remove"
            onClick={() => remove(p)}
          >
            <Trash2 size={14} aria-hidden="true" />
          </button>
        </>
      ) : null}
    </>
  );
  const renderPanel = (p: Panel) => (
    <PanelFrame panel={p} actions={panelActions(p)}>
      <PanelBody
        dashboardId={d.id}
        panel={p}
        vars={vars}
        range={range}
        preview={edit && draft !== null}
        height={bodyHeight(p.grid.h)}
        refetchMs={refresh}
      />
    </PanelFrame>
  );
  const conflict = isApiError(save.error) && save.error.status === 409;
  return (
    <div className={s.page}>
      {tv ? <TvCycle id={d.id} /> : null}
      <div className={s.head}>
        <nav className={l.breadcrumb} aria-label="Breadcrumb">
          <Link to="/dashboards">Dashboards</Link> /
        </nav>
        <h1 className={s.title}>{d.name}</h1>
        {edit ? <StatusChip variant="info">editing</StatusChip> : null}
        {refresh !== null && !edit ? (
          <StatusChip variant="ok" dot>
            auto-refresh {doc?.refresh}
          </StatusChip>
        ) : null}
        <span className={s.headActions}>
          {edit ? (
            <>
              <Button variant="ghost" icon={<Plus size={14} />} onClick={addPanel}>
                Add panel
              </Button>
              <Button variant="ghost" onClick={() => setVarsOpen(true)}>
                Variables
              </Button>
              <Button
                variant="ghost"
                onClick={() => {
                  setDraft(null);
                  navigate(`/dashboards/${encodeURIComponent(d.id)}`);
                }}
              >
                Discard
              </Button>
              <Button
                variant="primary"
                busy={save.isPending}
                disabled={draft === null}
                onClick={() => save.mutate({ ...d, doc: current })}
              >
                Save
              </Button>
            </>
          ) : (
            <>
              <Button variant="ghost" icon={<Share2 size={14} />} onClick={() => void share()}>
                Share
              </Button>
              <Button
                variant="ghost"
                icon={<Monitor size={14} />}
                onClick={() => setParams((p) => patchParams(p, { tv: tv ? null : '1' }))}
              >
                {tv ? 'Exit TV mode' : 'TV mode'}
              </Button>
              <Button
                variant="ghost"
                icon={<Download size={14} />}
                onClick={() => downloadUrl(dashApi.exportUrl(api, d.id))}
              >
                Export JSON
              </Button>
              {canEdit ? (
                <Button
                  variant="primary"
                  icon={<Pencil size={14} />}
                  onClick={() => navigate(`/dashboards/${encodeURIComponent(d.id)}/edit`)}
                >
                  Edit
                </Button>
              ) : null}
            </>
          )}
        </span>
      </div>
      {save.isError ? (
        <Notice kind="danger">
          {conflict
            ? 'Someone else saved this dashboard in the meantime. Reload it and apply your changes again.'
            : errorMessage(save.error)}
        </Notice>
      ) : null}
      {(current.variables ?? []).length > 0 ? <VariablesBar doc={current} values={vars} /> : null}
      {current.panels.length === 0 ? (
        <EmptyState
          icon={<LayoutGrid size={20} aria-hidden="true" />}
          title="This dashboard has no panels"
          hint={
            canEdit
              ? 'Add a panel, or use “Add to dashboard” on any explorer result.'
              : 'An editor can add panels.'
          }
          action={
            canEdit && !edit ? (
              <Button size="sm" onClick={() => navigate(`/dashboards/${encodeURIComponent(d.id)}/edit`)}>
                Edit dashboard
              </Button>
            ) : undefined
          }
        />
      ) : phone ? (
        <div className={s.stack}>
          {[...current.panels]
            .sort((a, b) => a.grid.y - b.grid.y || a.grid.x - b.grid.x)
            .map((p) => (
              <div key={p.id} style={{ height: p.grid.h * ROW_HEIGHT + (p.grid.h - 1) * GRID_GAP }}>
                {renderPanel(p)}
              </div>
            ))}
        </div>
      ) : (
        <Grid
          className={s.grid}
          layout={layout}
          cols={COLUMNS}
          rowHeight={ROW_HEIGHT}
          margin={[GRID_GAP, GRID_GAP]}
          containerPadding={[0, 0]}
          isDraggable={edit}
          isResizable={edit}
          draggableCancel="button, a, input, select, .cm-editor"
          onLayoutChange={(next) => {
            if (edit) {
              const applied = applyLayout(current, next);
              if (
                JSON.stringify(applied.panels.map((p) => p.grid)) !==
                JSON.stringify(current.panels.map((p) => p.grid))
              )
                update(applied);
            }
          }}
        >
          {current.panels.map((p) => (
            <div key={p.id}>{renderPanel(p)}</div>
          ))}
        </Grid>
      )}
      {varsOpen ? (
        <Modal title="Variables" onClose={() => setVarsOpen(false)} wide>
          <VariablesEditor
            variables={current.variables ?? []}
            onChange={(variables) => update({ ...current, variables })}
          />
        </Modal>
      ) : null}
    </div>
  );
}
