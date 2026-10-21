// SPDX-License-Identifier: Apache-2.0
//
// HTTP client for /api/v1 (SPEC §11.1): JSON bodies, the double-submit CSRF header taken from
// the tm_csrf cookie on mutating requests, typed errors, a timeout on every request and a hook
// that sends the user to the login page on 401.

import { byteRangeToUtf16 } from '../lib/utf8';

export const API_PREFIX = '/api/v1';
export const CSRF_COOKIE = 'tm_csrf';
export const CSRF_HEADER = 'X-Tidemark-CSRF';
export const DEFAULT_TIMEOUT_MS = 30_000;

/**
 * Error code of SPEC §11.1 (`bad_request`, `invalid_query`, `unauthorized`, `forbidden`,
 * `not_found`, `conflict`, `payload_too_large`, `rate_limited`,
 * `backpressure`, `internal`) or a client-side `network` / `timeout` / `http_<status>`.
 */
export type ErrorCode = string;

/** An API failure: status 0 for network errors and timeouts. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: ErrorCode;
  readonly details: Record<string, unknown>;

  constructor(status: number, code: ErrorCode, message: string, details: Record<string, unknown> = {}) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
  }

  /** For `invalid_query`: the error position as a UTF-16 range of `query`. */
  queryRange(query: string): { from: number; to: number } | null {
    if (this.code !== 'invalid_query') return null;
    const pos = this.details.pos;
    const len = this.details.len;
    if (typeof pos !== 'number') return null;
    return byteRangeToUtf16(query, pos, typeof len === 'number' ? len : 1);
  }

  get hint(): string | undefined {
    const h = this.details.hint;
    return typeof h === 'string' && h !== '' ? h : undefined;
  }

  /** For `rate_limited`: the `Retry-After` of the response in seconds. */
  get retryAfter(): number | undefined {
    const s = this.details.retryAfter;
    return typeof s === 'number' ? s : undefined;
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

/** Reads one cookie value from a `document.cookie` string. */
export function readCookie(cookies: string, name: string): string | undefined {
  for (const part of cookies.split(';')) {
    const eq = part.indexOf('=');
    if (eq < 0) continue;
    if (part.slice(0, eq).trim() === name) {
      try {
        return decodeURIComponent(part.slice(eq + 1).trim());
      } catch {
        return part.slice(eq + 1).trim();
      }
    }
  }
  return undefined;
}

export type QueryValue = string | number | boolean | undefined | null | readonly string[];

export interface RequestOptions {
  query?: Record<string, QueryValue>;
  body?: unknown;
  /** Multipart or other raw body (Content-Type is then set by the browser). */
  rawBody?: FormData | Blob;
  signal?: AbortSignal;
  timeoutMs?: number;
  /** Do not call onUnauthorized on 401 (login form, initial session probe). */
  skipAuthRedirect?: boolean;
  /** Path is relative to the site root instead of /api/v1 (e.g. `/api`, `/readyz`). */
  absolute?: boolean;
}

export interface ApiClientOptions {
  fetch?: typeof fetch;
  cookies?: () => string;
  onUnauthorized?: () => void;
  baseUrl?: string;
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

/** Builds `path?query` with repeated keys flattened to comma lists. */
export function buildUrl(base: string, path: string, query?: Record<string, QueryValue>): string {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(query ?? {})) {
    if (v === undefined || v === null || v === '') continue;
    if (Array.isArray(v)) {
      if (v.length > 0) params.set(k, v.join(','));
    } else {
      params.set(k, String(v));
    }
  }
  const qs = params.toString();
  return `${base}${path}${qs === '' ? '' : `?${qs}`}`;
}

/** Combines an optional caller signal with a timeout; returns the signal and a cleanup. */
function withTimeout(signal: AbortSignal | undefined, ms: number): { signal: AbortSignal; done: () => void } {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(new DOMException('timeout', 'TimeoutError')), ms);
  const onAbort = (): void => ctrl.abort(signal?.reason);
  if (signal) {
    if (signal.aborted) ctrl.abort(signal.reason);
    else signal.addEventListener('abort', onAbort, { once: true });
  }
  return {
    signal: ctrl.signal,
    done: () => {
      clearTimeout(timer);
      signal?.removeEventListener('abort', onAbort);
    },
  };
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return typeof v === 'object' && v !== null && !Array.isArray(v)
    ? (v as Record<string, unknown>)
    : undefined;
}

