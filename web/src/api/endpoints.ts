// SPDX-License-Identifier: Apache-2.0
//
// Typed wrappers for every /api/v1 route the UI uses (SPEC §11.2–§11.5). Each function
// takes the injected ApiClient so components never build URLs themselves.

import type { ApiClient } from './client';
import type {
  Alert,
  AlertHistoryEntry,
  AlertTestResult,
  ApiToken,
  AuditEntry,
  Channel,
  ChannelConfig,
  ChannelKind,
  Completion,
  Connection,
  ConnectionInput,
  Dashboard,
  DashboardDoc,
  DashboardTemplate,
  EventContext,
  Facet,
  FieldInfo,
  FieldMapping,
  HistogramResponse,
  ImportJob,
  IndexedField,
  LevelRule,
  LogEvent,
  LoginResponse,
  Me,
  MetricName,
  ProductInfo,
  PurgeJob,
  PurgeRequest,
  QueryRequest,
  QueryResponse,
  RemoteImportRequest,
  RetentionSettings,
  Role,
  RuleDoc,
  Settings,
  Signal,
  Snippet,
  Source,
  Stream,
  StreamsResponse,
  SystemReport,
  TokenScope,
  TraceResponse,
  TraceSummary,
  UploadImportRequest,
  User,
  ValidateResponse,
} from './types';

const enc = encodeURIComponent;
/** Uploads of up to 16 × 1 GiB need far longer than the default request timeout. */
const UPLOAD_TIMEOUT_MS = 3_600_000;

// ---- §11.2 auth & users ---------------------------------------------------------------------
export const auth = {
  me: (api: ApiClient) => api.get<Me>('/auth/me', { skipAuthRedirect: true }),
  login: (api: ApiClient, username: string, password: string) =>
    api.post<LoginResponse>('/auth/login', { username, password }, { skipAuthRedirect: true }),
  logout: (api: ApiClient) => api.post<undefined>('/auth/logout', undefined, { skipAuthRedirect: true }),
  changePassword: (api: ApiClient, currentPassword: string, newPassword: string) =>
    api.post<undefined>('/auth/password', { currentPassword, newPassword }),
};

export interface NewUser {
  username: string;
  displayName: string;
  password: string;
  role: Role;
}

export interface UserPatch {
  displayName?: string;
  role?: Role;
  disabled?: boolean;
  password?: string;
}

export const users = {
  list: (api: ApiClient) => api.get<User[]>('/users'),
  create: (api: ApiClient, u: NewUser) => api.post<User>('/users', u),
  update: (api: ApiClient, id: string, patch: UserPatch) => api.patch<User>(`/users/${enc(id)}`, patch),
  remove: (api: ApiClient, id: string) => api.delete(`/users/${enc(id)}`),
};

export const tokens = {
  list: (api: ApiClient) => api.get<ApiToken[]>('/tokens'),
  create: (api: ApiClient, name: string, scope: TokenScope, expiresAt: string | null) =>
    api.post<ApiToken>('/tokens', { name, scope, expiresAt }),
  remove: (api: ApiClient, id: string) => api.delete(`/tokens/${enc(id)}`),
};

// ---- §11.3 search ---------------------------------------------------------------------------
export interface RangeParams {
  from: string;
  to: string;
}

export const search = {
  query: (api: ApiClient, req: QueryRequest, signal?: AbortSignal) =>
    api.post<QueryResponse>('/query', req, { signal, timeoutMs: 120_000 }),
  validate: (api: ApiClient, q: string, signal?: AbortSignal) =>
    api.post<ValidateResponse>('/query/validate', { q }, { signal }),
  histogram: (
    api: ApiClient,
    p: RangeParams & { q: string; buckets?: number; signals?: readonly string[] },
    signal?: AbortSignal,
  ) => api.get<HistogramResponse>('/histogram', { query: { ...p }, signal }),
  facets: (
    api: ApiClient,
    p: RangeParams & { q: string; fields: readonly string[]; top?: number; signals?: readonly string[] },
    signal?: AbortSignal,
  ) => api.get<Facet[]>('/facets', { query: { ...p }, signal }),
  fields: (api: ApiClient, p: RangeParams & { source: Source }, signal?: AbortSignal) =>
    api.get<FieldInfo[]>('/fields', { query: { ...p }, signal }),
  event: (api: ApiClient, id: string) => api.get<LogEvent>(`/events/${enc(id)}`),
  context: (api: ApiClient, id: string, before = 5, after = 5) =>
    api.get<EventContext>(`/events/${enc(id)}/context`, { query: { before, after } }),
  traces: (api: ApiClient, p: RangeParams & { q: string; limit?: number }, signal?: AbortSignal) =>
    api.get<TraceSummary[]>('/traces', { query: { ...p }, signal }),
  trace: (api: ApiClient, traceId: string) => api.get<TraceResponse>(`/traces/${enc(traceId)}`),
  metricNames: (api: ApiClient, q: string, signal?: AbortSignal) =>
    api.get<MetricName[]>('/metrics/names', { query: { q }, signal }),
  complete: (api: ApiClient, q: string, pos: number, source: Source, signal?: AbortSignal) =>
    api.get<Completion>('/tql/complete', { query: { q, pos, source }, signal }),
  exportUrl: (
    api: ApiClient,
    p: RangeParams & { q: string; format: 'ndjson' | 'csv'; signals?: readonly string[] },
  ) => api.url('/export', { ...p }),
};

