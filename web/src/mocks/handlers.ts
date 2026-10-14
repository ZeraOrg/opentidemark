// SPDX-License-Identifier: Apache-2.0
//
// MSW request handlers for `--mode mock` (e2e tests and screenshots). They implement the
// /api/v1 contract of SPEC §11 closely enough to drive every screen: cookie session with the
// double-submit CSRF check (any seeded user can sign in), a tiny TQL subset for filtering,
// deterministic analytics results, in-memory CRUD, teams and the stream scope of §15.10 (every
// search path reads only the visible streams, narrowed by the `streams` / `env` selection).
// Never part of the production bundle.

import { http, HttpResponse, type JsonBodyType } from 'msw';
import type {
  Alert,
  ApiToken,
  Channel,
  Connection,
  Dashboard,
  Facet,
  ImportJob,
  LogEvent,
  QueryResponse,
  Signal,
  Stream,
  Team,
  TeamSummary,
  User,
} from '../api/types';
import { isStreamName } from '../api/types';
import * as D from './data';

const P = '/api/v1';
/** Holds the username of the signed-in user. */
const SESSION_KEY = 'tm.mock.session';
const MIN_PASSWORD = 12;
const MAX_TEAM_NAME = 64;
const DEFAULT_CREDS_KEY = 'tm.mock.defaultCredentials';
const CSRF = 'mock-csrf-token';
const HOUR = 3_600_000;

function storage(): Storage | null {
  try {
    return window.sessionStorage;
  } catch {
    return null;
  }
}

function sessionUser(): Omit<User, 'teams'> | null {
  const name = storage()?.getItem(SESSION_KEY);
  if (name === null || name === undefined) return null;
  return state.users.find((u) => u.username === name) ?? null;
}

function signedIn(): boolean {
  return sessionUser() !== null;
}

function defaultCredentials(): boolean {
  return storage()?.getItem(DEFAULT_CREDS_KEY) !== '0';
}

function err(status: number, code: string, message: string, details: Record<string, unknown> = {}) {
  return HttpResponse.json({ error: { code, message, details } }, { status });
}

const json = (body: JsonBodyType, status = 200) => HttpResponse.json(body, { status });

/** Session + CSRF checks (SPEC §11.1, §15.2). Returns an error response or null. */
function guard(request: Request): Response | null {
  if (!signedIn()) return err(401, 'unauthorized', 'Sign in required.');
  if (!['GET', 'HEAD'].includes(request.method) && request.headers.get('X-Tidemark-CSRF') !== CSRF)
    return err(403, 'forbidden', 'CSRF token missing or invalid.');
  return null;
}

async function body<T>(request: Request): Promise<T> {
  return (await request.json()) as T;
}

// ---- time ------------------------------------------------------------------------------------
const UNIT: Record<string, number> = { s: 1e3, m: 6e4, h: HOUR, d: 24 * HOUR, w: 7 * 24 * HOUR };
function resolve(v: string | null | undefined, fallback: number): number {
  if (v === null || v === undefined || v === '') return fallback;
  const m = /^now(?:-(\d+)([smhdw]))?$/.exec(v);
  if (m) return Date.now() - (m[1] === undefined ? 0 : Number(m[1]) * (UNIT[m[2] ?? 's'] ?? 0));
  const t = Date.parse(v);
  return Number.isNaN(t) ? fallback : t;
}

function range(from: string | null | undefined, to: string | null | undefined): [number, number] {
  const end = resolve(to, Date.now());
  return [resolve(from, end - HOUR), end];
}

// ---- tiny TQL subset -------------------------------------------------------------------------
const LEVEL_ORDER = ['verbose', 'debug', 'information', 'warning', 'error', 'fatal'];
const LEVEL_ALIAS: Record<string, string> = {
  info: 'information',
  warn: 'warning',
  err: 'error',
  dbg: 'debug',
  vrb: 'verbose',
  ftl: 'fatal',
};
const levelIndex = (v: string): number =>
  LEVEL_ORDER.indexOf(LEVEL_ALIAS[v.toLowerCase()] ?? v.toLowerCase());

function fieldValue(ev: LogEvent, field: string): unknown {
  switch (field) {
    case 'level':
      return ev.level;
    case 'service':
      return ev.service;
    case 'host':
      return ev.host;
    case 'environment':
      return ev.environment;
    case 'stream':
      return ev.stream;
    case 'message':
      return ev.message;
    case 'trace_id':
    case '@trace_id':
      return ev.traceId;
    case 'exception':
      return ev.exception;
    default:
      return ev.properties[field];
  }
}

function globMatch(value: string, pattern: string): boolean {
  if (pattern === '*') return value !== '';
  const re = new RegExp(
    `^${pattern
      .split('*')
      .map((p) => p.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
      .join('.*')}$`,
    'i',
  );
  return re.test(value);
}

function matchTerm(ev: LogEvent, term: string): boolean {
  const t = term.trim();
  if (t === '' || t === '*') return true;
  if (/^not\s+/i.test(t)) return !matchTerm(ev, t.replace(/^not\s+/i, ''));
  const cmp = /^([@\w.]+)\s*(>=|<=|>|<|=|!=|:)\s*"?([^"]*)"?$/.exec(t);
  if (cmp) {
    const [, field = '', op, raw = ''] = cmp;
    const v = fieldValue(ev, field);
    if (field === 'level') {
      const a = levelIndex(String(v));
      const b = levelIndex(raw);
      if (op === '>=') return a >= b;
      if (op === '<=') return a <= b;
      if (op === '>') return a > b;
      if (op === '<') return a < b;
      if (op === '!=') return a !== b;
      return a === b;
    }
    if (v === undefined || v === null) return false;
    if (op === ':' || op === '=') return globMatch(String(v), raw);
    if (op === '!=') return String(v) !== raw;
    const n = Number(v);
    const x = Number(raw);
    if (op === '>') return n > x;
    if (op === '<') return n < x;
    if (op === '>=') return n >= x;
    return n <= x;
  }
  const text = t.replace(/^"|"$/g, '').toLowerCase();
  return ev.message.toLowerCase().includes(text);
}

function filterPart(q: string): string {
  const parts = q.split('|').map((p) => p.trim());
  const first = /^source\s+\w+$/i.test(parts[0] ?? '') ? (parts[1] ?? '') : (parts[0] ?? '');
  return /^(stats|top|sort|limit|fields|where)\b/i.test(first) ? '' : first;
}

function matches(ev: LogEvent, q: string): boolean {
  const f = filterPart(q).replace(/[()]/g, ' ');
  return f.split(/\s+or\s+/i).some((alt) => alt.split(/\s+and\s+/i).every((t) => matchTerm(ev, t)));
}

function signalFilter(signals: string[]): (ev: LogEvent) => boolean {
  const qs = signals.map((id) => D.SIGNALS.find((s) => s.id === id)?.tql ?? '');
  return (ev) => qs.every((q) => matches(ev, q));
}

/** The effective scope of a request: visible streams (null = all) narrowed by the selection. */
interface Selection {
  streams: string[] | null;
  env: string[];
}

const ALL: Selection = { streams: null, env: [] };

const inStreams = (sel: Selection, stream: string): boolean =>
  sel.streams === null || sel.streams.includes(stream);

function selectEvents(
  q: string,
  from: number,
  to: number,
  signals: string[],
  sel: Selection = ALL,
): LogEvent[] {
  const sig = signalFilter(signals);
  return D.EVENTS.filter((ev) => {
    const t = Date.parse(ev.timestamp);
    return (
      t >= from &&
      t <= to &&
      inStreams(sel, ev.stream) &&
      (sel.env.length === 0 || sel.env.includes(ev.environment)) &&
      matches(ev, q) &&
      sig(ev)
    );
  });
}

