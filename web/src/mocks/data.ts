// SPDX-License-Identifier: Apache-2.0
//
// Deterministic demo data for `--mode mock` (CI e2e tests and design-review screenshots only;
// never part of the production bundle). Shapes follow SPEC §11.2–§11.5 exactly; values mirror
// the design artboards so screenshots can be compared with them.

import type {
  Alert,
  AlertHistoryEntry,
  ApiToken,
  AuditEntry,
  Channel,
  Connection,
  Dashboard,
  DashboardDoc,
  DashboardTemplate,
  FieldInfo,
  FieldMapping,
  ImportJob,
  IndexedField,
  LevelName,
  LogEvent,
  MetricName,
  Signal,
  Span,
  Stream,
  SystemReport,
  TraceSummary,
  User,
} from '../api/types';

/** A team of the mock metadata (SPEC §15.10): member ids and granted stream names. */
export interface MockTeam {
  id: string;
  name: string;
  description: string;
  memberIds: string[];
  streams: string[];
  createdAt: string;
  updatedAt: string;
}

/** Small deterministic PRNG (mulberry32) so every run renders the same data. */
export function rng(seed: number): () => number {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

export function hashString(s: string): number {
  let h = 2166136261;
  for (let i = 0; i < s.length; i++) h = Math.imul(h ^ s.charCodeAt(i), 16777619);
  return h >>> 0;
}

export const NOW = Date.now();
const MIN = 60_000;
const iso = (ms: number): string => new Date(ms).toISOString();

export const SERVICES = [
  'checkout-api',
  'inventory-worker',
  'auth-gateway',
  'payments-svc',
  'notification-worker',
];
const HOSTS: Record<string, string[]> = {
  'checkout-api': ['ck-prod-01', 'ck-prod-03'],
  'inventory-worker': ['wk-prod-07'],
  'auth-gateway': ['ag-prod-02'],
  'payments-svc': ['pay-prod-02'],
  'notification-worker': ['wk-prod-04'],
};
const STREAM_OF: Record<string, string> = {
  'checkout-api': 'api',
  'inventory-worker': 'workers',
  'auth-gateway': 'api',
  'payments-svc': 'payments',
  'notification-worker': 'workers',
};

/** Spans arrive through the OTLP collector except the payments service, which has its own key. */
export function spanStreamOf(service: string): string {
  return service === 'payments-svc' ? 'payments' : 'otel-traces';
}

interface Shape {
  service: string;
  level: LevelName;
  template: string;
  props: (r: () => number) => Record<string, unknown>;
  exception?: string;
  weight: number;
}

const pick = <T>(r: () => number, xs: readonly T[]): T => xs[Math.floor(r() * xs.length)] as T;
const id36 = (r: () => number, n: number): string =>
  Array.from({ length: n }, () => pick(r, [...'ABCDEFGHJKMNPQRSTVWXYZ0123456789'])).join('');
const user = (r: () => number): string => `u_${Math.floor(10000 + r() * 89999)}`;

export const TRACE_ID = '4bf92f3577b34da6a3ce929d0e0e4736';

const PAYMENT_EXCEPTION = `Payments.Provider.PaymentDeclinedException: Charge declined: insufficient_funds
   at Payments.Provider.StripeClient.ChargeAsync(ChargeRequest request) in /src/Payments/Provider/StripeClient.cs:line 88
   at Payments.Orders.ChargeOrderHandler.Handle(ChargeOrder cmd) in /src/Payments/Orders/ChargeOrderHandler.cs:line 41
   at Payments.Api.OrdersController.Charge(String orderId) in /src/Payments/Api/OrdersController.cs:line 57
   at Microsoft.AspNetCore.Mvc.Infrastructure.ActionMethodExecutor.TaskOfIActionResultExecutor.Execute()
   at Microsoft.AspNetCore.Mvc.Infrastructure.ControllerActionInvoker.InvokeActionMethodAsync()
   at Microsoft.AspNetCore.Mvc.Infrastructure.ControllerActionInvoker.InvokeNextActionFilterAsync()
   at Microsoft.AspNetCore.Routing.EndpointMiddleware.Invoke(HttpContext httpContext)
   at Microsoft.AspNetCore.Authorization.AuthorizationMiddleware.Invoke(HttpContext context)
   at Microsoft.AspNetCore.Authentication.AuthenticationMiddleware.Invoke(HttpContext context)
   at Microsoft.AspNetCore.Server.Kestrel.Core.Internal.Http.HttpProtocol.ProcessRequests()
   at System.Threading.Tasks.Task.ExecuteWithThreadLocal(Task& currentTaskSlot)
   at System.Threading.ThreadPoolWorkQueue.Dispatch()`;

const INVENTORY_EXCEPTION = `Checkout.Inventory.InventoryReservationException: reservation timed out after 1204 ms
   at Checkout.Inventory.ReservationClient.ReserveAsync(Sku sku, Int32 qty) in /src/Checkout/Inventory/ReservationClient.cs:line 112
   at Checkout.Orders.PlaceOrderHandler.Handle(PlaceOrder cmd) in /src/Checkout/Orders/PlaceOrderHandler.cs:line 64
   at Microsoft.AspNetCore.Routing.EndpointMiddleware.Invoke(HttpContext httpContext)`;

const SHAPES: Shape[] = [
  {
    service: 'checkout-api',
    level: 'Error',
    template: 'Order {OrderId} failed for user {UserId}: inventory reservation timed out after {Elapsed} ms',
    props: (r) => ({
      OrderId: `ord_${id36(r, 4)}`,
      UserId: user(r),
      Elapsed: 1204,
      exception_type: 'InventoryReservationException',
    }),
    exception: INVENTORY_EXCEPTION,
    weight: 3,
  },
  {
    service: 'checkout-api',
    level: 'Warning',
    template: 'Slow response on {RequestPath} took {Elapsed} ms (threshold {Threshold})',
    props: (r) => ({
      RequestPath: 'POST /api/v1/orders',
      Elapsed: Math.floor(260 + r() * 700),
      Threshold: 250,
      status_code: 200,
    }),
    weight: 4,
  },
  {
    service: 'inventory-worker',
    level: 'Warning',
    template: 'Retrying reservation for sku {Sku} attempt {Attempt}/{MaxAttempts} after {Reason}',
    props: (r) => ({
      Sku: `SKU-${Math.floor(10000 + r() * 89999)}`,
      Attempt: Math.floor(2 + r() * 3),
      MaxAttempts: 5,
      Reason: 'ETIMEDOUT',
    }),
    weight: 3,
  },
  {
    service: 'payments-svc',
    level: 'Error',
    template: 'Charge {ChargeId} declined by provider {Provider}: {Reason} (order {OrderId})',
    props: (r) => ({
      ChargeId: `ch_${id36(r, 5)}`,
      Provider: 'stripe',
      Reason: 'insufficient_funds',
      OrderId: `ord_${id36(r, 4)}`,
      UserId: user(r),
      Elapsed: Math.floor(200 + r() * 600),
      status_code: 402,
      exception_type: 'PaymentDeclinedException',
    }),
    exception: PAYMENT_EXCEPTION,
    weight: 2,
  },
  {
    service: 'auth-gateway',
    level: 'Warning',
    template: 'Token refresh for {UserId} exceeded budget: {Elapsed} ms from {ClientIp}',
    props: (r) => ({
      UserId: user(r),
      Elapsed: Math.floor(300 + r() * 200),
      ClientIp: `10.4.2.${Math.floor(r() * 250)}`,
    }),
    weight: 3,
  },
  {
    service: 'inventory-worker',
    level: 'Fatal',
    template: 'Lost connection to {Endpoint}; worker {Worker} shutting down',
    props: () => ({ Endpoint: 'redis://cache-02:6379', Worker: 'wk-prod-07' }),
    weight: 0.3,
  },
  {
    service: 'checkout-api',
    level: 'Warning',
    template: 'Cart {CartId} recalculated {Count} times in {Elapsed} ms for {UserId}',
    props: (r) => ({
      CartId: `cart_${id36(r, 4)}`,
      Count: Math.floor(3 + r() * 4),
      Elapsed: Math.floor(250 + r() * 100),
      UserId: user(r),
    }),
    weight: 2,
  },
  {
    service: 'checkout-api',
    level: 'Error',
    template: 'Unhandled {ExceptionType} on {RequestPath} → {StatusCode}',
    props: () => ({
      ExceptionType: 'InventoryReservationException',
      RequestPath: 'POST /api/v1/orders',
      StatusCode: 500,
      status_code: 500,
      exception_type: 'InventoryReservationException',
    }),
    exception: INVENTORY_EXCEPTION,
    weight: 1.5,
  },
  {
    service: 'notification-worker',
    level: 'Warning',
    template: 'Email to {UserId} deferred: provider rate limit {StatusCode}, retry in {Delay} s',
    props: (r) => ({ UserId: user(r), StatusCode: 429, Delay: 30, status_code: 429 }),
    weight: 1.5,
  },
  {
    service: 'payments-svc',
    level: 'Warning',
    template: 'Webhook {EventId} processed in {Elapsed} ms (p95 budget {Budget})',
    props: (r) => ({
      EventId: `evt_${id36(r, 4).toLowerCase()}`,
      Elapsed: Math.floor(400 + r() * 400),
      Budget: 250,
    }),
    weight: 2,
  },
  {
    service: 'auth-gateway',
    level: 'Error',
    template: 'Upstream {Upstream} returned {StatusCode} for {RequestPath}',
    props: () => ({
      Upstream: 'identity-core',
      StatusCode: 503,
      RequestPath: 'GET /oauth/introspect',
      status_code: 503,
    }),
    weight: 1.5,
  },
  {
    service: 'checkout-api',
    level: 'Information',
    template: 'Order {OrderId} placed by {UserId} in {Elapsed} ms',
    props: (r) => ({ OrderId: `ord_${id36(r, 4)}`, UserId: user(r), Elapsed: Math.floor(120 + r() * 300) }),
    weight: 4,
  },
  {
    service: 'inventory-worker',
    level: 'Warning',
    template: 'Queue {Queue} depth {Depth} exceeds soft limit {Limit}',
    props: (r) => ({ Queue: 'reservations', Depth: Math.floor(1100 + r() * 900), Limit: 1000 }),
    weight: 1,
  },
  {
    service: 'auth-gateway',
    level: 'Warning',
    template: 'Rate limit near threshold for key {KeyPrefix}: {Used}/{Limit} req/min',
    props: (r) => ({ KeyPrefix: 'tmk_7f2a…', Used: Math.floor(270 + r() * 29), Limit: 300 }),
    weight: 1,
  },
  {
    service: 'auth-gateway',
    level: 'Debug',
    template: 'Session {SessionId} validated in {Elapsed} ms',
    props: (r) => ({ SessionId: `s_${id36(r, 4).toLowerCase()}`, Elapsed: Math.floor(1 + r() * 9) }),
    weight: 1,
  },
  {
    service: 'inventory-worker',
    level: 'Verbose',
    template: 'Heartbeat ok · queue depth {Depth}',
    props: (r) => ({ Depth: Math.floor(r() * 900) }),
    weight: 0.6,
  },
];

function render(template: string, props: Record<string, unknown>): string {
  return template.replace(/\{([A-Za-z0-9_]+)\}/g, (_, k: string) => String(props[k] ?? `{${k}}`));
}

const TOTAL_WEIGHT = SHAPES.reduce((a, s) => a + s.weight, 0);

function shapeFor(r: () => number): Shape {
  let x = r() * TOTAL_WEIGHT;
  for (const s of SHAPES) {
    x -= s.weight;
    if (x <= 0) return s;
  }
  return SHAPES[0] as Shape;
}

function makeEvent(r: () => number, t: number, n: number, shape = shapeFor(r)): LogEvent {
  const props = shape.props(r);
  const host = pick(r, HOSTS[shape.service] ?? ['host-01']);
  const traced = shape.level === 'Error' || r() < 0.4;
  return {
    id: `0192${n.toString(16).padStart(4, '0')}-7a3c-7000-8000-${(t % 1e12).toString(16).padStart(12, '0')}`,
    timestamp: iso(t),
    stream: STREAM_OF[shape.service] ?? 'default',
    level: shape.level,
    service: shape.service,
    environment: r() < 0.94 ? 'production' : 'staging',
    host,
    messageTemplate: shape.template,
    message: render(shape.template, props),
    exception: shape.exception ?? '',
    eventType: (hashString(shape.template) & 0xffffff).toString(16).padStart(6, '0'),
    traceId: traced
      ? n === 3
        ? TRACE_ID
        : (hashString(`t${n}`) * 2654435761).toString(16).padStart(32, '0').slice(0, 32)
      : '',
    spanId: traced ? (n === 3 ? '00f067aa0ba902b7' : hashString(`s${n}`).toString(16).padStart(16, '0')) : '',
    protocol: shape.service === 'auth-gateway' ? 'otlp' : 'seq',
    connectionId: 'c1',
    properties: { ...props },
  };
}

/** Newest first: about 600 events over the last 15 minutes. */
export const EVENTS: LogEvent[] = (() => {
  const r = rng(42);
  const out: LogEvent[] = [];
  let t = NOW - 1200;
  for (let n = 0; n < 600; n++) {
    const shape = n === 3 ? (SHAPES[3] as Shape) : n === 5 ? (SHAPES[5] as Shape) : undefined;
    out.push(makeEvent(r, t, n, shape));
    t -= Math.floor(200 + r() * 2600);
  }
  return out;
})();

let liveCounter = 10_000;
const liveRng = rng(7);
/** A new event "now" for the simulated live tail. */
export function liveEvent(): LogEvent {
  const ev = makeEvent(liveRng, Date.now(), liveCounter++);
  return { ...ev, ingestedAt: iso(Date.now()) };
}

export const LEVEL_WEIGHT: Record<string, number> = {
  Verbose: 0.04,
  Debug: 0.06,
  Information: 0.22,
  Warning: 0.47,
  Error: 0.2,
  Fatal: 0.01,
};

export const FIELDS: FieldInfo[] = [
  ...[
    '@timestamp',
    'level',
    'service',
    'environment',
    'host',
    'message',
    'template',
    'exception',
    'trace_id',
    'span_id',
    'stream',
  ].map((name) => ({ name, builtin: true, numeric: false, hits: 0 })),
  { name: 'exception_type', builtin: false, numeric: false, hits: 3051 },
  { name: 'status_code', builtin: false, numeric: true, hits: 11240 },
  { name: 'Elapsed', builtin: false, numeric: true, hits: 9870 },
  { name: 'OrderId', builtin: false, numeric: false, hits: 8122 },
  { name: 'UserId', builtin: false, numeric: false, hits: 7310 },
  { name: 'RequestPath', builtin: false, numeric: false, hits: 6018 },
  { name: 'ChargeId', builtin: false, numeric: false, hits: 811 },
  { name: 'Provider', builtin: false, numeric: false, hits: 811 },
  { name: 'Sku', builtin: false, numeric: false, hits: 2204 },
  { name: 'Reason', builtin: false, numeric: false, hits: 2911 },
];

// ---- traces ------------------------------------------------------------------------------------
const TRACE_START = NOW - 4 * MIN;

interface SpanSeed {
  id: string;
  parent: string;
  service: string;
  name: string;
  kind: string;
  offset: number;
  dur: number;
  error?: string;
  attrs?: Record<string, unknown>;
}

const SPAN_SEEDS: SpanSeed[] = [
  {
    id: '00f067aa0ba902b7',
    parent: '',
    service: 'checkout-api',
    name: 'POST /api/v1/orders',
    kind: 'server',
    offset: 0,
    dur: 1412,
    error: 'inventory reservation failed',
    attrs: { 'http.method': 'POST', 'http.route': '/api/v1/orders', 'http.status_code': 500 },
  },
  {
    id: 'a1b2c3d4e5f60001',
    parent: '00f067aa0ba902b7',
    service: 'checkout-api',
    name: 'auth.verify',
    kind: 'internal',
    offset: 4,
    dur: 38,
  },
  {
    id: 'a1b2c3d4e5f60002',
    parent: 'a1b2c3d4e5f60001',
    service: 'auth-gateway',
    name: 'GET /oauth/introspect',
    kind: 'server',
    offset: 6,
    dur: 31,
    attrs: { 'http.method': 'GET' },
  },
  {
    id: 'a1b2c3d4e5f60003',
    parent: 'a1b2c3d4e5f60002',
    service: 'auth-gateway',
    name: 'cache.get session',
    kind: 'client',
    offset: 9,
    dur: 3,
    attrs: { 'db.system': 'redis' },
  },
  {
    id: 'a1b2c3d4e5f60004',
    parent: '00f067aa0ba902b7',
    service: 'checkout-api',
    name: 'orders.create',
    kind: 'internal',
    offset: 44,
    dur: 1340,
  },
  {
    id: 'a1b2c3d4e5f60005',
    parent: 'a1b2c3d4e5f60004',
    service: 'checkout-api',
    name: 'db.query SELECT cart',
    kind: 'client',
    offset: 48,
    dur: 24,
    attrs: { 'db.system': 'postgresql' },
  },
  {
    id: 'a1b2c3d4e5f60006',
    parent: 'a1b2c3d4e5f60004',
    service: 'checkout-api',
    name: 'pricing.calculate',
    kind: 'internal',
    offset: 76,
    dur: 14,
  },
  {
    id: '7a3c0f91b2e4d5c6',
    parent: 'a1b2c3d4e5f60004',
    service: 'inventory-worker',
    name: 'inventory.reserve',
    kind: 'client',
    offset: 96,
    dur: 1204,
    error: 'deadline exceeded after 1200ms',
    attrs: {
      'rpc.system': 'grpc',
      'rpc.method': 'Reserve',
      'net.peer.name': 'inventory-core.svc',
      sku: 'SKU-77421',
      'retry.attempt': 3,
      host: 'wk-prod-07',
    },
  },
  {
    id: 'a1b2c3d4e5f60008',
    parent: '7a3c0f91b2e4d5c6',
    service: 'inventory-worker',
    name: 'grpc Reserve (attempt 1)',
    kind: 'client',
    offset: 100,
    dur: 401,
    error: 'ETIMEDOUT',
  },
  {
    id: 'a1b2c3d4e5f60009',
    parent: '7a3c0f91b2e4d5c6',
    service: 'inventory-worker',
    name: 'grpc Reserve (attempt 2)',
    kind: 'client',
    offset: 510,
    dur: 398,
    error: 'ETIMEDOUT',
  },
  {
    id: 'a1b2c3d4e5f6000a',
    parent: '7a3c0f91b2e4d5c6',
    service: 'inventory-worker',
    name: 'grpc Reserve (attempt 3)',
    kind: 'client',
    offset: 912,
    dur: 386,
    error: 'deadline exceeded',
  },
  {
    id: 'a1b2c3d4e5f6000b',
    parent: 'a1b2c3d4e5f60004',
    service: 'payments-svc',
    name: 'payments.authorize (skipped)',
    kind: 'client',
    offset: 1302,
    dur: 6,
  },
  {
    id: 'a1b2c3d4e5f6000c',
    parent: 'a1b2c3d4e5f60004',
    service: 'checkout-api',
    name: 'db.query UPDATE order',
    kind: 'client',
    offset: 1312,
    dur: 41,
    attrs: { 'db.system': 'postgresql' },
  },
  {
    id: 'a1b2c3d4e5f6000d',
    parent: '00f067aa0ba902b7',
    service: 'notification-worker',
    name: 'notify.enqueue order.failed',
    kind: 'producer',
    offset: 1390,
    dur: 18,
  },
];

export const TRACE_SPANS: Span[] = SPAN_SEEDS.map((s) => ({
  traceId: TRACE_ID,
  spanId: s.id,
  parentSpanId: s.parent,
  stream: spanStreamOf(s.service),
  service: s.service,
  name: s.name,
  kind: s.kind,
  start: iso(TRACE_START + s.offset),
  durationMs: s.dur,
  statusCode: s.error !== undefined ? 'error' : 'unset',
  statusMessage: s.error ?? '',
  attributes: s.attrs ?? {},
  resourceAttributes: { 'service.name': s.service, 'deployment.environment': 'production' },
  events:
    s.error !== undefined
      ? [
          {
            time: iso(TRACE_START + s.offset + s.dur),
            name: 'exception',
            attributes: { 'exception.message': s.error },
          },
        ]
      : [],
  links: [],
}));

export const TRACE_EVENTS: LogEvent[] = (() => {
  const r = rng(99);
  const mk = (
    offset: number,
    level: LevelName,
    template: string,
    props: Record<string, unknown>,
    spanId: string,
  ): LogEvent => ({
    ...makeEvent(r, TRACE_START + offset, 900 + offset, SHAPES[2] as Shape),
    level,
    service: 'inventory-worker',
    messageTemplate: template,
    message: render(template, props),
    properties: props,
    exception: '',
    traceId: TRACE_ID,
    spanId,
  });
  return [
    mk(106, 'Debug', 'Reserve {Sku} qty {Qty}', { Sku: 'SKU-77421', Qty: 2 }, '7a3c0f91b2e4d5c6'),
    mk(
      709,
      'Warning',
      'Retrying attempt {Attempt}/{Max} after {Reason}',
      { Attempt: 2, Max: 5, Reason: 'ETIMEDOUT' },
      '7a3c0f91b2e4d5c6',
    ),
    mk(
      1197,
      'Warning',
      'Retrying attempt {Attempt}/{Max} after {Reason}',
      { Attempt: 3, Max: 5, Reason: 'ETIMEDOUT' },
      '7a3c0f91b2e4d5c6',
    ),
    mk(1300, 'Error', 'Reservation deadline exceeded', {}, '7a3c0f91b2e4d5c6'),
    mk(
      1398,
      'Error',
      'Order {OrderId} failed for user {UserId}',
      { OrderId: 'ord_8H3J9', UserId: 'u_41920' },
      '00f067aa0ba902b7',
    ),
  ];
})();

export const TRACE_SUMMARIES: TraceSummary[] = (() => {
  const r = rng(5);
  const roots = [
    ['checkout-api', 'POST /api/v1/orders'],
    ['checkout-api', 'GET /api/v1/cart'],
    ['auth-gateway', 'POST /oauth/token'],
    ['payments-svc', 'POST /charges'],
    ['inventory-worker', 'reservations.consume'],
    ['notification-worker', 'email.send'],
  ] as const;
  const out: TraceSummary[] = [
    {
      traceId: TRACE_ID,
      rootService: 'checkout-api',
      rootName: 'POST /api/v1/orders',
      start: iso(TRACE_START),
      durationMs: 1412,
      spanCount: 14,
      errorCount: 5,
      services: SERVICES,
    },
  ];
  for (let i = 0; i < 40; i++) {
    const [svc, name] = pick(r, roots);
    const err = r() < 0.2 ? Math.floor(1 + r() * 3) : 0;
    out.push({
      traceId:
        (hashString(`trace${i}`) * 2654435761).toString(16).padStart(8, '0') +
        hashString(`x${i}`).toString(16).padStart(8, '0') +
        '9c1d2e3f4a5b6c7d'.slice(0, 16),
      rootService: svc,
      rootName: name,
      start: iso(NOW - Math.floor(r() * 14 * MIN)),
      durationMs: Math.floor(20 + r() * (err > 0 ? 1800 : 600)),
      spanCount: Math.floor(3 + r() * 20),
      errorCount: err,
      services: SERVICES.filter(() => r() < 0.6)
        .concat(svc)
        .filter((v, j, a) => a.indexOf(v) === j),
    });
  }
  return out.sort((a, b) => Date.parse(b.start) - Date.parse(a.start));
})();

export const METRIC_NAMES: MetricName[] = [
  {
    name: 'http.server.request.duration',
    kind: 'histogram',
    unit: 's',
    description: 'Duration of HTTP server requests.',
  },
  {
    name: 'http.server.active_requests',
    kind: 'gauge',
    unit: '{request}',
    description: 'Number of active HTTP server requests.',
  },
  {
    name: 'process.cpu.utilization',
    kind: 'gauge',
    unit: '1',
    description: 'CPU utilization of the process.',
  },
  { name: 'process.memory.usage', kind: 'gauge', unit: 'By', description: 'Resident memory of the process.' },
  {
    name: 'db.client.connections.usage',
    kind: 'gauge',
    unit: '{connection}',
    description: 'Connections in use, by state.',
  },
  {
    name: 'messaging.queue.depth',
    kind: 'gauge',
    unit: '{message}',
    description: 'Messages waiting in the reservations queue.',
  },
  {
    name: 'tidemark.ingest.events',
    kind: 'sum',
    unit: '{event}',
    description: 'Events accepted by OpenTidemark.',
  },
  {
    name: 'tidemark.ingest.dropped',
    kind: 'sum',
    unit: '{event}',
    description: 'Events dropped by OpenTidemark.',
  },
  {
    name: 'runtime.dotnet.gc.collections.count',
    kind: 'sum',
    unit: '{collection}',
    description: 'Garbage collections since start.',
  },
];

// ---- signals, dashboards ---------------------------------------------------------------------
const created = iso(NOW - 30 * 86_400_000);
export const SIGNALS: Signal[] = [
  {
    id: 'sig-errors',
    name: 'Errors',
    tql: 'level >= error',
    color: 'danger',
    ownerId: 'u-admin',
    shared: true,
    source: 'logs',
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'sig-slow',
    name: 'Slow > 250 ms',
    tql: 'Elapsed > 250',
    color: 'warning',
    ownerId: 'u-admin',
    shared: true,
    source: 'logs',
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'sig-checkout',
    name: 'Checkout',
    tql: 'service:checkout-api',
    color: 'chart1',
    ownerId: 'u-admin',
    shared: true,
    source: 'logs',
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'sig-payments',
    name: 'Payments',
    tql: 'service:payments-svc',
    color: 'chart4',
    ownerId: 'u-admin',
    shared: false,
    source: 'logs',
    createdAt: created,
    updatedAt: created,
  },
];

export const CHECKOUT_DOC: DashboardDoc = {
  version: 1,
  timeRange: { from: 'now-6h', to: 'now' },
  refresh: '30s',
  variables: [
    {
      name: 'service',
      label: 'Service',
      type: 'field-values',
      source: 'logs',
      field: 'service',
      multi: true,
      default: ['checkout-api', 'payments-svc'],
    },
    {
      name: 'env',
      label: 'Environment',
      type: 'custom',
      options: ['production', 'staging'],
      multi: false,
      default: ['production'],
    },
  ],
  panels: [
    {
      id: 'rate',
      title: 'Request rate',
      type: 'stat',
      grid: { x: 0, y: 0, w: 3, h: 2 },
      query: 'source spans | service:$service | stats count() span auto',
      options: { unit: '/s' },
    },
    {
      id: 'errors',
      title: 'Error rate',
      type: 'stat',
      grid: { x: 3, y: 0, w: 3, h: 2 },
      query: 'level >= error and service:$service | stats count() span auto',
      options: { unit: '%', thresholds: [{ value: 2, color: 'danger' }] },
    },
    {
      id: 'p95',
      title: 'P95 latency',
      type: 'stat',
      grid: { x: 6, y: 0, w: 3, h: 2 },
      query: 'source spans | service:$service | stats p95(duration) span auto',
      options: { unit: 'ms', thresholds: [{ value: 400, color: 'warning' }] },
    },
    {
      id: 'volume',
      title: 'Log volume',
      type: 'stat',
      grid: { x: 9, y: 0, w: 3, h: 2 },
      query: 'service:$service | stats count() span auto',
      options: { unit: 'k/min' },
    },
    {
      id: 'reqrate',
      title: 'Request rate',
      type: 'timeseries',
      grid: { x: 0, y: 2, w: 6, h: 5 },
      query: 'source spans | service:$service | stats count() by route span auto',
      options: { style: 'area', unit: 'req/s', legend: 'bottom' },
    },
    {
      id: 'errrate',
      title: 'Error rate',
      type: 'timeseries',
      grid: { x: 6, y: 2, w: 6, h: 5 },
      query: 'level >= error and service:$service | stats count() span auto',
      options: { style: 'area', unit: '%', legend: 'hidden', thresholds: [{ value: 2, color: 'danger' }] },
    },
    {
      id: 'latency',
      title: 'Latency',
      type: 'timeseries',
      grid: { x: 0, y: 7, w: 4, h: 6 },
      query: 'source spans | service:$service | stats p50(duration), p95(duration), p99(duration) span auto',
      options: { style: 'line', unit: 'ms' },
    },
    {
      id: 'logvol',
      title: 'Log volume by service',
      type: 'timeseries',
      grid: { x: 4, y: 7, w: 4, h: 6 },
      query: 'service:$service | stats count() by service span 15m',
      options: { style: 'bars', stack: true },
    },
    {
      id: 'exceptions',
      title: 'Top exceptions',
      type: 'table',
      grid: { x: 8, y: 7, w: 4, h: 6 },
      query: 'service:$service | top 10 exception_type',
    },
  ],
};

export const DASHBOARDS: Dashboard[] = [
  {
    id: 'd-checkout',
    name: 'Checkout — service overview',
    description: 'Golden signals for the checkout flow.',
    ownerId: 'u-admin',
    shared: true,
    version: 7,
    doc: CHECKOUT_DOC,
    createdAt: created,
    updatedAt: iso(NOW - 2 * 3_600_000),
  },
  {
    id: 'd-ingest',
    name: 'Ingestion health',
    description: 'Accepted, dropped and queued events per protocol.',
    ownerId: 'u-admin',
    shared: true,
    version: 2,
    doc: {
      version: 1,
      timeRange: { from: 'now-24h', to: 'now' },
      panels: [
        {
          id: 'acc',
          title: 'Accepted events',
          type: 'timeseries',
          grid: { x: 0, y: 0, w: 8, h: 6 },
          query: 'source metrics | name:tidemark.ingest.events | stats sum(value) by protocol span auto',
          options: { style: 'bars', stack: true },
        },
        {
          id: 'note',
          title: 'Notes',
          type: 'markdown',
          grid: { x: 8, y: 0, w: 4, h: 6 },
          options: {
            content:
              '## Runbook\n\nDropped events usually mean a **paused stream** or back-pressure. See *Streams*.',
          },
        },
      ],
    },
    createdAt: created,
    updatedAt: iso(NOW - 26 * 3_600_000),
  },
  {
    id: 'd-payments',
    name: 'Payments declines',
    description: 'Decline reasons and providers.',
    ownerId: 'u-maria',
    shared: false,
    version: 1,
    doc: { version: 1, panels: [] },
    createdAt: created,
    updatedAt: iso(NOW - 3 * 86_400_000),
  },
];

export const TEMPLATES: DashboardTemplate[] = [
  {
    id: 'service-overview',
    name: 'Service overview (OTel)',
    description: 'Request rate, errors and latency from spans plus log volume.',
    doc: CHECKOUT_DOC,
  },
  {
    id: 'log-volume',
    name: 'Log volume & errors',
    description: 'Events by level and service with top exceptions.',
    doc: { version: 1, panels: [] },
  },
  {
    id: 'http-red',
    name: 'HTTP (RED metrics from spans)',
    description: 'Rate, errors and duration per route.',
    doc: { version: 1, panels: [] },
  },
  {
    id: 'ingestion-health',
    name: 'Ingestion health',
    description: 'OpenTidemark self-metrics: accepted, dropped, queue depth.',
    doc: { version: 1, panels: [] },
  },
];

// ---- alerts, channels -------------------------------------------------------------------------
export const CHANNELS: Channel[] = [
  {
    id: 'ch-slack',
    name: 'slack',
    kind: 'slack',
    config: { url: '••••' },
    version: 1,
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'ch-pd',
    name: 'pagerduty-webhook',
    kind: 'webhook',
    config: { url: '••••', secret: '••••', headers: { 'X-Routing-Key': '••••' } },
    version: 3,
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'ch-mail',
    name: 'oncall-email',
    kind: 'email',
    config: { to: ['oncall@example.com'] },
    version: 1,
    createdAt: created,
    updatedAt: created,
  },
];

type RuleSeed = [
  string,
  string,
  Alert['state'],
  string,
  string,
  number,
  string,
  string,
  string[],
  number | null,
  string,
  boolean?,
  string?,
];
const RULES: RuleSeed[] = [
  [
    'a-checkout',
    'Checkout errors spike',
    'firing',
    'service:checkout-api and level >= error | stats count() by service',
    '>',
    25,
    '2m',
    'critical',
    ['ch-slack', 'ch-pd'],
    142,
    'service',
  ],
  [
    'a-fatal',
    'Inventory worker fatal',
    'firing',
    'service:inventory-worker and level:fatal | stats count()',
    '>',
    0,
    '0s',
    'critical',
    ['ch-pd'],
    3,
    '',
  ],
  [
    'a-p95',
    'p95 latency orders',
    'pending',
    'source spans | service:checkout-api | stats p95(duration) by route',
    '>',
    400,
    '5m',
    'warning',
    ['ch-slack'],
    412,
    'route',
  ],
  [
    'a-auth',
    'Auth failures burst',
    'ok',
    'service:auth-gateway and status_code:401 | stats count()',
    '>',
    50,
    '1m',
    'warning',
    ['ch-slack'],
    4.1,
    '',
  ],
  [
    'a-decl',
    'Payment declines ratio',
    'ok',
    'service:payments-svc and Reason:* | stats count()',
    '>',
    120,
    '5m',
    'info',
    ['ch-mail'],
    38,
    '',
  ],
  [
    'a-drops',
    'Ingest drops',
    'ok',
    'source metrics | name:tidemark.ingest.dropped | stats sum(value)',
    '>',
    0,
    '1m',
    'warning',
    ['ch-pd'],
    0,
    '',
  ],
  [
    'a-notif',
    'Notification backlog',
    'ok',
    'service:notification-worker and "deferred" | stats count()',
    '>',
    200,
    '10m',
    'info',
    ['ch-slack'],
    61,
    '',
    true,
  ],
  [
    'a-legacy',
    'Legacy stream volume',
    'error',
    'stream:legacy and lvl:error | stats count()',
    '>',
    10,
    '5m',
    'warning',
    [],
    null,
    '',
    false,
    "query failed 3× — unknown field 'lvl'",
  ],
];

export const ALERTS: Alert[] = RULES.map(
  ([id, name, state, query, op, threshold, forDur, severity, channels, value, group, muted, err], i) => ({
    id,
    name,
    enabled: true,
    doc: {
      description: '',
      query,
      condition: { op: op as Alert['doc']['condition']['op'], threshold },
      window: '5m0s',
      every: '1m0s',
      for: forDur === '0s' ? '0s' : forDur.replace('m', 'm0s'),
      severity: severity as Alert['doc']['severity'],
      channels,
      noData: 'ok',
      groupBy: group === '' ? [] : [group],
      muteUntil: muted === true ? iso(NOW + 3 * 3_600_000) : null,
    },
    streams: id === 'a-decl' ? ['payments'] : null,
    state,
    stateSince: iso(NOW - (i + 1) * 7 * MIN),
    lastEvalAt: iso(NOW - 20_000),
    lastValue: value,
    lastError: err ?? '',
    groups:
      group === ''
        ? []
        : [
            {
              labels: { [group]: group === 'service' ? 'checkout-api' : 'POST /orders' },
              state,
              since: iso(NOW - 6 * MIN),
              value,
            },
          ],
    version: 3,
    createdAt: created,
    updatedAt: created,
  }),
);

export const HISTORY: AlertHistoryEntry[] = [
  {
    id: 'h1',
    at: iso(NOW - 4 * MIN),
    fromState: 'pending',
    toState: 'firing',
    value: 142,
    message: 'Checkout errors spike · service=checkout-api · value 142 > 25',
    group: { service: 'checkout-api' },
  },
  {
    id: 'h2',
    at: iso(NOW - 6 * MIN),
    fromState: 'ok',
    toState: 'pending',
    value: 61,
    message: 'Checkout errors spike · service=checkout-api · value 61 > 25',
    group: { service: 'checkout-api' },
  },
  {
    id: 'h3',
    at: iso(NOW - 11 * MIN),
    fromState: 'ok',
    toState: 'firing',
    value: 3,
    message: 'Inventory worker fatal · value 3 > 0',
    group: {},
  },
  {
    id: 'h4',
    at: iso(NOW - 3.5 * 3_600_000),
    fromState: 'firing',
    toState: 'ok',
    value: 4.1,
    message: 'Auth failures burst · value 4.1 ≤ 50',
    group: {},
  },
];

// ---- streams ----------------------------------------------------------------------------------
type StreamSeed = [
  string,
  'logs' | 'spans' | 'metrics',
  number,
  number,
  number,
  number | null,
  number,
  number,
  boolean,
  string[],
];
const STREAM_SEEDS: StreamSeed[] = [
  [
    'api',
    'logs',
    1.84e9,
    412 * 2 ** 30,
    9.6,
    90,
    802,
    718,
    false,
    ['checkout-api', 'auth-gateway', 'payments-svc'],
  ],
  [
    'workers',
    'logs',
    622e6,
    131 * 2 ** 30,
    8.8,
    null,
    207,
    197,
    false,
    ['inventory-worker', 'notification-worker'],
  ],
  ['payments', 'logs', 214e6, 58 * 2 ** 30, 7.1, 365, 65, 62.5, false, ['payments-svc']],
  [
    'otel-traces',
    'spans',
    3.12e9,
    290 * 2 ** 30,
    11.2,
    null,
    1600,
    1597,
    false,
    ['checkout-api', 'inventory-worker'],
  ],
  ['otel-metrics', 'metrics', 980e6, 44 * 2 ** 30, 14, null, 520, 521, false, ['checkout-api']],
  ['legacy', 'logs', 41e6, 9 * 2 ** 30, 6.2, 14, 0, 0, true, ['legacy-app']],
];

export const STREAMS: Stream[] = STREAM_SEEDS.map(
  ([name, source, rows, bytes, ratio, days, r5, r24, paused, top]) => ({
    id: `s-${name}`,
    name,
    description: '',
    retentionDays: source === 'logs' ? days : null,
    spansRetentionDays: source === 'spans' ? days : null,
    metricsRetentionDays: source === 'metrics' ? days : null,
    levelRules:
      name === 'api'
        ? [
            { levels: ['Verbose', 'Debug'], days: 3 },
            { levels: ['Information'], days: 30 },
          ]
        : [],
    paused,
    createdAt: '2026-07-15T00:00:00Z',
    updatedAt: created,
    stats: {
      [source]: {
        rows,
        oldest: '2026-07-15T00:00:04Z',
        newest: iso(NOW - 1200),
        ratePer5Min: r5,
        ratePer24h: r24,
        compressedBytes: bytes,
        uncompressedBytes: Math.round(bytes * ratio),
        compressionRatio: ratio,
        estimated: true,
        topServices: top,
      },
    },
  }),
);

export const MAPPINGS: FieldMapping[] = (
  [
    ['OrderId', 'string', 212e6, '2026-07-15'],
    ['UserId', 'string', 198e6, '2026-07-15'],
    ['Elapsed', 'number', 160e6, '2026-07-15'],
    ['status_code', 'number', 151e6, '2026-07-15'],
    ['RequestPath', 'string', 149e6, '2026-07-15'],
    ['ChargeId', 'string', 31e6, '2026-08-02'],
    ['sku', 'mixed', 28e6, '2026-08-19'],
    ['retry.attempt', 'number', 4.1e6, '2026-09-30'],
    ['user.email', 'string', 880e3, '2026-10-01'],
    ['exception_type', 'string', 3.1e6, '2026-07-15'],
  ] as const
).map(([key, type, hits, first], i) => ({
  source: 'logs' as const,
  key,
  type,
  hits,
  firstSeen: `${first}T00:00:00Z`,
  lastSeen: iso(NOW - (i === 8 ? 2 * 3_600_000 : 0)),
  indexed: key === 'OrderId' || key === 'UserId',
}));

export const INDEXED: IndexedField[] = [
  { id: 'if-1', source: 'logs', key: 'OrderId', column: 'f_orderid', createdAt: created },
  { id: 'if-2', source: 'logs', key: 'UserId', column: 'f_userid', createdAt: created },
];

// ---- connections, users, tokens, audit, system ------------------------------------------------
const conn = (c: Partial<Connection> & Pick<Connection, 'id' | 'name'>): Connection => ({
  description: '',
  keyPrefix: 'tmk_0000',
  protocols: ['seq'],
  defaultStream: 'default',
  allowedCidrs: [],
  allowedStreams: [],
  minimumLevel: null,
  appliedProperties: {},
  rateLimitEps: 0,
  expiresAt: null,
  revokedAt: null,
  rotationGraceUntil: null,
  lastUsedAt: null,
  lastUsedIp: '',
  eventsReceived: 0,
  createdAt: created,
  updatedAt: created,
  ...c,
});

export const CONNECTIONS: Connection[] = [
  conn({
    id: 'c1',
    name: 'checkout-api (prod)',
    keyPrefix: 'tmk_7f2a9c',
    protocols: ['seq', 'otlp'],
    defaultStream: 'api',
    allowedCidrs: ['10.4.0.0/16'],
    minimumLevel: 'Debug',
    lastUsedAt: iso(NOW - 2000),
    lastUsedIp: '10.4.1.22',
    eventsReceived: 1_840_221_004,
  }),
  conn({
    id: 'c2',
    name: 'inventory-worker',
    keyPrefix: 'tmk_c01e4b',
    protocols: ['seq'],
    defaultStream: 'workers',
    allowedCidrs: ['10.4.0.0/16'],
    lastUsedAt: iso(NOW - 1000),
    lastUsedIp: '10.4.3.7',
  }),
  conn({
    id: 'c3',
    name: 'otel-collector',
    keyPrefix: 'tmk_2b7d10',
    protocols: ['otlp'],
    defaultStream: 'otel-traces',
    rateLimitEps: 833,
    lastUsedAt: iso(NOW - 200),
    lastUsedIp: '10.4.9.1',
  }),
  conn({
    id: 'c4',
    name: 'edge-filebeat',
    keyPrefix: 'tmk_9a3f77',
    protocols: ['elastic'],
    defaultStream: 'api',
    allowedStreams: ['api', 'legacy'],
    expiresAt: '2026-12-31T00:00:00Z',
    rotationGraceUntil: iso(NOW + 6 * 86_400_000),
    lastUsedAt: iso(NOW - 14 * MIN),
  }),
  conn({
    id: 'c5',
    name: 'legacy-syslog',
    keyPrefix: 'tmk_e55b02',
    protocols: ['native'],
    defaultStream: 'legacy',
    revokedAt: iso(NOW - 41 * 86_400_000),
    lastUsedAt: iso(NOW - 41 * 86_400_000),
  }),
];

/** Users without their teams (the handlers add `teams` from TEAMS). */
export const USERS: Omit<User, 'teams'>[] = [
  {
    id: 'u-admin',
    username: 'admin',
    displayName: 'Administrator',
    role: 'admin',
    mustChangePassword: false,
    disabled: false,
    lastLoginAt: iso(NOW - 60_000),
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'u-maria',
    username: 'maria',
    displayName: 'Maria Okafor',
    role: 'editor',
    mustChangePassword: false,
    disabled: false,
    lastLoginAt: iso(NOW - 5 * 3_600_000),
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'u-sam',
    username: 'sam',
    displayName: 'Sam Lindqvist',
    role: 'viewer',
    mustChangePassword: false,
    disabled: false,
    lastLoginAt: null,
    createdAt: created,
    updatedAt: created,
  },
  {
    id: 'u-jonas',
    username: 'jonas',
    displayName: 'Jonas Weber',
    role: 'viewer',
    mustChangePassword: true,
    disabled: false,
    lastLoginAt: null,
    createdAt: created,
    updatedAt: created,
  },
];

export const TEAMS: MockTeam[] = [
  {
    id: 'tm-platform',
    name: 'Platform',
    description: 'Owns the checkout, inventory and notification services.',
    memberIds: ['u-maria'],
    streams: ['api', 'workers', 'otel-traces', 'otel-metrics'],
    createdAt: created,
    updatedAt: iso(NOW - 2 * 86_400_000),
  },
  {
    id: 'tm-legacy',
    name: 'Legacy migration',
    description: 'Temporary access to the paused legacy stream.',
    memberIds: [],
    streams: ['legacy'],
    createdAt: created,
    updatedAt: created,
  },
];

export const TOKENS: ApiToken[] = [
  {
    id: 't1',
    name: 'grafana-readonly',
    keyPrefix: 'tmt_4c1e',
    scope: 'read',
    expiresAt: null,
    revokedAt: null,
    lastUsedAt: iso(NOW - 3 * MIN),
    createdAt: created,
  },
  {
    id: 't2',
    name: 'ci-dashboards',
    keyPrefix: 'tmt_9b02',
    scope: 'write',
    expiresAt: '2027-01-31T00:00:00Z',
    revokedAt: null,
    lastUsedAt: iso(NOW - 2 * 86_400_000),
    createdAt: created,
  },
];

export const AUDIT: AuditEntry[] = (
  [
    ['admin', 'login.success', 'user:admin', {}],
    ['admin', 'connection.create', 'connection:payments-svc (prod)', { protocols: ['seq', 'otlp'] }],
    ['maria', 'dashboard.update', 'dashboard:Checkout — service overview', { version: 7 }],
    ['admin', 'alert.mute', 'alert:Notification backlog', { until: iso(NOW + 3 * 3_600_000) }],
    ['admin', 'stream.update', 'stream:api', { retentionDays: 90 }],
    ['system', 'purge.done', 'purge:p-17', { rows: 184220118 }],
    ['admin', 'purge.request', 'purge:p-17', { stream: 'api', q: 'level <= debug' }],
    ['maria', 'export', 'query', { q: 'service:payments-svc', format: 'csv' }],
    ['sam', 'login.failure', 'user:sam', { reason: 'bad password' }],
    ['admin', 'channel.test', 'channel:pagerduty-webhook', { ok: true }],
  ] as const
).map(([actor, action, target, details], i) => ({
  id: `au-${i}`,
  at: iso(NOW - (i * 37 + 3) * MIN),
  actor,
  ip: actor === 'system' ? '' : `10.4.0.${10 + i}`,
  action,
  target,
  details,
}));

export const SYSTEM: SystemReport = {
  version: typeof __TM_DEMO_VERSION__ === 'string' ? __TM_DEMO_VERSION__ : 'dev',
  startedAt: iso(NOW - 3 * 86_400_000 - 4 * 3_600_000),
  uptimeSeconds: 3 * 86400 + 4 * 3600,
  queueDepth: { events: 412, spans: 88, metrics: 12 },
  counters: {
    'ingest.events': 2_118_552_301,
    'ingest.spans': 3_120_448_002,
    'ingest.metrics': 980_112_664,
    'ingest.dropped': 0,
    'ingest.paused_dropped': 18_204,
    'wal.bytes': 12_582_912,
  },
  streams: STREAMS.length,
  clickhouse: {
    version: '25.8.4.13',
    disks: [{ name: 'default', freeBytes: 1.62 * 2 ** 40, totalBytes: 3.6 * 2 ** 40 }],
  },
};

export const IMPORTS: ImportJob[] = [
  {
    id: 'imp-2',
    kind: 'elastic',
    name: 'Legacy ELK logs',
    status: 'running',
    config: {
      url: 'https://es.internal:9200',
      auth: { type: 'basic', username: 'reader' },
      index: 'logs-*',
      timeField: '@timestamp',
      stream: 'legacy',
      pageSize: 5000,
    },
    rowsImported: 3_812_004,
    rowsTotal: 9_297_571,
    errors: 12,
    lastError: 'document 7f… has no @timestamp',
    warnings: [],
    cancelRequested: false,
    requestedBy: 'user:u-admin',
    startedAt: iso(NOW - 39 * MIN),
    finishedAt: null,
    createdAt: iso(NOW - 40 * MIN),
    updatedAt: iso(NOW - MIN),
  },
  {
    id: 'imp-1',
    kind: 'seq',
    name: 'Seq production',
    status: 'completed',
    config: {
      url: 'https://seq.internal',
      stream: 'api',
      pageSize: 1000,
      importEvents: true,
      importSignals: true,
    },
    rowsImported: 12_441_920,
    rowsTotal: 0,
    errors: 0,
    lastError: '',
    warnings: ['2 signals imported disabled: untranslatable filter'],
    cancelRequested: false,
    requestedBy: 'user:u-admin',
    startedAt: iso(NOW - 9 * 86_400_000),
    finishedAt: iso(NOW - 9 * 86_400_000 + 3_600_000),
    createdAt: iso(NOW - 9 * 86_400_000),
    updatedAt: iso(NOW - 9 * 86_400_000 + 3_600_000),
  },
];
