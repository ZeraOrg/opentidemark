// SPDX-License-Identifier: Apache-2.0
//
// Dashboard list (SPEC §11.4, §13.3): own and shared dashboards, create, duplicate, delete,
// import a JSON export, and the built-in templates instantiated for a chosen service.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Copy, LayoutGrid, Plus, Trash2, Upload } from 'lucide-react';
import { useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { dashboards as dashApi } from '../../api/endpoints';
import type { Dashboard, DashboardDoc, DashboardTemplate } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { hasRole, useMe } from '../../app/session';
import { Button, Checkbox, Field, Spinner, StatusChip, TextInput } from '../../components/controls';
import { Card, EmptyState, layoutStyles as l, Notice, Page, PageHead } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { formatAge } from '../../lib/time';
import s from './dashboards.module.css';

const MAX_IMPORT_BYTES = 2 * 1024 * 1024;

interface ImportDoc {
  name: string;
  description?: string;
  doc: DashboardDoc;
}

/** Validates the shape of an exported dashboard file (the server validates the document). */
export function parseImport(text: string): ImportDoc {
  let v: unknown;
  try {
    v = JSON.parse(text);
  } catch {
    throw new Error('The file is not valid JSON.');
  }
  if (typeof v !== 'object' || v === null) throw new Error('Expected a dashboard export object.');
  const o = v as Record<string, unknown>;
  const doc = o.doc as Record<string, unknown> | undefined;
  if (typeof o.name !== 'string' || o.name.trim() === '') throw new Error('The export has no name.');
  if (typeof doc !== 'object' || doc === null || !Array.isArray(doc.panels))
    throw new Error('The export has no panels document.');
  return {
    name: o.name,
    description: typeof o.description === 'string' ? o.description : undefined,
    doc: doc as unknown as DashboardDoc,
  };
}

function CreateDialog({ template, onClose }: { template: DashboardTemplate | null; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState(template?.name ?? '');
  const [service, setService] = useState('');
  const [shared, setShared] = useState(true);
  const create = useMutation({
    mutationFn: async () => {
      let doc: DashboardDoc = { version: 1, panels: [] };
      if (template !== null) {
        const fresh =
          service.trim() === ''
            ? template
            : ((await dashApi.templates(api, service.trim())).find((t) => t.id === template.id) ?? template);
        doc = fresh.doc;
      }
      return dashApi.create(api, {
        name: name.trim(),
        description: template?.description ?? '',
        shared,
        doc,
      });
    },
    onSuccess: (d) => {
      void qc.invalidateQueries({ queryKey: ['dashboards'] });
      navigate(`/dashboards/${encodeURIComponent(d.id)}${template === null ? '/edit' : ''}`);
    },
  });
  return (
    <Modal
      title={template === null ? 'New dashboard' : `From template: ${template.name}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={create.isPending}
            disabled={name.trim() === ''}
            onClick={() => create.mutate()}
          >
            Create
          </Button>
        </>
      }
    >
      <div className={l.stack}>
        <Field label="Name">
          {(id) => (
            <TextInput
              id={id}
              value={name}
              maxLength={200}
              onChange={(e) => setName(e.target.value)}
              data-autofocus
            />
          )}
        </Field>
        {template !== null ? (
          <Field
            label="Service"
            help="Becomes the default of the template's $service variable; empty = all services."
          >
            {(id) => (
              <TextInput
                id={id}
                mono
                value={service}
                onChange={(e) => setService(e.target.value)}
                placeholder="checkout-api"
              />
            )}
          </Field>
        ) : null}
        <Checkbox checked={shared} onChange={setShared}>
          Shared with everyone
        </Checkbox>
        {create.isError ? <Notice kind="danger">{errorMessage(create.error)}</Notice> : null}
      </div>
    </Modal>
  );
}

export default function DashboardsPage() {
  const api = useApi();
  const me = useMe();
  const qc = useQueryClient();
  const toast = useToast();
  const { now } = useServices();
  const navigate = useNavigate();
  const file = useRef<HTMLInputElement>(null);
  const [creating, setCreating] = useState<DashboardTemplate | null | undefined>(undefined);
  const [confirmDelete, setConfirmDelete] = useState<Dashboard | null>(null);
  const list = useQuery({ queryKey: ['dashboards'], queryFn: () => dashApi.list(api) });
  const templates = useQuery({ queryKey: ['dashboard-templates'], queryFn: () => dashApi.templates(api) });
  const canEdit = hasRole(me.role, 'editor');
  const invalidate = (): void => void qc.invalidateQueries({ queryKey: ['dashboards'] });
  const duplicate = useMutation({
    mutationFn: (d: Dashboard) => dashApi.duplicate(api, d.id),
    onSuccess: (d) => {
      invalidate();
      toast.push({ kind: 'success', title: `Created ${d.name}` });
    },
    onError: (e) => toast.push({ kind: 'danger', title: 'Duplicate failed', body: errorMessage(e) }),
  });
  const remove = useMutation({
    mutationFn: (d: Dashboard) => dashApi.remove(api, d.id),
    onSuccess: () => {
      invalidate();
      setConfirmDelete(null);
    },
  });
  const importFile = useMutation({
    mutationFn: async (f: File) => {
      if (f.size > MAX_IMPORT_BYTES) throw new Error('The file is larger than 2 MiB.');
      const doc = parseImport(await f.text());
      return dashApi.importDoc(api, doc);
    },
    onSuccess: (d) => {
      invalidate();
      navigate(`/dashboards/${encodeURIComponent(d.id)}`);
    },
    onError: (e) => toast.push({ kind: 'danger', title: 'Import failed', body: errorMessage(e) }),
  });
  return (
    <Page>
      <PageHead title="Dashboards" note="own and shared">
        {canEdit ? (
          <>
            <input
              ref={file}
              type="file"
              accept="application/json,.json"
              hidden
              onChange={(e) => {
                const f = e.target.files?.[0];
                if (f !== undefined) importFile.mutate(f);
                e.target.value = '';
              }}
            />
            <Button
              variant="ghost"
              icon={<Upload size={14} />}
              busy={importFile.isPending}
              onClick={() => file.current?.click()}
            >
              Import JSON
            </Button>
            <Button variant="primary" icon={<Plus size={14} />} onClick={() => setCreating(null)}>
              New dashboard
            </Button>
          </>
        ) : null}
      </PageHead>
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {list.isPending ? (
        <Spinner />
      ) : (list.data ?? []).length === 0 ? (
        <EmptyState
          icon={<LayoutGrid size={20} aria-hidden="true" />}
          title="No dashboards yet"
          hint="Start from a template below or add panels from the explorer."
        />
      ) : (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={l.table}>
            <thead>
              <tr>
                <th>Name</th>
                <th>Description</th>
                <th>Visibility</th>
                <th>Updated</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((d) => (
                <tr key={d.id}>
                  <td>
                    <Link to={`/dashboards/${encodeURIComponent(d.id)}`} className={s.dashLink}>
                      {d.name}
                    </Link>
                  </td>
                  <td className={l.muted}>{d.description}</td>
                  <td>
                    {d.shared ? (
                      <StatusChip variant="info">shared</StatusChip>
                    ) : (
                      <StatusChip variant="muted">private</StatusChip>
                    )}
                  </td>
                  <td className={l.subtle}>{formatAge(Date.parse(d.updatedAt), now())}</td>
                  <td className={s.rowActions}>
                    {canEdit ? (
                      <>
                        <Button
                          size="sm"
                          variant="ghost"
                          iconOnly
                          icon={<Copy size={14} />}
                          aria-label={`Duplicate ${d.name}`}
                          onClick={() => duplicate.mutate(d)}
                        />
                        {d.ownerId === me.id || me.role === 'admin' ? (
                          <Button
                            size="sm"
                            variant="ghost"
                            iconOnly
                            icon={<Trash2 size={14} />}
                            aria-label={`Delete ${d.name}`}
                            onClick={() => setConfirmDelete(d)}
                          />
                        ) : null}
                      </>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <Card title="Templates" subtitle="instantiated for one service or all">
        <div className={s.templates}>
          {(templates.data ?? []).map((t) => (
            <button
              type="button"
              key={t.id}
              className={s.template}
              disabled={!canEdit}
              onClick={() => setCreating(t)}
            >
              <LayoutGrid size={16} aria-hidden="true" />
              <strong>{t.name}</strong>
              <span>{t.description}</span>
            </button>
          ))}
        </div>
      </Card>
      {creating !== undefined ? (
        <CreateDialog template={creating} onClose={() => setCreating(undefined)} />
      ) : null}
      {confirmDelete !== null ? (
        <Modal
          title="Delete dashboard"
          onClose={() => setConfirmDelete(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirmDelete(null)}>
                Cancel
              </Button>
              <Button
                variant="dangerSolid"
                busy={remove.isPending}
                onClick={() => remove.mutate(confirmDelete)}
              >
                Delete
              </Button>
            </>
          }
        >
          <p>
            Delete <strong>{confirmDelete.name}</strong>? This cannot be undone; export it first if you may
            need it again.
          </p>
          {remove.isError ? <Notice kind="danger">{errorMessage(remove.error)}</Notice> : null}
        </Modal>
      ) : null}
    </Page>
  );
}
