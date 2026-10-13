// SPDX-License-Identifier: Apache-2.0
//
// Personal API tokens (SPEC §11.2, §15.3): list, create (the token value is shown once) and
// delete. Tokens authenticate with `Authorization: Bearer tmt_…`.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Copy, KeyRound, Lock, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '../../api/client';
import { tokens as tokensApi } from '../../api/endpoints';
import type { ApiToken, TokenScope } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { Button, Field, Select, Spinner, StatusChip, TextInput } from '../../components/controls';
import { EmptyState, layoutStyles as l, Notice } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { copyText } from '../../lib/download';
import { formatAge } from '../../lib/time';
import s from './settings.module.css';

const SCOPES: readonly { value: TokenScope; label: string }[] = [
  { value: 'read', label: 'read · queries and exports' },
  { value: 'write', label: 'write · also dashboards, signals, alerts' },
  { value: 'admin', label: 'admin · everything your role allows' },
];

function CreateDialog({ onClose }: { onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState('');
  const [scope, setScope] = useState<TokenScope>('read');
  const [expires, setExpires] = useState('');
  const create = useMutation({
    mutationFn: () =>
      tokensApi.create(
        api,
        name.trim(),
        scope,
        expires === '' ? null : new Date(`${expires}T00:00:00Z`).toISOString(),
      ),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['tokens'] }),
  });
  const token = create.data?.token;
  return (
    <Modal
      title="New API token"
      onClose={onClose}
      footer={
        token === undefined ? (
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
              Create token
            </Button>
          </>
        ) : (
          <Button variant="primary" onClick={onClose}>
            I saved the token
          </Button>
        )
      }
    >
      {token === undefined ? (
        <div className={l.stack}>
          <Field label="Name">
            {(id) => (
              <TextInput
                id={id}
                value={name}
                maxLength={128}
                onChange={(e) => setName(e.target.value)}
                data-autofocus
              />
            )}
          </Field>
          <Field label="Scope">
            {(id) => (
              <Select
                id={id}
                options={SCOPES}
                value={scope}
                onChange={(e) => setScope(e.target.value as TokenScope)}
              />
            )}
          </Field>
          <Field label="Expires" help="Empty = never.">
            {(id) => (
              <TextInput id={id} type="date" value={expires} onChange={(e) => setExpires(e.target.value)} />
            )}
          </Field>
          {create.isError ? <Notice kind="danger">{errorMessage(create.error)}</Notice> : null}
        </div>
      ) : (
        <div className={s.keyPanel}>
          <div className={s.keyTitle}>
            <Lock size={14} aria-hidden="true" /> API token — shown once
          </div>
          <div className={s.keyValue}>{token}</div>
          <Button
            variant="primary"
            size="sm"
            icon={<Copy size={14} />}
            onClick={() =>
              void copyText(token).then((ok) =>
                toast.push({ kind: ok ? 'success' : 'warning', title: ok ? 'Token copied' : 'Copy failed' }),
              )
            }
          >
            Copy token
          </Button>
          <p className={s.keyNote}>Use it as `Authorization: Bearer …`. Only a hash is stored.</p>
        </div>
      )}
    </Modal>
  );
}

export default function TokensPage() {
  const api = useApi();
  const qc = useQueryClient();
  const { now } = useServices();
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<ApiToken | null>(null);
  const list = useQuery({ queryKey: ['tokens'], queryFn: () => tokensApi.list(api) });
  const remove = useMutation({
    mutationFn: (t: ApiToken) => tokensApi.remove(api, t.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['tokens'] });
      setDeleting(null);
    },
  });
  return (
    <div className={s.page}>
      <div className={s.head}>
        <h1>API tokens</h1>
        <span className={l.subtle}>
          Personal tokens for scripts and integrations · shown once, stored hashed
        </span>
        <span className={s.headActions}>
          <Button variant="ghost" icon={<Plus size={14} />} onClick={() => setCreating(true)}>
            New token
          </Button>
        </span>
      </div>
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {list.isPending ? (
        <Spinner />
      ) : (list.data ?? []).length === 0 ? (
        <EmptyState
          icon={<KeyRound size={20} aria-hidden="true" />}
          title="No API tokens"
          hint="Create one to call the API from scripts."
        />
      ) : (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={l.table}>
            <thead>
              <tr>
                <th>Name</th>
                <th>Prefix</th>
                <th>Scope</th>
                <th>Expires</th>
                <th>Last used</th>
                <th>Status</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((t) => {
                const expired = t.expiresAt !== null && Date.parse(t.expiresAt) <= now();
                return (
                  <tr key={t.id}>
                    <td className={s.strong}>{t.name}</td>
                    <td className={l.monoCell}>{t.keyPrefix}…</td>
                    <td>{t.scope}</td>
                    <td className={l.subtle}>{t.expiresAt === null ? 'never' : t.expiresAt.slice(0, 10)}</td>
                    <td className={l.subtle}>
                      {t.lastUsedAt === null ? 'never' : formatAge(Date.parse(t.lastUsedAt), now())}
                    </td>
                    <td>
                      {t.revokedAt !== null ? (
                        <StatusChip variant="muted">revoked</StatusChip>
                      ) : expired ? (
                        <StatusChip variant="danger">expired</StatusChip>
                      ) : (
                        <StatusChip variant="ok">active</StatusChip>
                      )}
                    </td>
                    <td className={s.rowActions}>
                      <Button
                        size="sm"
                        variant="ghost"
                        iconOnly
                        icon={<Trash2 size={14} />}
                        aria-label={`Delete ${t.name}`}
                        onClick={() => setDeleting(t)}
                      />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {creating ? <CreateDialog onClose={() => setCreating(false)} /> : null}
      {deleting !== null ? (
        <Modal
          title={`Delete ${deleting.name}`}
          onClose={() => setDeleting(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setDeleting(null)}>
                Cancel
              </Button>
              <Button variant="dangerSolid" busy={remove.isPending} onClick={() => remove.mutate(deleting)}>
                Delete token
              </Button>
            </>
          }
        >
          <p>Requests with this token fail immediately.</p>
          {remove.isError ? <Notice kind="danger">{errorMessage(remove.error)}</Notice> : null}
        </Modal>
      ) : null}
    </div>
  );
}
