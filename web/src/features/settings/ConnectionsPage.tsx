// SPDX-License-Identifier: Apache-2.0
//
// Connections & keys (SPEC §9.1, §11.5, §15.3, §16.4, DESIGN §9.9): the connections table with
// edit / rotate / revoke / delete, the edit dialog, and the five-step wizard (name → protocols →
// stream & allowed streams → restrictions → key & snippets). The key is shown exactly once;
// snippets come from the server with the placeholder replaced by the new key in the browser only.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, Copy, Download, Lock, Pencil, Plus, RotateCw, ShieldOff, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { errorMessage } from '../../api/client';
import { connections as connApi, streams as streamsApi } from '../../api/endpoints';
import type {
  Connection,
  ConnectionInput,
  LevelName,
  Protocol,
  Snippet,
  StreamsResponse,
} from '../../api/types';
import { isStreamName, LEVELS, PROTOCOLS } from '../../api/types';
import { useApi, useServices } from '../../app/services';
import { useTopbarStatus } from '../../app/shellContext';
import {
  Button,
  Checkbox,
  Field,
  LevelChip,
  Select,
  Spinner,
  StatusChip,
  Tag,
  TextArea,
  TextInput,
} from '../../components/controls';
import { Card, CodeBlock, EmptyState, layoutStyles as l, Notice, Tabs } from '../../components/layout';
import { MultiSelect } from '../../components/MultiSelect';
import { Modal, useToast } from '../../components/overlay';
import { cx } from '../../lib/cx';
import { copyText, downloadText } from '../../lib/download';
import { formatAge } from '../../lib/time';
import s from './settings.module.css';
import { streamNameProblem } from './TeamPage';

export const KEY_PLACEHOLDER = '<YOUR_TIDEMARK_KEY>';
const STEPS = ['Name', 'Protocols', 'Stream', 'Restrictions', 'Key & snippets'] as const;
/** Protocol labels: third-party names are referential only (SPEC §19.1). */
const PROTOCOL_LABEL: Record<Protocol, string> = {
  seq: 'seq',
  otlp: 'otlp',
  elastic: 'elasticsearch _bulk',
  native: 'native json',
};
const MAX_GRACE_MINUTES = 1440;

/** Snippet text with the key filled in (the server only ever sends the placeholder). */
export function fillKey(content: string, key: string): string {
  return content.split(KEY_PLACEHOLDER).join(key);
}

/** Status of a connection for the table. */
export function connectionStatus(
  c: Connection,
  now: number,
): { variant: 'ok' | 'pending' | 'muted' | 'danger'; label: string } {
  if (c.revokedAt !== null) return { variant: 'muted', label: 'revoked' };
  if (c.expiresAt !== null && Date.parse(c.expiresAt) <= now) return { variant: 'danger', label: 'expired' };
  if (c.rotationGraceUntil !== null && Date.parse(c.rotationGraceUntil) > now) {
    const days = Math.ceil((Date.parse(c.rotationGraceUntil) - now) / 86_400_000);
    return { variant: 'pending', label: `rotating · grace ${days} d` };
  }
  return { variant: 'ok', label: 'active' };
}

/** "10.4.0.0/16 · streams api, payments · min level debug · rate 50 k/min · expires 2026-12-31" */
export function restrictionsText(c: Connection): string {
  const parts: string[] = [];
  if (c.allowedCidrs.length > 0) parts.push(c.allowedCidrs.join(', '));
  if (c.allowedStreams.length > 0) parts.push(`streams ${c.allowedStreams.join(', ')}`);
  if (c.minimumLevel !== null) parts.push(`min level ${c.minimumLevel.toLowerCase()}`);
  if (c.rateLimitEps > 0) parts.push(`rate ${Math.round((c.rateLimitEps * 60) / 1000)} k/min`);
  if (c.expiresAt !== null) parts.push(`expires ${c.expiresAt.slice(0, 10)}`);
  return parts.length === 0 ? '—' : parts.join(' · ');
}

