// SPDX-License-Identifier: Apache-2.0
//
// Streams / index management (SPEC §12, §16.4, DESIGN §9.8): stats per stream and source,
// default retention, and for the selected stream retention with level rules and a TTL preview,
// field mappings with indexed fields, purge with dry run and typed confirmation, pause / resume,
// create and delete.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Database, Pause, Play, Plus, RefreshCw, Search, Trash2, X } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { admin, streams as streamsApi } from '../../api/endpoints';
import type {
  FieldMapping,
  LevelName,
  LevelRule,
  PurgeJob,
  Source,
  SourceStats,
  Stream,
} from '../../api/types';
import { LEVELS } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { hasRole, useMe } from '../../app/session';
import { useTopbarStatus } from '../../app/shellContext';
import {
  Button,
  Field,
  LevelChip,
  Select,
  Spinner,
  StatusChip,
  Tag,
  TextInput,
} from '../../components/controls';
import { CodeBlock, EmptyState, layoutStyles as l, Notice, Page, Tabs } from '../../components/layout';
import { Modal, useToast } from '../../components/overlay';
import { QueryEditor } from '../../components/QueryEditor';
import { cx } from '../../lib/cx';
import { formatBytes, formatCount, formatRatePerMin } from '../../lib/format';
import { formatAge, formatDateTime } from '../../lib/time';
import {
  DEFAULT_DAYS,
  dropsPartitions,
  estimateBytes,
  levelRuleSummary,
  MAX_INDEXED,
  MAX_LEVEL_RULES,
  purgeConfirmation,
  retentionFor,
  retentionPatchKey,
  streamRows,
  ttlPreview,
} from './retention';
import s from './streams.module.css';

type DetailTab = 'overview' | 'retention' | 'mappings' | 'purge';
const PURGE_POLL_MS = 2_000;
const STREAM_NAME = /^[a-z0-9][a-z0-9._-]{0,127}$/;

