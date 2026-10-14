// SPDX-License-Identifier: Apache-2.0
//
// Settings → Teams → team (SPEC §11.5, §15.10, §16.1): details (rename, description, delete with
// typed confirmation), members (searchable multi-select of users) and streams (known streams plus
// typed names that need not exist yet). Every card saves on its own and shows whether it is dirty
// or saved.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, ChevronRight, Trash2 } from 'lucide-react';
import { useEffect, useState, type ReactNode } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { streams as streamsApi, teams as teamsApi, users as usersApi } from '../../api/endpoints';
import { isStreamName, type Team } from '../../api/types';
import { useApi } from '../../app/services';
import { Button, Field, Spinner, TextArea, TextInput } from '../../components/controls';
import { Card, layoutStyles as l, Notice } from '../../components/layout';
import { MultiSelect } from '../../components/MultiSelect';
import { Modal, useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import s from './settings.module.css';
import { TEAM_DESCRIPTION_MAX, TEAM_NAME_MAX } from './TeamsPage';

const SAVED_FLASH_MS = 2_500;
export const STREAM_NAME_HELP =
  'Lowercase letters, digits, dots, dashes and underscores; up to 64 characters.';

/** Returns why `name` is not a valid stream name, or null. */
export function streamNameProblem(name: string): string | null {
  return isStreamName(name) ? null : STREAM_NAME_HELP;
}

export function sameSet(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const xs = [...a].sort();
  const ys = [...b].sort();
  return xs.every((x, i) => x === ys[i]);
}

/** Splits a joined id list ('' = none). */
const splitIds = (joined: string): string[] => (joined === '' ? [] : joined.split(','));

/** What deleting the team changes for its members (SPEC §15.10). */
export function deleteConsequence(team: Pick<Team, 'members' | 'streams'>): string {
  const n = team.members.length;
  if (n === 0) return 'No one is in this team.';
  const who = n === 1 ? 'Its member loses' : `Its ${n} members lose`;
  if (team.streams.length === 0) return `${who} nothing: the team grants no streams.`;
  return `${who} access to its ${team.streams.length === 1 ? 'stream' : 'streams'} from the next request on, unless another team grants them.`;
}

/** Card footer: "Unsaved changes" while dirty, "Saved" briefly after a save, and the Save button. */
function SaveRow({
  dirty,
  saved,
  busy,
  onSave,
  children,
}: {
  dirty: boolean;
  saved: boolean;
  busy: boolean;
  onSave: () => void;
  children?: ReactNode;
}) {
  return (
    <div className={s.saveRow}>
      {children}
      <span className={s.saveState} role="status">
        {dirty ? (
          <span className={s.warnText}>Unsaved changes</span>
        ) : saved ? (
          <span className={s.savedText}>
            <Check size={14} aria-hidden="true" /> Saved
          </span>
        ) : null}
      </span>
      <Button variant="primary" size="sm" disabled={!dirty} busy={busy} onClick={onSave}>
        Save
      </Button>
    </div>
  );
}

function useSavedFlash(): [boolean, () => void] {
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    if (!saved) return undefined;
    const t = setTimeout(() => setSaved(false), SAVED_FLASH_MS);
    return () => clearTimeout(t);
  }, [saved]);
  return [saved, () => setSaved(true)];
}