/** The `.env` file offered for download after creation. */
export function envFile(c: Connection, key: string, base: string): string {
  return [
    `# OpenTidemark connection "${c.name}" (stream ${c.defaultStream})`,
    `TIDEMARK_URL=${base}`,
    `TIDEMARK_KEY=${key}`,
    `OTEL_EXPORTER_OTLP_ENDPOINT=${base}/otlp`,
    `OTEL_EXPORTER_OTLP_HEADERS=x-tidemark-key=${key}`,
    '',
  ].join('\n');
}

const splitCidrs = (t: string): string[] =>
  t
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x !== '');

interface Draft {
  name: string;
  description: string;
  protocols: Protocol[];
  defaultStream: string;
  allowedStreams: string[];
  cidrs: string;
  minimumLevel: string;
  rateLimitEps: string;
  expiresAt: string;
}

const EMPTY: Draft = {
  name: '',
  description: '',
  protocols: ['seq', 'otlp'],
  defaultStream: 'default',
  allowedStreams: [],
  cidrs: '',
  minimumLevel: '',
  rateLimitEps: '',
  expiresAt: '',
};

/** The draft of an existing connection for the edit dialog. */
export function draftOf(c: Connection): Draft {
  return {
    name: c.name,
    description: c.description,
    protocols: c.protocols,
    defaultStream: c.defaultStream,
    allowedStreams: c.allowedStreams,
    cidrs: c.allowedCidrs.join(', '),
    minimumLevel: c.minimumLevel ?? '',
    rateLimitEps: c.rateLimitEps === 0 ? '' : String(c.rateLimitEps),
    expiresAt: c.expiresAt === null ? '' : c.expiresAt.slice(0, 10),
  };
}

/** Why the allowed streams cannot be saved (SPEC §9.1: they must include the default), or null. */
export function allowedStreamsProblem(d: Pick<Draft, 'defaultStream' | 'allowedStreams'>): string | null {
  const def = d.defaultStream.trim();
  if (d.allowedStreams.length === 0 || d.allowedStreams.includes(def)) return null;
  return `Must include the default stream ${def}.`;
}

const streamStepValid = (d: Draft): boolean =>
  isStreamName(d.defaultStream.trim()) && allowedStreamsProblem(d) === null;

/** The request body of a draft (`minimumLevel` is sent as '' = none on edit). */
function bodyOf(d: Draft, edit: boolean): ConnectionInput {
  return {
    name: d.name.trim(),
    description: d.description.trim(),
    protocols: d.protocols,
    defaultStream: d.defaultStream.trim(),
    allowedCidrs: splitCidrs(d.cidrs),
    allowedStreams: d.allowedStreams,
    rateLimitEps: d.rateLimitEps === '' ? 0 : Number(d.rateLimitEps),
    expiresAt: d.expiresAt === '' ? null : new Date(`${d.expiresAt}T00:00:00Z`).toISOString(),
    ...(d.minimumLevel === '' && !edit ? {} : { minimumLevel: d.minimumLevel }),
  };
}

function StreamFields({
  d,
  set,
  streams,
}: {
  d: Draft;
  set: (p: Partial<Draft>) => void;
  streams: StreamsResponse | undefined;
}) {
  const known = (streams?.streams ?? []).map((st) => st.name);
  const items = [...new Set([...known, ...d.allowedStreams])].map((n) => ({ value: n, label: n }));
  return (
    <>
      <Field
        label="Default stream"
        help="Events without a stream property are written here. New names create the stream on first write."
        error={
          d.defaultStream.trim() !== '' && !isStreamName(d.defaultStream.trim())
            ? 'Not a valid stream name.'
            : undefined
        }
      >
        {(id) => (
          <>
            <TextInput
              id={id}
              mono
              list="tm-streams"
              value={d.defaultStream}
              onChange={(e) => set({ defaultStream: e.target.value })}
            />
            <datalist id="tm-streams">
              {known.map((name) => (
                <option key={name} value={name} />
              ))}
            </datalist>
          </>
        )}
      </Field>
      <Field
        label="Allowed streams"
        note="optional"
        help="Data that names another stream is written to the default stream instead. Empty = any stream."
        error={allowedStreamsProblem(d) ?? undefined}
      >
        {(id) => (
          <MultiSelect
            id={id}
            label="Allowed streams"
            mono
            items={items}
            value={d.allowedStreams}
            onChange={(allowedStreams) => set({ allowedStreams })}
            placeholder="Any stream"
            emptyText="No streams have data yet; type a name below."
            custom={{ validate: streamNameProblem, placeholder: 'stream name', note: 'no data yet' }}
          />
        )}
      </Field>
    </>
  );
}

