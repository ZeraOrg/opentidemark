// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from 'vitest';
import { copyText, downloadText, downloadUrl } from './download';

describe('downloads', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it('saves text through a temporary object URL', () => {
    vi.useFakeTimers();
    const create = vi.fn(() => 'blob:x');
    const revoke = vi.fn();
    Object.assign(URL, { createObjectURL: create, revokeObjectURL: revoke });
    const clicked: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(`${this.download}|${this.getAttribute('href') ?? ''}`);
    });
    downloadText('tidemark.env', 'A=1');
    expect(clicked).toEqual(['tidemark.env|blob:x']);
    expect(document.querySelector('a')).toBeNull();
    vi.advanceTimersByTime(1_000);
    expect(revoke).toHaveBeenCalledWith('blob:x');
  });

  it('starts same-origin downloads', () => {
    const clicked: string[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this.getAttribute('href') ?? '');
    });
    downloadUrl('/api/v1/export?q=x');
    expect(clicked).toEqual(['/api/v1/export?q=x']);
  });
});

describe('copyText', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('reports success and refusal', async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    expect(await copyText('k')).toBe(true);
    expect(writeText).toHaveBeenCalledWith('k');
    writeText.mockImplementationOnce(() => Promise.reject(new Error('denied')));
    expect(await copyText('k')).toBe(false);
  });
});
