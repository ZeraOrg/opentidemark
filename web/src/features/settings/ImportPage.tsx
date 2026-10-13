// SPDX-License-Identifier: Apache-2.0
//
// Settings → Import (SPEC §9.9, §11.5 imports): import jobs with progress, and new imports from
// Elasticsearch / OpenSearch, Seq (events and signals), Kibana saved objects and CLEF / NDJSON
// files. Warns when the imported range is older than the target stream's retention.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Upload } from 'lucide-react';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { errorMessage, isApiError } from '../../api/client';
import { admin, imports, streams as streamsApi } from '../../api/endpoints';
import type { ElasticAuthType, ElasticConfig, ImportJob, ImportKind, SeqConfig } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import {
  Button,
  Checkbox,
  Field,
  Segmented,
  Select,
  Spinner,
  StatusChip,
  TextArea,
  TextInput,
} from '../../components/controls';
import { Card, EmptyState, layoutStyles as l, Notice } from '../../components/layout';
import { cx } from '../../lib/cx';
import { formatInteger } from '../../lib/format';
import { formatAge } from '../../lib/time';
import { DEFAULT_DAYS } from '../streams/retention';
import s from './settings.module.css';

type Kind = ImportKind;
const POLL_MS = 3_000;
const GIB = 1024 ** 3;
const MAX_CLEF_FILES = 16;
const MAX_CLEF_BYTES = GIB;
const MAX_KIBANA_BYTES = 64 * 1024 ** 2;
const DAY_MS = 86_400_000;
const AUTH_TYPES: { value: ElasticAuthType; label: string }[] = [
  { value: 'none', label: 'none' },
  { value: 'basic', label: 'basic' },
  { value: 'apiKey', label: 'API key' },
  { value: 'bearer', label: 'bearer token' },
];

const STATUS = {
  queued: 'pending',
  running: 'info',
  completed: 'ok',
  failed: 'danger',
  cancelled: 'muted',
} as const;

/** Days of data older than the retention would be deleted right after the import. */
export function retentionWarning(fromIso: string, retentionDays: number, now: number): boolean {
  const from = Date.parse(fromIso);
  return Number.isFinite(from) && from < now - retentionDays * DAY_MS;
}

/** Where a job reads from: URL (+ index) for remote sources, file names for uploads. */
export function jobSource(job: ImportJob): string {
  const c = job.config;
  if ('url' in c) return 'index' in c ? `${c.url} · ${c.index}` : c.url;
  if ('files' in c && c.files !== undefined) return c.files.map((f) => f.name).join(', ');
  if ('file' in c && c.file !== undefined) return c.file.name;
  return '—';
}

/** Target stream of a job ('' = automatic). */
export function jobStream(job: ImportJob): string {
  const c = job.config;
  return 'stream' in c && typeof c.stream === 'string' ? c.stream : '';
}

/** Share of rows done (0–1), or null when the total is unknown. */
export function jobProgress(job: ImportJob): number | null {
  if (job.rowsTotal <= 0) return null;
  return Math.min(1, job.rowsImported / job.rowsTotal);
}

/** Problem with the files chosen for an upload, or null. */
export function uploadProblem(kind: 'clef-file' | 'kibana', files: readonly File[]): string | null {
  if (files.length === 0) return 'Choose a file.';
  if (kind === 'kibana') {
    if (files.length > 1) return 'Choose exactly one Kibana export.';
    return (files[0]?.size ?? 0) > MAX_KIBANA_BYTES ? 'Kibana exports are limited to 64 MiB.' : null;
  }
  if (files.length > MAX_CLEF_FILES) return `Choose at most ${MAX_CLEF_FILES} files.`;
  return files.some((f) => f.size > MAX_CLEF_BYTES) ? 'Files are limited to 1 GiB each.' : null;
}

/** Parses the optional Query DSL: '' → undefined, otherwise a JSON object or an error message. */
export function parseQueryDsl(text: string): { query?: Record<string, unknown>; error?: string } {
  if (text.trim() === '') return {};
  try {
    const v: unknown = JSON.parse(text);
    if (typeof v !== 'object' || v === null || Array.isArray(v) || Object.keys(v).length === 0)
      return { error: 'The query must be a non-empty JSON object.' };
    return { query: v as Record<string, unknown> };
  } catch {
    return { error: 'The query is not valid JSON.' };
  }
}

