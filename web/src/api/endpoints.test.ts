// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { jsonResponse, scriptedFetch } from '../test/fakes';
import { ApiClient } from './client';
import { admin, search, teams, users } from './endpoints';

function client() {
  const { fetch, calls } = scriptedFetch(() => jsonResponse([]));
  return { api: new ApiClient({ fetch, cookies: () => 'tm_csrf=t' }), calls };
}

describe('search selection parameters (SPEC §11.3)', () => {
  it('sends streams and env as comma lists on the GET routes and in the POST body', async () => {
    const { api, calls } = client();
    const scope = { streams: ['api', 'payments'], env: ['prod'] };
    await search.histogram(api, { q: '', from: 'now-1h', to: 'now', ...scope });
    await search.facets(api, { q: '', from: 'now-1h', to: 'now', fields: ['service'], ...scope });
    await search.fields(api, { source: 'logs', from: 'now-1h', to: 'now', ...scope });
    await search.traces(api, { q: '', from: 'now-1h', to: 'now', ...scope });
    await search.metricNames(api, { q: 'http', ...scope });
    await search.complete(api, { q: 'lev', pos: 3, source: 'logs', ...scope });
    await search.query(api, {
      q: '',
      from: 'now-1h',
      to: 'now',
      streams: scope.streams,
      environments: scope.env,
    });
    expect(search.exportUrl(api, { q: '*', from: 'a', to: 'b', format: 'csv', ...scope })).toBe(
      '/api/v1/export?q=*&from=a&to=b&format=csv&streams=api%2Cpayments&env=prod',
    );
    for (const c of calls.slice(0, 6)) expect(c.url).toMatch(/streams=api%2Cpayments&env=prod$/);
    expect(JSON.parse(String(calls[6]?.init.body))).toMatchObject({
      streams: ['api', 'payments'],
      environments: ['prod'],
    });
  });

  it('omits the selection when there is none', async () => {
    const { api, calls } = client();
    await search.histogram(api, { q: '', from: 'now-1h', to: 'now' });
    expect(calls[0]?.url).toBe('/api/v1/histogram?from=now-1h&to=now');
  });
});

describe('teams endpoints (SPEC §11.5)', () => {
  it('uses the documented routes and bodies', async () => {
    const { api, calls } = client();
    await teams.list(api);
    await teams.access(api);
    await teams.get(api, 't 1');
    await teams.create(api, { name: 'Payments' });
    await teams.update(api, 't1', { description: 'x' });
    await teams.setMembers(api, 't1', ['u1']);
    await teams.setStreams(api, 't1', ['payments']);
    await teams.remove(api, 't1');
    await users.setTeams(api, 'u1', ['t1']);
    expect(calls.map((c) => `${String(c.init.method)} ${c.url} ${String(c.init.body ?? '')}`)).toEqual([
      'GET /api/v1/teams ',
      'GET /api/v1/teams/access ',
      'GET /api/v1/teams/t%201 ',
      'POST /api/v1/teams {"name":"Payments"}',
      'PATCH /api/v1/teams/t1 {"description":"x"}',
      'PUT /api/v1/teams/t1/members {"userIds":["u1"]}',
      'PUT /api/v1/teams/t1/streams {"streams":["payments"]}',
      'DELETE /api/v1/teams/t1 ',
      'PUT /api/v1/users/u1/teams {"teamIds":["t1"]}',
    ]);
  });
});

describe('update endpoints (SPEC §11.5, §17.7)', () => {
  it('reads the status and posts the check without a body', async () => {
    const { api, calls } = client();
    await admin.update(api);
    await admin.checkUpdate(api);
    expect(calls.map((c) => `${String(c.init.method)} ${c.url} ${String(c.init.body ?? '')}`)).toEqual([
      'GET /api/v1/system/update ',
      'POST /api/v1/system/update/check ',
    ]);
  });
});
