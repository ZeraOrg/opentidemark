// SPDX-License-Identifier: Apache-2.0
//
// "Add to dashboard" (SPEC §13.3, DESIGN §9.3): appends a panel with the current query to an
// existing dashboard (optimistic concurrency via `version`) or creates a new dashboard.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { dashboards } from '../../api/endpoints';
import type { DashboardDoc, Panel, PanelType, ResultKind } from '../../api/types';
import { useApi } from '../../app/services';
import { Button, Field, Select, TextInput } from '../../components/controls';
import { layoutStyles as l, Notice } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';

const NEW = '__new__';
const GRID_COLUMNS = 12;

export const PANEL_TYPES: readonly { value: PanelType; label: string }[] = [
  { value: 'timeseries', label: 'Time series' },
  { value: 'bar', label: 'Bar' },
  { value: 'table', label: 'Table' },
  { value: 'stat', label: 'Stat' },
  { value: 'pie', label: 'Pie' },
  { value: 'gauge', label: 'Gauge' },
  { value: 'logs', label: 'Logs' },
  { value: 'traces', label: 'Traces' },
];

/** The panel type that fits a result kind. */
export function panelTypeFor(
  kind: ResultKind | null,
  source: 'logs' | 'spans' | 'metrics' = 'logs',
): PanelType {
  if (kind === 'timeseries') return 'timeseries';
  if (kind === 'table') return 'table';
  return source === 'spans' ? 'traces' : 'logs';
}

/** A unique panel id within `doc`. */
export function nextPanelId(doc: DashboardDoc): string {
  const used = new Set(doc.panels.map((p) => p.id));
  let i = doc.panels.length + 1;
  while (used.has(`p${i}`)) i++;
  return `p${i}`;
}

/** Appends `panel` below all existing panels (full row for logs/tables, half row otherwise). */
export function appendPanel(doc: DashboardDoc, p: Omit<Panel, 'id' | 'grid'>): DashboardDoc {
  const bottom = doc.panels.reduce((m, x) => Math.max(m, x.grid.y + x.grid.h), 0);
  const wide = p.type === 'logs' || p.type === 'traces' || p.type === 'table';
  const w = wide ? GRID_COLUMNS : GRID_COLUMNS / 2;
  const h = p.type === 'stat' || p.type === 'gauge' ? 3 : 6;
  return { ...doc, panels: [...doc.panels, { ...p, id: nextPanelId(doc), grid: { x: 0, y: bottom, w, h } }] };
}

export function AddToDashboardDialog({
  q,
  kind,
  title: initialTitle,
  onClose,
}: {
  q: string;
  kind: ResultKind | null;
  title: string;
  onClose: () => void;
}) {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const list = useQuery({ queryKey: ['dashboards'], queryFn: () => dashboards.list(api) });
  const [target, setTarget] = useState<string>(NEW);
  const [name, setName] = useState('New dashboard');
  const [title, setTitle] = useState(initialTitle);
  const [type, setType] = useState<PanelType>(panelTypeFor(kind));
  const add = useMutation({
    mutationFn: async () => {
      const panel = { title: title.trim() === '' ? 'Panel' : title.trim(), type, query: q };
      if (target === NEW) {
        return dashboards.create(api, {
          name: name.trim(),
          doc: appendPanel({ version: 1, panels: [] }, panel),
        });
      }
      const d = await dashboards.get(api, target);
      const doc = appendPanel(d.doc ?? { version: 1, panels: [] }, panel);
      return dashboards.update(api, d.id, {
        name: d.name,
        description: d.description,
        shared: d.shared,
        doc,
        version: d.version,
      });
    },
    onSuccess: (d) => {
      void qc.invalidateQueries({ queryKey: ['dashboards'] });
      void qc.invalidateQueries({ queryKey: ['dashboard', d.id] });
      toast.push({
        kind: 'success',
        title: `Panel added to ${d.name}`,
        actions: [
          { label: 'Open dashboard', onClick: () => navigate(`/dashboards/${encodeURIComponent(d.id)}`) },
        ],
      });
      onClose();
    },
  });
  const options = [
    { value: NEW, label: '+ New dashboard' },
    ...(list.data ?? []).map((d) => ({ value: d.id, label: d.name })),
  ];
  return (
    <Modal
      title="Add to dashboard"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={add.isPending}
            disabled={target === NEW && name.trim() === ''}
            onClick={() => add.mutate()}
          >
            Add panel
          </Button>
        </>
      }
    >
      <div className={l.stack}>
        <Field label="Dashboard">
          {(id) => (
            <Select id={id} options={options} value={target} onChange={(e) => setTarget(e.target.value)} />
          )}
        </Field>
        {target === NEW ? (
          <Field label="Dashboard name">
            {(id) => <TextInput id={id} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
        ) : null}
        <Field label="Panel title">
          {(id) => (
            <TextInput id={id} value={title} onChange={(e) => setTitle(e.target.value)} data-autofocus />
          )}
        </Field>
        <Field label="Panel type" note="pre-selected from the result kind">
          {(id) => (
            <Select
              id={id}
              options={PANEL_TYPES}
              value={type}
              onChange={(e) => setType(e.target.value as PanelType)}
            />
          )}
        </Field>
        {add.isError ? <Notice kind="danger">{errorMessage(add.error)}</Notice> : null}
      </div>
    </Modal>
  );
}
