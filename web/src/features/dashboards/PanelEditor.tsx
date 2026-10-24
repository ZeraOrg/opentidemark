// SPDX-License-Identifier: Apache-2.0
//
// Panel editor (SPEC §13.2, DESIGN §9.6): Builder (source → filter → measure → group by →
// interval → visualization) and Query (raw TQL) tabs, a live preview debounced by 400 ms and the
// panel options (title, unit, legend, style, stack, thresholds).

import { BarChart3, Gauge, LineChart, PieChart, Table, SquareSigma, Plus, X } from 'lucide-react';
import { useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router-dom';
import type {
  Dashboard,
  DashboardDoc,
  Panel,
  PanelOptions,
  PanelType,
  ThresholdColor,
} from '../../api/types';
import {
  Button,
  Field,
  RemovableChip,
  Segmented,
  Select,
  StatusChip,
  TextArea,
  TextInput,
  Toggle,
} from '../../components/controls';
import { Card, layoutStyles as l, Notice, Tabs } from '../../components/layout';
import { QueryEditor } from '../../components/QueryEditor';
import { TqlText } from '../../components/TqlText';
import { cx } from '../../lib/cx';
import { useDebounced } from '../../lib/hooks';
import type { TimeRange } from '../../lib/time';
import {
  builderProblem,
  emptyModel,
  fromTql,
  INTERVALS,
  MAX_GROUP_BY,
  MAX_MEASURES,
  MEASURE_FUNCTIONS,
  nextMeasure,
  toTql,
  withFn,
  type BuilderModel,
  type Interval,
  type Measure,
  type MeasureFn,
} from '../../tql/builder';
import { variablesOf } from '../../tql/tokenize';
import { useQueryCheck } from '../query/useQueryCheck';
import { PanelBody, PanelFrame } from './PanelView';
import s from './dashboards.module.css';

export const PREVIEW_DEBOUNCE_MS = 400;
const PREVIEW_HEIGHT = 420;
type EditorTab = 'builder' | 'query';

const VIZ: readonly { value: PanelType; label: string; icon: ReactNode }[] = [
  { value: 'timeseries', label: 'Time series', icon: <LineChart size={18} aria-hidden="true" /> },
  { value: 'bar', label: 'Bar', icon: <BarChart3 size={18} aria-hidden="true" /> },
  { value: 'table', label: 'Table', icon: <Table size={18} aria-hidden="true" /> },
  { value: 'stat', label: 'Stat', icon: <SquareSigma size={18} aria-hidden="true" /> },
  { value: 'pie', label: 'Pie', icon: <PieChart size={18} aria-hidden="true" /> },
  { value: 'gauge', label: 'Gauge', icon: <Gauge size={18} aria-hidden="true" /> },
];

const COLOR_OPTIONS: readonly { value: ThresholdColor; label: string }[] = (
  [
    'danger',
    'warning',
    'success',
    'info',
    'accent',
    'chart1',
    'chart2',
    'chart3',
    'chart4',
    'chart5',
    'chart6',
    'chart7',
    'chart8',
  ] as const
).map((c) => ({ value: c, label: c }));

const UNITS = ['short', 'none', 'ms', 's', '%', 'bytes', 'req/s', '/s', 'k/min'].map((u) => ({
  value: u,
  label: u,
}));

function Step({ n, title, hint, children }: { n: number; title: string; hint: string; children: ReactNode }) {
  return (
    <div className={s.step}>
      <span className={s.stepNum} aria-hidden="true">
        {n}
      </span>
      <div>
        <div className={s.stepTitle}>{title}</div>
        <div className={s.stepHint}>{hint}</div>
      </div>
      <div className={s.stepBody}>{children}</div>
    </div>
  );
}

/** The argument of one measure: a field, the quantile of `histogram_quantile`, none for `hist_mean`. */
function MeasureArgument({
  n,
  measure,
  onChange,
}: {
  n: number;
  measure: Measure;
  onChange: (field: string) => void;
}) {
  if (measure.fn === 'hist_mean') {
    return <span className={l.subtle}>mean of the recorded values</span>;
  }
  if (measure.fn === 'histogram_quantile') {
    return (
      <span className={s.measureArg}>
        <span className={l.subtle}>quantile</span>
        <TextInput
          aria-label={`Measure ${n} quantile`}
          mono
          type="number"
          inputMode="decimal"
          min={0}
          max={1}
          step="any"
          placeholder="0.95"
          className={s.quantileInput}
          value={measure.field}
          onChange={(e) => onChange(e.target.value)}
        />
      </span>
    );
  }
  return (
    <span className={s.measureArg}>
      <span className={l.subtle}>of</span>
      <TextInput
        aria-label={`Measure ${n} field`}
        mono
        placeholder="—"
        className={s.measureField}
        value={measure.field}
        onChange={(e) => onChange(e.target.value)}
      />
    </span>
  );
}

function Builder({
  model,
  onChange,
  readOnly,
}: {
  model: BuilderModel;
  onChange: (m: BuilderModel) => void;
  readOnly: boolean;
}) {
  const [groupDraft, setGroupDraft] = useState('');
  const set = (p: Partial<BuilderModel>): void => onChange({ ...model, ...p });
  const setMeasure = (i: number, next: (m: Measure) => Measure): void =>
    set({ measures: model.measures.map((x, j) => (j === i ? next(x) : x)) });
  const vars = variablesOf(model.filter);
  const problem = builderProblem(model);
  return (
    <fieldset className={s.builder} disabled={readOnly}>
      <Step n={1} title="Source" hint="logs · spans · metrics">
        <Segmented
          label="Source"
          value={model.source}
          onChange={(source) => set({ source })}
          options={[
            { value: 'logs', label: 'logs' },
            { value: 'spans', label: 'spans' },
            { value: 'metrics', label: 'metrics' },
          ]}
        />
      </Step>
      <Step n={2} title="Filter" hint="TQL with autocomplete">
        <QueryEditor
          value={model.filter}
          onChange={(filter) => set({ filter })}
          source={model.source}
          ariaLabel="Panel filter"
          size="sm"
          showIcon={false}
          readOnly={readOnly}
          trailing={vars.map((v) => (
            <span key={v} className={s.varTag}>
              ${v}
            </span>
          ))}
        />
      </Step>
      <Step n={3} title="Measure" hint={`up to ${MAX_MEASURES}`}>
        <div className={l.stack}>
          {model.measures.map((m, i) => (
            <div key={i} className={s.measureRow}>
              <Select
                aria-label={`Measure ${i + 1} function`}
                className={s.measureFn}
                options={MEASURE_FUNCTIONS.map((f) => ({ value: f, label: f }))}
                value={m.fn}
                onChange={(e) => setMeasure(i, (x) => withFn(x, e.target.value as MeasureFn))}
              />
              <MeasureArgument
                n={i + 1}
                measure={m}
                onChange={(field) => setMeasure(i, (x) => ({ ...x, field }))}
              />
              {model.measures.length > 1 ? (
                <Button
                  size="sm"
                  variant="ghost"
                  iconOnly
                  icon={<X size={14} />}
                  aria-label={`Remove measure ${i + 1}`}
                  onClick={() => set({ measures: model.measures.filter((_, j) => j !== i) })}
                />
              ) : null}
            </div>
          ))}
          {model.measures.length < MAX_MEASURES ? (
            <div>
              <Button
                size="sm"
                variant="ghost"
                icon={<Plus size={14} />}
                onClick={() => set({ measures: [...model.measures, nextMeasure(model.measures)] })}
              >
                measure
              </Button>
            </div>
          ) : null}
          {problem !== null ? (
            <Notice kind="warning" className={s.measureProblem}>
              {problem}
            </Notice>
          ) : null}
        </div>
      </Step>
      <Step n={4} title="Group by" hint={`≤ ${MAX_GROUP_BY} fields`}>
        <div className={l.row}>
          {model.groupBy.map((g) => (
            <RemovableChip
              key={g}
              label={g}
              onRemove={() => set({ groupBy: model.groupBy.filter((x) => x !== g) })}
            >
              {g}
            </RemovableChip>
          ))}
          {model.groupBy.length < MAX_GROUP_BY ? (
            <TextInput
              aria-label="Add group-by field"
              mono
              placeholder="add field…"
              value={groupDraft}
              className={s.groupInput}
              onChange={(e) => setGroupDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && groupDraft.trim() !== '') {
                  e.preventDefault();
                  set({ groupBy: [...model.groupBy, groupDraft.trim()] });
                  setGroupDraft('');
                }
              }}
            />
          ) : null}
        </div>
      </Step>
      <Step n={5} title="Interval" hint="span">
        <Segmented<Interval>
          label="Interval"
          value={model.interval}
          onChange={(interval) => set({ interval })}
          options={INTERVALS.map((v) => ({ value: v, label: v }))}
        />
      </Step>
      <Step n={6} title="Visualization" hint="panel type">
        <div className={s.vizGrid} role="radiogroup" aria-label="Visualization">
          {VIZ.map((v) => (
            <button
              type="button"
              key={v.value}
              role="radio"
              aria-checked={model.viz === v.value}
              className={s.vizTile}
              onClick={() => set({ viz: v.value })}
            >
              {v.icon}
              {v.label}
            </button>
          ))}
        </div>
      </Step>
    </fieldset>
  );
}