// ---- §11.4 signals, dashboards, alerts, channels -------------------------------------------
export interface SignalInput {
  name: string;
  tql: string;
  color: string;
  shared: boolean;
  source: Source;
}

export const signals = {
  list: (api: ApiClient) => api.get<Signal[]>('/signals'),
  create: (api: ApiClient, s: SignalInput) => api.post<Signal>('/signals', s),
  update: (api: ApiClient, id: string, s: SignalInput & { updatedAt: string }) =>
    api.put<Signal>(`/signals/${enc(id)}`, s),
  remove: (api: ApiClient, id: string) => api.delete(`/signals/${enc(id)}`),
};

export interface DashboardInput {
  name: string;
  description?: string;
  shared?: boolean;
  doc: DashboardDoc;
}

export const dashboards = {
  list: (api: ApiClient) => api.get<Dashboard[]>('/dashboards'),
  get: (api: ApiClient, id: string) => api.get<Dashboard>(`/dashboards/${enc(id)}`),
  create: (api: ApiClient, d: DashboardInput) => api.post<Dashboard>('/dashboards', d),
  update: (api: ApiClient, id: string, d: DashboardInput & { version: number }) =>
    api.put<Dashboard>(`/dashboards/${enc(id)}`, d),
  remove: (api: ApiClient, id: string) => api.delete(`/dashboards/${enc(id)}`),
  templates: (api: ApiClient, service?: string) =>
    api.get<DashboardTemplate[]>('/dashboards/templates', { query: { service } }),
  importDoc: (api: ApiClient, d: DashboardInput) => api.post<Dashboard>('/dashboards/import', d),
  exportUrl: (api: ApiClient, id: string) => api.url(`/dashboards/${enc(id)}/export`),
  duplicate: (api: ApiClient, id: string, name?: string) =>
    api.post<Dashboard>(`/dashboards/${enc(id)}/duplicate`, name === undefined ? {} : { name }),
  resolvePanel: (api: ApiClient, id: string, panelId: string, vars: Record<string, string[]>) =>
    api.post<{ q: string }>(`/dashboards/${enc(id)}/panels/${enc(panelId)}/query`, { vars }),
};

export interface AlertInput {
  name: string;
  enabled: boolean;
  doc: RuleDoc;
}

export const alerts = {
  list: (api: ApiClient) => api.get<Alert[]>('/alerts'),
  get: (api: ApiClient, id: string) => api.get<Alert>(`/alerts/${enc(id)}`),
  create: (api: ApiClient, a: AlertInput) => api.post<Alert>('/alerts', a),
  update: (api: ApiClient, id: string, a: AlertInput & { version: number }) =>
    api.put<Alert>(`/alerts/${enc(id)}`, a),
  remove: (api: ApiClient, id: string) => api.delete(`/alerts/${enc(id)}`),
  test: (api: ApiClient, id: string) => api.post<AlertTestResult>(`/alerts/${enc(id)}/test`),
  mute: (api: ApiClient, id: string, until: string | null) =>
    api.post<Alert>(`/alerts/${enc(id)}/mute`, { until }),
  history: (api: ApiClient, id: string, limit = 20) =>
    api.get<AlertHistoryEntry[]>(`/alerts/${enc(id)}/history`, { query: { limit } }),
};

export interface ChannelInput {
  name: string;
  kind: ChannelKind;
  config: ChannelConfig;
}