function JobRow({ job }: { job: ImportJob }) {
  const api = useApi();
  const qc = useQueryClient();
  const { now } = useServices();
  const cancel = useMutation({
    mutationFn: () => imports.cancel(api, job.id),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['imports'] }),
  });
  const active = job.status === 'queued' || job.status === 'running';
  const progress = jobProgress(job);
  const stream = jobStream(job);
  return (
    <tr>
      <td>
        <div>{job.name}</div>
        <div className={l.subtle}>{job.kind}</div>
      </td>
      <td className={cx(l.muted, s.importSource)} title={jobSource(job)}>
        {jobSource(job)}
      </td>
      <td className={l.monoCell}>{stream === '' ? 'automatic' : stream}</td>
      <td>
        <StatusChip variant={STATUS[job.status]}>{job.status}</StatusChip>
        {progress !== null && active ? (
          <span
            className={s.progress}
            role="progressbar"
            aria-label={`${job.name} progress`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(progress * 100)}
          >
            <span style={{ width: `${Math.round(progress * 100)}%` }} />
          </span>
        ) : null}
      </td>
      <td className={l.num}>
        {formatInteger(job.rowsImported)}
        {job.rowsTotal > 0 ? <span className={l.subtle}> / {formatInteger(job.rowsTotal)}</span> : null}
      </td>
      <td
        className={cx(l.num, job.errors > 0 && s.dangerText)}
        title={[job.lastError, ...job.warnings].filter((x) => x !== '').join('\n')}
      >
        {formatInteger(job.errors)}
        {job.warnings.length > 0 ? <span className={l.subtle}> · {job.warnings.length} warn</span> : null}
      </td>
      <td className={l.subtle}>{formatAge(Date.parse(job.startedAt ?? job.createdAt), now())}</td>
      <td className={s.rowActions}>
        {active ? (
          <Button
            size="sm"
            variant="ghost"
            busy={cancel.isPending}
            disabled={job.cancelRequested}
            onClick={() => cancel.mutate()}
          >
            {job.cancelRequested ? 'Cancelling…' : 'Cancel'}
          </Button>
        ) : null}
      </td>
    </tr>
  );
}

