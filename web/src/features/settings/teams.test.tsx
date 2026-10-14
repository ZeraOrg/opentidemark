// SPDX-License-Identifier: Apache-2.0

import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Route, Routes } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import type { Team, TeamSummary, User } from '../../api/types';
import { jsonResponse } from '../../test/fakes';
import { ADMIN_ME, renderSignedIn, restrictedMe } from '../../test/session';
import { accessSummary } from './UsersPage';
import TeamPage, { deleteConsequence, sameSet, streamNameProblem } from './TeamPage';
import TeamsPage, { userCount } from './TeamsPage';

const SUMMARIES: TeamSummary[] = [
  {
    id: 't1',
    name: 'Payments',
    description: 'Card and wallet services',
    memberCount: 2,
    streams: ['payments', 'api', 'b', 'c', 'd', 'e'],
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-12T00:00:00Z',
  },
  {
    id: 't2',
    name: 'Empty',
    description: '',
    memberCount: 0,
    streams: [],
    createdAt: '2026-10-01T00:00:00Z',
    updatedAt: '2026-10-12T00:00:00Z',
  },
];

const TEAM: Team = {
  id: 't1',
  name: 'Payments',
  description: 'Card and wallet services',
  members: [{ id: 'u-sam', username: 'sam', displayName: 'Sam Lindqvist', role: 'viewer' }],
  streams: ['payments'],
  createdAt: '2026-10-01T00:00:00Z',
  updatedAt: '2026-10-12T00:00:00Z',
};

const USERS: User[] = [
  {
    id: 'u-sam',
    username: 'sam',
    displayName: 'Sam Lindqvist',
    role: 'viewer',
    mustChangePassword: false,
    disabled: false,
    lastLoginAt: null,
    teams: [{ id: 't1', name: 'Payments' }],
    createdAt: '',
    updatedAt: '',
  },
  {
    id: 'u-maria',
    username: 'maria',
    displayName: 'Maria Okafor',
    role: 'editor',
    mustChangePassword: false,
    disabled: false,
    lastLoginAt: null,
    teams: [],
    createdAt: '',
    updatedAt: '',
  },
];

describe('teams helpers', () => {
  it('validates stream names and compares sets', () => {
    expect(streamNameProblem('payments')).toBeNull();
    expect(streamNameProblem('Payments')).not.toBeNull();
    expect(streamNameProblem('')).not.toBeNull();
    expect(sameSet(['a', 'b'], ['b', 'a'])).toBe(true);
    expect(sameSet(['a'], ['a', 'b'])).toBe(false);
    expect(userCount(1)).toBe('1 user');
    expect(userCount(3)).toBe('3 users');
    expect(deleteConsequence({ members: [], streams: [] })).toBe('No one is in this team.');
    expect(deleteConsequence({ members: TEAM.members, streams: [] })).toBe(
      'Its member loses nothing: the team grants no streams.',
    );
    expect(deleteConsequence({ members: TEAM.members, streams: ['a', 'b'] })).toContain(
      'Its member loses access to its streams',
    );
  });

  it('summarises the account access', () => {
    expect(accessSummary(ADMIN_ME).title).toBe('You see every stream.');
    expect(accessSummary(restrictedMe(['a', 'b'])).title).toBe('You see 2 streams.');
    expect(accessSummary(restrictedMe([])).detail).toContain('grants no streams yet');
    expect(
      accessSummary({ ...ADMIN_ME, role: 'viewer', streamAccess: 'default-all', teams: [] }).detail,
    ).toContain('in no team');
    expect(
      accessSummary({ ...ADMIN_ME, role: 'viewer', allStreams: false, streamAccess: 'default-none' }).title,
    ).toBe('You see no streams.');
  });
});

