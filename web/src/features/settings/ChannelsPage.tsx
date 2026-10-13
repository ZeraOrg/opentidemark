// SPDX-License-Identifier: Apache-2.0
//
// Notification channels (SPEC §11.4, §14.2): webhook (URL, HMAC secret, headers), Slack, Teams
// and email. Secrets come back masked as "••••"; sending the mask keeps the stored value. Viewers
// see the list, admins create, edit, test and delete.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Send, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '../../api/client';
import { channels as channelsApi } from '../../api/endpoints';
import type { Channel, ChannelConfig, ChannelKind } from '../../api/types';
import { useApi } from '../../app/services';
import { hasRole, useMe } from '../../app/session';
import { Button, Field, Select, Spinner, Tag, TextArea, TextInput } from '../../components/controls';
import { EmptyState, layoutStyles as l, Notice } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import s from './settings.module.css';

export const MASK = '••••';
const KINDS: readonly { value: ChannelKind; label: string }[] = [
  { value: 'webhook', label: 'Webhook' },
  { value: 'slack', label: 'Slack' },
  { value: 'teams', label: 'Microsoft Teams' },
  { value: 'email', label: 'Email' },
];

/** "Header: value" lines → map (invalid lines are skipped). */
export function parseHeaders(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split('\n')) {
    const i = line.indexOf(':');
    if (i <= 0) continue;
    out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}

/** The config a channel kind accepts (SPEC §14.2), built from the form fields. */
export function buildConfig(
  kind: ChannelKind,
  f: { url: string; secret: string; headers: string; to: string },
): ChannelConfig {
  if (kind === 'email') return { to: f.to.split(/[\s,]+/).filter((x) => x !== '') };
  if (kind === 'webhook') {
    const headers = parseHeaders(f.headers);
    return {
      url: f.url,
      ...(f.secret !== '' ? { secret: f.secret } : {}),
      ...(Object.keys(headers).length > 0 ? { headers } : {}),
    };
  }
  return { url: f.url };
}

function target(c: Channel): string {
  if (c.kind === 'email') return (c.config.to ?? []).join(', ');
  return c.config.url === MASK ? 'URL stored encrypted' : (c.config.url ?? '');
}