/** Scale from sampled demo events to the volumes shown in the design (≈ 12.8 k / 15 min). */
const SCALE = 21;

// ---- analytics -------------------------------------------------------------------------------
function kindOf(q: string): QueryResponse['kind'] {
  if (/\|\s*(stats|top)\b/i.test(q)) return /\bspan\b/i.test(q) ? 'timeseries' : 'table';
  return 'events';
}

function byFields(q: string): string[] {
  const m = /\bby\s+([\w.,\s]+?)(?:\s+span\b|$|\|)/i.exec(q);
  return m
    ? (m[1] ?? '')
        .split(',')
        .map((s) => s.trim())
        .filter((s) => s !== '')
    : [];
}

function aggregates(q: string): string[] {
  const m = /\bstats\s+(.+?)(?:\s+by\b|\s+span\b|$|\|)/i.exec(q);
  if (!m) return ['count()'];
  return (m[1] ?? 'count()').split(/,\s*(?![^(]*\))/).map((s) => s.trim());
}

const GROUP_VALUES: Record<string, string[]> = {
  service: D.SERVICES,
  route: ['POST /orders', 'GET /cart', 'other'],
  level: ['Warning', 'Error', 'Fatal'],
  protocol: ['seq', 'otlp', 'elastic'],
  host: ['ck-prod-03', 'ck-prod-01', 'wk-prod-07'],
};

function spanMs(q: string, from: number, to: number): number {
  const m = /\bspan\s+(\d+)([smhd])\b/i.exec(q);
  if (m) return Number(m[1]) * (UNIT[m[2] ?? 'm'] ?? 6e4);
  const steps = [1e4, 3e4, 6e4, 3e5, 6e5, 9e5, 18e5, HOUR, 3 * HOUR, 6 * HOUR, 24 * HOUR];
  return steps.find((s) => (to - from) / s <= 80) ?? 24 * HOUR;
}

function series(seed: string, n: number, base: number, trend: number, spike: boolean): number[] {
  const r = D.rng(D.hashString(seed));
  return Array.from({ length: n }, (_, i) => {
    const x = i / Math.max(1, n - 1);
    const wave = Math.sin(i / 2.3) * 0.12 + (r() - 0.5) * 0.18;
    const bump = spike && x > 0.88 && x < 0.95 ? 1.8 : 0;
    return Math.max(0, Math.round(base * (1 + trend * x + wave + bump) * 100) / 100);
  });
}

function baseFor(agg: string, group: string): number {
  if (/p99/.test(agg)) return 520;
  if (/p95/.test(agg)) return 380;
  if (/p50|avg/.test(agg)) return 140;
  const known = GROUP_VALUES.service?.indexOf(group) ?? -1;
  const i = known >= 0 ? known : D.hashString(group) % 5;
  return [12, 18, 24, 12, 4][i] ?? 20;
}

function timeseries(q: string, from: number, to: number): QueryResponse {
  const span = spanMs(q, from, to);
  const start = Math.floor(from / span) * span;
  const n = Math.max(2, Math.ceil((to - start) / span));
  const by = byFields(q);
  const aggs = aggregates(q);
  const groups = by.length === 0 ? [''] : (GROUP_VALUES[by[0] ?? ''] ?? ['a', 'b']).slice(0, 5);
  const xs = Array.from({ length: n }, (_, i) => start + i * span);
  return {
    kind: 'timeseries',
    span,
    columns: [
      { name: '@bucket', type: 'time' },
      ...by.map((b) => ({ name: b, type: 'string' as const })),
      ...aggs.map((a) => ({ name: a, type: 'number' as const })),
    ],
    series: groups.map((g) => {
      const cols = aggs.map((a) =>
        series(
          `${q}|${g}|${a}`,
          n,
          baseFor(a, g),
          /p\d\d/.test(a) ? 0.5 : 1.4,
          g === 'checkout-api' || g === '',
        ),
      );
      return {
        key: by.length === 0 ? {} : { [by[0] ?? 'k']: g },
        points: xs.map((t, i) => [t, ...cols.map((c) => c[i] ?? null)]),
      };
    }),
    stats: { elapsedMs: 212, rowsRead: 4_120_000, bytesRead: 91_000_000, interpretedAsText: false },
  };
}

function table(q: string, from: number, to: number, signals: string[]): QueryResponse {
  const top = /\btop\s+(\d+)\s+([\w.]+)/i.exec(q);
  const stats = { elapsedMs: 96, rowsRead: 1_204_000, bytesRead: 40_000_000, interpretedAsText: false };
  if (top) {
    const field = top[2] ?? 'service';
    const counts = new Map<string, number>();
    for (const ev of selectEvents(q, from, to, signals)) {
      const v = fieldValue(ev, field);
      if (v !== undefined && v !== '') counts.set(String(v), (counts.get(String(v)) ?? 0) + SCALE);
    }
    if (field === 'exception_type') {
      for (const [k, v] of [
        ['TimeoutException', 338],
        ['TokenExpiredException', 127],
        ['RedisConnectionException', 116],
        ['ValidationException', 64],
      ] as const)
        counts.set(k, v);
    }
    const rows = [...counts.entries()].sort((a, b) => b[1] - a[1]).slice(0, Number(top[1] ?? 10));
    return {
      kind: 'table',
      columns: [
        { name: field, type: 'string' },
        { name: 'count', type: 'number' },
      ],
      rows,
      stats,
    };
  }
  const by = byFields(q);
  const aggs = aggregates(q);
  const groups = by.length === 0 ? [''] : (GROUP_VALUES[by[0] ?? ''] ?? ['a']);
  const rows = groups.map((g) => [
    ...(by.length === 0 ? [] : [g]),
    ...aggs.map((a) => Math.round(baseFor(a, g) * 31 * 100) / 100),
  ]);
  return {
    kind: 'table',
    columns: [
      ...by.map((b) => ({ name: b, type: 'string' as const })),
      ...aggs.map((a) => ({ name: a, type: 'number' as const })),
    ],
    rows,
    stats,
  };
}

interface QueryBody {
  q: string;
  from: string;
  to: string;
  limit?: number;
  cursor?: string;
  signals?: string[];
  streams?: string[];
  environments?: string[];
}

function runQuery(b: QueryBody, sel: Selection): QueryResponse {
  const [from, to] = range(b.from, b.to);
  const kind = kindOf(b.q);
  if (sel.streams !== null && sel.streams.length === 0) return emptyResult(kind);
  if (kind === 'timeseries') return timeseries(b.q, from, to);
  if (kind === 'table') return table(b.q, from, to, b.signals ?? []);
  if (/^\s*source\s+spans/i.test(b.q)) {
    return {
      kind: 'events',
      columns: [
        { name: 'start', type: 'time' },
        { name: 'service', type: 'string' },
        { name: 'name', type: 'string' },
        { name: 'duration_ms', type: 'number' },
      ],
      events: D.TRACE_SPANS.filter((s) => inStreams(sel, s.stream)).map((s) => ({
        start: s.start,
        service: s.service,
        name: s.name,
        duration_ms: s.durationMs,
        trace_id: s.traceId,
      })),
      stats: { elapsedMs: 31, rowsRead: 14, bytesRead: 2048, interpretedAsText: false },
    };
  }
  const all = selectEvents(b.q, from, to, b.signals ?? [], sel);
  const offset = b.cursor === undefined ? 0 : Number(atob(b.cursor));
  const limit = b.limit ?? 200;
  const page = all.slice(offset, offset + limit);
  return {
    kind: 'events',
    events: page,
    ...(offset + limit < all.length ? { nextCursor: btoa(String(offset + limit)) } : {}),
    stats: {
      elapsedMs: 84,
      rowsRead: all.length * SCALE,
      bytesRead: all.length * SCALE * 410,
      interpretedAsText: /^[a-z]+$/i.test(b.q.trim()) && !/^(and|or)$/.test(b.q),
    },
  };
}