function RestrictionFields({ d, set }: { d: Draft; set: (p: Partial<Draft>) => void }) {
  return (
    <div className={s.wizardGrid}>
      <Field label="Allowed source CIDRs" help="Comma or space separated; empty = any address.">
        {(id) => (
          <TextInput
            id={id}
            mono
            value={d.cidrs}
            placeholder="10.4.0.0/16"
            onChange={(e) => set({ cidrs: e.target.value })}
          />
        )}
      </Field>
      <Field label="Minimum level">
        {(id) => (
          <Select
            id={id}
            options={[
              { value: '', label: 'all levels' },
              ...LEVELS.map((v: LevelName) => ({ value: v, label: v })),
            ]}
            value={d.minimumLevel}
            onChange={(e) => set({ minimumLevel: e.target.value })}
          />
        )}
      </Field>
      <Field label="Rate limit (events/s)" help="0 or empty = no limit.">
        {(id) => (
          <TextInput
            id={id}
            type="number"
            min={0}
            value={d.rateLimitEps}
            onChange={(e) => set({ rateLimitEps: e.target.value })}
          />
        )}
      </Field>
      <Field label="Expires">
        {(id) => (
          <TextInput
            id={id}
            type="date"
            value={d.expiresAt}
            onChange={(e) => set({ expiresAt: e.target.value })}
          />
        )}
      </Field>
    </div>
  );
}

function StepStrip({ step }: { step: number }) {
  return (
    <ol className={s.steps} aria-label="Progress">
      {STEPS.map((label, i) => (
        <li
          key={label}
          className={cx(s.stepItem, i < step && s.stepDone, i === step && s.stepCurrent)}
          aria-current={i === step ? 'step' : undefined}
        >
          <span className={s.stepMark}>{i < step ? <Check size={12} aria-hidden="true" /> : i + 1}</span>
          {label}
          {i < STEPS.length - 1 ? (
            <span className={s.stepArrow} aria-hidden="true">
              →
            </span>
          ) : null}
        </li>
      ))}
    </ol>
  );
}