function OptionsCard({ panel, onChange }: { panel: Panel; onChange: (p: Panel) => void }) {
  const o = panel.options ?? {};
  const setO = (p: Partial<PanelOptions>): void => onChange({ ...panel, options: { ...o, ...p } });
  const thresholds = o.thresholds ?? [];
  return (
    <Card title="Panel options">
      <div className={s.optionsGrid}>
        <Field label="Title" className={s.optTitle}>
          {(id) => (
            <TextInput
              id={id}
              value={panel.title}
              maxLength={200}
              onChange={(e) => onChange({ ...panel, title: e.target.value })}
            />
          )}
        </Field>
        <Field label="Unit">
          {(id) => (
            <Select
              id={id}
              options={UNITS}
              value={o.unit ?? 'short'}
              onChange={(e) => setO({ unit: e.target.value })}
            />
          )}
        </Field>
        <Field label="Legend">
          {(id) => (
            <Select
              id={id}
              options={['bottom', 'right', 'hidden'].map((v) => ({ value: v, label: v }))}
              value={o.legend ?? 'bottom'}
              onChange={(e) => setO({ legend: e.target.value as PanelOptions['legend'] })}
            />
          )}
        </Field>
        <Field label="Style">
          {() => (
            <Segmented
              label="Style"
              value={o.style ?? 'line'}
              onChange={(style) => setO({ style })}
              options={[
                { value: 'line', label: 'Line' },
                { value: 'area', label: 'Area' },
                { value: 'bars', label: 'Bars' },
              ]}
            />
          )}
        </Field>
        <Field label="Stack">
          {() => (
            <Toggle checked={o.stack === true} onChange={(stack) => setO({ stack })}>
              Stacked
            </Toggle>
          )}
        </Field>
        <Field label="Thresholds" className={s.optThresholds}>
          {() => (
            <div className={l.stack}>
              {thresholds.map((t, i) => (
                <div key={i} className={s.thresholdRow}>
                  <TextInput
                    aria-label={`Threshold ${i + 1} value`}
                    type="number"
                    value={String(t.value)}
                    onChange={(e) =>
                      setO({
                        thresholds: thresholds.map((x, j) =>
                          j === i ? { ...x, value: Number(e.target.value) } : x,
                        ),
                      })
                    }
                  />
                  <Select
                    aria-label={`Threshold ${i + 1} colour`}
                    options={COLOR_OPTIONS}
                    value={t.color}
                    onChange={(e) =>
                      setO({
                        thresholds: thresholds.map((x, j) =>
                          j === i ? { ...x, color: e.target.value as ThresholdColor } : x,
                        ),
                      })
                    }
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    iconOnly
                    icon={<X size={14} />}
                    aria-label={`Remove threshold ${i + 1}`}
                    onClick={() => setO({ thresholds: thresholds.filter((_, j) => j !== i) })}
                  />
                </div>
              ))}
              <div>
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<Plus size={14} />}
                  onClick={() => setO({ thresholds: [...thresholds, { value: 100, color: 'danger' }] })}
                >
                  add
                </Button>
              </div>
            </div>
          )}
        </Field>
      </div>
    </Card>
  );
}