/** What an empty scope returns: a well-formed, empty result of the query's kind (SPEC §15.10). */
function emptyResult(kind: QueryResponse['kind']): QueryResponse {
  const stats = { elapsedMs: 2, rowsRead: 0, bytesRead: 0, interpretedAsText: false };
  if (kind === 'timeseries') return { kind, span: 60_000, columns: [], series: [], stats };
  if (kind === 'table') return { kind, columns: [], rows: [], stats };
  return { kind, events: [], stats };
}

const MISSPELLINGS: Record<string, string> = {
  warnign: 'warning',
  eror: 'error',
  servce: 'service',
  infromation: 'information',
};

function validate(q: string): {
  ok: boolean;
  kind?: string;
  interpretedAsText: boolean;
  error?: { message: string; pos: number; len: number; hint?: string };
} {
  const enc = new TextEncoder();
  for (const [bad, good] of Object.entries(MISSPELLINGS)) {
    const i = q.indexOf(bad);
    if (i >= 0) {
      return {
        ok: false,
        interpretedAsText: false,
        error: {
          message: `unknown level '${bad}'`,
          pos: enc.encode(q.slice(0, i)).length,
          len: enc.encode(bad).length,
          hint: `did you mean '${good}'?`,
        },
      };
    }
  }
  const open = (q.match(/\(/g) ?? []).length;
  const close = (q.match(/\)/g) ?? []).length;
  if (open !== close) {
    return {
      ok: false,
      interpretedAsText: false,
      error: { message: 'unbalanced parentheses', pos: enc.encode(q).length, len: 1, hint: "add ')'" },
    };
  }
  return { ok: true, kind: kindOf(q), interpretedAsText: false };
}

const STAGE_SUGGESTIONS = [
  ['| stats count() by service', 'aggregate → table'],
  ['| stats count() by level span 5m', 'aggregate → time series'],
  ['| top 10 exception_type', 'most frequent values'],
] as const;

function complete(q: string, pos: number) {
  const before = q.slice(0, pos);
  const word = /[\w.@$-]*$/.exec(before)?.[0] ?? '';
  const from = pos - word.length;
  const lw = word.toLowerCase();
  const field = /([\w.@]+)\s*(?::|>=|<=|>|<|=)\s*[\w.*-]*$/.exec(before)?.[1];
  const out: { kind: string; label: string; insertText: string; detail?: string }[] = [];
  if (field !== undefined && /[:=<>]\s*[\w.*-]*$/.test(before)) {
    const vals =
      field === 'level'
        ? ['verbose', 'debug', 'information', 'warning', 'error', 'fatal']
        : field === 'service'
          ? D.SERVICES
          : [];
    for (const v of vals)
      if (v.startsWith(lw))
        out.push({
          kind: 'value',
          label: v,
          insertText: v,
          detail: field === 'service' ? `${(4812 / (1 + vals.indexOf(v))).toFixed(0)}` : '',
        });
    return { from, to: pos, field, suggestions: out };
  }
  if (/(^|\s)$/.test(before) || word !== '') {
    for (const [label, detail] of STAGE_SUGGESTIONS)
      if (word === '' && before.trim() !== '') out.push({ kind: 'stage', label, insertText: label, detail });
    for (const k of ['and', 'or', 'not'])
      if (k.startsWith(lw) && before.trim() !== '')
        out.push({
          kind: 'keyword',
          label: k,
          insertText: `${k} `,
          detail: k === 'and' ? 'both conditions' : k === 'or' ? 'either condition' : 'negation',
        });
    for (const f of D.FIELDS) {
      if (!f.name.toLowerCase().startsWith(lw)) continue;
      out.push({
        kind: 'field',
        label: `${f.name}:`,
        insertText: `${f.name}:`,
        detail: f.builtin
          ? 'built-in'
          : `${f.numeric ? 'number' : 'string'} · ${(f.hits / 1000).toFixed(1)}k hits`,
      });
    }
  }
  return { from, to: pos, suggestions: out.slice(0, 50) };
}

function histogram(url: URL, sel: Selection) {
  const [from, to] = range(url.searchParams.get('from'), url.searchParams.get('to'));
  const n = Math.min(1000, Number(url.searchParams.get('buckets') ?? 80));
  const q = url.searchParams.get('q') ?? '';
  const signals = (url.searchParams.get('signals') ?? '').split(',').filter((s) => s !== '');
  const steps = [1e3, 5e3, 1e4, 3e4, 6e4, 3e5, 6e5, 18e5, HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, 24 * HOUR];
  const span = steps.find((s) => (to - from) / s <= n) ?? 24 * HOUR;
  const start = Math.floor(from / span) * span;
  const count = Math.ceil((to - start) / span);
  const evs = selectEvents(q, from, to, signals, sel);
  const buckets = Array.from({ length: count }, (_, i) => ({
    t: new Date(start + i * span).toISOString(),
    counts: { Verbose: 0, Debug: 0, Information: 0, Warning: 0, Error: 0, Fatal: 0 } as Record<
      string,
      number
    >,
  }));
  for (const ev of evs) {
    const i = Math.floor((Date.parse(ev.timestamp) - start) / span);
    const b = buckets[i];
    if (b !== undefined) b.counts[ev.level] = (b.counts[ev.level] ?? 0) + SCALE;
  }
  return { span, buckets };
}

function facets(url: URL, sel: Selection): Facet[] {
  const [from, to] = range(url.searchParams.get('from'), url.searchParams.get('to'));
  const q = url.searchParams.get('q') ?? '';
  const signals = (url.searchParams.get('signals') ?? '').split(',').filter((s) => s !== '');
  const fields = (url.searchParams.get('fields') ?? 'service,level').split(',');
  const top = Number(url.searchParams.get('top') ?? 10);
  const evs = selectEvents(q, from, to, signals, sel);
  return fields.map((field) => {
    const counts = new Map<string, number>();
    let total = 0;
    for (const ev of evs) {
      const v = fieldValue(ev, field);
      if (v === undefined || v === '') continue;
      const key = field === 'level' ? String(v).toLowerCase() : String(v);
      counts.set(key, (counts.get(key) ?? 0) + SCALE);
      total += SCALE;
    }
    const values = [...counts.entries()]
      .sort((a, b) => b[1] - a[1])
      .slice(0, top)
      .map(([value, count]) => ({ value, count }));
    return { field, values, otherCount: total - values.reduce((a, v) => a + v.count, 0), sampled: false };
  });
}

// ---- mutable state ---------------------------------------------------------------------------
const state = {
  signals: [...D.SIGNALS],
  dashboards: [...D.DASHBOARDS],
  alerts: [...D.ALERTS],
  channels: [...D.CHANNELS],
  connections: [...D.CONNECTIONS],
  users: [...D.USERS],
  tokens: [...D.TOKENS],
  streams: [...D.STREAMS],
  indexed: [...D.INDEXED],
  teams: D.TEAMS.map((t) => ({ ...t, memberIds: [...t.memberIds], streams: [...t.streams] })),
  denyByDefault: false,
};
let seq = 100;
const nextId = (p: string): string => `${p}-${seq++}`;
const stamp = (): string => new Date().toISOString();