function KeyStep({
  conn,
  keyValue,
  onDone,
  onAnother,
}: {
  conn: Connection;
  keyValue: string;
  onDone: () => void;
  onAnother: () => void;
}) {
  const api = useApi();
  const toast = useToast();
  const snippets = useQuery({
    queryKey: ['snippets', conn.id],
    queryFn: () => connApi.snippets(api, conn.id),
  });
  const [tab, setTab] = useState('serilog');
  const [leaving, setLeaving] = useState<null | 'done' | 'another'>(null);
  const base = window.location.origin;
  const list: Snippet[] = snippets.data ?? [];
  const current = list.find((x) => x.id === tab) ?? list[0];
  const copy = async (text: string, what: string): Promise<void> => {
    const ok = await copyText(text);
    toast.push({ kind: ok ? 'success' : 'warning', title: ok ? `${what} copied` : 'Copy failed' });
  };
  return (
    <div className={s.keyStep}>
      <div className={l.stack}>
        <div className={l.row}>
          <StatusChip variant="ok">created</StatusChip>
          <strong>{conn.name}</strong>
          <span className={l.subtle}>
            · {conn.protocols.join(', ')} · stream <span className={s.mono}>{conn.defaultStream}</span>
          </span>
        </div>
        <div className={s.keyPanel}>
          <div className={s.keyTitle}>
            <Lock size={14} aria-hidden="true" /> Connection key — shown once
          </div>
          <div className={s.keyValue}>{keyValue}</div>
          <div className={l.row}>
            <Button
              variant="primary"
              size="sm"
              icon={<Copy size={14} />}
              onClick={() => void copy(keyValue, 'Key')}
            >
              Copy key
            </Button>
            <Button
              variant="ghost"
              size="sm"
              icon={<Download size={14} />}
              onClick={() =>
                downloadText(`${conn.name.replace(/[^\w.-]+/g, '-')}.env`, envFile(conn, keyValue, base))
              }
            >
              Download .env
            </Button>
          </div>
          <p className={s.keyNote}>
            Stored as SHA-256 + 8-char prefix <span className={s.mono}>{conn.keyPrefix}</span>. It cannot be
            retrieved again; rotate to get a new one.
          </p>
        </div>
        <dl className={l.kv}>
          <dt>Ingest URL</dt>
          <dd>{base}</dd>
          <dt>Seq endpoint</dt>
          <dd>/api/events/raw</dd>
          <dt>OTLP/HTTP</dt>
          <dd>/otlp/v1/logs · /otlp/v1/traces · /otlp/v1/metrics</dd>
          <dt>
            Elasticsearch <code>_bulk</code>
          </dt>
          <dd>/es/_bulk</dd>
          <dt>Source CIDRs</dt>
          <dd>{conn.allowedCidrs.length === 0 ? 'any' : conn.allowedCidrs.join(', ')}</dd>
          <dt>Allowed streams</dt>
          <dd>{conn.allowedStreams.length === 0 ? 'any' : conn.allowedStreams.join(', ')}</dd>
          <dt>Min level</dt>
          <dd>{conn.minimumLevel === null ? 'all' : <LevelChip level={conn.minimumLevel} />}</dd>
        </dl>
        <div className={l.row}>
          <Button variant="ghost" onClick={() => setLeaving('another')}>
            Create another
          </Button>
          <Button variant="ghost" onClick={() => setLeaving('done')}>
            Done
          </Button>
        </div>
      </div>
      <div className={s.snippets}>
        <div className={s.snippetHead}>
          <Tabs
            label="Snippets"
            value={current?.id ?? tab}
            onChange={setTab}
            items={list.map((x) => ({ value: x.id, label: x.title.replace(/\s*\(.*\)$/, '') }))}
          />
          {current !== undefined ? (
            <Button
              size="sm"
              variant="ghost"
              icon={<Copy size={14} />}
              onClick={() => void copy(fillKey(current.content, keyValue), 'Snippet')}
            >
              Copy snippet
            </Button>
          ) : null}
        </div>
        {snippets.isPending ? <Spinner /> : null}
        {current !== undefined ? (
          <CodeBlock label={current.title}>{fillKey(current.content, keyValue)}</CodeBlock>
        ) : null}
        <p className={s.footnote}>
          Snippets are generated with the new key filled in. The Seq-compatible endpoint accepts CLEF; OTLP
          accepts protobuf and JSON.
        </p>
      </div>
      {leaving !== null ? (
        <Modal
          title="Leave this step?"
          onClose={() => setLeaving(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setLeaving(null)}>
                Stay
              </Button>
              <Button variant="primary" onClick={() => (leaving === 'done' ? onDone() : onAnother())}>
                I saved the key
              </Button>
            </>
          }
        >
          <p>You will not see this key again. Make sure it is copied or downloaded.</p>
        </Modal>
      ) : null}
    </div>
  );
}

