// SPDX-License-Identifier: Apache-2.0
//
// DTOs of the /api/v1 contract (SPEC §11.2–§11.5). Field names mirror the Go views in
// internal/api; `T | null` marks values the server may send as null.

export type Role = 'admin' | 'editor' | 'viewer';
export type Source = 'logs' | 'spans' | 'metrics';
export type LevelName = 'Verbose' | 'Debug' | 'Information' | 'Warning' | 'Error' | 'Fatal';
export const LEVELS: readonly LevelName[] = ['Verbose', 'Debug', 'Information', 'Warning', 'Error', 'Fatal'];

// ---- §11.2 auth & users --------------------------------------------------------------------
/** A team as referenced from a user (`{id, name}`). */
export interface TeamRef {
  id: string;
  name: string;
}

/** How the caller's stream scope was resolved (SPEC §15.10). */
export type StreamAccessMode = 'admin' | 'teams' | 'default-all' | 'default-none';

export interface Me {
  id: string;
  username: string;
  displayName: string;
  role: Role;
  mustChangePassword: boolean;
  defaultCredentials: boolean;
  /** True when the caller sees every stream; `streams` is then `[]`. */
  allStreams: boolean;
  /** The visible stream names when not `allStreams`. */
  streams: string[];
  teams: TeamRef[];
  streamAccess: StreamAccessMode;
}

export interface User {
  id: string;
  username: string;
  displayName: string;
  role: Role;
  mustChangePassword: boolean;
  disabled: boolean;
  lastLoginAt: string | null;
  teams: TeamRef[];
  createdAt: string;
  updatedAt: string;
}

export interface LoginResponse {
  user: User;
  mustChangePassword: boolean;
}

export type TokenScope = 'read' | 'write' | 'admin';
export interface ApiToken {
  id: string;
  name: string;
  keyPrefix: string;
  scope: TokenScope;
  expiresAt: string | null;
  revokedAt: string | null;
  lastUsedAt: string | null;
  createdAt: string;
  token?: string;
}

// ---- §11.3 search ----------------------------------------------------------------------------
export type ResultKind = 'events' | 'table' | 'timeseries';
export type ColumnType = 'time' | 'string' | 'number' | 'bool' | 'json' | 'map' | 'array' | 'duration_ms';

export interface Column {
  name: string;
  type: ColumnType;
}

export interface LogEvent {
  id: string;
  timestamp: string;
  ingestedAt?: string;
  stream: string;
  level: LevelName;
  service: string;
  environment: string;
  host: string;
  messageTemplate: string;
  message: string;
  exception: string;
  eventType: string;
  traceId: string;
  spanId: string;
  protocol: string;
  connectionId: string;
  properties: Record<string, unknown>;
}

export interface QueryStats {
  elapsedMs: number;
  rowsRead: number;
  bytesRead: number;
  interpretedAsText: boolean;
}

export interface Series {
  key: Record<string, unknown>;
  points: (number | null)[][];
}

export interface QueryResponse {
  kind: ResultKind;
  events?: (LogEvent | Record<string, unknown>)[];
  nextCursor?: string;
  span?: number;
  columns?: Column[];
  rows?: unknown[][];
  series?: Series[];
  stats: QueryStats;
}

export interface QueryRequest {
  q: string;
  from: string;
  to: string;
  limit?: number;
  cursor?: string;
  span?: string;
  signals?: string[];
  /** Selection within the caller's scope (SPEC §11.3); absent = the whole scope. */
  streams?: string[];
  /** Logs only. */
  environments?: string[];
}

/** The stream / environment selection of the GET search routes (`streams=a,b&env=x`). */
export interface ScopeParams {
  streams?: readonly string[];
  env?: readonly string[];
}

export interface ValidateResponse {
  ok: boolean;
  kind?: ResultKind;
  interpretedAsText: boolean;
  error?: { message: string; pos: number; len: number; hint?: string };
}

export interface HistogramBucket {
  t: string;
  counts: Record<string, number>;
}
export interface HistogramResponse {
  span: number;
  buckets: HistogramBucket[];
}

export interface Facet {
  field: string;
  values: { value: string; count: number }[];
  otherCount: number;
  sampled: boolean;
}

export interface FieldInfo {
  name: string;
  builtin: boolean;
  numeric: boolean;
  hits: number;
}

export interface EventContext {
  before: LogEvent[];
  event: LogEvent;
  after: LogEvent[];
}

export interface TraceSummary {
  traceId: string;
  rootService: string;
  rootName: string;
  start: string;
  durationMs: number;
  spanCount: number;
  errorCount: number;
  services: string[];
}

export interface SpanEvent {
  time: string;
  name: string;
  attributes?: Record<string, unknown>;
}