function randomKey(prefix: string): string {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789';
  const bytes = new Uint8Array(40);
  crypto.getRandomValues(bytes);
  return prefix + Array.from(bytes, (b) => alphabet[b % alphabet.length]).join('');
}

// ---- teams and stream scope (SPEC §15.10) --------------------------------------------------------
const teamRefs = (userId: string) =>
  state.teams.filter((t) => t.memberIds.includes(userId)).map((t) => ({ id: t.id, name: t.name }));

const withTeams = (u: Omit<User, 'teams'>): User => ({ ...u, teams: teamRefs(u.id) });

/** The streams a user may see: null = every stream (SPEC §15.10 resolution order). */
function access(u: Omit<User, 'teams'>): { streams: string[] | null; mode: string } {
  if (u.role === 'admin') return { streams: null, mode: 'admin' };
  const teams = state.teams.filter((t) => t.memberIds.includes(u.id));
  if (teams.length > 0)
    return { streams: [...new Set(teams.flatMap((t) => t.streams))].sort(), mode: 'teams' };
  return state.denyByDefault ? { streams: [], mode: 'default-none' } : { streams: null, mode: 'default-all' };
}

const listOf = (url: URL, name: string): string[] =>
  url.searchParams
    .getAll(name)
    .flatMap((v) => v.split(','))
    .filter((v) => v !== '');

/** The caller's scope narrowed by a selection (a selection outside the scope yields nothing). */
function selection(streams: string[], env: string[]): Selection {
  const u = sessionUser();
  const visible = u === null ? [] : access(u).streams;
  if (streams.length === 0) return { streams: visible, env };
  return { streams: visible === null ? streams : streams.filter((s) => visible.includes(s)), env };
}

const selectionOf = (url: URL): Selection => selection(listOf(url, 'streams'), listOf(url, 'env'));

function me() {
  const u = sessionUser() ?? (state.users[0] as Omit<User, 'teams'>);
  const isAdmin = u.role === 'admin';
  const acc = access(u);
  return {
    id: u.id,
    username: u.username,
    displayName: u.displayName,
    role: u.role,
    mustChangePassword: isAdmin ? defaultCredentials() : u.mustChangePassword,
    defaultCredentials: isAdmin && defaultCredentials(),
    allStreams: acc.streams === null,
    streams: acc.streams ?? [],
    teams: teamRefs(u.id),
    streamAccess: acc.mode,
  };
}

const teamSummary = (t: D.MockTeam): TeamSummary => ({
  id: t.id,
  name: t.name,
  description: t.description,
  memberCount: t.memberIds.length,
  streams: [...t.streams].sort(),
  createdAt: t.createdAt,
  updatedAt: t.updatedAt,
});

const teamDetail = (t: D.MockTeam): Team => ({
  id: t.id,
  name: t.name,
  description: t.description,
  members: t.memberIds
    .map((id) => state.users.find((u) => u.id === id))
    .filter((u): u is Omit<User, 'teams'> => u !== undefined)
    .map((u) => ({ id: u.id, username: u.username, displayName: u.displayName, role: u.role })),
  streams: [...t.streams].sort(),
  createdAt: t.createdAt,
  updatedAt: t.updatedAt,
});

const byName = (a: { name: string }, b: { name: string }): number => a.name.localeCompare(b.name);

function teamNameTaken(name: string, exceptId: string): boolean {
  return state.teams.some((t) => t.id !== exceptId && t.name.toLowerCase() === name.toLowerCase());
}

function updateTeam(id: string, patch: Partial<D.MockTeam>): D.MockTeam | undefined {
  state.teams = state.teams.map((t) => (t.id === id ? { ...t, ...patch, updatedAt: stamp() } : t));
  return state.teams.find((t) => t.id === id);
}

/** Alert rules visible to the caller: every stream needs `allStreams`, else a subset (SPEC §14.1). */
function visibleAlerts(): Alert[] {
  const u = sessionUser();
  const visible = u === null ? [] : access(u).streams;
  if (visible === null) return state.alerts;
  return state.alerts.filter((a) => a.streams !== null && a.streams.every((s) => visible.includes(s)));
}

function snippets(c: Connection) {
  const base = window.location.origin;
  const key = '<YOUR_TIDEMARK_KEY>';
  return [
    {
      id: 'serilog',
      title: 'Serilog (Serilog.Sinks.Seq)',
      language: 'csharp',
      content: `Log.Logger = new LoggerConfiguration()\n    .WriteTo.Seq("${base}", apiKey: "${key}")\n    .CreateLogger();`,
    },
    {
      id: 'nlog',
      title: 'NLog (NLog.Targets.Seq)',
      language: 'xml',
      content: `<target xsi:type="Seq" name="tidemark" serverUrl="${base}" apiKey="${key}" />`,
    },
    {
      id: 'otel',
      title: 'OpenTelemetry SDK (environment variables)',
      language: 'shell',
      content: `OTEL_EXPORTER_OTLP_ENDPOINT=${base}/otlp\nOTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf\nOTEL_EXPORTER_OTLP_HEADERS=x-tidemark-key=${key}`,
    },
    {
      id: 'filebeat',
      title: 'Filebeat',
      language: 'yaml',
      content: `output.elasticsearch:\n  hosts: ["${base}"]\n  path: "/es"\n  api_key: "tidemark:${key}"\n  index: "${c.defaultStream}"`,
    },
    {
      id: 'fluentbit',
      title: 'Fluent Bit',
      language: 'ini',
      content: `[OUTPUT]\n    Name   es\n    Match  *\n    Host   ${window.location.hostname}\n    Path   /es\n    HTTP_User   tidemark\n    HTTP_Passwd ${key}\n    Index  ${c.defaultStream}`,
    },
    {
      id: 'vector',
      title: 'Vector',
      language: 'toml',
      content: `[sinks.tidemark]\ntype = "elasticsearch"\ninputs = ["*"]\nendpoints = ["${base}/es"]\nbulk.index = "${c.defaultStream}"\nauth.strategy = "basic"\nauth.user = "tidemark"\nauth.password = "${key}"`,
    },
    {
      id: 'logstash',
      title: 'Logstash',
      language: 'ruby',
      content: `output {\n  http {\n    url => "${base}/api/v1/ingest/logs"\n    http_method => "post"\n    format => "json_batch"\n    headers => { "X-Tidemark-Key" => "${key}" }\n  }\n}`,
    },
  ];
}

type Handler = (args: {
  request: Request;
  params: Record<string, string | readonly string[] | undefined>;
  url: URL;
}) => Promise<Response> | Response;

function authed(fn: Handler) {
  return async ({
    request,
    params,
  }: {
    request: Request;
    params: Record<string, string | readonly string[] | undefined>;
  }) => {
    const g = guard(request);
    if (g !== null) return g;
    return fn({ request, params, url: new URL(request.url) });
  };
}

const str = (v: string | readonly string[] | undefined): string => (typeof v === 'string' ? v : '');