function Wizard({ onClose }: { onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const [step, setStep] = useState(0);
  const [d, setD] = useState<Draft>(EMPTY);
  const [created, setCreated] = useState<{ conn: Connection; key: string } | null>(null);
  const streams = useQuery({ queryKey: ['streams'], queryFn: () => streamsApi.list(api) });
  const set = (p: Partial<Draft>): void => setD((x) => ({ ...x, ...p }));
  const create = useMutation({
    mutationFn: () => connApi.create(api, bodyOf(d, false)),
    onSuccess: (c) => {
      void qc.invalidateQueries({ queryKey: ['connections'] });
      setCreated({ conn: c, key: c.key ?? '' });
      setStep(4);
    },
  });
  const valid = [d.name.trim() !== '', d.protocols.length > 0, streamStepValid(d), true][step] ?? true;
  return (
    <Card title="New connection" headExtra={<StepStrip step={step} />}>
      {step === 0 ? (
        <div className={s.wizardBody}>
          <Field label="Name" help="Shown in the audit log and the connections table.">
            {(id) => (
              <TextInput
                id={id}
                value={d.name}
                maxLength={128}
                onChange={(e) => set({ name: e.target.value })}
                data-autofocus
              />
            )}
          </Field>
          <Field label="Description">
            {(id) => (
              <TextArea
                id={id}
                rows={2}
                value={d.description}
                onChange={(e) => set({ description: e.target.value })}
              />
            )}
          </Field>
        </div>
      ) : null}
      {step === 1 ? (
        <div className={s.wizardBody} role="group" aria-label="Protocols">
          {PROTOCOLS.map((p) => (
            <Checkbox
              key={p}
              checked={d.protocols.includes(p)}
              onChange={(on) =>
                set({ protocols: on ? [...d.protocols, p] : d.protocols.filter((x) => x !== p) })
              }
            >
              <span className={s.mono}>{PROTOCOL_LABEL[p]}</span>
            </Checkbox>
          ))}
          <p className={s.footnote}>Requests over other protocols are rejected for this key.</p>
        </div>
      ) : null}
      {step === 2 ? (
        <div className={s.wizardBody}>
          <StreamFields d={d} set={set} streams={streams.data} />
        </div>
      ) : null}
      {step === 3 ? <RestrictionFields d={d} set={set} /> : null}
      {step === 4 && created !== null ? (
        <KeyStep
          conn={created.conn}
          keyValue={created.key}
          onDone={onClose}
          onAnother={() => {
            setCreated(null);
            setD(EMPTY);
            setStep(0);
          }}
        />
      ) : null}
      {create.isError ? <Notice kind="danger">{errorMessage(create.error)}</Notice> : null}
      {step < 4 ? (
        <div className={s.wizardFoot}>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          {step > 0 ? (
            <Button variant="ghost" onClick={() => setStep(step - 1)}>
              Back
            </Button>
          ) : null}
          {step < 3 ? (
            <Button variant="primary" disabled={!valid} onClick={() => setStep(step + 1)}>
              Next
            </Button>
          ) : (
            <Button variant="primary" busy={create.isPending} onClick={() => create.mutate()}>
              Create connection
            </Button>
          )}
        </div>
      ) : null}
    </Card>
  );
}

function EditDialog({ conn, onClose }: { conn: Connection; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const [d, setD] = useState<Draft>(() => draftOf(conn));
  const streams = useQuery({ queryKey: ['streams'], queryFn: () => streamsApi.list(api), staleTime: 60_000 });
  const set = (p: Partial<Draft>): void => setD((x) => ({ ...x, ...p }));
  const save = useMutation({
    mutationFn: () => connApi.update(api, conn.id, bodyOf(d, true)),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['connections'] });
      toast.push({ kind: 'success', title: `${d.name.trim()} saved` });
      onClose();
    },
  });
  const valid = d.name.trim() !== '' && d.protocols.length > 0 && streamStepValid(d);
  return (
    <Modal
      title={`Edit ${conn.name}`}
      onClose={onClose}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={save.isPending} disabled={!valid} onClick={() => save.mutate()}>
            Save
          </Button>
        </>
      }
    >
      <div className={l.stack}>
        <div className={s.wizardGrid}>
          <Field label="Name">
            {(id) => (
              <TextInput
                id={id}
                value={d.name}
                maxLength={128}
                onChange={(e) => set({ name: e.target.value })}
                data-autofocus
              />
            )}
          </Field>
          <Field label="Description">
            {(id) => (
              <TextInput
                id={id}
                value={d.description}
                onChange={(e) => set({ description: e.target.value })}
              />
            )}
          </Field>
        </div>
        <div role="group" aria-label="Protocols" className={l.row}>
          {PROTOCOLS.map((p) => (
            <Checkbox
              key={p}
              checked={d.protocols.includes(p)}
              onChange={(on) =>
                set({ protocols: on ? [...d.protocols, p] : d.protocols.filter((x) => x !== p) })
              }
            >
              <span className={s.mono}>{PROTOCOL_LABEL[p]}</span>
            </Checkbox>
          ))}
        </div>
        <div className={s.wizardGrid}>
          <StreamFields d={d} set={set} streams={streams.data} />
        </div>
        <RestrictionFields d={d} set={set} />
        <p className={s.footnote}>
          The key is unchanged; rotate it from the table. Changes apply to the next request.
        </p>
        {save.isError ? <Notice kind="danger">{errorMessage(save.error)}</Notice> : null}
      </div>
    </Modal>
  );
}