function RetentionEditor({ stream, source, defaults }: { stream: Stream; source: Source; defaults: number }) {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const initialDays = retentionFor(stream, source);
  const [days, setDays] = useState(String(initialDays ?? defaults));
  const [rules, setRules] = useState<LevelRule[]>(stream.levelRules);
  const reset = (): void => {
    setDays(String(initialDays ?? defaults));
    setRules(stream.levelRules);
  };
  useEffect(reset, [stream, source, initialDays, defaults]);
  const n = Number(days);
  const valid =
    Number.isInteger(n) &&
    n >= 1 &&
    n <= 36500 &&
    rules.every((r) => r.levels.length > 0 && r.days >= 1 && r.days <= 36500);
  const apply = useMutation({
    mutationFn: () =>
      streamsApi.update(api, stream.name, {
        [retentionPatchKey(source)]: n === defaults && initialDays === null ? null : n,
        ...(source === 'logs' ? { levelRules: rules } : {}),
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['streams'] });
      toast.push({ kind: 'success', title: `Retention of ${stream.name} applied` });
    },
  });
  const toggleLevel = (i: number, lv: LevelName): void =>
    setRules(
      rules.map((r, j) =>
        j === i
          ? { ...r, levels: r.levels.includes(lv) ? r.levels.filter((x) => x !== lv) : [...r.levels, lv] }
          : r,
      ),
    );
  return (
    <div className={s.col}>
      <div className={l.sectionLabel}>Retention</div>
      <Field
        label={`Keep ${source === 'logs' ? 'events' : source} for`}
        help={`Default for ${source} is ${defaults} days. Applied as a ClickHouse TTL clause; idempotent.`}
      >
        {(id) => (
          <div className={s.daysRow}>
            <TextInput
              id={id}
              type="number"
              min={1}
              max={36500}
              value={days}
              onChange={(e) => setDays(e.target.value)}
            />
            <span className={s.unit}>days</span>
          </div>
        )}
      </Field>
      {source === 'logs' ? (
        <>
          <div className={l.sectionLabel}>Level rules</div>
          {rules.map((r, i) => (
            <div key={i} className={s.levelRule}>
              <div className={s.levels} role="group" aria-label={`Levels of rule ${i + 1}`}>
                {LEVELS.map((lv) => (
                  <button
                    type="button"
                    key={lv}
                    aria-pressed={r.levels.includes(lv)}
                    className={s.levelToggle}
                    onClick={() => toggleLevel(i, lv)}
                  >
                    <LevelChip level={lv} />
                  </button>
                ))}
              </div>
              <TextInput
                aria-label={`Days of rule ${i + 1}`}
                type="number"
                min={1}
                value={String(r.days)}
                onChange={(e) =>
                  setRules(rules.map((x, j) => (j === i ? { ...x, days: Number(e.target.value) } : x)))
                }
              />
              <Button
                size="sm"
                variant="ghost"
                iconOnly
                icon={<X size={14} />}
                aria-label={`Remove rule ${i + 1}`}
                onClick={() => setRules(rules.filter((_, j) => j !== i))}
              />
            </div>
          ))}
          {rules.length < MAX_LEVEL_RULES ? (
            <div>
              <Button
                size="sm"
                variant="ghost"
                icon={<Plus size={14} />}
                onClick={() => setRules([...rules, { levels: ['Verbose', 'Debug'], days: 3 }])}
              >
                Add level rule
              </Button>
            </div>
          ) : null}
          <CodeBlock label="TTL preview">
            {ttlPreview(stream.name, Number.isFinite(n) ? n : defaults, rules)}
          </CodeBlock>
        </>
      ) : null}
      {apply.isError ? <Notice kind="danger">{errorMessage(apply.error)}</Notice> : null}
      <div className={l.row}>
        <Button variant="primary" disabled={!valid} busy={apply.isPending} onClick={() => apply.mutate()}>
          Apply retention
        </Button>
        <Button variant="ghost" onClick={reset}>
          Reset
        </Button>
      </div>
    </div>
  );
}

