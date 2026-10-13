// SPDX-License-Identifier: Apache-2.0
//
// Audit log (SPEC §11.5 GET /audit, §15.5): filter by actor, action and time range; details
// expand as JSON.

import { useQuery } from '@tanstack/react-query';
import { Fragment, useState } from 'react';
import { errorMessage } from '../../api/client';
import { admin } from '../../api/endpoints';
import { useApi } from '../../app/services';
import { Button, Select, Spinner, TextInput } from '../../components/controls';
import { EmptyState, JsonView, layoutStyles as l, Notice } from '../../components/layout';
import { cx } from '../../lib/cx';
import { useDebounced } from '../../lib/hooks';
import { formatDateTime } from '../../lib/time';
import s from './settings.module.css';

const RANGES = [
  { value: 'now-24h', label: 'Last 24 hours' },
  { value: 'now-7d', label: 'Last 7 days' },
  { value: 'now-30d', label: 'Last 30 days' },
  { value: '', label: 'All' },
];
const LIMIT = 500;

export default function AuditPage() {
  const api = useApi();
  const [actor, setActor] = useState('');
  const [action, setAction] = useState('');
  const [from, setFrom] = useState('now-7d');
  const [open, setOpen] = useState<string | null>(null);
  const a = useDebounced(actor.trim(), 300);
  const b = useDebounced(action.trim(), 300);
  const list = useQuery({
    queryKey: ['audit', a, b, from],
    queryFn: () =>
      admin.audit(api, { actor: a, action: b, from: from === '' ? undefined : from, limit: LIMIT }),
  });
  return (
    <div className={s.page}>
      <div className={s.head}>
        <h1>Audit log</h1>
        <span className={l.subtle}>sign-ins, changes, purges and exports</span>
      </div>
      <div className={s.filters}>
        <TextInput
          aria-label="Actor"
          placeholder="Actor"
          value={actor}
          onChange={(e) => setActor(e.target.value)}
        />
        <TextInput
          aria-label="Action"
          placeholder="Action (e.g. purge, login.failure)"
          mono
          value={action}
          onChange={(e) => setAction(e.target.value)}
        />
        <Select
          aria-label="Time range"
          options={RANGES}
          value={from}
          onChange={(e) => setFrom(e.target.value)}
        />
      </div>
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {list.isPending ? (
        <Spinner />
      ) : (list.data ?? []).length === 0 ? (
        <EmptyState title="No audit entries" hint="Change the filters or the time range." />
      ) : (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={cx(l.table, l.compact)}>
            <thead>
              <tr>
                <th>Time</th>
                <th>Actor</th>
                <th>IP</th>
                <th>Action</th>
                <th>Target</th>
                <th aria-label="Details" />
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((e) => (
                <Fragment key={e.id}>
                  <tr>
                    <td className={l.monoCell}>{formatDateTime(Date.parse(e.at))}</td>
                    <td>{e.actor}</td>
                    <td className={l.monoCell}>{e.ip}</td>
                    <td className={l.monoCell}>{e.action}</td>
                    <td className={l.muted}>{e.target}</td>
                    <td className={s.rowActions}>
                      <Button
                        size="sm"
                        variant="ghost"
                        aria-expanded={open === e.id}
                        onClick={() => setOpen(open === e.id ? null : e.id)}
                      >
                        Details
                      </Button>
                    </td>
                  </tr>
                  {open === e.id ? (
                    <tr>
                      <td colSpan={6}>
                        <JsonView value={e.details} />
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
