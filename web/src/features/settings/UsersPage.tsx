// SPDX-License-Identifier: Apache-2.0
//
// Account and users (SPEC §11.2, §15.2): every user can change their own password (the
// default-credentials banner links here, `#account`); admins also manage users — create with an
// initial password, change role, disable, reset the password, delete.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { KeyRound, Plus, Trash2 } from 'lucide-react';
import { useState, type FormEvent } from 'react';
import { errorMessage } from '../../api/client';
import { auth, users as usersApi } from '../../api/endpoints';
import type { Role, User } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { hasRole, ME_KEY, useMe } from '../../app/session';
import { Button, Field, Select, Spinner, StatusChip, TextInput } from '../../components/controls';
import { Card, layoutStyles as l, Notice } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { formatAge } from '../../lib/time';
import { passwordProblem } from '../auth/password';
import s from './settings.module.css';

const ROLES: readonly { value: Role; label: string }[] = [
  { value: 'viewer', label: 'viewer · read' },
  { value: 'editor', label: 'editor · signals, dashboards, alerts' },
  { value: 'admin', label: 'admin · everything' },
];

function AccountCard() {
  const api = useApi();
  const me = useMe();
  const qc = useQueryClient();
  const toast = useToast();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const problem = next === '' ? null : passwordProblem(next, me.username, current);
  const mismatch = confirm !== '' && confirm !== next;
  const change = useMutation({
    mutationFn: () => auth.changePassword(api, current, next),
    onSuccess: () => {
      setCurrent('');
      setNext('');
      setConfirm('');
      void qc.invalidateQueries({ queryKey: ME_KEY });
      toast.push({
        kind: 'success',
        title: 'Password changed',
        body: 'Your other sessions were signed out.',
      });
    },
  });
  const submit = (e: FormEvent): void => {
    e.preventDefault();
    if (problem === null && !mismatch && next !== '' && current !== '') change.mutate();
  };
  return (
    <Card title="Your account" subtitle={`${me.username} · ${me.role}`}>
      <form id="account" className={s.accountForm} onSubmit={submit} noValidate>
        {me.defaultCredentials ? (
          <Notice kind="danger">The default admin password is still in use. Change it now.</Notice>
        ) : null}
        <Field label="Current password">
          {(id) => (
            <TextInput
              id={id}
              type="password"
              autoComplete="current-password"
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          )}
        </Field>
        <Field
          label="New password"
          help="At least 12 characters, not your username, not a common password."
          error={problem ?? undefined}
        >
          {(id) => (
            <TextInput
              id={id}
              type="password"
              autoComplete="new-password"
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
          )}
        </Field>
        <Field label="Confirm new password" error={mismatch ? 'The passwords do not match.' : undefined}>
          {(id) => (
            <TextInput
              id={id}
              type="password"
              autoComplete="new-password"
              value={confirm}
              onChange={(e) => setConfirm(e.target.value)}
            />
          )}
        </Field>
        {change.isError ? <Notice kind="danger">{errorMessage(change.error)}</Notice> : null}
        <div>
          <Button
            type="submit"
            variant="primary"
            icon={<KeyRound size={14} />}
            busy={change.isPending}
            disabled={problem !== null || mismatch || next === '' || current === ''}
          >
            Change password
          </Button>
        </div>
      </form>
    </Card>
  );
}