function Mappings({ stream, canAdmin }: { stream: Stream; canAdmin: boolean }) {
  const api = useApi();
  const qc = useQueryClient();
  const [filter, setFilter] = useState('');
  const fields = useQuery({
    queryKey: ['stream-fields', stream.name],
    queryFn: () => streamsApi.fields(api, stream.name),
  });
  const indexed = useQuery({ queryKey: ['indexed-fields'], queryFn: () => streamsApi.indexedFields(api) });
  const add = useMutation({
    mutationFn: (m: FieldMapping) => streamsApi.addIndexedField(api, m.source, m.key),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['indexed-fields'] });
      void qc.invalidateQueries({ queryKey: ['stream-fields'] });
    },
  });
  const count = indexed.data?.length ?? 0;
  const list = (fields.data ?? []).filter((f) => f.key.toLowerCase().includes(filter.trim().toLowerCase()));
  return (
    <div className={s.col}>
      <div className={s.mapHead}>
        <span className={l.sectionLabel}>Field mappings</span>
        <Tag>{fields.data?.length ?? 0} keys</Tag>
        <label className={s.search}>
          <Search size={14} aria-hidden="true" />
          <input
            type="search"
            placeholder="Search keys"
            aria-label="Search keys"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </label>
        <span className={l.subtle}>
          indexed {count} / {MAX_INDEXED}
        </span>
      </div>
      {fields.isPending ? <Spinner /> : null}
      <div className={cx(l.card, l.tableWrap, s.mapTable)}>
        <table className={cx(l.table, l.compact)}>
          <thead>
            <tr>
              <th>Key</th>
              <th>Type</th>
              <th className={l.num}>Hits</th>
              <th>First seen</th>
              <th>Last seen</th>
              <th>Index</th>
            </tr>
          </thead>
          <tbody>
            {list.map((f) => (
              <tr key={`${f.source}:${f.key}`}>
                <td className={l.monoCell}>{f.key}</td>
                <td>
                  <Tag warning={f.type === 'mixed'}>{f.type}</Tag>
                </td>
                <td className={l.num}>{formatCount(f.hits)}</td>
                <td className={l.subtle}>
                  {new Date(f.firstSeen).toLocaleDateString(undefined, { month: 'short', day: '2-digit' })}
                </td>
                <td className={l.subtle}>{formatAge(Date.parse(f.lastSeen), Date.now())}</td>
                <td>
                  {f.indexed ? (
                    <StatusChip variant="ok">indexed</StatusChip>
                  ) : canAdmin ? (
                    <Button
                      size="sm"
                      variant="ghost"
                      disabled={f.type === 'mixed' || count >= MAX_INDEXED}
                      busy={add.isPending && add.variables.key === f.key}
                      onClick={() => add.mutate(f)}
                    >
                      Index field
                    </Button>
                  ) : (
                    <span className={l.subtle}>—</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {add.isError ? <Notice kind="danger">{errorMessage(add.error)}</Notice> : null}
      <p className={s.footnote}>
        Indexing creates a materialized column + bloom-filter skip index, then materializes existing parts
        asynchronously. Mixed-type keys cannot be indexed.
      </p>
    </div>
  );
}

const PURGE_RANGES = [
  { value: '30d', label: 'Older than 30 days' },
  { value: '7d', label: 'Older than 7 days' },
  { value: 'all', label: 'Everything until now' },
  { value: 'custom', label: 'Custom range' },
];

function PurgeCard({ stream, source, stats }: { stream: Stream; source: Source; stats: SourceStats | null }) {
  const api = useApi();
  const qc = useQueryClient();
  const { now } = useServices();
  const [preset, setPreset] = useState('30d');
  const [from, setFrom] = useState(stats?.oldest ?? '2000-01-01T00:00:00Z');
  const [to, setTo] = useState(new Date(now() - 30 * 86_400_000).toISOString());
  const [q, setQ] = useState('');
  const [confirm, setConfirm] = useState('');
  const [job, setJob] = useState<PurgeJob | null>(null);
  const range = useMemo(() => {
    if (preset === 'custom') return { from, to };
    const start = stats?.oldest ?? '2000-01-01T00:00:00Z';
    if (preset === 'all') return { from: start, to: new Date(now()).toISOString() };
    const days = preset === '7d' ? 7 : 30;
    return { from: start, to: new Date(now() - days * 86_400_000).toISOString() };
  }, [preset, from, to, stats, now]);
  const req = {
    stream: stream.name,
    source,
    q: q.trim() === '' ? undefined : q.trim(),
    from: range.from,
    to: range.to,
  };
  const dry = useMutation({ mutationFn: () => streamsApi.purge(api, { ...req, dryRun: true }) });
  const run = useMutation({
    mutationFn: () => streamsApi.purge(api, { ...req, dryRun: false }),
    onSuccess: (r) => {
      if ('id' in r) setJob(r);
      setConfirm('');
    },
  });
  const status = useQuery({
    queryKey: ['purge', job?.id],
    queryFn: () => streamsApi.purgeStatus(api, job?.id ?? ''),
    enabled: job !== null,
    refetchInterval: (query) =>
      query.state.data?.status === 'done' || query.state.data?.status === 'failed' ? false : PURGE_POLL_MS,
  });
  useEffect(() => {
    if (status.data?.status === 'done') void qc.invalidateQueries({ queryKey: ['streams'] });
  }, [status.data?.status, qc]);
  const want = purgeConfirmation(stream.name);
  const estimate = dry.data !== undefined && 'rowsEstimate' in dry.data ? dry.data.rowsEstimate : null;
  const bytes = estimate === null ? null : estimateBytes(estimate, stats);
  const partitions = dropsPartitions('', q, Date.parse(range.from), Date.parse(range.to));
  const matches = confirm === want;
  return (
    <section className={s.purge} aria-label="Purge events">
      <h3 className={s.purgeTitle}>
        <Trash2 size={16} aria-hidden="true" /> Purge events
      </h3>
      <Field label="Time range">
        {(id) => (
          <Select id={id} options={PURGE_RANGES} value={preset} onChange={(e) => setPreset(e.target.value)} />
        )}
      </Field>
      {preset === 'custom' ? (
        <div className={s.rangeRow}>
          <TextInput aria-label="From" mono value={from} onChange={(e) => setFrom(e.target.value)} />
          <TextInput aria-label="To" mono value={to} onChange={(e) => setTo(e.target.value)} />
        </div>
      ) : (
        <div className={s.rangeText}>
          {formatDateTime(Date.parse(range.from)).slice(0, 10)} →{' '}
          {formatDateTime(Date.parse(range.to)).slice(0, 10)}
        </div>
      )}
      <Field label="Filter (TQL, optional)">
        {() => (
          <QueryEditor
            value={q}
            onChange={setQ}
            ariaLabel="Purge filter"
            source={source}
            size="sm"
            showIcon={false}
            placeholder="level <= debug"
          />
        )}
      </Field>
      <div className={s.dryRow}>
        <Button variant="default" busy={dry.isPending} onClick={() => dry.mutate()}>
          Dry run
        </Button>
        <span className={l.subtle}>Estimates rows, never deletes</span>
      </div>
      {estimate !== null ? (
        <div className={s.dryResult} role="status">
          <strong>Dry run:</strong> ~{estimate.toLocaleString('en-US')} rows
          {bytes !== null ? ` (≈ ${formatBytes(bytes)})` : ''} match.{' '}
          {q.trim() !== ''
            ? 'Filter present → lightweight DELETE FROM … WHERE; partitions will not be dropped.'
            : partitions
              ? 'Whole days are dropped as partitions, the rest with a lightweight DELETE.'
              : 'Rows are removed with a lightweight DELETE; partitions will not be dropped.'}
        </div>
      ) : null}
      {dry.isError ? <Notice kind="danger">{errorMessage(dry.error)}</Notice> : null}
      <Field
        label={`Type ${want} to confirm`}
        error={confirm !== '' && !matches ? 'Confirmation does not match yet.' : undefined}
      >
        {(id) => (
          <TextInput
            id={id}
            mono
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            aria-invalid={confirm !== '' && !matches}
            className={cx(confirm !== '' && !matches && s.invalid)}
          />
        )}
      </Field>
      <Button variant="dangerSolid" disabled={!matches} busy={run.isPending} onClick={() => run.mutate()}>
        Purge {estimate !== null ? `${formatCount(estimate)} rows` : 'rows'}
      </Button>
      {run.isError ? <Notice kind="danger">{errorMessage(run.error)}</Notice> : null}
      {job !== null ? (
        <Notice
          kind={
            status.data?.status === 'failed' ? 'danger' : status.data?.status === 'done' ? 'success' : 'info'
          }
        >
          Purge job {job.id}: {status.data?.status ?? job.status}
          {status.data?.droppedPartitions ? ` · dropped partitions ${status.data.droppedPartitions}` : ''}
        </Notice>
      ) : null}
      <p className={s.footnote}>
        Every purge is written to the audit log with the actor, range, filter and estimate.
      </p>
    </section>
  );
}

function StreamDetail({
  stream,
  source,
  stats,
  defaults,
  canAdmin,
}: {
  stream: Stream;
  source: Source;
  stats: SourceStats | null;
  defaults: number;
  canAdmin: boolean;
}) {
  const api = useApi();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [tab, setTab] = useState<DetailTab>('overview');
  const [deleting, setDeleting] = useState(false);
  const [confirm, setConfirm] = useState('');
  const pause = useMutation({
    mutationFn: () => streamsApi.update(api, stream.name, { paused: !stream.paused }),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['streams'] }),
  });
  const remove = useMutation({
    mutationFn: () => streamsApi.remove(api, stream.name),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['streams'] });
      navigate('/streams');
    },
  });
  const show = (t: DetailTab): boolean => tab === 'overview' || tab === t;
  return (
    <section className={cx(l.card, s.detail)} aria-label={`Stream ${stream.name}`}>
      <header className={s.detailHead}>
        <span className={s.detailName}>{stream.name}</span>
        {stream.paused ? (
          <StatusChip variant="muted">paused</StatusChip>
        ) : (
          <StatusChip variant="ok">active</StatusChip>
        )}
        <span className={s.detailMeta}>
          {source}
          {stats !== null
            ? ` · oldest ${formatDateTime(Date.parse(stats.oldest)).slice(0, 10)} · newest ${formatDateTime(Date.parse(stats.newest)).slice(11)} · top services: ${stats.topServices.join(', ')}`
            : ' · no data yet'}
        </span>
        {canAdmin ? (
          <span className={s.detailActions}>
            <Button
              variant="ghost"
              icon={stream.paused ? <Play size={14} /> : <Pause size={14} />}
              busy={pause.isPending}
              onClick={() => pause.mutate()}
            >
              {stream.paused ? 'Resume stream' : 'Pause stream'}
            </Button>
            <Button variant="danger" onClick={() => setTab('purge')}>
              Purge…
            </Button>
            {stream.name !== 'default' ? (
              <Button
                variant="ghost"
                iconOnly
                icon={<Trash2 size={14} />}
                aria-label={`Delete stream ${stream.name}`}
                onClick={() => setDeleting(true)}
              />
            ) : null}
          </span>
        ) : null}
      </header>
      <Tabs<DetailTab>
        label="Stream"
        value={tab}
        onChange={setTab}
        className={s.detailTabs}
        items={[
          { value: 'overview', label: 'Overview' },
          { value: 'retention', label: 'Retention' },
          { value: 'mappings', label: 'Mappings' },
          { value: 'purge', label: 'Purge' },
        ]}
      />
      <div className={cx(s.detailBody, tab === 'overview' && s.threeCols)}>
        {show('retention') && canAdmin ? (
          <RetentionEditor stream={stream} source={source} defaults={defaults} />
        ) : null}
        {show('mappings') ? <Mappings stream={stream} canAdmin={canAdmin} /> : null}
        {show('purge') && canAdmin ? <PurgeCard stream={stream} source={source} stats={stats} /> : null}
      </div>
      {pause.isError ? <Notice kind="danger">{errorMessage(pause.error)}</Notice> : null}
      {deleting ? (
        <Modal
          title={`Delete stream ${stream.name}`}
          onClose={() => setDeleting(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setDeleting(false)}>
                Cancel
              </Button>
              <Button
                variant="dangerSolid"
                disabled={confirm !== stream.name}
                busy={remove.isPending}
                onClick={() => remove.mutate()}
              >
                Delete stream and data
              </Button>
            </>
          }
        >
          <div className={l.stack}>
            <p>
              All events, spans and metric points of this stream are deleted. New data re-creates the stream
              with the default retention.
            </p>
            <Field label={`Type ${stream.name} to confirm`}>
              {(id) => (
                <TextInput id={id} mono value={confirm} onChange={(e) => setConfirm(e.target.value)} />
              )}
            </Field>
            {remove.isError ? <Notice kind="danger">{errorMessage(remove.error)}</Notice> : null}
          </div>
        </Modal>
      ) : null}
    </section>
  );
}