function RotateDialog({ conn, onClose }: { conn: Connection; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const [grace, setGrace] = useState('60');
  const rotate = useMutation({
    mutationFn: () => connApi.rotate(api, conn.id, Number(grace)),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['connections'] }),
  });
  const n = Number(grace);
  const key = rotate.data?.key;
  return (
    <Modal
      title={`Rotate key of ${conn.name}`}
      onClose={onClose}
      footer={
        key === undefined ? (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button
              variant="primary"
              busy={rotate.isPending}
              disabled={!Number.isInteger(n) || n < 0 || n > MAX_GRACE_MINUTES}
              onClick={() => rotate.mutate()}
            >
              Rotate key
            </Button>
          </>
        ) : (
          <Button variant="primary" onClick={onClose}>
            I saved the key
          </Button>
        )
      }
    >
      {key === undefined ? (
        <div className={l.stack}>
          <Field label="Grace period (minutes)" help="The old key keeps working this long (0–1440).">
            {(id) => (
              <TextInput
                id={id}
                type="number"
                min={0}
                max={MAX_GRACE_MINUTES}
                value={grace}
                onChange={(e) => setGrace(e.target.value)}
              />
            )}
          </Field>
          {rotate.isError ? <Notice kind="danger">{errorMessage(rotate.error)}</Notice> : null}
        </div>
      ) : (
        <div className={s.keyPanel}>
          <div className={s.keyTitle}>
            <Lock size={14} aria-hidden="true" /> New key — shown once
          </div>
          <div className={s.keyValue}>{key}</div>
          <Button
            variant="primary"
            size="sm"
            icon={<Copy size={14} />}
            onClick={() =>
              void copyText(key).then((ok) =>
                toast.push({ kind: ok ? 'success' : 'warning', title: ok ? 'Key copied' : 'Copy failed' }),
              )
            }
          >
            Copy key
          </Button>
        </div>
      )}
    </Modal>
  );
}