export interface SpanLink {
  traceId: string;
  spanId: string;
  traceState?: string;
  attributes?: Record<string, unknown>;
}

export interface Span {
  traceId: string;
  spanId: string;
  parentSpanId: string;
  stream: string;
  service: string;
  name: string;
  kind: string;
  start: string;
  durationMs: number;
  statusCode: string;
  statusMessage: string;
  attributes: Record<string, unknown>;
  resourceAttributes: Record<string, unknown>;
  events: SpanEvent[];
  links: SpanLink[];
}

export interface TraceResponse {
  spans: Span[];
  events: LogEvent[];
}

export interface MetricName {
  name: string;
  kind: string;
  unit: string;
  description: string;
}

export type SuggestionKind = 'field' | 'value' | 'operator' | 'keyword' | 'stage' | 'function';
export interface Suggestion {
  kind: SuggestionKind;
  label: string;
  insertText: string;
  detail?: string;
}
export interface Completion {
  from: number;
  to: number;
  field?: string;
  suggestions: Suggestion[];
}

// ---- §11.4 signals, dashboards, alerts, channels ---------------------------------------------
export interface Signal {
  id: string;
  name: string;
  tql: string;
  color: string;
  ownerId: string;
  shared: boolean;
  source: Source;
  createdAt: string;
  updatedAt: string;
}

export type PanelType =
  'timeseries' | 'bar' | 'stat' | 'gauge' | 'table' | 'pie' | 'logs' | 'traces' | 'markdown';
export type ThresholdColor =
  | 'accent'
  | 'success'
  | 'warning'
  | 'danger'
  | 'info'
  | 'chart1'
  | 'chart2'
  | 'chart3'
  | 'chart4'
  | 'chart5'
  | 'chart6'
  | 'chart7'
  | 'chart8';

export interface PanelOptions {
  stack?: boolean;
  style?: 'line' | 'area' | 'bars';
  unit?: string;
  decimals?: number;
  legend?: 'bottom' | 'right' | 'hidden';
  thresholds?: { value: number; color: ThresholdColor }[];
  content?: string;
}

export interface Panel {
  id: string;
  title: string;
  type: PanelType;
  grid: { x: number; y: number; w: number; h: number };
  query?: string;
  options?: PanelOptions;
}

export interface Variable {
  name: string;
  label?: string;
  type: 'field-values' | 'custom' | 'text';
  source?: Source;
  field?: string;
  options?: string[];
  multi?: boolean;
  default?: string[];
}

export interface DashboardDoc {
  version: 1;
  timeRange?: { from: string; to: string };
  refresh?: string;
  variables?: Variable[];
  panels: Panel[];
}

export interface Dashboard {
  id: string;
  name: string;
  description: string;
  ownerId: string;
  shared: boolean;
  version: number;
  doc?: DashboardDoc;
  createdAt: string;
  updatedAt: string;
}

export interface DashboardTemplate {
  id: string;
  name: string;
  description: string;
  doc: DashboardDoc;
}

export type ConditionOp = '>' | '>=' | '<' | '<=' | '==' | '!=';
export type Severity = 'info' | 'warning' | 'critical';
export type NoDataPolicy = 'ok' | 'keep' | 'alerting';
export type AlertState = 'ok' | 'pending' | 'firing' | 'error';

export interface RuleDoc {
  description: string;
  query: string;
  condition: { op: ConditionOp; threshold: number };
  window: string;
  every: string;
  for: string;
  severity: Severity;
  channels: string[];
  noData: NoDataPolicy;
  groupBy: string[];
  muteUntil: string | null;
}

export interface AlertGroup {
  labels: Record<string, string>;
  state: string;
  since: string;
  value: number | null;
}

export interface Alert {
  id: string;
  name: string;
  enabled: boolean;
  doc: RuleDoc;
  /** Streams the rule reads; `null` = every stream (SPEC §14.1, §15.10). */
  streams: string[] | null;
  state: AlertState;
  stateSince: string | null;
  lastEvalAt: string | null;
  lastValue: number | null;
  lastError: string;
  groups: AlertGroup[];
  version: number;
  createdAt: string;
  updatedAt: string;
}

export interface AlertTestResult {
  state: string;
  error?: string;
  samples: { labels: Record<string, string>; value: number | null }[];
  groups: AlertGroup[];
  transitions: { labels: Record<string, string>; from: string; to: string; message: string }[];
}

export interface AlertHistoryEntry {
  id: string;
  at: string;
  fromState: string;
  toState: string;
  value: number | null;
  message: string;
  group: Record<string, string>;
}