function DefaultRetentionDialog({ onClose }: { onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const settings = useQuery({ queryKey: ['settings'], queryFn: () => admin.settings(api) });
  const [vals, setVals] = useState<Record<Source, string> | null>(null);
  const cur =
    vals ??
    (settings.data === undefined
      ? null
      : {
          logs: String(settings.data.retention.logsDays),
          spans: String(settings.data.retention.spansDays),
          metrics: String(settings.data.retention.metricsDays),
        });
  const save = useMutation({
    mutationFn: () =>
      admin.patchSettings(api, {
        retention: {
          logsDays: Number(cur?.logs),
          spansDays: Number(cur?.spans),
          metricsDays: Number(cur?.metrics),
        },
      }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['settings'] });
      onClose();
    },
  });
  return (
    <Modal
      title="Default retention"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={save.isPending}
            disabled={cur === null}
            onClick={() => save.mutate()}
          >
            Save
          </Button>
        </>
      }
    >
      {cur === null ? (
        <Spinner />
      ) : (
        <div className={l.stack}>
          {(['logs', 'spans', 'metrics'] as const).map((src) => (
            <Field key={src} label={`${src} (days)`}>
              {(id) => (
                <TextInput
                  id={id}
                  type="number"
                  min={1}
                  value={cur[src]}
                  onChange={(e) => setVals({ ...cur, [src]: e.target.value })}
                />
              )}
            </Field>
          ))}
          <p className={l.subtle}>
            Streams without an override use these values. Changes re-apply the TTL rules.
          </p>
          {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
        </div>
      )}
    </Modal>
  );
}