function NewImport() {
  const api = useApi();
  const qc = useQueryClient();
  const { now } = useServices();
  const [kind, setKind] = useState<Kind>('elastic');
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const [authType, setAuthType] = useState<ElasticAuthType>('none');
  const [user, setUser] = useState('');
  const [secret, setSecret] = useState('');
  const [index, setIndex] = useState('logs-*');
  const [timeField, setTimeField] = useState('@timestamp');
  const [query, setQuery] = useState('');
  const [filter, setFilter] = useState('');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [stream, setStream] = useState('');
  const [importEvents, setImportEvents] = useState(true);
  const [importSignals, setImportSignals] = useState(false);
  const [files, setFiles] = useState<File[]>([]);
  const streams = useQuery({ queryKey: ['streams'], queryFn: () => streamsApi.list(api) });
  const settings = useQuery({ queryKey: ['settings'], queryFn: () => admin.settings(api) });
  const target = streams.data?.streams.find((x) => x.name === stream);
  const retention = target?.retentionDays ?? settings.data?.retention.logsDays ?? DEFAULT_DAYS.logs;
  const range = {
    ...(from !== '' ? { from: new Date(`${from}T00:00:00Z`).toISOString() } : {}),
    ...(to !== '' ? { to: new Date(`${to}T00:00:00Z`).toISOString() } : {}),
  };
  const common = {
    ...(name.trim() === '' ? {} : { name: name.trim() }),
  };
  const streamPart = stream === '' ? {} : { stream };
  const dsl = parseQueryDsl(query);
  const upload = kind === 'kibana' || kind === 'clef-file' ? kind : null;
  const fileProblem = upload === null ? null : uploadProblem(upload, files);
  const start = useMutation({
    mutationFn: () => {
      if (upload === 'kibana') return imports.upload(api, { kind: 'kibana', ...common, config: {} }, files);
      if (upload === 'clef-file')
        return imports.upload(api, { kind: 'clef-file', ...common, config: streamPart }, files);
      if (kind === 'seq') {
        const config: SeqConfig = {
          url,
          ...(secret === '' ? {} : { apiKey: secret }),
          ...(filter === '' ? {} : { filter }),
          importEvents,
          importSignals,
          ...streamPart,
          ...range,
        };
        return imports.create(api, { kind: 'seq', ...common, config });
      }
      const auth: ElasticConfig['auth'] =
        authType === 'basic'
          ? { type: 'basic', username: user, password: secret }
          : authType === 'apiKey'
            ? { type: 'apiKey', apiKey: secret }
            : authType === 'bearer'
              ? { type: 'bearer', token: secret }
              : { type: 'none' };
      const config: ElasticConfig = {
        url,
        auth,
        index,
        timeField,
        ...(dsl.query === undefined ? {} : { query: dsl.query }),
        ...streamPart,
        ...range,
      };
      return imports.create(api, { kind: 'elastic', ...common, config });
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['imports'] });
      setSecret('');
      setFiles([]);
    },
  });
  const remote = upload === null;
  const valid = remote
    ? /^https?:\/\//.test(url) &&
      (kind !== 'elastic' || (index !== '' && dsl.error === undefined)) &&
      (kind !== 'seq' || importEvents || importSignals)
    : fileProblem === null;
  const warn = range.from !== undefined && retentionWarning(range.from, retention, now());
  return (
    <Card title="New import" subtitle="runs on the server; resumable; never duplicates rows">
      <div className={l.stack}>
        <Segmented<Kind>
          label="Source"
          value={kind}
          onChange={(k) => {
            setKind(k);
            setFiles([]);
          }}
          options={[
            { value: 'elastic', label: 'Elasticsearch / OpenSearch' },
            { value: 'seq', label: 'Seq' },
            { value: 'kibana', label: 'Kibana saved objects' },
            { value: 'clef-file', label: 'CLEF / NDJSON files' },
          ]}
        />
        <Field label="Job name (optional)">
          {(id) => (
            <TextInput
              id={id}
              value={name}
              maxLength={128}
              placeholder={`Import ${kind}`}
              onChange={(e) => setName(e.target.value)}
            />
          )}
        </Field>
        {remote ? (
          <div className={s.importGrid}>
            <Field label="URL">
              {(id) => (
                <TextInput
                  id={id}
                  mono
                  value={url}
                  placeholder={kind === 'seq' ? 'https://seq.internal' : 'https://es.internal:9200'}
                  onChange={(e) => setUrl(e.target.value)}
                />
              )}
            </Field>
            {kind === 'elastic' ? (
              <>
                <Field label="Authentication">
                  {(id) => (
                    <Select
                      id={id}
                      options={AUTH_TYPES}
                      value={authType}
                      onChange={(e) => setAuthType(e.target.value as ElasticAuthType)}
                    />
                  )}
                </Field>
                {authType === 'basic' ? (
                  <Field label="Username">
                    {(id) => <TextInput id={id} value={user} onChange={(e) => setUser(e.target.value)} />}
                  </Field>
                ) : null}
                {authType !== 'none' ? (
                  <Field
                    label={authType === 'basic' ? 'Password' : authType === 'apiKey' ? 'API key' : 'Token'}
                  >
                    {(id) => (
                      <TextInput
                        id={id}
                        type="password"
                        value={secret}
                        onChange={(e) => setSecret(e.target.value)}
                      />
                    )}
                  </Field>
                ) : null}
                <Field label="Index">
                  {(id) => (
                    <TextInput id={id} mono value={index} onChange={(e) => setIndex(e.target.value)} />
                  )}
                </Field>
                <Field label="Time field">
                  {(id) => (
                    <TextInput
                      id={id}
                      mono
                      value={timeField}
                      onChange={(e) => setTimeField(e.target.value)}
                    />
                  )}
                </Field>
                <Field label="Query DSL (optional JSON object)" className={s.wide} error={dsl.error}>
                  {(id) => (
                    <TextArea
                      id={id}
                      mono
                      rows={3}
                      value={query}
                      onChange={(e) => setQuery(e.target.value)}
                    />
                  )}
                </Field>
              </>
            ) : (
              <>
                <Field label="API key (optional)" help="Needs read permission.">
                  {(id) => (
                    <TextInput
                      id={id}
                      type="password"
                      value={secret}
                      onChange={(e) => setSecret(e.target.value)}
                    />
                  )}
                </Field>
                <Field label="Seq filter (optional)">
                  {(id) => (
                    <TextInput id={id} mono value={filter} onChange={(e) => setFilter(e.target.value)} />
                  )}
                </Field>
                <Checkbox checked={importEvents} onChange={setImportEvents}>
                  Import events
                </Checkbox>
                <Checkbox checked={importSignals} onChange={setImportSignals}>
                  Import shared Seq signals
                </Checkbox>
              </>
            )}
            <Field label="From">
              {(id) => (
                <TextInput id={id} type="date" value={from} onChange={(e) => setFrom(e.target.value)} />
              )}
            </Field>
            <Field label="To">
              {(id) => <TextInput id={id} type="date" value={to} onChange={(e) => setTo(e.target.value)} />}
            </Field>
          </div>
        ) : (
          <label className={s.fileDrop}>
            <Upload size={18} aria-hidden="true" />
            <span>
              {files.length === 0
                ? kind === 'kibana'
                  ? 'Choose a Kibana .ndjson export (≤ 64 MiB)'
                  : 'Choose up to 16 CLEF / NDJSON files (.gz allowed, ≤ 1 GiB each)'
                : files.map((f) => f.name).join(', ')}
            </span>
            <input
              type="file"
              multiple={kind === 'clef-file'}
              accept={kind === 'kibana' ? '.ndjson' : '.json,.ndjson,.clef,.gz,.txt'}
              onChange={(e) => setFiles([...(e.target.files ?? [])])}
            />
          </label>
        )}
        {kind !== 'kibana' ? (
          <Field
            label="Target stream"
            help="Automatic = derived from the source index (Elasticsearch) or the default stream."
          >
            {(id) => (
              <Select
                id={id}
                options={[
                  { value: '', label: 'automatic' },
                  ...(streams.data?.streams ?? []).map((x) => ({ value: x.name, label: x.name })),
                ]}
                value={stream}
                onChange={(e) => setStream(e.target.value)}
              />
            )}
          </Field>
        ) : null}
        {warn ? (
          <Notice kind="warning">
            Data before {new Date(now() - retention * DAY_MS).toISOString().slice(0, 10)} is older than the{' '}
            {retention}-day retention of the target and would be deleted right away.{' '}
            {stream !== '' ? (
              <Link to={`/streams/${encodeURIComponent(stream)}`}>Extend the retention first</Link>
            ) : (
              'Choose a stream with a longer retention.'
            )}
          </Notice>
        ) : null}
        {files.length > 0 && fileProblem !== null ? <Notice kind="danger">{fileProblem}</Notice> : null}
        {start.isError ? <Notice kind="danger">{errorMessage(start.error)}</Notice> : null}
        <div>
          <Button variant="primary" disabled={!valid} busy={start.isPending} onClick={() => start.mutate()}>
            Start import
          </Button>
        </div>
      </div>
    </Card>
  );
}