/** The `Retry-After` header of a 429 in whole seconds, when it is one (never a date). */
function retryAfterSeconds(res: Response): number | undefined {
  const header = res.headers.get('Retry-After');
  if (header === null || !/^\d+$/.test(header.trim())) return undefined;
  return Number(header);
}

async function errorFrom(res: Response): Promise<ApiError> {
  let body: unknown;
  try {
    body = await res.json();
  } catch {
    body = undefined;
  }
  const retryAfter = retryAfterSeconds(res);
  const extra = retryAfter === undefined ? {} : { retryAfter };
  const err = asRecord(asRecord(body)?.error);
  if (err && typeof err.code === 'string') {
    const message = typeof err.message === 'string' ? err.message : res.statusText;
    return new ApiError(res.status, err.code, message, { ...extra, ...asRecord(err.details) });
  }
  return new ApiError(res.status, `http_${res.status}`, res.statusText || `HTTP ${res.status}`, extra);
}

/** The /api/v1 client. One instance is created by the composition root (src/main.tsx). */
export class ApiClient {
  private readonly fetchFn: typeof fetch;
  private readonly cookies: () => string;
  private readonly base: string;
  private readonly root: string;
  private onUnauthorized: (() => void) | undefined;

  constructor(opts: ApiClientOptions = {}) {
    this.fetchFn = opts.fetch ?? ((input, init) => window.fetch(input, init));
    this.cookies = opts.cookies ?? (() => document.cookie);
    this.root = opts.baseUrl ?? '';
    this.base = this.root + API_PREFIX;
    this.onUnauthorized = opts.onUnauthorized;
  }

  /** Sets the 401 handler (the router installs it once it exists). */
  setUnauthorizedHandler(fn: (() => void) | undefined): void {
    this.onUnauthorized = fn;
  }

  /** Absolute URL of an API path, e.g. for downloads and EventSource. */
  url(path: string, query?: Record<string, QueryValue>): string {
    return buildUrl(this.base, path, query);
  }

  get<T>(path: string, opts: RequestOptions = {}): Promise<T> {
    return this.request<T>('GET', path, opts);
  }

  post<T>(path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
    return this.request<T>('POST', path, { ...opts, body });
  }

  put<T>(path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
    return this.request<T>('PUT', path, { ...opts, body });
  }

  patch<T>(path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
    return this.request<T>('PATCH', path, { ...opts, body });
  }

  delete<T = void>(path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
    return this.request<T>('DELETE', path, { ...opts, body });
  }

  async request<T>(method: string, path: string, opts: RequestOptions = {}): Promise<T> {
    const headers: Record<string, string> = { Accept: 'application/json' };
    let body: BodyInit | undefined;
    if (opts.rawBody !== undefined) {
      body = opts.rawBody;
    } else if (opts.body !== undefined) {
      headers['Content-Type'] = 'application/json';
      body = JSON.stringify(opts.body);
    }
    if (!SAFE_METHODS.has(method)) {
      const csrf = readCookie(this.cookies(), CSRF_COOKIE);
      if (csrf !== undefined) headers[CSRF_HEADER] = csrf;
    }
    const t = withTimeout(opts.signal, opts.timeoutMs ?? DEFAULT_TIMEOUT_MS);
    let res: Response;
    try {
      const url = opts.absolute ? buildUrl(this.root, path, opts.query) : this.url(path, opts.query);
      res = await this.fetchFn(url, {
        method,
        headers,
        body,
        credentials: 'same-origin',
        signal: t.signal,
      });
    } catch (e) {
      t.done();
      if (opts.signal?.aborted) throw e;
      if (t.signal.aborted) throw new ApiError(0, 'timeout', 'The request timed out.');
      throw new ApiError(0, 'network', 'The server could not be reached.');
    }
    try {
      if (!res.ok) {
        const err = await errorFrom(res);
        if (res.status === 401 && !opts.skipAuthRedirect) this.onUnauthorized?.();
        throw err;
      }
      if (res.status === 204 || res.headers.get('Content-Length') === '0') return undefined as T;
      const type = res.headers.get('Content-Type') ?? '';
      if (type.includes('application/json')) return (await res.json()) as T;
      return (await res.text()) as T;
    } finally {
      t.done();
    }
  }
}

/** A user-facing message for any error thrown by the client or a query. */
export function errorMessage(e: unknown): string {
  if (isApiError(e)) {
    return e.message;
  }
  if (e instanceof Error) return e.message;
  return 'Something went wrong.';
}