describe('TeamsPage', () => {
  it('shows the access card and the teams, and confirms turning deny-by-default on', async () => {
    const patches: unknown[] = [];
    let deny = false;
    renderSignedIn(<TeamsPage />, {
      path: '/settings/teams',
      respond: (url, init) => {
        if (url === '/api/v1/teams/access') return jsonResponse({ denyByDefault: deny, usersWithoutTeam: 3 });
        if (url === '/api/v1/teams') return jsonResponse(SUMMARIES);
        if (url === '/api/v1/settings' && init.method === 'PATCH') {
          patches.push(JSON.parse(String(init.body)));
          deny = true;
          return jsonResponse({
            retention: { logsDays: 1, spansDays: 1, metricsDays: 1 },
            access: { denyByDefault: true },
            ui: {},
          });
        }
        return undefined;
      },
    });
    expect(
      await screen.findByText('3 users currently in no team. Admins always see every stream.'),
    ).toBeInTheDocument();
    const row = screen.getByRole('row', { name: /Payments/ });
    expect(within(row).getByRole('link', { name: 'Payments' })).toHaveAttribute('href', '/settings/teams/t1');
    expect(within(row).getByText('+2')).toHaveAttribute('title', 'd, e');
    expect(screen.getByRole('row', { name: /Empty/ })).toHaveTextContent('none');
    await userEvent.click(screen.getByRole('switch', { name: 'Deny by default' }));
    const dialog = await screen.findByRole('dialog', { name: 'Turn on deny by default?' });
    expect(dialog).toHaveTextContent('3 users in no team will see no logs');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Turn on' }));
    await waitFor(() => expect(patches).toEqual([{ access: { denyByDefault: true } }]));
    expect(await screen.findByRole('switch', { name: 'Deny by default' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
  });
});

describe('TeamPage', () => {
  it('saves each card on its own with dirty and saved states', async () => {
    const puts: { url: string; body: unknown }[] = [];
    renderSignedIn(
      <Routes>
        <Route path="/settings/teams/:id" element={<TeamPage />} />
      </Routes>,
      {
        path: '/settings/teams/t1',
        respond: (url, init) => {
          if (url === '/api/v1/teams/t1' && init.method === 'PATCH') {
            puts.push({ url, body: JSON.parse(String(init.body)) });
            return jsonResponse({ ...TEAM, name: 'Payments EU' });
          }
          if (url === '/api/v1/teams/t1') return jsonResponse(TEAM);
          if (url === '/api/v1/users') return jsonResponse(USERS);
          if (url === '/api/v1/streams')
            return jsonResponse({ streams: [{ name: 'api' }, { name: 'payments' }], statsUpdatedAt: null });
          if (url === '/api/v1/teams/t1/streams') {
            puts.push({ url, body: JSON.parse(String(init.body)) });
            return jsonResponse({ ...TEAM, streams: ['payments', 'api', 'new-stream'] });
          }
          return undefined;
        },
      },
    );
    expect(await screen.findByRole('heading', { level: 1, name: 'Payments' })).toBeInTheDocument();
    const details = screen.getByRole('region', { name: 'Details' });
    const saves = screen.getAllByRole('button', { name: 'Save' });
    expect(saves.every((b) => b.hasAttribute('disabled'))).toBe(true);
    await userEvent.type(within(details).getByLabelText('Name'), ' EU');
    expect(within(details).getByText('Unsaved changes')).toBeInTheDocument();
    await userEvent.click(within(details).getByRole('button', { name: 'Save' }));
    await waitFor(() =>
      expect(puts[0]?.body).toEqual({ name: 'Payments EU', description: 'Card and wallet services' }),
    );
    expect(await within(details).findByText('Saved')).toBeInTheDocument();

    const streams = screen.getByRole('region', { name: 'Streams' });
    await userEvent.click(within(streams).getByRole('button', { name: /^Streams:/ }));
    const dialog = screen.getByRole('dialog', { name: 'Streams' });
    await userEvent.click(within(dialog).getByLabelText('api'));
    const custom = within(dialog).getByLabelText('Add streams by name');
    await userEvent.type(custom, 'Bad Name{Enter}');
    expect(within(dialog).getByRole('alert')).toHaveTextContent('Lowercase letters');
    await userEvent.clear(custom);
    await userEvent.type(custom, 'new-stream{Enter}');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Done' }));
    expect(within(streams).getByText('no data yet')).toBeInTheDocument();
    await userEvent.click(within(streams).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(puts[1]?.body).toEqual({ streams: ['payments', 'api', 'new-stream'] }));
    expect(await within(streams).findByText('Saved')).toBeInTheDocument();
  });

  it('deletes only after the name is typed', async () => {
    const deletes: string[] = [];
    renderSignedIn(
      <Routes>
        <Route path="/settings/teams/:id" element={<TeamPage />} />
        <Route path="/settings/teams" element={<h1>Teams list</h1>} />
      </Routes>,
      {
        path: '/settings/teams/t1',
        respond: (url, init) => {
          if (url === '/api/v1/teams/t1' && init.method === 'DELETE') {
            deletes.push(url);
            return new Response(null, { status: 204 });
          }
          if (url === '/api/v1/teams/t1') return jsonResponse(TEAM);
          if (url === '/api/v1/users') return jsonResponse(USERS);
          if (url === '/api/v1/streams') return jsonResponse({ streams: [], statsUpdatedAt: null });
          return undefined;
        },
      },
    );
    await userEvent.click(await screen.findByRole('button', { name: 'Delete team' }));
    const dialog = screen.getByRole('dialog', { name: 'Delete Payments' });
    const confirm = within(dialog).getByRole('button', { name: 'Delete team' });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText('Type Payments to confirm'), 'Payments');
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);
    expect(await screen.findByRole('heading', { name: 'Teams list' })).toBeInTheDocument();
    expect(deletes).toEqual(['/api/v1/teams/t1']);
  });
});