export default function ImportPage() {
  const api = useApi();
  const jobs = useQuery({
    queryKey: ['imports'],
    queryFn: () => imports.list(api),
    retry: false,
    refetchInterval: (q) =>
      (q.state.data ?? []).some((j) => j.status === 'running' || j.status === 'queued') ? POLL_MS : false,
  });
  if (jobs.isError && isApiError(jobs.error) && jobs.error.status === 404) {
    return (
      <div className={s.page}>
        <EmptyState title="Import is not available" hint="This server does not offer the import API." />
      </div>
    );
  }
  return (
    <div className={s.page}>
      <div className={s.head}>
        <h1>Import</h1>
        <span className={l.subtle}>
          Move data and saved searches from Elasticsearch, OpenSearch, Kibana and Seq
        </span>
      </div>
      {jobs.isError ? <Notice kind="danger">{errorMessage(jobs.error)}</Notice> : null}
      {jobs.isPending ? (
        <Spinner />
      ) : (jobs.data ?? []).length > 0 ? (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={l.table}>
            <thead>
              <tr>
                <th>Job</th>
                <th>Source</th>
                <th>Stream</th>
                <th>Status</th>
                <th className={l.num}>Rows</th>
                <th className={l.num}>Errors</th>
                <th>Started</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {(jobs.data ?? []).map((j) => (
                <JobRow key={j.id} job={j} />
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
      <NewImport />
    </div>
  );
}