export function PanelEditor({
  dashboard,
  panel: initial,
  doc,
  vars,
  range,
  onApply,
  onDiscard,
}: {
  dashboard: Dashboard;
  panel: Panel;
  doc: DashboardDoc;
  vars: Record<string, string[]>;
  range: TimeRange;
  onApply: (p: Panel) => void;
  onDiscard: () => void;
}) {
  const [panel, setPanel] = useState<Panel>(initial);
  const parsed = useMemo(() => fromTql(initial.query ?? '', initial.type), [initial]);
  const [model, setModel] = useState<BuilderModel>(parsed ?? { ...emptyModel(), viz: initial.type });
  const [tab, setTab] = useState<EditorTab>(
    parsed !== null || initial.type === 'markdown' ? 'builder' : 'query',
  );
  const [builderLocked, setBuilderLocked] = useState(parsed === null);
  const check = useQueryCheck(panel.query ?? '', panel.type !== 'markdown');
  const previewPanel = useDebounced(panel, PREVIEW_DEBOUNCE_MS);
  const setBuilder = (m: BuilderModel): void => {
    setModel(m);
    setPanel({ ...panel, type: m.viz, query: toTql(m) });
  };
  const switchTab = (t: EditorTab): void => {
    if (t === 'builder') {
      const m = fromTql(panel.query ?? '', panel.type);
      setBuilderLocked(m === null);
      if (m !== null) setModel(m);
    }
    setTab(t);
  };
  const kind = check.kind ?? (panel.type === 'markdown' ? null : 'timeseries');
  const problem =
    tab === 'builder' && !builderLocked && panel.type !== 'markdown' ? builderProblem(model) : null;
  return (
    <div className={s.page}>
      <div className={s.head}>
        <nav className={l.breadcrumb} aria-label="Breadcrumb">
          <Link to="/dashboards">Dashboards</Link> /
          <Link to={`/dashboards/${encodeURIComponent(dashboard.id)}/edit`}>{dashboard.name}</Link> /
        </nav>
        <h1 className={s.title}>Edit panel</h1>
        {kind !== null ? <StatusChip variant="info">{kind}</StatusChip> : null}
        <span className={s.headActions}>
          <span className={s.note}>Preview updates 400 ms after each change</span>
          <Button variant="highlight" onClick={onDiscard}>
            Discard
          </Button>
          <Button
            variant="primary"
            disabled={check.error !== null || problem !== null || panel.title.trim() === ''}
            onClick={() => onApply(panel)}
          >
            Apply
          </Button>
        </span>
      </div>
      <div className={s.editorCols}>
        <section className={cx(l.card, s.editorCard)}>
          <div className={s.editorTabs}>
            <Tabs<EditorTab>
              label="Panel editor"
              value={tab}
              onChange={switchTab}
              items={[
                { value: 'builder', label: 'Builder' },
                { value: 'query', label: 'Query' },
              ]}
            />
            <span className={s.note}>Builder → Query is lossless</span>
          </div>
          <div className={s.editorBody}>
            {panel.type === 'markdown' ? (
              <Field label="Markdown">
                {(id) => (
                  <TextArea
                    id={id}
                    rows={14}
                    value={panel.options?.content ?? ''}
                    onChange={(e) =>
                      setPanel({ ...panel, options: { ...panel.options, content: e.target.value } })
                    }
                  />
                )}
              </Field>
            ) : tab === 'builder' ? (
              <>
                {builderLocked ? (
                  <Notice kind="info">
                    This query does not fit the builder shape, so the Builder is read-only. Edit it in the
                    Query tab.
                  </Notice>
                ) : null}
                <Builder model={model} onChange={setBuilder} readOnly={builderLocked} />
                <div className={l.sectionLabel}>Generated TQL</div>
                <div className={s.generated}>
                  <TqlText q={panel.query ?? ''} />
                  <button type="button" className={s.linkButton} onClick={() => switchTab('query')}>
                    Edit in Query tab
                  </button>
                </div>
              </>
            ) : (
              <div className={l.stack}>
                <QueryEditor
                  value={panel.query ?? ''}
                  onChange={(query) => setPanel({ ...panel, query })}
                  ariaLabel="Panel query"
                  error={check.error}
                />
                <Field label="Visualization">
                  {(id) => (
                    <Select
                      id={id}
                      options={[
                        ...VIZ.map((v) => ({ value: v.value, label: v.label })),
                        { value: 'logs', label: 'Logs' },
                        { value: 'traces', label: 'Traces' },
                      ]}
                      value={panel.type}
                      onChange={(e) => setPanel({ ...panel, type: e.target.value as PanelType })}
                    />
                  )}
                </Field>
                <div className={l.subtle}>
                  Variables: {(doc.variables ?? []).map((v) => `$${v.name}`).join(', ') || 'none'}
                </div>
              </div>
            )}
          </div>
        </section>
        <div className={s.editorRight}>
          <PanelFrame
            panel={{ ...previewPanel, title: `${previewPanel.title}` }}
            className={s.preview}
            actions={<span className={s.note}>live preview</span>}
          >
            <PanelBody
              dashboardId={dashboard.id}
              panel={previewPanel}
              vars={vars}
              range={range}
              preview
              height={PREVIEW_HEIGHT}
            />
          </PanelFrame>
          <OptionsCard panel={panel} onChange={setPanel} />
        </div>
      </div>
    </div>
  );
}
