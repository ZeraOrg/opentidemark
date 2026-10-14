// SPDX-License-Identifier: Apache-2.0

import { act, screen } from '@testing-library/react';
import { useSearchParams } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { ADMIN_ME, renderSignedIn, restrictedMe } from '../test/session';
import {
  isScope,
  narrowScope,
  scopeDescription,
  scopeFromParams,
  scopeLabel,
  scopePatch,
  useDataScope,
  usesScope,
  type DataScope,
} from './scope';
import { useMe } from './session';

describe('scope helpers', () => {
  it('reads and writes the URL parameters', () => {
    const p = new URLSearchParams('streams=api,payments,api,&env=prod');
    expect(scopeFromParams(p)).toEqual({ streams: ['api', 'payments'], env: ['prod'] });
    expect(scopePatch({ streams: ['api', 'payments'], env: [] })).toEqual({
      streams: 'api,payments',
      env: null,
    });
    expect(scopePatch({ streams: [], env: ['prod', 'stage'] })).toEqual({ streams: null, env: 'prod,stage' });
  });

  it('caps the selection at the server limits', () => {
    const many = Array.from({ length: 120 }, (_, i) => `s${i}`).join(',');
    expect(scopeFromParams(new URLSearchParams(`streams=${many}`)).streams).toHaveLength(100);
  });

  it('narrows a selection to the visible streams', () => {
    const scope = { streams: ['api', 'secret'], env: ['prod'] };
    expect(narrowScope(scope, ['api', 'payments'])).toEqual({ streams: ['api'], env: ['prod'] });
    expect(narrowScope(scope, null)).toBe(scope);
  });

  it('labels the selection', () => {
    expect(scopeLabel({ streams: [], env: [] })).toBe('All streams');
    expect(scopeLabel({ streams: ['payments'], env: [] })).toBe('payments');
    expect(scopeLabel({ streams: ['a', 'b', 'c'], env: ['prod'] })).toBe('3 streams · prod');
    expect(scopeLabel({ streams: [], env: ['a', 'b'] })).toBe('All streams · 2 environments');
    expect(scopeDescription({ streams: [], env: [] }, null)).toBe('all streams, all environments');
    expect(scopeDescription({ streams: ['a'], env: ['p'] }, ['a', 'b'])).toBe('streams a, environments p');
    expect(scopeDescription({ streams: [], env: [] }, ['a', 'b'])).toBe(
      'all my streams (2), all environments',
    );
  });

  it('guards stored values and knows the scoped routes', () => {
    expect(isScope({ streams: ['a'], env: [] })).toBe(true);
    expect(isScope({ streams: 'a', env: [] })).toBe(false);
    expect(isScope(null)).toBe(false);
    expect(usesScope('/events')).toBe(true);
    expect(usesScope('/dashboards/d1')).toBe(true);
    expect(usesScope('/dashboards')).toBe(false);
    expect(usesScope('/settings/teams')).toBe(false);
  });
});

let latest: DataScope | null = null;

function Probe() {
  const scope = useDataScope();
  const me = useMe();
  const [params] = useSearchParams();
  latest = scope;
  return (
    <div>
      <span data-testid="url">{params.toString()}</span>
      <span data-testid="me">{me.username}</span>
    </div>
  );
}

describe('useDataScope', () => {
  it('round-trips the URL and remembers the choice', async () => {
    const { services } = renderSignedIn(<Probe />, { path: '/events?streams=api&env=prod' });
    await screen.findByText('admin');
    expect(latest?.streams).toEqual(['api']);
    expect(latest?.params).toEqual({ streams: ['api'], env: ['prod'] });
    expect(latest?.body).toEqual({ streams: ['api'], environments: ['prod'] });
    expect(latest?.visible).toBeNull();
    act(() => latest?.set({ streams: ['payments', 'api'], env: [] }));
    expect(screen.getByTestId('url')).toHaveTextContent('streams=payments%2Capi');
    expect(latest?.key).toBe('payments,api|');
    expect(services.store.get('tm.scope')).toBe('{"streams":["payments","api"],"env":[]}');
    act(() => latest?.set({ streams: [], env: [] }));
    expect(screen.getByTestId('url')).toHaveTextContent('');
    expect(latest?.params).toEqual({});
  });

  it('never selects a stream the user cannot see', async () => {
    renderSignedIn(<Probe />, { me: restrictedMe(['payments']), path: '/events?streams=api,payments' });
    await screen.findByText('sam');
    expect(latest?.streams).toEqual(['payments']);
    expect(latest?.visible).toEqual(['payments']);
    expect(latest?.none).toBe(false);
  });

  it('reports a user without any stream', async () => {
    renderSignedIn(<Probe />, { me: { ...restrictedMe([]), streamAccess: 'default-none' } });
    await screen.findByText('sam');
    expect(latest?.none).toBe(true);
    expect(ADMIN_ME.allStreams).toBe(true);
  });
});
