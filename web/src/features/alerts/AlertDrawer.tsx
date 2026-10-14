// SPDX-License-Identifier: Apache-2.0
//
// Alert rule editor drawer (SPEC §14.1, §15.10, §16.4, DESIGN §9.7): name, query with validation,
// the streams the rule reads (every stream, or chosen ones; required for editors who see only
// some streams), condition / threshold / window / for, a live preview of the last 3 hours with
// the threshold line and the region where the condition holds, severity, no-data policy, group
// by, channels, test, mute and delete.

import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { BellOff, ChevronDown, FlaskConical } from 'lucide-react';
import { useMemo, useState } from 'react';
import { errorMessage } from '../../api/client';
import { alerts as alertsApi, channels as channelsApi, search } from '../../api/endpoints';
import type { Alert, AlertTestResult, ConditionOp, NoDataPolicy, RuleDoc, Severity } from '../../api/types';
import { useVisibleStreams } from '../../app/scope';
import { useApi, useServices } from '../../app/services';
import { useMe } from '../../app/session';
import { Chart, conditionHolds } from '../../components/Chart';
import {
  Button,
  Checkbox,
  Field,
  RemovableChip,
  Segmented,
  Select,
  Spinner,
  StatusChip,
  Tag,
  TextInput,
} from '../../components/controls';
import { layoutStyles as l, Notice } from '../../components/layout';
import { MultiSelect } from '../../components/MultiSelect';
import { Drawer, DrawerFooter, Modal, Popover, useToast } from '../../components/overlay';
import { QueryEditor } from '../../components/QueryEditor';
import { parseGoDuration, shortDuration } from '../../lib/duration';
import { formatNumber } from '../../lib/format';
import { timeseriesToChart } from '../../lib/series';
import { formatClock } from '../../lib/time';
import { useQueryCheck } from '../query/useQueryCheck';
import { streamNameProblem } from '../settings/TeamPage';
import {
  EVERY,
  FOR,
  isMuted,
  newRule,
  PREVIEW_RANGE,
  previewQuery,
  stateChip,
  WINDOWS,
  withGroupBy,
} from './rules';
import s from './alerts.module.css';

const OPS: readonly ConditionOp[] = ['>', '>=', '<', '<=', '==', '!='];
const HOUR_MS = 3_600_000;

const durationOptions = (xs: readonly string[], current: string) => {
  const cur = shortDuration(current);
  const all = xs.includes(cur) ? xs : [cur, ...xs];
  return all.map((v) => ({ value: v, label: v }));
};

/** First time (ms) from which the condition held until the end of the preview, if it holds now. */
export function holdingSince(
  x: readonly number[],
  values: readonly (number | null)[],
  op: ConditionOp,
  threshold: number,
): number | null {
  let since: number | null = null;
  for (let i = 0; i < x.length; i++) {
    const v = values[i];
    if (v !== null && v !== undefined && conditionHolds(v, op, threshold)) since ??= x[i] ?? null;
    else since = null;
  }
  return since;
}

function Preview({ doc }: { doc: RuleDoc }) {
  const api = useApi();
  const q = previewQuery(doc.query, doc.every);
  const res = useQuery({
    queryKey: ['query', q, PREVIEW_RANGE.from, PREVIEW_RANGE.to, 'alert-preview'],
    queryFn: ({ signal }) => search.query(api, { q, ...PREVIEW_RANGE }, signal),
    enabled: doc.query.trim() !== '',
  });
  const data = useMemo(
    () => (res.data?.kind === 'timeseries' ? timeseriesToChart(res.data) : null),
    [res.data],
  );
  const first = data?.series[0];
  const since =
    data === null || first === undefined
      ? null
      : holdingSince(data.x, first.values, doc.condition.op, doc.condition.threshold);
  return (
    <section className={s.preview} aria-label="Preview">
      <header className={s.previewHead}>
        <strong>Preview</strong>
        <span className={l.subtle}>last 3 h · evaluated every {shortDuration(doc.every)}</span>
        {since !== null ? (
          <span className={s.since}>above threshold since {formatClock(since, false).slice(0, 5)} →</span>
        ) : null}
      </header>
      {res.isPending ? <Spinner /> : null}
      {res.isError ? <div className={s.previewError}>{errorMessage(res.error)}</div> : null}
      {data !== null ? (
        <Chart
          data={data}
          height={170}
          ariaLabel="Alert preview"
          style={data.series.length > 1 ? 'line' : 'area'}
          legend={data.series.length > 1 ? 'bottom' : 'hidden'}
          colors={data.series.map((_, i) => (i === 0 ? 1 : i + 1))}
          thresholds={[
            {
              value: doc.condition.threshold,
              color: 'danger',
              label: `threshold ${formatNumber(doc.condition.threshold)}`,
            },
          ]}
          shade={{ op: doc.condition.op, value: doc.condition.threshold }}
        />
      ) : null}
    </section>
  );
}

