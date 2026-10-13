// SPDX-License-Identifier: Apache-2.0

import { afterEach, describe, expect, it, vi } from 'vitest';
import { jsonResponse, scriptedFetch } from '../test/fakes';
import { ApiClient, ApiError, buildUrl, CSRF_HEADER, errorMessage, isApiError, readCookie } from './client';

/** Like fetch on a request that never answers: rejects once the signal aborts. */
function hangUntilAborted(init: RequestInit | undefined): Promise<Response> {
  return new Promise((_resolve, reject) => {
    const fail = (): void => reject(new DOMException('aborted', 'AbortError'));
    if (init?.signal?.aborted) fail();
    init?.signal?.addEventListener('abort', fail);
  });
}

function headersOf(init: RequestInit): Record<string, string> {
  return init.headers as Record<string, string>;
}

describe('readCookie', () => {
  it.each([
    ['tm_csrf=abc; other=1', 'abc'],
    ['other=1;  tm_csrf=a%20b', 'a b'],
    ['tm_csrf=%E0%A4%A', '%E0%A4%A'],
    ['junk; other=1', undefined],
    ['', undefined],
  ])('%j', (cookies, want) => {
    expect(readCookie(cookies, 'tm_csrf')).toBe(want);
  });
});

describe('buildUrl', () => {
  it('drops empty values and joins lists', () => {
    expect(buildUrl('/api/v1', '/query', { q: 'a b', n: 5, b: false, e: '', u: undefined, z: null })).toBe(
      '/api/v1/query?q=a+b&n=5&b=false',
    );
    expect(buildUrl('', '/x', { signals: ['a', 'b'], none: [] })).toBe('/x?signals=a%2Cb');
    expect(buildUrl('', '/x')).toBe('/x');
  });
});