export const channels = {
  list: (api: ApiClient) => api.get<Channel[]>('/channels'),
  create: (api: ApiClient, c: ChannelInput) => api.post<Channel>('/channels', c),
  update: (api: ApiClient, id: string, c: ChannelInput & { version: number }) =>
    api.put<Channel>(`/channels/${enc(id)}`, c),
  remove: (api: ApiClient, id: string) => api.delete(`/channels/${enc(id)}`),
  test: (api: ApiClient, id: string) =>
    api.post<{ ok: boolean; error?: string }>(`/channels/${enc(id)}/test`),
};

// ---- §11.5 connections, streams, admin ------------------------------------------------------
export const connections = {
  list: (api: ApiClient) => api.get<Connection[]>('/connections'),
  create: (api: ApiClient, c: ConnectionInput) => api.post<Connection>('/connections', c),
  update: (api: ApiClient, id: string, c: ConnectionInput) =>
    api.patch<Connection>(`/connections/${enc(id)}`, c),
  remove: (api: ApiClient, id: string) => api.delete(`/connections/${enc(id)}`),
  rotate: (api: ApiClient, id: string, graceMinutes: number) =>
    api.post<Connection>(`/connections/${enc(id)}/rotate`, { graceMinutes }),
  revoke: (api: ApiClient, id: string) => api.post<Connection>(`/connections/${enc(id)}/revoke`),
  snippets: (api: ApiClient, id: string) => api.get<Snippet[]>(`/connections/${enc(id)}/snippets`),
};

export interface StreamInput {
  name?: string;
  description?: string;
  retentionDays?: number | null;
  spansRetentionDays?: number | null;
  metricsRetentionDays?: number | null;
  levelRules?: LevelRule[];
  paused?: boolean;
}

export const streams = {
  list: (api: ApiClient) => api.get<StreamsResponse>('/streams'),
  create: (api: ApiClient, s: StreamInput & { name: string }) => api.post<Stream>('/streams', s),
  update: (api: ApiClient, name: string, s: StreamInput) => api.patch<Stream>(`/streams/${enc(name)}`, s),
  remove: (api: ApiClient, name: string) => api.delete(`/streams/${enc(name)}`, { confirm: name }),
  fields: (api: ApiClient, name: string) => api.get<FieldMapping[]>(`/streams/${enc(name)}/fields`),
  indexedFields: (api: ApiClient) => api.get<IndexedField[]>('/indexed-fields'),
  addIndexedField: (api: ApiClient, source: Source, key: string) =>
    api.post<IndexedField>('/indexed-fields', { source, key }),
  removeIndexedField: (api: ApiClient, id: string) => api.delete(`/indexed-fields/${enc(id)}`),
  purge: (api: ApiClient, req: PurgeRequest) =>
    api.post<{ dryRun: true; rowsEstimate: number } | PurgeJob>('/purge', req),
  purgeStatus: (api: ApiClient, id: string) => api.get<PurgeJob>(`/purge/${enc(id)}`),
};

export const admin = {
  settings: (api: ApiClient) => api.get<Settings>('/settings'),
  patchSettings: (api: ApiClient, patch: { retention?: RetentionSettings; ui?: Record<string, unknown> }) =>
    api.patch<Settings>('/settings', patch),
  audit: (
    api: ApiClient,
    p: { from?: string; to?: string; actor?: string; action?: string; limit?: number },
  ) => api.get<AuditEntry[]>('/audit', { query: { ...p } }),
  system: (api: ApiClient) => api.get<SystemReport>('/system'),
  product: (api: ApiClient) => api.get<ProductInfo>('/api', { absolute: true, skipAuthRedirect: true }),
};

// ---- §11.5 imports (§9.9) --------------------------------------------------------------------
export const imports = {
  list: (api: ApiClient) => api.get<ImportJob[]>('/imports'),
  create: (api: ApiClient, req: RemoteImportRequest) => api.post<ImportJob>('/imports', req),
  /** Multipart upload: the `job` part first, then one `file` part per file (§11.5). */
  upload: (api: ApiClient, job: UploadImportRequest, files: readonly File[]) => {
    const form = new FormData();
    form.append('job', JSON.stringify(job));
    for (const f of files) form.append('file', f, f.name);
    return api.request<ImportJob>('POST', '/imports', { rawBody: form, timeoutMs: UPLOAD_TIMEOUT_MS });
  },
  cancel: (api: ApiClient, id: string) => api.post<ImportJob>(`/imports/${enc(id)}/cancel`),
};