function crud<T extends { id: string; version?: number }>(
  name: string,
  list: () => T[],
  set: (xs: T[]) => void,
  create: (b: Record<string, unknown>) => T,
) {
  return [
    http.get(
      `${P}/${name}`,
      authed(() => json(list() as unknown as JsonBodyType)),
    ),
    http.post(
      `${P}/${name}`,
      authed(async ({ request }) => {
        const item = create(await body<Record<string, unknown>>(request));
        set([...list(), item]);
        return json(item as unknown as JsonBodyType, 201);
      }),
    ),
    http.get(
      `${P}/${name}/:id`,
      authed(({ params }) => {
        const item = list().find((x) => x.id === str(params.id));
        return item === undefined
          ? err(404, 'not_found', `${name} not found`)
          : json(item as unknown as JsonBodyType);
      }),
    ),
    http.put(
      `${P}/${name}/:id`,
      authed(async ({ request, params }) => {
        const b = await body<Record<string, unknown>>(request);
        const cur = list().find((x) => x.id === str(params.id));
        if (cur === undefined) return err(404, 'not_found', `${name} not found`);
        if (cur.version !== undefined && b.version !== cur.version)
          return err(409, 'conflict', 'This item was changed by someone else. Reload and try again.');
        const next = { ...cur, ...b, version: (cur.version ?? 0) + 1, updatedAt: stamp() } as T;
        set(list().map((x) => (x.id === cur.id ? next : x)));
        return json(next as unknown as JsonBodyType);
      }),
    ),
    http.delete(
      `${P}/${name}/:id`,
      authed(({ params }) => {
        set(list().filter((x) => x.id !== str(params.id)));
        return new HttpResponse(null, { status: 204 });
      }),
    ),
  ];
}

