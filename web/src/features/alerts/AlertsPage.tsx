// SPDX-License-Identifier: Apache-2.0
//
// Alert rules (SPEC §11.4, §14, DESIGN §9.7): counts of firing / pending rules, filter and
// segmented All / Firing / Muted, the rules table and recent transitions from alert_history;
// `/alerts/:id` (and `/alerts/new`) open the editor drawer.

import { useQueries, useQuery } from '@tanstack/react-query';
import { Bell, Pencil, Plus, Search } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { alerts as alertsApi, channels as channelsApi } from '../../api/endpoints';
import type { Alert, AlertHistoryEntry } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { hasRole, useMe } from '../../app/session';
import { useTopbarStatus } from '../../app/shellContext';
import { Button, Segmented, Spinner, StatusChip, Tag } from '../../components/controls';
import { Card, EmptyState, layoutStyles as l, Notice } from '../../components/layout';
import { cx } from '../../lib/cx';
import { formatNumber } from '../../lib/format';
import { formatAge, formatClock } from '../../lib/time';
import { AlertDrawer } from './AlertDrawer';
import { conditionText, groupText, isMuted, stateChip, stateVariant, type RuleFilter } from './rules';
import s from './alerts.module.css';

const HISTORY_PER_RULE = 10;
const RECENT = 12;
const NOTIFYING = new Set(['firing', 'error']);

const SEVERITY_VARIANT = { critical: 'danger', warning: 'pending', info: 'info' } as const;

/** Notifications are sent on → firing / → error and on firing|error → ok (SPEC §14.3). */
export function notifies(h: AlertHistoryEntry): boolean {
  return NOTIFYING.has(h.toState) || (h.toState === 'ok' && NOTIFYING.has(h.fromState));
}

function useRecentTransitions(rules: readonly Alert[]) {
  const api = useApi();
  const results = useQueries({
    queries: rules.map((r) => ({
      queryKey: ['alert-history', r.id],
      queryFn: () => alertsApi.history(api, r.id, HISTORY_PER_RULE),
    })),
  });
  return useMemo(() => {
    const out: { rule: Alert; h: AlertHistoryEntry }[] = [];
    results.forEach((res, i) => {
      const rule = rules[i];
      if (rule !== undefined) for (const h of res.data ?? []) out.push({ rule, h });
    });
    return out.sort((a, b) => Date.parse(b.h.at) - Date.parse(a.h.at)).slice(0, RECENT);
  }, [results, rules]);
}