export type ChannelKind = 'webhook' | 'slack' | 'teams' | 'email';
export interface ChannelConfig {
  url?: string;
  secret?: string;
  headers?: Record<string, string>;
  to?: string[];
}
export interface Channel {
  id: string;
  name: string;
  kind: ChannelKind;
  config: ChannelConfig;
  version: number;
  createdAt: string;
  updatedAt: string;
}

// ---- §11.5 connections, streams, admin -------------------------------------------------------
export type Protocol = 'seq' | 'elastic' | 'otlp' | 'native';
export const PROTOCOLS: readonly Protocol[] = ['seq', 'otlp', 'elastic', 'native'];

export interface Connection {
  id: string;
  name: string;
  description: string;
  keyPrefix: string;
  protocols: Protocol[];
  defaultStream: string;
  allowedCidrs: string[];
  /** Streams the key may write to; `[]` = any (SPEC §9.1). */
  allowedStreams: string[];
  minimumLevel: LevelName | null;
  appliedProperties: Record<string, unknown>;
  rateLimitEps: number;
  expiresAt: string | null;
  revokedAt: string | null;
  rotationGraceUntil: string | null;
  lastUsedAt: string | null;
  lastUsedIp: string;
  eventsReceived: number;
  createdAt: string;
  updatedAt: string;
  key?: string;
}

export interface ConnectionInput {
  name?: string;
  description?: string;
  protocols?: Protocol[];
  defaultStream?: string;
  allowedCidrs?: string[];
  allowedStreams?: string[];
  minimumLevel?: string;
  rateLimitEps?: number;
  expiresAt?: string | null;
}

export interface Snippet {
  id: string;
  title: string;
  language: string;
  content: string;
}

export interface SourceStats {
  rows: number;
  oldest: string;
  newest: string;
  ratePer5Min: number;
  ratePer24h: number;
  compressedBytes: number;
  uncompressedBytes: number;
  compressionRatio: number;
  estimated: boolean;
  topServices: string[];
}

export interface LevelRule {
  levels: string[];
  days: number;
}

export interface Stream {
  id: string;
  name: string;
  description: string;
  retentionDays: number | null;
  spansRetentionDays: number | null;
  metricsRetentionDays: number | null;
  levelRules: LevelRule[];
  paused: boolean;
  createdAt: string;
  updatedAt: string;
  stats?: Partial<Record<Source, SourceStats>>;
}

export interface StreamsResponse {
  streams: Stream[];
  statsUpdatedAt: string | null;
}

export interface FieldMapping {
  source: Source;
  key: string;
  type: 'string' | 'number' | 'mixed';
  hits: number;
  firstSeen: string;
  lastSeen: string;
  indexed: boolean;
}

export interface IndexedField {
  id: string;
  source: Source;
  key: string;
  column: string;
  createdAt: string;
}

export interface PurgeRequest {
  stream?: string;
  source: Source;
  q?: string;
  from: string;
  to: string;
  dryRun: boolean;
}

export interface PurgeJob {
  id: string;
  stream: string;
  source: string;
  q: string;
  from: string;
  to: string;
  status: 'pending' | 'running' | 'done' | 'failed';
  rowsEstimate: number;
  requestedBy: string;
  droppedPartitions: string;
  createdAt: string;
  updatedAt: string;
}

export interface RetentionSettings {
  logsDays: number;
  spansDays: number;
  metricsDays: number;
}

export interface AccessSettings {
  /** Users in no team see nothing (true) or every stream (false, the default). */
  denyByDefault: boolean;
}

export interface Settings {
  retention: RetentionSettings;
  access: AccessSettings;
  ui: Record<string, unknown>;
}

// ---- §11.5 teams (§15.10) ---------------------------------------------------------------------
export interface TeamSummary {
  id: string;
  name: string;
  description: string;
  memberCount: number;
  streams: string[];
  createdAt: string;
  updatedAt: string;
}

export interface TeamMember {
  id: string;
  username: string;
  displayName: string;
  role: Role;
}

export interface Team {
  id: string;
  name: string;
  description: string;
  members: TeamMember[];
  streams: string[];
  createdAt: string;
  updatedAt: string;
}

/** `GET /teams/access`: the deny-by-default setting and how many users it would affect. */
export interface TeamsAccess {
  denyByDefault: boolean;
  usersWithoutTeam: number;
}

/** Stream names (SPEC §8.1). */
export const STREAM_NAME_RE = /^[a-z0-9][a-z0-9._-]{0,63}$/;
export const isStreamName = (s: string): boolean => STREAM_NAME_RE.test(s);

export interface AuditEntry {
  id: string;
  at: string;
  actor: string;
  ip: string;
  action: string;
  target: string;
  details: unknown;
}