export const handlers = [
  // ---- §11.2 auth ----
  http.post(`${P}/auth/login`, async ({ request }) => {
    const b = await body<{ username: string; password: string }>(request);
    const u = state.users.find((x) => x.username === b.username && !x.disabled);
    const seeded = b.username === 'admin' && b.password === 'admin';
    if (u === undefined || (!seeded && b.password.length < MIN_PASSWORD)) {
      return err(401, 'unauthorized', 'Incorrect username or password.', { remainingAttempts: 4 });
    }
    storage()?.setItem(SESSION_KEY, u.username);
    if (u.role === 'admin' && !seeded) storage()?.setItem(DEFAULT_CREDS_KEY, '0');
    document.cookie = `tm_csrf=${CSRF}; path=/; SameSite=Strict`;
    const m = me();
    return json({
      user: withTeams(u) as unknown as JsonBodyType,
      mustChangePassword: m.mustChangePassword,
    });
  }),
  http.post(`${P}/auth/logout`, () => {
    storage()?.removeItem(SESSION_KEY);
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`${P}/auth/me`, () => (signedIn() ? json(me()) : err(401, 'unauthorized', 'Sign in required.'))),
  http.post(
    `${P}/auth/password`,
    authed(async ({ request }) => {
      const b = await body<{ currentPassword: string; newPassword: string }>(request);
      if (b.newPassword.length < 12)
        return err(400, 'bad_request', 'The new password must have at least 12 characters.');
      storage()?.setItem(DEFAULT_CREDS_KEY, '0');
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  http.get(
    `${P}/users`,
    authed(() => json(state.users.map(withTeams) as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/users`,
    authed(async ({ request }) => {
      const b = await body<{ username: string; displayName: string; role: User['role'] }>(request);
      const u: Omit<User, 'teams'> = {
        id: nextId('u'),
        username: b.username,
        displayName: b.displayName,
        role: b.role,
        mustChangePassword: true,
        disabled: false,
        lastLoginAt: null,
        createdAt: stamp(),
        updatedAt: stamp(),
      };
      state.users = [...state.users, u];
      return json(withTeams(u) as unknown as JsonBodyType, 201);
    }),
  ),
  http.patch(
    `${P}/users/:id`,
    authed(async ({ request, params }) => {
      const b = await body<Partial<User>>(request);
      state.users = state.users.map((u) =>
        u.id === str(params.id) ? { ...u, ...b, updatedAt: stamp() } : u,
      );
      const u = state.users.find((x) => x.id === str(params.id));
      return u === undefined
        ? err(404, 'not_found', 'user not found')
        : json(withTeams(u) as unknown as JsonBodyType);
    }),
  ),
  http.put(
    `${P}/users/:id/teams`,
    authed(async ({ request, params }) => {
      const { teamIds } = await body<{ teamIds: string[] }>(request);
      const u = state.users.find((x) => x.id === str(params.id));
      if (u === undefined) return err(404, 'not_found', 'user not found');
      if (teamIds.some((id) => !state.teams.some((t) => t.id === id)))
        return err(400, 'bad_request', 'unknown team', { field: 'teamIds' });
      state.teams = state.teams.map((t) => {
        const member = teamIds.includes(t.id);
        if (member === t.memberIds.includes(u.id)) return t;
        return {
          ...t,
          memberIds: member ? [...t.memberIds, u.id] : t.memberIds.filter((id) => id !== u.id),
          updatedAt: stamp(),
        };
      });
      return json(withTeams(u) as unknown as JsonBodyType);
    }),
  ),
  http.delete(
    `${P}/users/:id`,
    authed(({ params }) => {
      if (str(params.id) === 'u-admin') return err(409, 'conflict', 'The last admin cannot be deleted.');
      state.users = state.users.filter((u) => u.id !== str(params.id));
      state.teams = state.teams.map((t) => ({
        ...t,
        memberIds: t.memberIds.filter((id) => id !== str(params.id)),
      }));
      return new HttpResponse(null, { status: 204 });
    }),
  ),

  // ---- §11.5 teams (§15.10) ----
  http.get(
    `${P}/teams/access`,
    authed(() =>
      json({
        denyByDefault: state.denyByDefault,
        usersWithoutTeam: state.users.filter(
          (u) => u.role !== 'admin' && !state.teams.some((t) => t.memberIds.includes(u.id)),
        ).length,
      }),
    ),
  ),
  http.get(
    `${P}/teams`,
    authed(() => json([...state.teams].sort(byName).map(teamSummary) as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/teams`,
    authed(async ({ request }) => {
      const b = await body<{ name: string; description?: string }>(request);
      const name = b.name.trim();
      if (name === '' || name.length > MAX_TEAM_NAME)
        return err(400, 'bad_request', 'invalid name', { field: 'name' });
      if (teamNameTaken(name, '')) return err(409, 'conflict', `A team named ${name} already exists.`);
      const t: D.MockTeam = {
        id: nextId('tm'),
        name,
        description: (b.description ?? '').trim(),
        memberIds: [],
        streams: [],
        createdAt: stamp(),
        updatedAt: stamp(),
      };
      state.teams = [...state.teams, t];
      return json(teamDetail(t) as unknown as JsonBodyType, 201);
    }),
  ),
  http.get(
    `${P}/teams/:id`,
    authed(({ params }) => {
      const t = state.teams.find((x) => x.id === str(params.id));
      return t === undefined
        ? err(404, 'not_found', 'team not found')
        : json(teamDetail(t) as unknown as JsonBodyType);
    }),
  ),
  http.patch(
    `${P}/teams/:id`,
    authed(async ({ request, params }) => {
      const b = await body<{ name?: string; description?: string }>(request);
      const cur = state.teams.find((x) => x.id === str(params.id));
      if (cur === undefined) return err(404, 'not_found', 'team not found');
      const name = b.name === undefined ? cur.name : b.name.trim();
      if (name === '' || name.length > MAX_TEAM_NAME)
        return err(400, 'bad_request', 'invalid name', { field: 'name' });
      if (teamNameTaken(name, cur.id)) return err(409, 'conflict', `A team named ${name} already exists.`);
      const t = updateTeam(cur.id, {
        name,
        description: b.description === undefined ? cur.description : b.description.trim(),
      });
      return json(teamDetail(t as D.MockTeam) as unknown as JsonBodyType);
    }),
  ),
  http.delete(
    `${P}/teams/:id`,
    authed(({ params }) => {
      state.teams = state.teams.filter((t) => t.id !== str(params.id));
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  http.put(
    `${P}/teams/:id/members`,
    authed(async ({ request, params }) => {
      const { userIds } = await body<{ userIds: string[] }>(request);
      if (!state.teams.some((t) => t.id === str(params.id))) return err(404, 'not_found', 'team not found');
      if (userIds.some((id) => !state.users.some((u) => u.id === id)))
        return err(400, 'bad_request', 'unknown user', { field: 'userIds' });
      const t = updateTeam(str(params.id), { memberIds: [...new Set(userIds)] });
      return json(teamDetail(t as D.MockTeam) as unknown as JsonBodyType);
    }),
  ),
  http.put(
    `${P}/teams/:id/streams`,
    authed(async ({ request, params }) => {
      const { streams } = await body<{ streams: string[] }>(request);
      if (!state.teams.some((t) => t.id === str(params.id))) return err(404, 'not_found', 'team not found');
      if (streams.some((s) => !isStreamName(s)))
        return err(400, 'bad_request', 'invalid stream name', { field: 'streams' });
      const t = updateTeam(str(params.id), { streams: [...new Set(streams)] });
      return json(teamDetail(t as D.MockTeam) as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/tokens`,
    authed(() => json(state.tokens as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/tokens`,
    authed(async ({ request }) => {
      const b = await body<{ name: string; scope: ApiToken['scope']; expiresAt: string | null }>(request);
      const token = randomKey('tmt_');
      const t: ApiToken = {
        id: nextId('t'),
        name: b.name,
        keyPrefix: token.slice(0, 8),
        scope: b.scope,
        expiresAt: b.expiresAt,
        revokedAt: null,
        lastUsedAt: null,
        createdAt: stamp(),
      };
      state.tokens = [...state.tokens, t];
      return json({ ...t, token } as unknown as JsonBodyType, 201);
    }),
  ),
  http.delete(
    `${P}/tokens/:id`,
    authed(({ params }) => {
      state.tokens = state.tokens.filter((t) => t.id !== str(params.id));
      return new HttpResponse(null, { status: 204 });
    }),
  ),

  // ---- §11.3 search ----
  http.post(
    `${P}/query`,
    authed(async ({ request }) => {
      const b = await body<QueryBody>(request);
      const v = validate(b.q);
      if (!v.ok && v.error !== undefined)
        return err(400, 'invalid_query', v.error.message, {
          pos: v.error.pos,
          len: v.error.len,
          hint: v.error.hint,
        });
      if ((b.streams ?? []).some((s) => !isStreamName(s)))
        return err(400, 'bad_request', 'invalid stream name', { field: 'streams' });
      return json(runQuery(b, selection(b.streams ?? [], b.environments ?? [])) as unknown as JsonBodyType);
    }),
  ),
  http.post(
    `${P}/query/validate`,
    authed(async ({ request }) => json(validate((await body<{ q: string }>(request)).q))),
  ),
  http.get(
    `${P}/histogram`,
    authed(({ url }) => json(histogram(url, selectionOf(url)))),
  ),
  http.get(
    `${P}/facets`,
    authed(({ url }) => json(facets(url, selectionOf(url)) as unknown as JsonBodyType)),
  ),
  http.get(
    `${P}/fields`,
    authed(({ url }) => {
      const sel = selectionOf(url);
      return json(
        (sel.streams !== null && sel.streams.length === 0 ? [] : D.FIELDS) as unknown as JsonBodyType,
      );
    }),
  ),
  http.get(
    `${P}/events/:id`,
    authed(({ params }) => {
      const ev = [...D.EVENTS, ...D.TRACE_EVENTS].find((e) => e.id === str(params.id));
      return ev === undefined || !inStreams(selection([], []), ev.stream)
        ? err(404, 'not_found', 'event not found')
        : json(ev as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/events/:id/context`,
    authed(({ params, url }) => {
      const i = D.EVENTS.findIndex((e) => e.id === str(params.id));
      if (i < 0) return err(404, 'not_found', 'event not found');
      const ev = D.EVENTS[i] as LogEvent;
      const n = Number(url.searchParams.get('before') ?? 20);
      const same = D.EVENTS.filter((e) => e.service === ev.service);
      const j = same.indexOf(ev);
      return json({
        before: same.slice(j + 1, j + 1 + n).reverse(),
        event: ev,
        after: same.slice(Math.max(0, j - n), j).reverse(),
      } as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/traces`,
    authed(({ url }) => {
      const sel = selectionOf(url);
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
      const svc = /service:([\w-]+)/.exec(q)?.[1];
      const errs = /status:error|error/.test(q);
      // Summaries cover visible spans only: services outside the scope disappear and the root
      // becomes the first visible service (SPEC §15.10).
      return json(
        D.TRACE_SUMMARIES.map((t) => {
          const services = t.services.filter((x) => inStreams(sel, D.spanStreamOf(x)));
          return {
            ...t,
            services,
            rootService: services.includes(t.rootService) ? t.rootService : (services[0] ?? ''),
          };
        }).filter(
          (t) =>
            t.services.length > 0 &&
            (svc === undefined || t.services.includes(svc)) &&
            (!errs || t.errorCount > 0),
        ) as unknown as JsonBodyType,
      );
    }),
  ),
  http.get(
    `${P}/traces/:id`,
    authed(({ params }) => {
      const sel = selection([], []);
      const id = str(params.id).toLowerCase();
      const visibleSpans = D.TRACE_SPANS.filter((s) => inStreams(sel, s.stream));
      if (id === D.TRACE_ID) {
        if (visibleSpans.length === 0) return err(404, 'not_found', 'trace not found');
        const events = D.TRACE_EVENTS.filter((e) => inStreams(sel, e.stream));
        return json({ spans: visibleSpans, events } as unknown as JsonBodyType);
      }
      const sum = D.TRACE_SUMMARIES.find((t) => t.traceId === id);
      if (sum === undefined) return err(404, 'not_found', 'trace not found');
      const spans = visibleSpans.slice(0, Math.min(sum.spanCount, 6)).map((s) => ({ ...s, traceId: id }));
      if (spans.length === 0) return err(404, 'not_found', 'trace not found');
      return json({ spans, events: [] } as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/metrics/names`,
    authed(({ url }) => {
      const sel = selectionOf(url);
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
      if (!inStreams(sel, 'otel-metrics')) return json([]);
      return json(D.METRIC_NAMES.filter((m) => m.name.includes(q)) as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/tql/complete`,
    authed(({ url }) => {
      const q = url.searchParams.get('q') ?? '';
      const bytePos = Number(url.searchParams.get('pos') ?? new TextEncoder().encode(q).length);
      const pos = new TextDecoder().decode(new TextEncoder().encode(q).slice(0, bytePos)).length;
      const c = complete(q, pos);
      const enc = new TextEncoder();
      return json({
        ...c,
        from: enc.encode(q.slice(0, c.from)).length,
        to: enc.encode(q.slice(0, c.to)).length,
      });
    }),
  ),
  http.get(
    `${P}/export`,
    authed(({ url }) => {
      const [from, to] = range(url.searchParams.get('from'), url.searchParams.get('to'));
      const evs = selectEvents(url.searchParams.get('q') ?? '', from, to, [], selectionOf(url));
      return new HttpResponse(evs.map((e) => JSON.stringify(e)).join('\n'), {
        headers: {
          'Content-Type': 'application/x-ndjson',
          'Content-Disposition': 'attachment; filename=events.ndjson',
        },
      });
    }),
  ),

  // ---- §11.4 ----
  ...crud<Signal>(
    'signals',
    () => state.signals,
    (xs) => (state.signals = xs),
    (b) => ({
      ...(b as unknown as Signal),
      id: nextId('sig'),
      ownerId: 'u-admin',
      createdAt: stamp(),
      updatedAt: stamp(),
    }),
  ),
  http.get(
    `${P}/dashboards/templates`,
    authed(() => json(D.TEMPLATES as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/dashboards/import`,
    authed(async ({ request }) => {
      const b = await body<Dashboard>(request);
      const d: Dashboard = {
        ...b,
        id: nextId('d'),
        ownerId: 'u-admin',
        shared: false,
        version: 1,
        description: b.description ?? '',
        createdAt: stamp(),
        updatedAt: stamp(),
      };
      state.dashboards = [...state.dashboards, d];
      return json(d as unknown as JsonBodyType, 201);
    }),
  ),
  http.get(
    `${P}/dashboards`,
    authed(() => json(state.dashboards.map(({ doc: _doc, ...rest }) => rest) as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/dashboards/:id/duplicate`,
    authed(({ params }) => {
      const src = state.dashboards.find((d) => d.id === str(params.id));
      if (src === undefined) return err(404, 'not_found', 'dashboard not found');
      const d = { ...src, id: nextId('d'), name: `${src.name} (copy)`, shared: false, version: 1 };
      state.dashboards = [...state.dashboards, d];
      return json(d as unknown as JsonBodyType, 201);
    }),
  ),
  http.get(
    `${P}/dashboards/:id/export`,
    authed(({ params }) => {
      const d = state.dashboards.find((x) => x.id === str(params.id));
      return d === undefined
        ? err(404, 'not_found', 'dashboard not found')
        : json({ name: d.name, description: d.description, doc: d.doc } as unknown as JsonBodyType);
    }),
  ),
  http.post(
    `${P}/dashboards/:id/panels/:panelId/query`,
    authed(async ({ request, params }) => {
      const d = state.dashboards.find((x) => x.id === str(params.id));
      const p = d?.doc?.panels.find((x) => x.id === str(params.panelId));
      if (p?.query === undefined) return err(404, 'not_found', 'panel not found');
      const vars = (await body<{ vars?: Record<string, string[]> }>(request)).vars ?? {};
      const q = p.query.replace(/\$(\w+)/g, (_, n: string) => {
        const v = vars[n] ?? d?.doc?.variables?.find((x) => x.name === n)?.default ?? ['*'];
        return v.length === 1 ? (v[0] ?? '*') : `(${v.join(' or ')})`;
      });
      return json({ q });
    }),
  ),
  ...crud<Dashboard>(
    'dashboards',
    () => state.dashboards,
    (xs) => (state.dashboards = xs),
    (b) => ({
      ...(b as unknown as Dashboard),
      id: nextId('d'),
      ownerId: 'u-admin',
      version: 1,
      description: String(b.description ?? ''),
      shared: b.shared === true,
      createdAt: stamp(),
      updatedAt: stamp(),
    }),
  ),
  http.post(
    `${P}/alerts/:id/test`,
    authed(({ params }) => {
      const a = state.alerts.find((x) => x.id === str(params.id));
      if (a === undefined) return err(404, 'not_found', 'alert not found');
      return json({
        state: a.state,
        samples: [{ labels: {}, value: a.lastValue }],
        groups: a.groups,
        transitions: [],
      } as unknown as JsonBodyType);
    }),
  ),
  http.post(
    `${P}/alerts/:id/mute`,
    authed(async ({ request, params }) => {
      const { until } = await body<{ until: string | null }>(request);
      state.alerts = state.alerts.map((a) =>
        a.id === str(params.id) ? { ...a, doc: { ...a.doc, muteUntil: until }, version: a.version + 1 } : a,
      );
      return json(state.alerts.find((a) => a.id === str(params.id)) as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/alerts/:id/history`,
    authed(({ params }) =>
      json(
        (str(params.id) === 'all'
          ? D.HISTORY
          : D.HISTORY.filter((h) =>
              h.message.startsWith(state.alerts.find((a) => a.id === str(params.id))?.name ?? '\u0000'),
            )) as unknown as JsonBodyType,
      ),
    ),
  ),
  ...crud<Alert>(
    'alerts',
    visibleAlerts,
    (xs) => (state.alerts = xs),
    (b) => ({
      ...(b as unknown as Alert),
      streams: Array.isArray(b.streams) ? (b.streams as string[]) : null,
      id: nextId('a'),
      state: 'ok',
      stateSince: null,
      lastEvalAt: null,
      lastValue: null,
      lastError: '',
      groups: [],
      version: 1,
      createdAt: stamp(),
      updatedAt: stamp(),
    }),
  ),
  http.post(
    `${P}/channels/:id/test`,
    authed(() => json({ ok: true })),
  ),
  ...crud<Channel>(
    'channels',
    () => state.channels,
    (xs) => (state.channels = xs),
    (b) => ({
      ...(b as unknown as Channel),
      id: nextId('ch'),
      version: 1,
      createdAt: stamp(),
      updatedAt: stamp(),
    }),
  ),

  // ---- §11.5 ----
  http.get(
    `${P}/connections`,
    authed(() => json(state.connections as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/connections`,
    authed(async ({ request }) => {
      const b = await body<Partial<Connection>>(request);
      const key = randomKey('tmk_');
      const c: Connection = {
        ...(D.CONNECTIONS[0] as Connection),
        ...b,
        id: nextId('c'),
        keyPrefix: key.slice(0, 8),
        lastUsedAt: null,
        lastUsedIp: '',
        eventsReceived: 0,
        revokedAt: null,
        rotationGraceUntil: null,
        createdAt: stamp(),
        updatedAt: stamp(),
      } as Connection;
      state.connections = [...state.connections, c];
      return json({ ...c, key } as unknown as JsonBodyType, 201);
    }),
  ),
  http.patch(
    `${P}/connections/:id`,
    authed(async ({ request, params }) => {
      const b = await body<Partial<Connection>>(request);
      state.connections = state.connections.map((c) =>
        c.id === str(params.id) ? { ...c, ...b, updatedAt: stamp() } : c,
      );
      return json(state.connections.find((c) => c.id === str(params.id)) as unknown as JsonBodyType);
    }),
  ),
  http.delete(
    `${P}/connections/:id`,
    authed(({ params }) => {
      state.connections = state.connections.filter((c) => c.id !== str(params.id));
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  http.post(
    `${P}/connections/:id/rotate`,
    authed(async ({ request, params }) => {
      const { graceMinutes } = await body<{ graceMinutes: number }>(request);
      const key = randomKey('tmk_');
      state.connections = state.connections.map((c) =>
        c.id === str(params.id)
          ? {
              ...c,
              keyPrefix: key.slice(0, 8),
              rotationGraceUntil:
                graceMinutes > 0 ? new Date(Date.now() + graceMinutes * 60_000).toISOString() : null,
            }
          : c,
      );
      return json({
        ...state.connections.find((c) => c.id === str(params.id)),
        key,
      } as unknown as JsonBodyType);
    }),
  ),
  http.post(
    `${P}/connections/:id/revoke`,
    authed(({ params }) => {
      state.connections = state.connections.map((c) =>
        c.id === str(params.id) ? { ...c, revokedAt: stamp() } : c,
      );
      return json(state.connections.find((c) => c.id === str(params.id)) as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/connections/:id/snippets`,
    authed(({ params }) => {
      const c = state.connections.find((x) => x.id === str(params.id));
      return c === undefined ? err(404, 'not_found', 'connection not found') : json(snippets(c));
    }),
  ),
  http.get(
    `${P}/streams`,
    authed(() =>
      json({
        streams: state.streams,
        statsUpdatedAt: new Date(Date.now() - 2 * 60_000).toISOString(),
      } as unknown as JsonBodyType),
    ),
  ),
  http.post(
    `${P}/streams`,
    authed(async ({ request }) => {
      const b = await body<Partial<Stream> & { name: string }>(request);
      const s: Stream = {
        id: nextId('s'),
        description: '',
        retentionDays: null,
        spansRetentionDays: null,
        metricsRetentionDays: null,
        levelRules: [],
        paused: false,
        createdAt: stamp(),
        updatedAt: stamp(),
        ...b,
      };
      state.streams = [...state.streams, s];
      return json(s as unknown as JsonBodyType, 201);
    }),
  ),
  http.patch(
    `${P}/streams/:name`,
    authed(async ({ request, params }) => {
      const b = await body<Partial<Stream>>(request);
      state.streams = state.streams.map((s) =>
        s.name === str(params.name) ? { ...s, ...b, updatedAt: stamp() } : s,
      );
      const { stats: _stats, ...rest } = state.streams.find((s) => s.name === str(params.name)) as Stream;
      return json(rest as unknown as JsonBodyType);
    }),
  ),
  http.delete(
    `${P}/streams/:name`,
    authed(async ({ request, params }) => {
      const { confirm } = await body<{ confirm: string }>(request);
      if (confirm !== str(params.name))
        return err(400, 'bad_request', 'confirmation does not match the stream name');
      if (confirm === 'default') return err(409, 'conflict', 'the default stream cannot be deleted');
      state.streams = state.streams.filter((s) => s.name !== confirm);
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  http.get(
    `${P}/streams/:name/fields`,
    authed(() =>
      json(
        D.MAPPINGS.map((m) => ({
          ...m,
          indexed: state.indexed.some((i) => i.key === m.key),
        })) as unknown as JsonBodyType,
      ),
    ),
  ),
  http.get(
    `${P}/indexed-fields`,
    authed(() => json(state.indexed as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/indexed-fields`,
    authed(async ({ request }) => {
      const b = await body<{ source: 'logs'; key: string }>(request);
      const f = {
        id: nextId('if'),
        source: b.source,
        key: b.key,
        column: `f_${b.key.toLowerCase().replace(/[^a-z0-9]+/g, '_')}`,
        createdAt: stamp(),
      };
      state.indexed = [...state.indexed, f];
      return json(f, 201);
    }),
  ),
  http.delete(
    `${P}/indexed-fields/:id`,
    authed(({ params }) => {
      state.indexed = state.indexed.filter((f) => f.id !== str(params.id));
      return new HttpResponse(null, { status: 204 });
    }),
  ),
  http.post(
    `${P}/purge`,
    authed(async ({ request }) => {
      const b = await body<{
        stream?: string;
        source: string;
        q?: string;
        from: string;
        to: string;
        dryRun: boolean;
      }>(request);
      if (b.dryRun) return json({ dryRun: true, rowsEstimate: 184_220_118 });
      return json(
        {
          id: nextId('p'),
          stream: b.stream ?? '',
          source: b.source,
          q: b.q ?? '',
          from: b.from,
          to: b.to,
          status: 'pending',
          rowsEstimate: 184_220_118,
          requestedBy: 'admin',
          droppedPartitions: '',
          createdAt: stamp(),
          updatedAt: stamp(),
        },
        202,
      );
    }),
  ),
  http.get(
    `${P}/purge/:id`,
    authed(({ params }) =>
      json({
        id: str(params.id),
        stream: 'api',
        source: 'logs',
        q: '',
        from: '',
        to: '',
        status: 'done',
        rowsEstimate: 184_220_118,
        requestedBy: 'admin',
        droppedPartitions: '',
        createdAt: stamp(),
        updatedAt: stamp(),
      }),
    ),
  ),
  http.get(
    `${P}/settings`,
    authed(() =>
      json({
        retention: { logsDays: 30, spansDays: 7, metricsDays: 30 },
        access: { denyByDefault: state.denyByDefault },
        ui: {},
      }),
    ),
  ),
  http.patch(
    `${P}/settings`,
    authed(async ({ request }) => {
      const b = await body<{ access?: { denyByDefault?: unknown } }>(request);
      if (b.access !== undefined) {
        if (typeof b.access.denyByDefault !== 'boolean')
          return err(400, 'bad_request', 'access must be {denyByDefault: boolean}', { field: 'access' });
        state.denyByDefault = b.access.denyByDefault;
      }
      return json({
        retention: { logsDays: 30, spansDays: 7, metricsDays: 30 },
        access: { denyByDefault: state.denyByDefault },
        ui: {},
        ...b,
        ...(b.access !== undefined ? { access: { denyByDefault: state.denyByDefault } } : {}),
      } as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/audit`,
    authed(({ url }) => {
      const action = url.searchParams.get('action') ?? '';
      const actor = url.searchParams.get('actor') ?? '';
      return json(
        D.AUDIT.filter(
          (a) => a.action.includes(action) && a.actor.includes(actor),
        ) as unknown as JsonBodyType,
      );
    }),
  ),
  http.get(
    `${P}/system`,
    authed(() => json(D.SYSTEM as unknown as JsonBodyType)),
  ),
  http.get(
    `${P}/imports`,
    authed(() => json(D.IMPORTS as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/imports`,
    authed(async ({ request }) => {
      const multipart = (request.headers.get('Content-Type') ?? '').startsWith('multipart/form-data');
      let job: { kind: ImportJob['kind']; name?: string; config: ImportJob['config'] };
      let files: { name: string; size: number }[] = [];
      if (multipart) {
        const form = await request.formData();
        job = JSON.parse(String(form.get('job'))) as typeof job;
        files = form
          .getAll('file')
          .map((f) => (f instanceof File ? { name: f.name, size: f.size } : { name: '', size: 0 }));
      } else {
        job = await body<typeof job>(request);
      }
      const config: ImportJob['config'] =
        job.kind === 'clef-file'
          ? { ...job.config, files, node: 'node-1' }
          : job.kind === 'kibana'
            ? { file: files[0] ?? { name: '', size: 0 } }
            : job.config;
      const created: ImportJob = {
        id: nextId('imp'),
        kind: job.kind,
        name: job.name ?? `Import ${job.kind}`,
        status: job.kind === 'kibana' ? 'completed' : 'queued',
        config,
        rowsImported: 0,
        rowsTotal: 0,
        errors: 0,
        lastError: '',
        warnings: [],
        cancelRequested: false,
        requestedBy: 'user:u-admin',
        startedAt: null,
        finishedAt: null,
        createdAt: stamp(),
        updatedAt: stamp(),
      };
      return json(created as unknown as JsonBodyType, 201);
    }),
  ),
  http.post(
    `${P}/imports/:id/cancel`,
    authed(({ params }) =>
      json({
        ...(D.IMPORTS.find((i) => i.id === str(params.id)) ?? D.IMPORTS[0]),
        status: 'cancelled',
      } as unknown as JsonBodyType),
    ),
  ),

  // ---- operational ----
  http.get(
    '/readyz',
    () => new HttpResponse('ok', { status: 200, headers: { 'Content-Type': 'text/plain' } }),
  ),
  http.get('/api', () => json({ Product: 'Tidemark', Version: D.SYSTEM.version })),
];