export default function AlertsPage() {
  const api = useApi();
  const me = useMe();
  const { now } = useServices();
  const navigate = useNavigate();
  const { id } = useParams();
  const [filter, setFilter] = useState('');
  const [segment, setSegment] = useState<RuleFilter>('all');
  const rules = useQuery({
    queryKey: ['alerts'],
    queryFn: () => alertsApi.list(api),
    refetchInterval: 30_000,
  });
  const channels = useQuery({ queryKey: ['channels'], queryFn: () => channelsApi.list(api) });
  const channelName = new Map((channels.data ?? []).map((c) => [c.id, c.name]));
  const all = rules.data ?? [];
  const recent = useRecentTransitions(all);
  const firing = all.filter((a) => a.state === 'firing').length;
  const pending = all.filter((a) => a.state === 'pending').length;
  useTopbarStatus(rules.data === undefined ? null : `${all.length} rules · ${firing} firing`);
  const shown = all.filter((a) => {
    const t = filter.trim().toLowerCase();
    if (t !== '' && !a.name.toLowerCase().includes(t) && !a.doc.query.toLowerCase().includes(t)) return false;
    if (segment === 'firing') return a.state === 'firing' || a.state === 'pending';
    if (segment === 'muted') return isMuted(a, now());
    return true;
  });
  const canEdit = hasRole(me.role, 'editor');
  const open = id === undefined ? undefined : id === 'new' ? null : all.find((a) => a.id === id);
  return (
    <div className={cx(s.layout, open !== undefined && s.withDrawer)}>
      <div className={cx(l.page, s.main)}>
        <div className={s.head}>
          <h1>Alert rules</h1>
          {firing > 0 ? <StatusChip variant="firing">{firing} firing</StatusChip> : null}
          {pending > 0 ? <StatusChip variant="pending">{pending} pending</StatusChip> : null}
          <label className={s.filter}>
            <Search size={14} aria-hidden="true" />
            <input
              type="search"
              placeholder="Filter rules"
              aria-label="Filter rules"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
            />
          </label>
          <Segmented<RuleFilter>
            label="Show"
            value={segment}
            onChange={setSegment}
            options={[
              { value: 'all', label: 'All' },
              { value: 'firing', label: 'Firing' },
              { value: 'muted', label: 'Muted' },
            ]}
          />
          {canEdit ? (
            <Button variant="ghost" icon={<Plus size={14} />} onClick={() => navigate('/alerts/new')}>
              New rule
            </Button>
          ) : null}
        </div>
        {rules.isError ? <Notice kind="danger">{errorMessage(rules.error)}</Notice> : null}
        {rules.isPending ? (
          <Spinner />
        ) : all.length === 0 ? (
          <EmptyState
            icon={<Bell size={20} aria-hidden="true" />}
            title="No alert rules"
            hint="A rule runs a stats query on a schedule and notifies channels."
          />
        ) : (
          <div className={cx(l.card, l.tableWrap)}>
            <table className={cx(l.table, s.rules, open !== undefined && s.compact)}>
              <thead>
                <tr>
                  <th>State</th>
                  <th>Rule</th>
                  <th>Query</th>
                  <th>Condition</th>
                  <th>Severity</th>
                  <th className={l.num}>Last value</th>
                  <th>Channels</th>
                  <th>Last change</th>
                  <th aria-label="Edit" />
                </tr>
              </thead>
              <tbody>
                {shown.map((a) => {
                  const chip = stateChip(a, now());
                  const muted = isMuted(a, now());
                  const note =
                    a.state === 'error' && a.lastError !== ''
                      ? a.lastError
                      : muted && a.doc.muteUntil !== null
                        ? `muted until ${formatClock(Date.parse(a.doc.muteUntil), false).slice(0, 5)}`
                        : a.doc.groupBy.length > 0
                          ? `group: ${a.doc.groupBy.join(', ')}`
                          : '';
                  return (
                    <tr
                      key={a.id}
                      aria-selected={a.id === id}
                      className={s.ruleRow}
                      onClick={() => navigate(`/alerts/${encodeURIComponent(a.id)}`)}
                    >
                      <td>
                        <StatusChip variant={chip.variant}>{chip.label}</StatusChip>
                      </td>
                      <td>
                        <div className={s.ruleName}>{a.name}</div>
                        {note !== '' ? (
                          <div className={cx(s.ruleNote, a.state === 'error' && s.errorNote)}>{note}</div>
                        ) : null}
                      </td>
                      <td>
                        <span className={s.query} title={a.doc.query}>
                          {a.doc.query}
                        </span>
                      </td>
                      <td className={s.condition}>{conditionText(a.doc)}</td>
                      <td>
                        <StatusChip variant={SEVERITY_VARIANT[a.doc.severity]}>{a.doc.severity}</StatusChip>
                      </td>
                      <td
                        className={cx(
                          l.num,
                          a.state === 'firing' && s.firingValue,
                          a.state === 'pending' && s.pendingValue,
                        )}
                      >
                        {formatNumber(a.lastValue)}
                      </td>
                      <td>
                        <span className={s.tags}>
                          {a.doc.channels.map((c) => (
                            <Tag key={c}>{channelName.get(c) ?? c}</Tag>
                          ))}
                        </span>
                      </td>
                      <td className={l.subtle}>
                        {a.stateSince === null ? '—' : formatAge(Date.parse(a.stateSince), now())}
                      </td>
                      <td>
                        <Button
                          size="sm"
                          variant="ghost"
                          iconOnly
                          icon={<Pencil size={14} />}
                          aria-label={`Edit ${a.name}`}
                          onClick={(e) => {
                            e.stopPropagation();
                            navigate(`/alerts/${encodeURIComponent(a.id)}`);
                          }}
                        />
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
        <Card title="Recent transitions" subtitle="alert_history" flush>
          {recent.length === 0 ? (
            <div className={s.noHistory}>No transitions yet.</div>
          ) : (
            <ol className={s.transitions}>
              {recent.map(({ rule, h }) => {
                const st =
                  h.toState === 'ok'
                    ? { variant: 'ok' as const, label: 'Resolved' }
                    : stateVariant(h.toState);
                const group = groupText(h.group);
                return (
                  <li key={`${rule.id}-${h.id}`} className={s.transition}>
                    <span className={s.tTime}>{formatClock(Date.parse(h.at), false)}</span>
                    <span>
                      <StatusChip variant={st.variant}>{st.label}</StatusChip>
                    </span>
                    <span className={s.tText}>
                      <strong>{rule.name}</strong>
                      {group !== '' ? ` · ${group}` : ''}
                      {h.value !== null
                        ? ` · value ${formatNumber(h.value)} ${h.toState === 'ok' ? '≤' : rule.doc.condition.op} ${formatNumber(rule.doc.condition.threshold)}`
                        : ''}
                    </span>
                    <span className={s.tNotified}>
                      {notifies(h) && rule.doc.channels.length > 0
                        ? `notified ${rule.doc.channels.length}`
                        : '—'}
                    </span>
                  </li>
                );
              })}
            </ol>
          )}
        </Card>
      </div>
      {open !== undefined ? <AlertDrawer key={id} alert={open} onClose={() => navigate('/alerts')} /> : null}
    </div>
  );
}
