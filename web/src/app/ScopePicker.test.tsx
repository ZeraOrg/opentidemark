// SPDX-License-Identifier: Apache-2.0

import { screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { Route, Routes, useSearchParams } from 'react-router-dom';
import { describe, expect, it } from 'vitest';
import { jsonResponse, memoryStore } from '../test/fakes';
import { ADMIN_ME, renderSignedIn, restrictedMe } from '../test/session';
import { ScopePicker, useRememberedScope } from './ScopePicker';

const FACET = [
  {
    field: 'environment',
    values: [
      { value: 'production', count: 10 },
      { value: 'staging', count: 2 },
    ],
    otherCount: 0,
    sampled: false,
  },
];

function UrlProbe() {
  const [params] = useSearchParams();
  return <output data-testid="url">{params.toString()}</output>;
}

function Scoped() {
  useRememberedScope(true);
  return (
    <>
      <ScopePicker />
      <UrlProbe />
    </>
  );
}

function setup(me = ADMIN_ME, path = '/events', streams = ['api', 'workers', 'payments']) {
  return renderSignedIn(
    <Routes>
      <Route path="/events" element={<Scoped />} />
    </Routes>,
    {
      me,
      path,
      respond: (url) => {
        if (url.startsWith('/api/v1/streams'))
          return jsonResponse({ streams: streams.map((name) => ({ name })), statsUpdatedAt: null });
        if (url.startsWith('/api/v1/facets')) return jsonResponse(FACET);
        return undefined;
      },
    },
  );
}

describe('ScopePicker', () => {
  it('lets an admin choose streams and environments, applied to the URL and remembered', async () => {
    const { services } = setup();
    const trigger = await screen.findByRole('button', { name: /^Data scope: all streams, all environments/ });
    expect(trigger).toHaveTextContent('All streams');
    await userEvent.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Data scope' });
    expect(within(dialog).queryByLabelText('Filter streams')).toBeNull();
    await userEvent.click(await within(dialog).findByLabelText('payments'));
    await userEvent.click(await within(dialog).findByLabelText('production'));
    await userEvent.click(within(dialog).getByRole('button', { name: 'Apply' }));
    expect(screen.getByTestId('url')).toHaveTextContent('streams=payments&env=production');
    expect(
      screen.getByRole('button', { name: /^Data scope: streams payments, environments production/ }),
    ).toHaveTextContent('payments · production');
    expect(services.store.get('tm.scope')).toBe('{"streams":["payments"],"env":["production"]}');
    expect(screen.queryByRole('dialog')).toBeNull();
  });

  it('offers a filter above eight streams and closes on Escape with focus on the trigger', async () => {
    const many = Array.from({ length: 12 }, (_, i) => `stream-${i}`);
    setup(ADMIN_ME, '/events', many);
    const trigger = await screen.findByRole('button', { name: /^Data scope/ });
    await userEvent.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Data scope' });
    const filter = await within(dialog).findByLabelText('Filter streams');
    await userEvent.type(filter, 'stream-1');
    expect(within(dialog).getAllByRole('checkbox')).toHaveLength(3 + 1 + 2); // stream-1, -10, -11 + env rows
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('dialog')).toBeNull();
    expect(trigger).toHaveFocus();
    expect(screen.getByTestId('url')).toHaveTextContent('');
  });

  it('moves between rows with the arrow keys', async () => {
    setup();
    await userEvent.click(await screen.findByRole('button', { name: /^Data scope/ }));
    const dialog = screen.getByRole('dialog', { name: 'Data scope' });
    const all = await within(dialog).findByLabelText('All my streams');
    all.focus();
    await userEvent.keyboard('{ArrowDown}');
    expect(within(dialog).getByLabelText('api')).toHaveFocus();
    await userEvent.keyboard('{ArrowUp}{ArrowUp}');
    expect(within(dialog).getByLabelText('workers')).toHaveFocus();
  });

  it('shows a static chip for a single visible stream and nothing for none', async () => {
    setup(restrictedMe(['payments']));
    expect(await screen.findByTitle('The only stream you can see')).toHaveTextContent('payments');
    expect(screen.queryByRole('button', { name: /^Data scope/ })).toBeNull();
  });

  it('renders nothing for a user without streams', async () => {
    const { calls } = setup({ ...restrictedMe([]), streamAccess: 'default-none' });
    await screen.findByTestId('url');
    expect(screen.queryByRole('button', { name: /^Data scope/ })).toBeNull();
    expect(screen.queryByTitle('The only stream you can see')).toBeNull();
    expect(calls.filter((c) => c.url.startsWith('/api/v1/streams'))).toHaveLength(0);
  });

  it('lists only the visible streams of a restricted user', async () => {
    setup(restrictedMe(['api', 'payments']));
    const trigger = await screen.findByRole('button', { name: /^Data scope/ });
    await userEvent.click(trigger);
    const dialog = screen.getByRole('dialog', { name: 'Data scope' });
    expect(within(dialog).getByLabelText('api')).toBeInTheDocument();
    expect(within(dialog).queryByLabelText('workers')).toBeNull();
  });

  it('applies the remembered scope when a scoped route opens without one', async () => {
    renderSignedIn(
      <Routes>
        <Route path="/events" element={<Scoped />} />
      </Routes>,
      {
        path: '/events',
        services: { store: memoryStore({ 'tm.scope': '{"streams":["api"],"env":[]}' }) },
        respond: (url) =>
          url.startsWith('/api/v1/streams')
            ? jsonResponse({ streams: [{ name: 'api' }, { name: 'b' }], statsUpdatedAt: null })
            : undefined,
      },
    );
    expect(await screen.findByTestId('url')).toHaveTextContent('streams=api');
  });
});
