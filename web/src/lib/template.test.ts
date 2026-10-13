// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import {
  messageSegments,
  renderTemplate,
  renderValue,
  templateProperties,
  tokenizeTemplate,
} from './template';

describe('renderValue', () => {
  it.each([
    ['abc', 'abc'],
    [42, '42'],
    [true, 'true'],
    [null, 'null'],
    [undefined, 'null'],
    [{ a: 1 }, '{"a":1}'],
    [[1, 'x'], '[1,"x"]'],
  ])('%j', (v, s) => {
    expect(renderValue(v)).toBe(s);
  });

  it('falls back to String for values JSON cannot encode', () => {
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(renderValue(cyclic)).toBe('[object Object]');
  });
});

describe('tokenizeTemplate', () => {
  it('splits holes with format and alignment, and unescapes braces', () => {
    expect(tokenizeTemplate('{{x}} {@User} took {Elapsed,8:0.0} ms {bad')).toEqual([
      { kind: 'text', text: '{x} ' },
      { kind: 'hole', name: 'User', raw: '{@User}' },
      { kind: 'text', text: ' took ' },
      { kind: 'hole', name: 'Elapsed', raw: '{Elapsed,8:0.0}' },
      { kind: 'text', text: ' ms {bad' },
    ]);
  });
});

describe('renderTemplate', () => {
  it('highlights known properties and keeps unknown holes verbatim', () => {
    expect(renderTemplate('Order {OrderId} by {User} in {Missing}', { OrderId: 7, User: { id: 1 } })).toEqual(
      [
        { kind: 'text', text: 'Order ' },
        { kind: 'prop', name: 'OrderId', text: '7' },
        { kind: 'text', text: ' by ' },
        { kind: 'prop', name: 'User', text: '{"id":1}' },
        { kind: 'text', text: ' in {Missing}' },
      ],
    );
  });
});

describe('messageSegments', () => {
  const props = { OrderId: 7 };
  it('uses the template when it renders to the stored message', () => {
    expect(messageSegments('Order 7 placed', 'Order {OrderId} placed', props)).toHaveLength(3);
  });
  it('lets the stored message win when it differs', () => {
    expect(messageSegments('custom', 'Order {OrderId} placed', props)).toEqual([
      { kind: 'text', text: 'custom' },
    ]);
  });
  it('renders the template when there is no stored message', () => {
    expect(
      messageSegments('', 'Order {OrderId}', props)
        .map((s) => s.text)
        .join(''),
    ).toBe('Order 7');
  });
  it('handles plain messages and empty events', () => {
    expect(messageSegments('plain', '', props)).toEqual([{ kind: 'text', text: 'plain' }]);
    expect(messageSegments('', '', props)).toEqual([]);
  });
});

describe('templateProperties', () => {
  it('lists the referenced names', () => {
    expect([...templateProperties('{A} {$B} {A} {{C}}')]).toEqual(['A', 'B']);
  });
});