export default function ConnectionsPage() {
  const api = useApi();
  const qc = useQueryClient();
  const { now } = useServices();
  const [wizard, setWizard] = useState(false);
  const [editing, setEditing] = useState<Connection | null>(null);
  const [rotating, setRotating] = useState<Connection | null>(null);
  const [confirm, setConfirm] = useState<{ conn: Connection; action: 'revoke' | 'delete' } | null>(null);
  const list = useQuery({ queryKey: ['connections'], queryFn: () => connApi.list(api) });
  useTopbarStatus(list.data === undefined ? null : `${list.data.length} connections`);
  const act = useMutation({
    mutationFn: async (c: { conn: Connection; action: 'revoke' | 'delete' }): Promise<void> => {
      if (c.action === 'revoke') await connApi.revoke(api, c.conn.id);
      else await connApi.remove(api, c.conn.id);
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: ['connections'] });
      setConfirm(null);
    },
  });
  return (
    <div className={s.page}>
      <div className={s.head}>
        <h1>Connections</h1>
        <span className={l.subtle}>One key per connection · keys are shown once and stored hashed</span>
        <span className={s.headActions}>
          <Button variant="ghost" icon={<Plus size={14} />} onClick={() => setWizard(true)} disabled={wizard}>
            New connection
          </Button>
        </span>
      </div>
      {list.isError ? <Notice kind="danger">{errorMessage(list.error)}</Notice> : null}
      {list.isPending ? (
        <Spinner />
      ) : (list.data ?? []).length === 0 && !wizard ? (
        <EmptyState
          icon={<Lock size={20} aria-hidden="true" />}
          title="No connections yet"
          hint="Every client needs a connection key to send data."
          action={
            <Button size="sm" onClick={() => setWizard(true)}>
              Create connection
            </Button>
          }
        />
      ) : (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={l.table}>
            <thead>
              <tr>
                <th>Name</th>
                <th>Protocols</th>
                <th>Stream</th>
                <th>Last used</th>
                <th>Key prefix</th>
                <th>Restrictions</th>
                <th>Status</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {(list.data ?? []).map((c) => {
                const st = connectionStatus(c, now());
                return (
                  <tr key={c.id}>
                    <td className={s.strong}>{c.name}</td>
                    <td>
                      <span className={s.tags}>
                        {c.protocols.map((p) => (
                          <Tag key={p}>{PROTOCOL_LABEL[p] ?? p}</Tag>
                        ))}
                      </span>
                    </td>
                    <td className={l.monoCell}>{c.defaultStream}</td>
                    <td>
                      {c.lastUsedAt === null ? (
                        <span className={l.subtle}>never</span>
                      ) : (
                        formatAge(Date.parse(c.lastUsedAt), now())
                      )}
                      {c.lastUsedIp !== '' ? <span className={l.subtle}> · {c.lastUsedIp}</span> : null}
                    </td>
                    <td className={l.monoCell}>{c.keyPrefix}…</td>
                    <td className={l.muted}>{restrictionsText(c)}</td>
                    <td>
                      <StatusChip variant={st.variant}>{st.label}</StatusChip>
                    </td>
                    <td className={s.rowActions}>
                      {c.revokedAt === null ? (
                        <>
                          <Button
                            size="sm"
                            variant="ghost"
                            iconOnly
                            icon={<Pencil size={14} />}
                            aria-label={`Edit ${c.name}`}
                            title="Edit"
                            onClick={() => setEditing(c)}
                          />
                          <Button
                            size="sm"
                            variant="ghost"
                            iconOnly
                            icon={<RotateCw size={14} />}
                            aria-label={`Rotate key of ${c.name}`}
                            title="Rotate"
                            onClick={() => setRotating(c)}
                          />
                          <Button
                            size="sm"
                            variant="ghost"
                            iconOnly
                            icon={<ShieldOff size={14} />}
                            aria-label={`Revoke ${c.name}`}
                            title="Revoke"
                            onClick={() => setConfirm({ conn: c, action: 'revoke' })}
                          />
                        </>
                      ) : null}
                      <Button
                        size="sm"
                        variant="ghost"
                        iconOnly
                        icon={<Trash2 size={14} />}
                        aria-label={`Delete ${c.name}`}
                        title="Delete"
                        onClick={() => setConfirm({ conn: c, action: 'delete' })}
                      />
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
      {wizard ? <Wizard onClose={() => setWizard(false)} /> : null}
      {editing !== null ? <EditDialog conn={editing} onClose={() => setEditing(null)} /> : null}
      {rotating !== null ? <RotateDialog conn={rotating} onClose={() => setRotating(null)} /> : null}
      {confirm !== null ? (
        <Modal
          title={confirm.action === 'revoke' ? `Revoke ${confirm.conn.name}` : `Delete ${confirm.conn.name}`}
          onClose={() => setConfirm(null)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirm(null)}>
                Cancel
              </Button>
              <Button variant="dangerSolid" busy={act.isPending} onClick={() => act.mutate(confirm)}>
                {confirm.action === 'revoke' ? 'Revoke key' : 'Delete connection'}
              </Button>
            </>
          }
        >
          <p>
            {confirm.action === 'revoke'
              ? 'Clients using this key are rejected immediately. This cannot be undone; create a new connection to resume.'
              : 'The connection and its key are removed. Data already ingested is kept.'}
          </p>
          {act.isError ? <Notice kind="danger">{errorMessage(act.error)}</Notice> : null}
        </Modal>
      ) : null}
    </div>
  );
}