function ChannelPicker({ value, onChange }: { value: string[]; onChange: (v: string[]) => void }) {
  const api = useApi();
  const list = useQuery({ queryKey: ['channels'], queryFn: () => channelsApi.list(api) });
  const byId = new Map((list.data ?? []).map((c) => [c.id, c]));
  return (
    <Popover
      className={s.channelMenu}
      trigger={({ open, toggle }) => (
        <button
          type="button"
          className={s.multi}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-label="Channels"
          onClick={toggle}
        >
          {value.length === 0 ? (
            <span className={l.subtle}>No channels</span>
          ) : (
            value.map((id) => <Tag key={id}>{byId.get(id)?.name ?? id}</Tag>)
          )}
          <ChevronDown size={14} aria-hidden="true" className={s.chev} />
        </button>
      )}
    >
      <div role="listbox" aria-multiselectable="true" aria-label="Channels" className={l.stack}>
        {(list.data ?? []).length === 0 ? (
          <span className={l.subtle}>No channels yet (Settings → Channels).</span>
        ) : null}
        {(list.data ?? []).map((c) => (
          <Checkbox
            key={c.id}
            checked={value.includes(c.id)}
            onChange={(on) => onChange(on ? [...value, c.id] : value.filter((x) => x !== c.id))}
          >
            {c.name} <span className={l.subtle}>{c.kind}</span>
          </Checkbox>
        ))}
      </div>
    </Popover>
  );
}

/** Why a streams choice cannot be saved (SPEC §14.1), or null. */
export function streamsProblem(streams: readonly string[] | null, allStreams: boolean): string | null {
  if (streams === null) return allStreams ? null : 'Choose at least one of the streams you can see.';
  if (streams.length === 0) return 'Choose at least one stream, or read every stream.';
  return null;
}

function StreamsField({
  value,
  onChange,
}: {
  value: string[] | null;
  onChange: (v: string[] | null) => void;
}) {
  const me = useMe();
  const { names, all, loading } = useVisibleStreams();
  const items = (names ?? []).map((n) => ({ value: n, label: n }));
  const problem = streamsProblem(value, me.allStreams);
  return (
    <Field
      label="Streams"
      help={
        all
          ? 'Every stream, or only the chosen ones. A rule limited to streams is seen only by users who see all of them.'
          : 'The rule reads only streams you can see.'
      }
      error={problem ?? undefined}
    >
      {(id) => (
        <div className={s.streamsField}>
          {all ? (
            <Checkbox checked={value === null} onChange={(on) => onChange(on ? null : [])}>
              All streams
            </Checkbox>
          ) : null}
          {value !== null ? (
            <MultiSelect
              id={id}
              label="Streams"
              mono
              items={items}
              value={value}
              onChange={onChange}
              loading={loading}
              placeholder="Choose streams"
              emptyText="No streams yet."
              custom={
                all
                  ? { validate: streamNameProblem, placeholder: 'stream name', note: 'no data yet' }
                  : undefined
              }
            />
          ) : null}
        </div>
      )}
    </Field>
  );
}

function TestResult({ r }: { r: AlertTestResult }) {
  return (
    <Notice kind={r.error !== undefined ? 'danger' : 'info'}>
      Evaluated now: <strong>{r.state}</strong>
      {r.error !== undefined ? ` · ${r.error}` : ''}
      {r.samples.length > 0 ? ` · ${r.samples.map((x) => formatNumber(x.value)).join(', ')}` : ''}
      {r.transitions.length > 0
        ? ` · would transition ${r.transitions.map((t) => `${t.from} → ${t.to}`).join(', ')}`
        : ''}
    </Notice>
  );
}