function DetailsCard({ team }: { team: Team }) {
  const api = useApi();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const [name, setName] = useState(team.name);
  const [description, setDescription] = useState(team.description);
  const [deleting, setDeleting] = useState(false);
  const [confirm, setConfirm] = useState('');
  const [saved, flash] = useSavedFlash();
  // Follow the server when the details change (a save, or a reload); other cards' saves leave
  // this draft alone.
  useEffect(() => setName(team.name), [team.name]);
  useEffect(() => setDescription(team.description), [team.description]);
  const dirty = name.trim() !== team.name || description.trim() !== team.description;
  const valid = name.trim() !== '' && name.trim().length <= TEAM_NAME_MAX;
  const save = useMutation({
    mutationFn: () => teamsApi.update(api, team.id, { name: name.trim(), description: description.trim() }),
    onSuccess: (t) => {
      qc.setQueryData(['team', team.id], t);
      void qc.invalidateQueries({ queryKey: ['teams'] });
      void qc.invalidateQueries({ queryKey: ['users'] });
      flash();
    },
  });
  const remove = useMutation({
    mutationFn: () => teamsApi.remove(api, team.id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['teams'] });
      void qc.invalidateQueries({ queryKey: ['teams-access'] });
      void qc.invalidateQueries({ queryKey: ['users'] });
      toast.push({ kind: 'success', title: `Team ${team.name} deleted` });
      navigate('/settings/teams');
    },
  });
  return (
    <Card
      title="Details"
      actions={
        <Button size="sm" variant="danger" icon={<Trash2 size={14} />} onClick={() => setDeleting(true)}>
          Delete team
        </Button>
      }
    >
      <div className={s.detailsGrid}>
        <Field label="Name" error={valid ? undefined : 'A name of 1 to 64 characters is required.'}>
          {(id) => (
            <TextInput
              id={id}
              value={name}
              maxLength={TEAM_NAME_MAX}
              onChange={(e) => setName(e.target.value)}
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
      </div>
      {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
      <SaveRow dirty={dirty} saved={saved} busy={save.isPending} onSave={() => valid && save.mutate()} />
      {deleting ? (
        <Modal
          title={`Delete ${team.name}`}
          onClose={() => setDeleting(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setDeleting(false)}>
                Cancel
              </Button>
              <Button
                variant="dangerSolid"
                busy={remove.isPending}
                disabled={confirm !== team.name}
                onClick={() => remove.mutate()}
              >
                Delete team
              </Button>
            </>
          }
        >
          <div className={l.stack}>
            <p>{deleteConsequence(team)} Alert rules and dashboards are kept.</p>
            <Field label={`Type ${team.name} to confirm`}>
              {(id) => (
                <TextInput
                  id={id}
                  mono
                  value={confirm}
                  aria-invalid={confirm !== '' && confirm !== team.name}
                  onChange={(e) => setConfirm(e.target.value)}
                  data-autofocus
                />
              )}
            </Field>
            {remove.isError ? <Notice kind="danger">{errorMessage(remove.error)}</Notice> : null}
          </div>
        </Modal>
      ) : null}
    </Card>
  );
}

function MembersCard({ team }: { team: Team }) {
  const api = useApi();
  const qc = useQueryClient();
  const users = useQuery({ queryKey: ['users'], queryFn: () => usersApi.list(api) });
  const serverIds = team.members.map((m) => m.id).join(',');
  const [ids, setIds] = useState<string[]>(() => splitIds(serverIds));
  const [saved, flash] = useSavedFlash();
  useEffect(() => setIds(splitIds(serverIds)), [serverIds]);
  const dirty = !sameSet(ids, splitIds(serverIds));
  const save = useMutation({
    mutationFn: () => teamsApi.setMembers(api, team.id, ids),
    onSuccess: (t) => {
      qc.setQueryData(['team', team.id], t);
      void qc.invalidateQueries({ queryKey: ['teams'] });
      void qc.invalidateQueries({ queryKey: ['teams-access'] });
      void qc.invalidateQueries({ queryKey: ['users'] });
      void qc.invalidateQueries({ queryKey: ['me'] });
      flash();
    },
  });
  const items = (users.data ?? [])
    .filter((u) => !u.disabled || ids.includes(u.id))
    .map((u) => ({
      value: u.id,
      label: u.displayName === '' ? u.username : u.displayName,
      detail: `${u.username} · ${u.role}${u.disabled ? ' · disabled' : ''}`,
    }));
  return (
    <Card title="Members" subtitle={`${ids.length} of ${(users.data ?? []).length} users`}>
      {users.isError ? <Notice kind="danger">{errorMessage(users.error)}</Notice> : null}
      <Field label="Users" help="Admins see every stream anyway; adding them here changes nothing for them.">
        {(id) => (
          <MultiSelect
            id={id}
            label="Users"
            items={items}
            value={ids}
            onChange={setIds}
            loading={users.isPending}
            placeholder="No members"
            emptyText="No users yet."
          />
        )}
      </Field>
      {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
      <SaveRow dirty={dirty} saved={saved} busy={save.isPending} onSave={() => save.mutate()} />
    </Card>
  );
}

function StreamsCard({ team }: { team: Team }) {
  const api = useApi();
  const qc = useQueryClient();
  const known = useQuery({ queryKey: ['streams'], queryFn: () => streamsApi.list(api), staleTime: 60_000 });
  const serverNames = team.streams.join(',');
  const [names, setNames] = useState<string[]>(() => splitIds(serverNames));
  const [saved, flash] = useSavedFlash();
  useEffect(() => setNames(splitIds(serverNames)), [serverNames]);
  const dirty = !sameSet(names, splitIds(serverNames));
  const save = useMutation({
    mutationFn: () => teamsApi.setStreams(api, team.id, names),
    onSuccess: (t) => {
      qc.setQueryData(['team', team.id], t);
      void qc.invalidateQueries({ queryKey: ['teams'] });
      void qc.invalidateQueries({ queryKey: ['me'] });
      flash();
    },
  });
  const items = (known.data?.streams ?? []).map((st) => ({ value: st.name, label: st.name }));
  return (
    <Card title="Streams" subtitle={`${names.length} granted`}>
      {known.isError ? <Notice kind="danger">{errorMessage(known.error)}</Notice> : null}
      <Field
        label="Granted streams"
        help="Members see the logs, spans and metrics of these streams. A name that has no data yet can be granted ahead of time."
      >
        {(id) => (
          <MultiSelect
            id={id}
            label="Streams"
            mono
            items={items}
            value={names}
            onChange={setNames}
            loading={known.isPending}
            placeholder="No streams"
            emptyText="No streams have data yet; type a name below."
            custom={{ validate: streamNameProblem, placeholder: 'new stream name', note: 'no data yet' }}
          />
        )}
      </Field>
      {names.length === 0 ? (
        <Notice kind="warning">This team grants no streams: its members see nothing through it.</Notice>
      ) : null}
      {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
      <SaveRow dirty={dirty} saved={saved} busy={save.isPending} onSave={() => save.mutate()} />
    </Card>
  );
}

export default function TeamPage() {
  const api = useApi();
  const { id = '' } = useParams();
  const team = useQuery({ queryKey: ['team', id], queryFn: () => teamsApi.get(api, id) });
  return (
    <div className={cx(s.page, s.narrowStack)}>
      <nav className={l.breadcrumb} aria-label="Breadcrumb">
        <Link to="/settings/teams">Teams</Link>
        <ChevronRight size={14} aria-hidden="true" />
        <span className={s.strong}>{team.data?.name ?? '…'}</span>
      </nav>
      {team.isPending ? <Spinner /> : null}
      {team.isError ? (
        <Notice kind="danger">
          {errorMessage(team.error)} <Link to="/settings/teams">Back to teams</Link>
        </Notice>
      ) : null}
      {team.data !== undefined ? (
        <>
          <div className={s.head}>
            <h1>{team.data.name}</h1>
            <span className={l.subtle}>
              {team.data.members.length} {team.data.members.length === 1 ? 'member' : 'members'} ·{' '}
              {team.data.streams.length} {team.data.streams.length === 1 ? 'stream' : 'streams'}
            </span>
          </div>
          <DetailsCard team={team.data} />
          <div className={s.teamGrid}>
            <MembersCard team={team.data} />
            <StreamsCard team={team.data} />
          </div>
        </>
      ) : null}
    </div>
  );
}
