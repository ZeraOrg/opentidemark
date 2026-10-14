// SPDX-License-Identifier: Apache-2.0
//
// Settings → Teams (SPEC §11.5, §15.10, §16.1): the access card (deny-by-default switch with the
// consequence spelled out and the number of users in no team), the teams table and the new-team
// dialog. Admin only; the detail page is TeamPage.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Plus, Users } from 'lucide-react';
import { useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { admin as adminApi, teams as teamsApi } from '../../api/endpoints';
import type { TeamSummary } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { Button, Field, Spinner, Tag, TextArea, TextInput, Toggle } from '../../components/controls';
import { Card, EmptyState, layoutStyles as l, Notice } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { formatAge } from '../../lib/time';
import s from './settings.module.css';

export const TEAM_NAME_MAX = 64;
export const TEAM_DESCRIPTION_MAX = 1024;
const STREAM_TAGS = 4;

/** "3 users" / "1 user". */
export function userCount(n: number): string {
  return `${n} ${n === 1 ? 'user' : 'users'}`;
}

export function AccessCard() {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const [confirm, setConfirm] = useState(false);
  const access = useQuery({ queryKey: ['teams-access'], queryFn: () => teamsApi.access(api) });
  const patch = useMutation({
    mutationFn: (denyByDefault: boolean) => adminApi.patchSettings(api, { access: { denyByDefault } }),
    onSuccess: (settings) => {
      qc.setQueryData(['teams-access'], (cur: { usersWithoutTeam: number } | undefined) => ({
        usersWithoutTeam: cur?.usersWithoutTeam ?? 0,
        denyByDefault: settings.access.denyByDefault,
      }));
      void qc.invalidateQueries({ queryKey: ['teams-access'] });
      void qc.invalidateQueries({ queryKey: ['me'] });
      toast.push({
        kind: 'success',
        title: settings.access.denyByDefault ? 'Deny by default is on' : 'Deny by default is off',
        body: settings.access.denyByDefault
          ? 'Users in no team now see no data.'
          : 'Users in no team now see every stream.',
      });
      setConfirm(false);
    },
  });
  const deny = access.data?.denyByDefault ?? false;
  const without = access.data?.usersWithoutTeam ?? 0;
  const change = (on: boolean): void => {
    if (on && without > 0) setConfirm(true);
    else patch.mutate(on);
  };
  return (
    <Card title="Users in no team" subtitle="what a user sees before an admin adds them to a team">
      {access.isError ? <Notice kind="danger">{errorMessage(access.error)}</Notice> : null}
      <div className={s.accessRow}>
        <Toggle checked={deny} onChange={change} className={s.accessToggle}>
          Deny by default
        </Toggle>
        <div className={s.accessText}>
          {deny ? (
            <p>
              A user in no team sees <strong>no data</strong> until they are added to a team.
            </p>
          ) : (
            <p>
              A user in no team sees <strong>every stream</strong>. Turn this on to make teams the only way to
              see data.
            </p>
          )}
          <p className={l.subtle}>
            {access.isPending ? (
              <Spinner size={12} />
            ) : (
              <>
                {without === 0 ? 'Every user is in a team.' : `${userCount(without)} currently in no team.`}{' '}
                Admins always see every stream.
              </>
            )}
          </p>
        </div>
      </div>
      {patch.isError ? <Notice kind="danger">{errorMessage(patch.error)}</Notice> : null}
      {confirm ? (
        <Modal
          title="Turn on deny by default?"
          onClose={() => setConfirm(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirm(false)}>
                Cancel
              </Button>
              <Button variant="primary" busy={patch.isPending} onClick={() => patch.mutate(true)}>
                Turn on
              </Button>
            </>
          }
        >
          <p>
            <strong>{userCount(without)}</strong> in no team will see no logs, traces or metrics from the next
            request on, until they are added to a team. Admins keep seeing every stream.
          </p>
        </Modal>
      ) : null}
    </Card>
  );
}

function NewTeamDialog({ onClose }: { onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState('');
  const [description, setDescription] = useState('');
  const create = useMutation({
    mutationFn: () => teamsApi.create(api, { name: name.trim(), description: description.trim() }),
    onSuccess: (t) => {
      void qc.invalidateQueries({ queryKey: ['teams'] });
      void qc.invalidateQueries({ queryKey: ['teams-access'] });
      onClose();
      navigate(`/settings/teams/${encodeURIComponent(t.id)}`);
    },
  });
  const valid = name.trim() !== '' && name.trim().length <= TEAM_NAME_MAX;
  return (
    <Modal
      title="New team"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={create.isPending} disabled={!valid} onClick={() => create.mutate()}>
            Create team
          </Button>
        </>
      }
    >
      <form
        className={l.stack}
        onSubmit={(e) => {
          e.preventDefault();
          if (valid) create.mutate();
        }}
      >
        <Field label="Name" help="Up to 64 characters; names are unique ignoring case.">
          {(id) => (
            <TextInput
              id={id}
              value={name}
              maxLength={TEAM_NAME_MAX}
              placeholder="Payments"
              onChange={(e) => setName(e.target.value)}
              data-autofocus
            />
          )}
        </Field>
        <Field label="Description" note="optional">
          {(id) => (
            <TextArea
              id={id}
              rows={2}
              value={description}
              maxLength={TEAM_DESCRIPTION_MAX}
              onChange={(e) => setDescription(e.target.value)}
            />
          )}
        </Field>
        <p className={s.footnote}>Members and streams are added on the team page.</p>
        {create.isError ? <Notice kind="danger">{errorMessage(create.error)}</Notice> : null}
      </form>
    </Modal>
  );
}

function StreamTags({ streams, members }: { streams: readonly string[]; members: number }) {
  if (streams.length === 0) {
    // Only a team that has members and grants nothing is worth a warning.
    return members > 0 ? (
      <span className={s.warnText}>none · members see nothing</span>
    ) : (
      <span className={l.subtle}>none</span>
    );
  }
  const shown = streams.slice(0, STREAM_TAGS);
  return (
    <span className={s.tags}>
      {shown.map((st) => (
        <Tag key={st}>{st}</Tag>
      ))}
      {streams.length > shown.length ? (
        <Tag title={streams.slice(STREAM_TAGS).join(', ')}>+{streams.length - shown.length}</Tag>
      ) : null}
    </span>
  );
}

function TeamsTable({ teams }: { teams: readonly TeamSummary[] }) {
  const { now } = useServices();
  return (
    <div className={cx(l.card, l.tableWrap)}>
      <table className={l.table}>
        <thead>
          <tr>
            <th>Team</th>
            <th>Description</th>
            <th className={l.num}>Members</th>
            <th>Streams</th>
            <th>Updated</th>
          </tr>
        </thead>
        <tbody>
          {teams.map((t) => (
            <tr key={t.id}>
              <td className={s.strong}>
                <Link to={`/settings/teams/${encodeURIComponent(t.id)}`}>{t.name}</Link>
              </td>
              <td className={cx(l.muted, s.teamDescription)} title={t.description}>
                {t.description === '' ? '—' : t.description}
              </td>
              <td className={l.num}>{t.memberCount}</td>
              <td>
                <StreamTags streams={t.streams} members={t.memberCount} />
              </td>
              <td className={l.subtle}>{formatAge(Date.parse(t.updatedAt), now())}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export default function TeamsPage() {
  const api = useApi();
  const [creating, setCreating] = useState(false);
  const list = useQuery({ queryKey: ['teams'], queryFn: () => teamsApi.list(api) });
  const teams = list.data ?? [];
  return (
    <div className={cx(s.page, s.narrowStack)}>
      <div className={s.head}>
        <h1>Teams</h1>
        <span className={l.subtle}>
          Teams decide which streams a user sees · roles decide what they may do
        </span>
        <span className={s.headActions}>
          <Button variant="ghost" icon={<Plus size={14} />} onClick={() => setCreating(true)}>
            New team
          </Button>
        </span>
      </div>
      <AccessCard />
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {list.isPending ? (
        <Spinner />
      ) : teams.length === 0 ? (
        <EmptyState
          icon={<Users size={20} aria-hidden="true" />}
          title="No teams yet"
          hint="Create a team, add members and grant it the streams they may see."
          action={
            <Button size="sm" onClick={() => setCreating(true)}>
              New team
            </Button>
          }
        />
      ) : (
        <TeamsTable teams={teams} />
      )}
      {creating ? <NewTeamDialog onClose={() => setCreating(false)} /> : null}
    </div>
  );
}