function UserDialog({ user, onClose }: { user: User | null; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const [username, setUsername] = useState(user?.username ?? '');
  const [displayName, setDisplayName] = useState(user?.displayName ?? '');
  const [role, setRole] = useState<Role>(user?.role ?? 'viewer');
  const [password, setPassword] = useState('');
  const problem =
    password === ''
      ? user === null
        ? 'Set an initial password.'
        : null
      : passwordProblem(password, username);
  const save = useMutation({
    mutationFn: () =>
      user === null
        ? usersApi.create(api, { username: username.trim(), displayName: displayName.trim(), role, password })
        : usersApi.update(api, user.id, {
            displayName: displayName.trim(),
            role,
            ...(password === '' ? {} : { password }),
          }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['users'] });
      onClose();
    },
  });
  return (
    <Modal
      title={user === null ? 'New user' : `Edit ${user.username}`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={save.isPending}
            disabled={username.trim() === '' || problem !== null}
            onClick={() => save.mutate()}
          >
            {user === null ? 'Create user' : 'Save'}
          </Button>
        </>
      }
    >
      <div className={l.stack}>
        <Field label="Username">
          {(id) => (
            <TextInput
              id={id}
              mono
              value={username}
              disabled={user !== null}
              onChange={(e) => setUsername(e.target.value)}
              data-autofocus
            />
          )}
        </Field>
        <Field label="Display name">
          {(id) => <TextInput id={id} value={displayName} onChange={(e) => setDisplayName(e.target.value)} />}
        </Field>
        <Field label="Role">
          {(id) => (
            <Select id={id} options={ROLES} value={role} onChange={(e) => setRole(e.target.value as Role)} />
          )}
        </Field>
        <Field
          label={user === null ? 'Initial password' : 'Reset password'}
          help={
            user === null
              ? 'The user must change it at first sign-in.'
              : 'Leave empty to keep the current password; a reset signs the user out.'
          }
          error={password !== '' ? (problem ?? undefined) : undefined}
        >
          {(id) => (
            <TextInput
              id={id}
              type="password"
              autoComplete="new-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          )}
        </Field>
        {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
      </div>
    </Modal>
  );
}

function UsersTable() {
  const api = useApi();
  const me = useMe();
  const qc = useQueryClient();
  const { now } = useServices();
  const [editing, setEditing] = useState<User | null | undefined>(undefined);
  const [deleting, setDeleting] = useState<User | null>(null);
  const list = useQuery({ queryKey: ['users'], queryFn: () => usersApi.list(api) });
  const invalidate = (): void => void qc.invalidateQueries({ queryKey: ['users'] });
  const toggle = useMutation({
    mutationFn: (u: User) => usersApi.update(api, u.id, { disabled: !u.disabled }),
    onSuccess: invalidate,
  });
  const remove = useMutation({
    mutationFn: (u: User) => usersApi.remove(api, u.id),
    onSuccess: () => {
      invalidate();
      setDeleting(null);
    },
  });
  return (
    <Card
      title="Users"
      actions={
        <Button size="sm" variant="ghost" icon={<Plus size={14} />} onClick={() => setEditing(null)}>
          New user
        </Button>
      }
      flush
    >
      {list.isPending ? <Spinner /> : null}
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {toggle.isError ? <Notice kind="danger">{errorMessage(toggle.error)}</Notice> : null}
      <div className={l.tableWrap}>
        <table className={l.table}>
          <thead>
            <tr>
              <th>Username</th>
              <th>Name</th>
              <th>Role</th>
              <th>Status</th>
              <th>Last sign-in</th>
              <th aria-label="Actions" />
            </tr>
          </thead>
          <tbody>
            {(list.data ?? []).map((u) => (
              <tr key={u.id}>
                <td className={l.monoCell}>{u.username}</td>
                <td>{u.displayName}</td>
                <td>{u.role}</td>
                <td>
                  {u.disabled ? (
                    <StatusChip variant="muted">disabled</StatusChip>
                  ) : u.mustChangePassword ? (
                    <StatusChip variant="pending">must change password</StatusChip>
                  ) : (
                    <StatusChip variant="ok">active</StatusChip>
                  )}
                </td>
                <td className={l.subtle}>
                  {u.lastLoginAt === null ? 'never' : formatAge(Date.parse(u.lastLoginAt), now())}
                </td>
                <td className={s.rowActions}>
                  <Button size="sm" variant="ghost" onClick={() => setEditing(u)}>
                    Edit
                  </Button>
                  {u.id !== me.id ? (
                    <>
                      <Button
                        size="sm"
                        variant="ghost"
                        busy={toggle.isPending && toggle.variables.id === u.id}
                        onClick={() => toggle.mutate(u)}
                      >
                        {u.disabled ? 'Enable' : 'Disable'}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        iconOnly
                        icon={<Trash2 size={14} />}
                        aria-label={`Delete ${u.username}`}
                        onClick={() => setDeleting(u)}
                      />
                    </>
                  ) : null}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {editing !== undefined ? <UserDialog user={editing} onClose={() => setEditing(undefined)} /> : null}
      {deleting !== null ? (
        <Modal
          title={`Delete ${deleting.username}`}
          onClose={() => setDeleting(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setDeleting(null)}>
                Cancel
              </Button>
              <Button variant="dangerSolid" busy={remove.isPending} onClick={() => remove.mutate(deleting)}>
                Delete user
              </Button>
            </>
          }
        >
          <p>
            The user, their sessions and API tokens are removed. Dashboards and signals they own are kept.
          </p>
          {remove.isError ? <Notice kind="danger">{errorMessage(remove.error)}</Notice> : null}
        </Modal>
      ) : null}
    </Card>
  );
}

export default function UsersPage() {
  const me = useMe();
  return (
    <div className={cx(s.page, s.narrowStack)}>
      <div className={s.head}>
        <h1>{hasRole(me.role, 'admin') ? 'Users' : 'Account'}</h1>
        <span className={l.subtle}>
          Roles: viewer reads, editor also changes signals, dashboards and alerts, admin manages everything
        </span>
      </div>
      <AccountCard />
      {hasRole(me.role, 'admin') ? <UsersTable /> : null}
    </div>
  );
}