describe('ApiClient', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('sends JSON with the CSRF header on mutating requests only', async () => {
    const { fetch, calls } = scriptedFetch(() => jsonResponse({ ok: true }));
    const api = new ApiClient({ fetch, cookies: () => 'tm_csrf=tok' });
    await expect(api.post('/signals', { name: 'x' })).resolves.toEqual({ ok: true });
    await api.get('/signals', { query: { q: 'a' } });
    await api.put('/signals/1', {});
    await api.patch('/signals/1', {});
    await api.delete('/signals/1');
    expect(calls.map((c) => `${String(c.init.method)} ${c.url}`)).toEqual([
      'POST /api/v1/signals',
      'GET /api/v1/signals?q=a',
      'PUT /api/v1/signals/1',
      'PATCH /api/v1/signals/1',
      'DELETE /api/v1/signals/1',
    ]);
    const post = calls[0]?.init ?? {};
    expect(headersOf(post)[CSRF_HEADER]).toBe('tok');
    expect(headersOf(post)['Content-Type']).toBe('application/json');
    expect(post.body).toBe('{"name":"x"}');
    expect(post.credentials).toBe('same-origin');
    expect(headersOf(calls[1]?.init ?? {})[CSRF_HEADER]).toBeUndefined();
  });

  it('omits the CSRF header when there is no cookie and sends raw bodies as is', async () => {
    const { fetch, calls } = scriptedFetch(() => new Response(null, { status: 204 }));
    const api = new ApiClient({ fetch, cookies: () => '', baseUrl: 'http://h' });
    const form = new FormData();
    await expect(api.request('POST', '/imports', { rawBody: form })).resolves.toBeUndefined();
    const init = calls[0]?.init ?? {};
    expect(calls[0]?.url).toBe('http://h/api/v1/imports');
    expect(init.body).toBe(form);
    expect(headersOf(init)['Content-Type']).toBeUndefined();
    expect(headersOf(init)[CSRF_HEADER]).toBeUndefined();
  });

  it('resolves absolute paths against the site root and returns text bodies', async () => {
    const { fetch, calls } = scriptedFetch(
      () => new Response('ok', { status: 200, headers: { 'Content-Type': 'text/plain' } }),
    );
    const api = new ApiClient({ fetch, cookies: () => '' });
    await expect(api.get('/readyz', { absolute: true })).resolves.toBe('ok');
    expect(calls[0]?.url).toBe('/readyz');
    expect(api.url('/tail', { q: 'x' })).toBe('/api/v1/tail?q=x');
  });

  it('treats Content-Length 0 as no body', async () => {
    const { fetch } = scriptedFetch(
      () => new Response('', { status: 200, headers: { 'Content-Length': '0' } }),
    );
    await expect(new ApiClient({ fetch, cookies: () => '' }).post('/x')).resolves.toBeUndefined();
  });

  it('maps API errors and calls the 401 handler', async () => {
    const { fetch } = scriptedFetch(() =>
      jsonResponse({ error: { code: 'unauthorized', message: 'Sign in.', details: { a: 1 } } }, 401),
    );
    const onUnauthorized = vi.fn();
    const api = new ApiClient({ fetch, cookies: () => '', onUnauthorized });
    const err = await api.get('/auth/me').catch((e: unknown) => e);
    expect(isApiError(err)).toBe(true);
    expect(err).toMatchObject({ status: 401, code: 'unauthorized', message: 'Sign in.', details: { a: 1 } });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    await api.get('/auth/me', { skipAuthRedirect: true }).catch(() => undefined);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    api.setUnauthorizedHandler(undefined);
    await api.get('/auth/me').catch(() => undefined);
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
  });

  it('falls back to http_<status> for non-API error bodies', async () => {
    const { fetch } = scriptedFetch(() => new Response('<html>', { status: 502, statusText: 'Bad Gateway' }));
    const err = await new ApiClient({ fetch, cookies: () => '' }).get('/x').catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 502, code: 'http_502', message: 'Bad Gateway' });
    const { fetch: f2 } = scriptedFetch(() => jsonResponse({ error: { code: 'conflict' } }, 409));
    const e2 = await new ApiClient({ fetch: f2, cookies: () => '' }).get('/x').catch((e: unknown) => e);
    expect(e2).toMatchObject({ code: 'conflict', details: {} });
    const { fetch: f3 } = scriptedFetch(() => new Response(null, { status: 500 }));
    const e3 = await new ApiClient({ fetch: f3, cookies: () => '' }).get('/x').catch((e: unknown) => e);
    expect(e3).toMatchObject({ code: 'http_500', message: 'HTTP 500' });
  });

  it('reports network failures and timeouts', async () => {
    const down = new ApiClient({ fetch: () => Promise.reject(new TypeError('failed')), cookies: () => '' });
    await expect(down.get('/x')).rejects.toMatchObject({ status: 0, code: 'network' });

    const hang: typeof fetch = (_url, init) => hangUntilAborted(init);
    const slow = new ApiClient({ fetch: hang, cookies: () => '' });
    await expect(slow.get('/x', { timeoutMs: 5 })).rejects.toMatchObject({ code: 'timeout' });
  });

  it('rethrows caller aborts unchanged', async () => {
    const hang: typeof fetch = (_url, init) => hangUntilAborted(init);
    const api = new ApiClient({ fetch: hang, cookies: () => '' });
    const ctrl = new AbortController();
    const p = api.get('/x', { signal: ctrl.signal });
    ctrl.abort();
    await expect(p).rejects.toMatchObject({ name: 'AbortError' });
    const pre = new AbortController();
    pre.abort();
    await expect(api.get('/x', { signal: pre.signal })).rejects.toMatchObject({ name: 'AbortError' });
  });

  it('uses window.fetch and document.cookie by default', async () => {
    const spy = vi.spyOn(window, 'fetch').mockResolvedValue(jsonResponse([1]));
    document.cookie = 'tm_csrf=fromdoc';
    await expect(new ApiClient().post('/x')).resolves.toEqual([1]);
    const init = spy.mock.calls[0]?.[1] ?? {};
    expect(headersOf(init)[CSRF_HEADER]).toBe('fromdoc');
    spy.mockRestore();
  });
});

describe('ApiError', () => {
  it('converts invalid_query byte positions to UTF-16', () => {
    const e = new ApiError(400, 'invalid_query', 'bad', { pos: 7, len: 3, hint: 'did you mean error?' });
    expect(e.queryRange('lävel:errr')).toEqual({ from: 6, to: 9 });
    expect(e.hint).toBe('did you mean error?');
    expect(new ApiError(400, 'invalid_query', 'bad', { pos: 1 }).queryRange('abc')).toEqual({
      from: 1,
      to: 2,
    });
    expect(new ApiError(400, 'invalid_query', 'bad', {}).queryRange('abc')).toBeNull();
    expect(new ApiError(400, 'bad_request', 'bad', { pos: 1 }).queryRange('abc')).toBeNull();
    expect(new ApiError(400, 'bad_request', 'bad', { hint: '' }).hint).toBeUndefined();
  });
});

describe('errorMessage', () => {
  it.each([
    [new ApiError(403, 'forbidden', 'Not allowed.'), 'Not allowed.'],
    [new Error('boom'), 'boom'],
    ['weird', 'Something went wrong.'],
  ])('%s', (e, want) => {
    expect(errorMessage(e)).toBe(want);
  });
});
