// SPDX-License-Identifier: Apache-2.0
//
// MSW request handlers for `--mode mock` (e2e tests and screenshots). They implement the
// /api/v1 contract of SPEC §11 closely enough to drive every screen: cookie session with the
// double-submit CSRF check, a tiny TQL subset for filtering, deterministic analytics results and
// in-memory CRUD. Never part of the production bundle.

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
  User,
} from '../api/types';
import * as D from './data';

const P = '/api/v1';
const SESSION_KEY = 'tm.mock.session';
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

function signedIn(): boolean {
  return storage()?.getItem(SESSION_KEY) === '1';
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

function selectEvents(q: string, from: number, to: number, signals: string[]): LogEvent[] {
  const sig = signalFilter(signals);
  return D.EVENTS.filter((ev) => {
    const t = Date.parse(ev.timestamp);
    return t >= from && t <= to && matches(ev, q) && sig(ev);
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
}

function runQuery(b: QueryBody): QueryResponse {
  const [from, to] = range(b.from, b.to);
  const kind = kindOf(b.q);
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
      events: D.TRACE_SPANS.map((s) => ({
        start: s.start,
        service: s.service,
        name: s.name,
        duration_ms: s.durationMs,
        trace_id: s.traceId,
      })),
      stats: { elapsedMs: 31, rowsRead: 14, bytesRead: 2048, interpretedAsText: false },
    };
  }
  const all = selectEvents(b.q, from, to, b.signals ?? []);
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

function histogram(url: URL) {
  const [from, to] = range(url.searchParams.get('from'), url.searchParams.get('to'));
  const n = Math.min(1000, Number(url.searchParams.get('buckets') ?? 80));
  const q = url.searchParams.get('q') ?? '';
  const signals = (url.searchParams.get('signals') ?? '').split(',').filter((s) => s !== '');
  const steps = [1e3, 5e3, 1e4, 3e4, 6e4, 3e5, 6e5, 18e5, HOUR, 3 * HOUR, 6 * HOUR, 12 * HOUR, 24 * HOUR];
  const span = steps.find((s) => (to - from) / s <= n) ?? 24 * HOUR;
  const start = Math.floor(from / span) * span;
  const count = Math.ceil((to - start) / span);
  const evs = selectEvents(q, from, to, signals);
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

function facets(url: URL): Facet[] {
  const [from, to] = range(url.searchParams.get('from'), url.searchParams.get('to'));
  const q = url.searchParams.get('q') ?? '';
  const signals = (url.searchParams.get('signals') ?? '').split(',').filter((s) => s !== '');
  const fields = (url.searchParams.get('fields') ?? 'service,level').split(',');
  const top = Number(url.searchParams.get('top') ?? 10);
  const evs = selectEvents(q, from, to, signals);
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

function me() {
  const admin = state.users[0] as User;
  return {
    id: admin.id,
    username: admin.username,
    displayName: admin.displayName,
    role: admin.role,
    mustChangePassword: defaultCredentials(),
    defaultCredentials: defaultCredentials(),
  };
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
      content: `output {\n  elasticsearch {\n    hosts => ["${base}/es"]\n    user => "tidemark"\n    password => "${key}"\n    index => "${c.defaultStream}"\n  }\n}`,
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
    if (b.username !== 'admin' || (b.password !== 'admin' && b.password.length < 12)) {
      return err(401, 'unauthorized', 'Incorrect username or password.', { remainingAttempts: 4 });
    }
    storage()?.setItem(SESSION_KEY, '1');
    if (b.password !== 'admin') storage()?.setItem(DEFAULT_CREDS_KEY, '0');
    document.cookie = `tm_csrf=${CSRF}; path=/; SameSite=Strict`;
    const m = me();
    return json({
      user: state.users[0] as unknown as JsonBodyType,
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
    authed(() => json(state.users as unknown as JsonBodyType)),
  ),
  http.post(
    `${P}/users`,
    authed(async ({ request }) => {
      const b = await body<{ username: string; displayName: string; role: User['role'] }>(request);
      const u: User = {
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
      return json(u as unknown as JsonBodyType, 201);
    }),
  ),
  http.patch(
    `${P}/users/:id`,
    authed(async ({ request, params }) => {
      const b = await body<Partial<User>>(request);
      state.users = state.users.map((u) =>
        u.id === str(params.id) ? { ...u, ...b, updatedAt: stamp() } : u,
      );
      return json(state.users.find((u) => u.id === str(params.id)) as unknown as JsonBodyType);
    }),
  ),
  http.delete(
    `${P}/users/:id`,
    authed(({ params }) => {
      if (str(params.id) === 'u-admin') return err(409, 'conflict', 'The last admin cannot be deleted.');
      state.users = state.users.filter((u) => u.id !== str(params.id));
      return new HttpResponse(null, { status: 204 });
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
      return json(runQuery(b) as unknown as JsonBodyType);
    }),
  ),
  http.post(
    `${P}/query/validate`,
    authed(async ({ request }) => json(validate((await body<{ q: string }>(request)).q))),
  ),
  http.get(
    `${P}/histogram`,
    authed(({ url }) => json(histogram(url))),
  ),
  http.get(
    `${P}/facets`,
    authed(({ url }) => json(facets(url) as unknown as JsonBodyType)),
  ),
  http.get(
    `${P}/fields`,
    authed(() => json(D.FIELDS as unknown as JsonBodyType)),
  ),
  http.get(
    `${P}/events/:id`,
    authed(({ params }) => {
      const ev = [...D.EVENTS, ...D.TRACE_EVENTS].find((e) => e.id === str(params.id));
      return ev === undefined
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
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
      const svc = /service:([\w-]+)/.exec(q)?.[1];
      const errs = /status:error|error/.test(q);
      return json(
        D.TRACE_SUMMARIES.filter(
          (t) => (svc === undefined || t.services.includes(svc)) && (!errs || t.errorCount > 0),
        ) as unknown as JsonBodyType,
      );
    }),
  ),
  http.get(
    `${P}/traces/:id`,
    authed(({ params }) => {
      const id = str(params.id).toLowerCase();
      if (id === D.TRACE_ID)
        return json({ spans: D.TRACE_SPANS, events: D.TRACE_EVENTS } as unknown as JsonBodyType);
      const sum = D.TRACE_SUMMARIES.find((t) => t.traceId === id);
      if (sum === undefined) return err(404, 'not_found', 'trace not found');
      const spans = D.TRACE_SPANS.slice(0, Math.min(sum.spanCount, 6)).map((s) => ({ ...s, traceId: id }));
      return json({ spans, events: [] } as unknown as JsonBodyType);
    }),
  ),
  http.get(
    `${P}/metrics/names`,
    authed(({ url }) => {
      const q = (url.searchParams.get('q') ?? '').toLowerCase();
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
      const evs = selectEvents(url.searchParams.get('q') ?? '', from, to, []);
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
    () => state.alerts,
    (xs) => (state.alerts = xs),
    (b) => ({
      ...(b as unknown as Alert),
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
    authed(() => json({ retention: { logsDays: 30, spansDays: 7, metricsDays: 30 }, ui: {} })),
  ),
  http.patch(
    `${P}/settings`,
    authed(async ({ request }) =>
      json({
        retention: { logsDays: 30, spansDays: 7, metricsDays: 30 },
        ui: {},
        ...(await body<Record<string, unknown>>(request)),
      } as JsonBodyType),
    ),
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