export interface SystemReport {
  version: string;
  startedAt: string;
  uptimeSeconds: number;
  queueDepth: Record<string, number>;
  counters: Record<string, number>;
  streams: number;
  clickhouse?: { version: string; disks: { name: string; freeBytes: number; totalBytes: number }[] };
  clickhouseError?: string;
}

// ---- §11.5 updates (§17.7) ------------------------------------------------------------------
export type InstallKind = 'systemd' | 'windows-service' | 'container' | 'manual' | 'dev';

/** Why one-click install is not available (`apply.reason`, and `details.reason` of a 409). */
export type UpdateApplyReason =
  'disabled' | 'dev' | 'container' | 'ha' | 'not-a-service' | 'helper-missing' | 'unsupported-platform';

/** Why `POST /system/update/apply` was refused now (`details.reason` of a 409). */
export type UpdateRefusal = UpdateApplyReason | 'draining' | 'wal-failed' | 'in-progress';

/** Code of the last failed release check. */
export type UpdateErrorCode = 'unreachable' | 'rate_limited' | 'invalid_response' | 'not_found';

/** `idle`, or the step of the update being applied. */
export type UpdateState = 'idle' | 'downloading' | 'verifying' | 'staged' | 'installing' | 'restarting';

/** Archive download progress in bytes, while `state` is `downloading`. */
export interface UpdateProgress {
  bytes: number;
  total: number;
}

/** The outcome of the last update attempt. */
export interface UpdateResult {
  phase: 'installed' | 'rolled_back' | 'failed';
  from?: string;
  to: string;
  at: string;
  message?: string;
}

export interface UpdateRelease {
  version: string;
  prerelease: boolean;
  publishedAt: string;
  notesUrl: string;
}

/** `GET /system/update`: the update status of this node. */
export interface UpdateStatus {
  enabled: boolean;
  current: string;
  nodeId: string;
  installKind: InstallKind;
  apply: { supported: boolean; reason?: UpdateApplyReason };
  /** Only while a newer release is known. */
  latest?: UpdateRelease;
  available: boolean;
  /** Time of the last attempt, successful or not. */
  checkedAt?: string;
  error?: UpdateErrorCode;
  state: UpdateState;
  progress?: UpdateProgress;
  lastResult?: UpdateResult;
}

// ---- §11.5 imports (§9.9) ----------------------------------------------------------------------
export type ImportKind = 'elastic' | 'seq' | 'clef-file' | 'kibana';
export type ImportStatus = 'queued' | 'running' | 'completed' | 'failed' | 'cancelled';
export type ElasticAuthType = 'none' | 'basic' | 'apiKey' | 'bearer';

/** Elasticsearch / OpenSearch source (§9.9). Secrets are accepted on create, never returned. */
export interface ElasticConfig {
  url: string;
  auth: { type: ElasticAuthType; username?: string; password?: string; apiKey?: string; token?: string };
  caCert?: string;
  index: string;
  timeField?: string;
  from?: string;
  to?: string;
  query?: Record<string, unknown>;
  stream?: string;
  pageSize?: number;
}

export interface SeqConfig {
  url: string;
  apiKey?: string;
  from?: string;
  to?: string;
  filter?: string;
  stream?: string;
  pageSize?: number;
  importEvents?: boolean;
  importSignals?: boolean;
}

export interface ImportedFile {
  name: string;
  size: number;
}

export interface ClefConfig {
  stream?: string;
  files?: ImportedFile[];
  node?: string;
}

export interface KibanaConfig {
  file?: ImportedFile;
}

/** §11.5 ImportJob: `config` comes back without secrets and server-side paths. */
export interface ImportJob {
  id: string;
  kind: ImportKind;
  name: string;
  status: ImportStatus;
  config: ElasticConfig | SeqConfig | ClefConfig | KibanaConfig;
  rowsImported: number;
  /** 0 = unknown. */
  rowsTotal: number;
  errors: number;
  lastError: string;
  warnings: string[];
  cancelRequested: boolean;
  /** `user:<id>` or `system:cli`. */
  requestedBy: string;
  startedAt?: string | null;
  finishedAt?: string | null;
  createdAt: string;
  updatedAt: string;
}

/** POST /imports with a JSON body. */
export type RemoteImportRequest =
  | { kind: 'elastic'; name?: string; config: ElasticConfig }
  | { kind: 'seq'; name?: string; config: SeqConfig };

/** The `job` part of a multipart POST /imports. */
export type UploadImportRequest =
  | { kind: 'clef-file'; name?: string; config: { stream?: string } }
  | { kind: 'kibana'; name?: string; config: Record<string, never> };

/** GET /api (Seq-compatible probe, public): product and version. */
export interface ProductInfo {
  Product: string;
  Version: string;
}