export function AlertDrawer({ alert, onClose }: { alert: Alert | null; onClose: () => void }) {
  const api = useApi();
  const qc = useQueryClient();
  const toast = useToast();
  const { now } = useServices();
  const me = useMe();
  const init = alert ?? { ...newRule(), id: '', version: 0 };
  const [name, setName] = useState(init.name);
  // A new rule reads the editor's whole scope: every stream, or all the visible ones.
  const [streams, setStreams] = useState<string[] | null>(
    alert === null ? (me.allStreams ? null : [...me.streams]) : alert.streams,
  );
  const [doc, setDoc] = useState<RuleDoc>({
    ...init.doc,
    window: shortDuration(init.doc.window),
    every: shortDuration(init.doc.every),
    for: shortDuration(init.doc.for),
  });
  const [groupDraft, setGroupDraft] = useState('');
  const [confirmDelete, setConfirmDelete] = useState(false);
  const check = useQueryCheck(doc.query);
  const set = (p: Partial<RuleDoc>): void => setDoc((d) => ({ ...d, ...p }));
  const done = (title: string): void => {
    void qc.invalidateQueries({ queryKey: ['alerts'] });
    toast.push({ kind: 'success', title });
  };
  const save = useMutation({
    mutationFn: () =>
      alert === null
        ? alertsApi.create(api, { name: name.trim(), enabled: true, doc, streams })
        : alertsApi.update(api, alert.id, {
            name: name.trim(),
            enabled: alert.enabled,
            doc,
            streams,
            version: alert.version,
          }),
    onSuccess: () => {
      done('Rule saved');
      onClose();
    },
  });
  const mute = useMutation({
    mutationFn: (until: string | null) => alertsApi.mute(api, alert?.id ?? '', until),
    onSuccess: (a) => {
      done(
        a.doc.muteUntil === null
          ? 'Rule unmuted'
          : `Muted until ${formatClock(Date.parse(a.doc.muteUntil), false).slice(0, 5)}`,
      );
      onClose();
    },
  });
  const remove = useMutation({
    mutationFn: () => alertsApi.remove(api, alert?.id ?? ''),
    onSuccess: () => {
      done('Rule deleted');
      onClose();
    },
  });
  const test = useMutation({ mutationFn: () => alertsApi.test(api, alert?.id ?? '') });
  const chip = alert === null ? null : stateChip(alert, now());
  const muted = alert !== null && isMuted(alert, now());
  const forOk = parseGoDuration(doc.for) !== null;
  const invalid =
    name.trim() === '' ||
    check.error !== null ||
    !forOk ||
    !Number.isFinite(doc.condition.threshold) ||
    streamsProblem(streams, me.allStreams) !== null;
  return (
    <Drawer
      wide
      label={alert === null ? 'New rule' : `Edit rule ${alert.name}`}
      title={alert === null ? 'New rule' : 'Edit rule'}
      onClose={onClose}
      status={chip !== null ? <StatusChip variant={chip.variant}>{chip.label}</StatusChip> : undefined}
      headActions={
        alert !== null ? (
          <Button
            size="sm"
            variant="ghost"
            icon={<BellOff size={14} />}
            busy={mute.isPending}
            onClick={() => mute.mutate(muted ? null : new Date(now() + HOUR_MS).toISOString())}
          >
            {muted ? 'Unmute' : 'Mute 1 h'}
          </Button>
        ) : undefined
      }
      footer={
        <DrawerFooter
          left={
            alert !== null ? (
              <Button variant="danger" onClick={() => setConfirmDelete(true)}>
                Delete
              </Button>
            ) : undefined
          }
          right={
            <>
              {alert !== null ? (
                <Button
                  variant="ghost"
                  icon={<FlaskConical size={14} />}
                  busy={test.isPending}
                  onClick={() => test.mutate()}
                >
                  Test
                </Button>
              ) : null}
              <Button variant="ghost" onClick={onClose}>
                Cancel
              </Button>
              <Button
                variant="primary"
                busy={save.isPending}
                disabled={invalid}
                onClick={() => save.mutate()}
              >
                Save rule
              </Button>
            </>
          }
        />
      }
    >
      <Field label="Name">
        {(id) => (
          <TextInput
            id={id}
            value={name}
            maxLength={200}
            onChange={(e) => setName(e.target.value)}
            data-autofocus
          />
        )}
      </Field>
      <Field label="Query" note="must yield one numeric column">
        {() => (
          <QueryEditor
            value={doc.query}
            onChange={(query) => set({ query })}
            ariaLabel="Rule query"
            error={check.error}
            showIcon={false}
            scope={streams === null ? undefined : { streams }}
          />
        )}
      </Field>
      <StreamsField value={streams} onChange={setStreams} />
      <div className={s.conditionRow}>
        <Field label="Condition">
          {(id) => (
            <Select
              id={id}
              options={OPS.map((o) => ({ value: o, label: o }))}
              value={doc.condition.op}
              onChange={(e) => set({ condition: { ...doc.condition, op: e.target.value as ConditionOp } })}
            />
          )}
        </Field>
        <Field label="Threshold">
          {(id) => (
            <TextInput
              id={id}
              type="number"
              value={String(doc.condition.threshold)}
              onChange={(e) => set({ condition: { ...doc.condition, threshold: Number(e.target.value) } })}
            />
          )}
        </Field>
        <Field label="Window">
          {(id) => (
            <Select
              id={id}
              options={durationOptions(WINDOWS, doc.window)}
              value={shortDuration(doc.window)}
              onChange={(e) => set({ window: e.target.value })}
            />
          )}
        </Field>
        <Field label="For">
          {(id) => (
            <Select
              id={id}
              options={durationOptions(FOR, doc.for)}
              value={shortDuration(doc.for)}
              onChange={(e) => set({ for: e.target.value })}
            />
          )}
        </Field>
      </div>
      <Field label="Evaluate every">
        {(id) => (
          <Select
            id={id}
            options={durationOptions(EVERY, doc.every)}
            value={shortDuration(doc.every)}
            onChange={(e) => set({ every: e.target.value })}
          />
        )}
      </Field>
      <Preview doc={doc} />
      <div className={s.twoCols}>
        <Field label="Severity">
          {() => (
            <Segmented<Severity>
              label="Severity"
              value={doc.severity}
              onChange={(severity) => set({ severity })}
              options={[
                { value: 'info', label: 'info' },
                { value: 'warning', label: 'warning' },
                { value: 'critical', label: 'critical' },
              ]}
            />
          )}
        </Field>
        <Field label="No data">
          {() => (
            <Segmented<NoDataPolicy>
              label="No data"
              value={doc.noData}
              onChange={(noData) => set({ noData })}
              options={[
                { value: 'ok', label: 'ok' },
                { value: 'keep', label: 'keep' },
                { value: 'alerting', label: 'alerting' },
              ]}
            />
          )}
        </Field>
        <Field label="Group by">
          {() => (
            <div className={s.multi}>
              {doc.groupBy.map((g) => (
                <RemovableChip
                  key={g}
                  label={g}
                  onRemove={() =>
                    set({
                      groupBy: doc.groupBy.filter((x) => x !== g),
                      query: withGroupBy(
                        doc.query,
                        doc.groupBy.filter((x) => x !== g),
                      ),
                    })
                  }
                >
                  {g}
                </RemovableChip>
              ))}
              <input
                className={s.chipInput}
                aria-label="Add group-by field"
                placeholder={doc.groupBy.length === 0 ? 'add field…' : ''}
                value={groupDraft}
                onChange={(e) => setGroupDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && groupDraft.trim() !== '') {
                    e.preventDefault();
                    const next = [...doc.groupBy, groupDraft.trim()];
                    set({ groupBy: next, query: withGroupBy(doc.query, next) });
                    setGroupDraft('');
                  }
                }}
              />
            </div>
          )}
        </Field>
        <Field label="Channels">
          {() => <ChannelPicker value={doc.channels} onChange={(channels) => set({ channels })} />}
        </Field>
      </div>
      {alert !== null && alert.lastError !== '' ? (
        <Notice kind="danger">Last evaluation failed: {alert.lastError}</Notice>
      ) : null}
      {test.data !== undefined ? <TestResult r={test.data} /> : null}
      {[save, mute, remove, test].map((m, i) =>
        m.isError ? (
          <Notice key={i} kind="danger">
            {errorMessage(m.error)}
          </Notice>
        ) : null,
      )}
      {confirmDelete ? (
        <Modal
          title="Delete rule"
          onClose={() => setConfirmDelete(false)}
          footer={
            <>
              <Button variant="ghost" onClick={() => setConfirmDelete(false)}>
                Cancel
              </Button>
              <Button variant="dangerSolid" busy={remove.isPending} onClick={() => remove.mutate()}>
                Delete rule
              </Button>
            </>
          }
        >
          <p>
            Delete <strong>{alert?.name}</strong> and its history?
          </p>
        </Modal>
      ) : null}
    </Drawer>
  );
}