function ChannelDialog({ channel, onClose }: { channel: Channel | null; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const [name, setName] = useState(channel?.name ?? '');
  const [kind, setKind] = useState<ChannelKind>(channel?.kind ?? 'webhook');
  const [url, setUrl] = useState(channel?.config.url ?? '');
  const [secret, setSecret] = useState(channel?.config.secret ?? '');
  const [headers, setHeaders] = useState(
    Object.entries(channel?.config.headers ?? {})
      .map(([k, v]) => `${k}: ${v}`)
      .join('\n'),
  );
  const [to, setTo] = useState((channel?.config.to ?? []).join(', '));
  const save = useMutation({
    mutationFn: () => {
      const body = { name: name.trim(), kind, config: buildConfig(kind, { url, secret, headers, to }) };
      return channel === null
        ? channelsApi.create(api, body)
        : channelsApi.update(api, channel.id, { ...body, version: channel.version });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['channels'] });
      onClose();
    },
  });
  const valid = name.trim() !== '' && (kind === 'email' ? to.trim() !== '' : url.trim() !== '');
  return (
    <Modal
      title={channel === null ? 'New channel' : `Edit ${channel.name}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!valid} busy={save.isPending} onClick={() => save.mutate()}>
            Save channel
          </Button>
        </>
      }
    >
      <div className={l.stack}>
        <Field label="Name">
          {(id) => (
            <TextInput id={id} value={name} onChange={(e) => setName(e.target.value)} data-autofocus />
          )}
        </Field>
        <Field label="Kind">
          {(id) => (
            <Select
              id={id}
              options={KINDS}
              value={kind}
              onChange={(e) => setKind(e.target.value as ChannelKind)}
            />
          )}
        </Field>
        {kind === 'email' ? (
          <Field
            label="Recipients"
            help="1–20 addresses, comma separated. The SMTP server is configured on the server."
          >
            {(id) => <TextInput id={id} value={to} onChange={(e) => setTo(e.target.value)} />}
          </Field>
        ) : (
          <Field
            label={kind === 'webhook' ? 'URL' : 'Incoming webhook URL'}
            help="https only; private addresses are blocked unless the server allows them. Stored encrypted."
          >
            {(id) => (
              <TextInput
                id={id}
                mono
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder="https://"
              />
            )}
          </Field>
        )}
        {kind === 'webhook' ? (
          <>
            <Field label="HMAC secret" help="Optional; signs the body as X-Tidemark-Signature: sha256=<hex>.">
              {(id) => (
                <TextInput
                  id={id}
                  type="password"
                  value={secret}
                  onChange={(e) => setSecret(e.target.value)}
                />
              )}
            </Field>
            <Field label="Headers" help="One per line: Name: value (values stored encrypted).">
              {(id) => (
                <TextArea
                  id={id}
                  mono
                  rows={3}
                  value={headers}
                  onChange={(e) => setHeaders(e.target.value)}
                />
              )}
            </Field>
          </>
        ) : null}
        {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
      </div>
    </Modal>
  );
}

export default function ChannelsPage() {
  const api = useApi();
  const me = useMe();
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<Channel | null | undefined>(undefined);
  const [deleting, setDeleting] = useState<Channel | null>(null);
  const list = useQuery({ queryKey: ['channels'], queryFn: () => channelsApi.list(api) });
  const admin = hasRole(me.role, 'admin');
  const test = useMutation({
    mutationFn: (c: Channel) => channelsApi.test(api, c.id),
    onSuccess: (r, c) =>
      toast.push(
        r.ok
          ? { kind: 'success', title: `Test notification sent to ${c.name}` }
          : { kind: 'danger', title: `Test of ${c.name} failed`, body: r.error },
      ),
    onError: (e) => toast.push({ kind: 'danger', title: 'Test failed', body: errorMessage(e) }),
  });
  const remove = useMutation({
    mutationFn: (c: Channel) => channelsApi.remove(api, c.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['channels'] });
      setDeleting(null);
    },
  });
  return (
    <div className={s.page}>
      <div className={s.head}>
        <h1>Channels</h1>
        <span className={l.subtle}>
          Where alert notifications go · secrets are stored encrypted and never shown again
        </span>
        {admin ? (
          <span className={s.headActions}>
            <Button variant="ghost" icon={<Plus size={14} />} onClick={() => setEditing(null)}>
              New channel
            </Button>
          </span>
        ) : null}
      </div>
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {list.isPending ? (
        <Spinner />
      ) : (list.data ?? []).length === 0 ? (
        <EmptyState
          icon={<Send size={20} aria-hidden="true" />}
          title="No channels"
          hint="Add a webhook, Slack, Teams or email channel for alert rules."
        />
      ) : (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={l.table}>
            <thead>
              <tr>
                <th>Name</th>
                <th>Kind</th>
                <th>Target</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((c) => (
                <tr key={c.id}>
                  <td className={s.strong}>{c.name}</td>
                  <td>
                    <Tag>{c.kind}</Tag>
                  </td>
                  <td className={l.muted}>{target(c)}</td>
                  <td className={s.rowActions}>
                    {admin ? (
                      <>
                        <Button
                          size="sm"
                          variant="ghost"
                          icon={<Send size={14} />}
                          busy={test.isPending && test.variables.id === c.id}
                          onClick={() => test.mutate(c)}
                        >
                          Test
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setEditing(c)}>
                          Edit
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          iconOnly
                          icon={<Trash2 size={14} />}
                          aria-label={`Delete ${c.name}`}
                          onClick={() => setDeleting(c)}
                        />
                      </>
                    ) : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {editing !== undefined ? (
        <ChannelDialog channel={editing} onClose={() => setEditing(undefined)} />
      ) : null}
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
                Delete channel
              </Button>
            </>
          }
        >
          <p>Channels used by an alert rule cannot be deleted; remove them from the rules first.</p>
          {remove.isError ? <Notice kind="danger">{errorMessage(remove.error)}</Notice> : null}
        </Modal>
      ) : null}
    </div>
  );
}