function NewStreamDialog({ onClose }: { onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState('');
  const create = useMutation({
    mutationFn: () => streamsApi.create(api, { name }),
    onSuccess: (st) => {
      void qc.invalidateQueries({ queryKey: ['streams'] });
      navigate(`/streams/${encodeURIComponent(st.name)}`);
      onClose();
    },
  });
  const valid = STREAM_NAME.test(name);
  return (
    <Modal
      title="New stream"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!valid} busy={create.isPending} onClick={() => create.mutate()}>
            Create
          </Button>
        </>
      }
    >
      <div className={l.stack}>
        <Field
          label="Name"
          help="Lower-case letters, digits, '.', '_' and '-'."
          error={name !== '' && !valid ? 'Invalid stream name.' : undefined}
        >
          {(id) => (
            <TextInput id={id} mono value={name} onChange={(e) => setName(e.target.value)} data-autofocus />
          )}
        </Field>
        {create.isError ? <Notice kind="danger">{errorMessage(create.error)}</Notice> : null}
      </div>
    </Modal>
  );
}

export default function StreamsPage() {
  const api = useApi();
  const me = useMe();
  const qc = useQueryClient();
  const { now } = useServices();
  const { name } = useParams();
  const navigate = useNavigate();
  const [dialog, setDialog] = useState<'retention' | 'new' | null>(null);
  const list = useQuery({ queryKey: ['streams'], queryFn: () => streamsApi.list(api) });
  const settings = useQuery({
    queryKey: ['settings'],
    queryFn: () => admin.settings(api),
    enabled: me.role === 'admin',
  });
  const canAdmin = hasRole(me.role, 'admin');
  const rows = streamRows(list.data?.streams ?? []);
  const selectedName = name ?? rows[0]?.stream.name;
  const selected = rows.find((r) => r.stream.name === selectedName);
  useTopbarStatus(list.data === undefined ? null : `${list.data.streams.length} streams`);
  const defaultsFor = (src: Source): number => {
    const r = settings.data?.retention;
    if (r === undefined) return DEFAULT_DAYS[src];
    return src === 'spans' ? r.spansDays : src === 'metrics' ? r.metricsDays : r.logsDays;
  };
  const updated = list.data?.statsUpdatedAt;
  return (
    <Page>
      <div className={s.head}>
        <h1>Streams</h1>
        <span className={l.subtle}>
          Index management
          {updated !== null && updated !== undefined
            ? ` · stats refreshed ${formatAge(Date.parse(updated), now())} (estimated from system.parts)`
            : ''}
        </span>
        <span className={s.headActions}>
          <Button
            variant="ghost"
            icon={<RefreshCw size={14} />}
            busy={list.isFetching}
            onClick={() => void qc.invalidateQueries({ queryKey: ['streams'] })}
          >
            Refresh stats
          </Button>
          {canAdmin ? (
            <>
              <Button variant="ghost" onClick={() => setDialog('retention')}>
                Default retention
              </Button>
              <Button variant="ghost" icon={<Plus size={14} />} onClick={() => setDialog('new')}>
                New stream
              </Button>
            </>
          ) : null}
        </span>
      </div>
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {list.isPending ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<Database size={20} aria-hidden="true" />}
          title="No streams yet"
          hint="Streams are created on the first write of a connection."
        />
      ) : (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={l.table}>
            <thead>
              <tr>
                <th>Stream</th>
                <th>Source</th>
                <th className={l.num}>Rows</th>
                <th className={l.num}>Size (compressed)</th>
                <th className={l.num}>Ratio</th>
                <th>Retention</th>
                <th className={l.num}>Ingest 5m</th>
                <th className={l.num}>24 h</th>
                <th>Status</th>
                <th aria-label="Open" />
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const src = r.source ?? 'logs';
                const days = retentionFor(r.stream, src) ?? defaultsFor(src);
                const rules = src === 'logs' ? levelRuleSummary(r.stream.levelRules) : '';
                return (
                  <tr
                    key={`${r.stream.name}:${r.source ?? ''}`}
                    aria-selected={r.stream.name === selectedName}
                    className={s.row}
                    onClick={() => navigate(`/streams/${encodeURIComponent(r.stream.name)}`)}
                  >
                    <td className={s.streamName}>{r.stream.name}</td>
                    <td className={l.subtle}>{r.source ?? '—'}</td>
                    <td className={l.num}>{r.stats === null ? '—' : formatCount(r.stats.rows)}</td>
                    <td className={l.num}>
                      {r.stats === null ? '—' : formatBytes(r.stats.compressedBytes)}
                      {r.stats !== null && r.stats.uncompressedBytes > 0 ? (
                        <span className={l.subtle}> / {formatBytes(r.stats.uncompressedBytes)}</span>
                      ) : null}
                    </td>
                    <td className={l.num}>
                      {r.stats === null || r.stats.compressionRatio === 0
                        ? '—'
                        : `${r.stats.compressionRatio.toFixed(1)}×`}
                    </td>
                    <td>
                      {days} d{rules !== '' ? <span className={l.subtle}> · {rules}</span> : null}
                    </td>
                    <td className={l.num}>
                      {r.stats === null ? '—' : formatRatePerMin(r.stats.ratePer5Min)}
                    </td>
                    <td className={l.num}>
                      {r.stats === null ? '—' : formatCount(r.stats.ratePer24h * 86_400)}
                    </td>
                    <td>
                      {r.stream.paused ? (
                        <StatusChip variant="muted">paused</StatusChip>
                      ) : (
                        <StatusChip variant="ok">active</StatusChip>
                      )}
                    </td>
                    <td>
                      <Link
                        to={`/streams/${encodeURIComponent(r.stream.name)}`}
                        onClick={(e) => e.stopPropagation()}
                      >
                        Open
                      </Link>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {selected !== undefined ? (
        <StreamDetail
          key={selected.stream.name}
          stream={selected.stream}
          source={selected.source ?? 'logs'}
          stats={selected.stats}
          defaults={defaultsFor(selected.source ?? 'logs')}
          canAdmin={canAdmin}
        />
      ) : null}
      {dialog === 'retention' ? <DefaultRetentionDialog onClose={() => setDialog(null)} /> : null}
      {dialog === 'new' ? <NewStreamDialog onClose={() => setDialog(null)} /> : null}
    </Page>
  );
}
