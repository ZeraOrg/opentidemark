// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from 'vitest';
import { memoryStore } from '../test/fakes';
import { cx } from './cx';
import { browserStore, isStringArray, readJSON } from './storage';

describe('browserStore', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    window.localStorage.clear();
  });

  it('reads, writes and removes', () => {
    const s = browserStore();
    s.set('k', 'v');
    expect(s.get('k')).toBe('v');
    s.remove('k');
    expect(s.get('k')).toBeNull();
  });

  it('never throws when storage is unavailable', () => {
    const boom = (): never => {
      throw new Error('blocked');
    };
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(boom);
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(boom);
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(boom);
    const s = browserStore();
    expect(() => s.set('k', 'v')).not.toThrow();
    expect(s.get('k')).toBeNull();
    expect(() => s.remove('k')).not.toThrow();
  });
});

describe('readJSON', () => {
  it('returns the stored value when it passes the guard', () => {
    expect(readJSON(memoryStore({ k: '["a"]' }), 'k', [], isStringArray)).toEqual(['a']);
  });
  it.each([
    ['missing', {}],
    ['invalid JSON', { k: '[' }],
    ['wrong shape', { k: '[1]' }],
  ])('falls back when %s', (_name, data) => {
    expect(readJSON(memoryStore(data), 'k', ['d'], isStringArray)).toEqual(['d']);
  });
});

describe('cx', () => {
  it('joins truthy class names', () => {
    expect(cx('a', false, null, undefined, '', 'b')).toBe('a b');
  });
});
