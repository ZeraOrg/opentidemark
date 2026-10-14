// SPDX-License-Identifier: Apache-2.0
//
// Dashboard variables (SPEC §13.1, DESIGN §9.5): the variables bar with one picker per variable
// (field values from facets, custom options or free text; multi-select with "+n") and the editor
// used in edit mode.

import { useQuery } from '@tanstack/react-query';
import { ChevronDown, Plus, Trash2 } from 'lucide-react';
import { useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { search } from '../../api/endpoints';
import type { DashboardDoc, Source, Variable } from '../../api/types';
import { useDataScope } from '../../app/scope';
import { useApi } from '../../app/services';
import { patchParams, useTimeRange } from '../../app/url';
import { Button, Checkbox, Field, Select, Tag, TextInput } from '../../components/controls';
import { layoutStyles as l } from '../../components/layout';
import { Popover } from '../../components/overlay';
import { valuesLabel, variablePatch } from './vars';
import s from './dashboards.module.css';

const MAX_VARIABLES = 20;
const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;

function useOptions(v: Variable): string[] {
  const api = useApi();
  const { range } = useTimeRange();
  const scope = useDataScope();
  const facet = useQuery({
    queryKey: ['var-options', v.source, v.field, range.from, range.to, scope.key],
    queryFn: ({ signal }) =>
      search.facets(
        api,
        {
          q: v.source !== undefined && v.source !== 'logs' ? `source ${v.source}` : '',
          ...range,
          fields: [v.field ?? ''],
          top: 50,
          ...scope.params,
        },
        signal,
      ),
    enabled: v.type === 'field-values' && (v.field ?? '') !== '',
    staleTime: 60_000,
  });
  if (v.type === 'custom') return v.options ?? [];
  return (facet.data?.[0]?.values ?? []).map((x) => x.value);
}

function VariablePicker({ v, values }: { v: Variable; values: string[] }) {
  const [, setParams] = useSearchParams();
  const options = useOptions(v);
  const set = (next: string[]): void =>
    setParams((p) => patchParams(p, variablePatch(v, next)), { replace: true });
  if (v.type === 'text') {
    return (
      <label className={s.varBox}>
        <span className={s.varName}>${v.name}</span>
        <input
          className={s.varInput}
          value={values.join(',')}
          aria-label={v.label ?? v.name}
          onChange={(e) => set(e.target.value === '' ? [] : [e.target.value])}
        />
      </label>
    );
  }
  const all = ['*', ...options.filter((o) => o !== '*')];
  const extra = values.includes('*') ? 0 : Math.max(0, values.length - 1);
  return (
    <Popover
      trigger={({ open, toggle }) => (
        <button
          type="button"
          className={s.varBox}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-label={`${v.label ?? v.name}: ${values.join(', ')}`}
          onClick={toggle}
        >
          <span className={s.varName}>${v.name}</span>
          <span className={s.varValue}>{valuesLabel(values)}</span>
          {extra > 0 ? <Tag>+{extra}</Tag> : null}
          <ChevronDown size={14} aria-hidden="true" className={s.varChevron} />
        </button>
      )}
    >
      {(close) => (
        <div
          className={s.varMenu}
          role="listbox"
          aria-multiselectable={v.multi === true}
          aria-label={v.label ?? v.name}
        >
          {all.map((o) => {
            const checked = o === '*' ? values.includes('*') || values.length === 0 : values.includes(o);
            const label = o === '*' ? 'All' : o;
            if (v.multi === true) {
              return (
                <Checkbox
                  key={o}
                  checked={checked}
                  onChange={(on) => {
                    if (o === '*') set(['*']);
                    else {
                      const base = values.filter((x) => x !== '*' && x !== o);
                      set(on ? [...base, o] : base.length === 0 ? ['*'] : base);
                    }
                  }}
                >
                  {label}
                </Checkbox>
              );
            }
            return (
              <button
                type="button"
                key={o}
                role="option"
                aria-selected={checked}
                className={s.varOption}
                onClick={() => {
                  set([o]);
                  close();
                }}
              >
                {label}
              </button>
            );
          })}
        </div>
      )}
    </Popover>
  );
}

export function VariablesBar({ doc, values }: { doc: DashboardDoc; values: Record<string, string[]> }) {
  return (
    <div className={s.varsBar} role="group" aria-label="Variables">
      <span className={l.sectionLabel}>Variables</span>
      {(doc.variables ?? []).map((v) => (
        <VariablePicker key={v.name} v={v} values={values[v.name] ?? []} />
      ))}
      <span className={s.varsNote}>Variables substitute on the query AST · never string concatenation</span>
    </div>
  );
}

const TYPES = [
  { value: 'field-values', label: 'Field values' },
  { value: 'custom', label: 'Custom list' },
  { value: 'text', label: 'Text' },
];
const SOURCES = [
  { value: 'logs', label: 'logs' },
  { value: 'spans', label: 'spans' },
  { value: 'metrics', label: 'metrics' },
];

const splitList = (t: string): string[] =>
  t
    .split(',')
    .map((x) => x.trim())
    .filter((x) => x !== '');

export function VariablesEditor({
  variables,
  onChange,
}: {
  variables: Variable[];
  onChange: (v: Variable[]) => void;
}) {
  const [local, setLocal] = useState(variables);
  const set = (next: Variable[]): void => {
    setLocal(next);
    if (next.every((v) => NAME_RE.test(v.name))) onChange(next);
  };
  const patch = (i: number, p: Partial<Variable>): void =>
    set(local.map((v, j) => (j === i ? { ...v, ...p } : v)));
  return (
    <div className={l.stack}>
      {local.map((v, i) => (
        <div key={i} className={s.varEdit}>
          <Field label="Name" error={NAME_RE.test(v.name) ? undefined : 'letters, digits and _'}>
            {(id) => (
              <TextInput id={id} mono value={v.name} onChange={(e) => patch(i, { name: e.target.value })} />
            )}
          </Field>
          <Field label="Label">
            {(id) => (
              <TextInput
                id={id}
                value={v.label ?? ''}
                onChange={(e) => patch(i, { label: e.target.value })}
              />
            )}
          </Field>
          <Field label="Type">
            {(id) => (
              <Select
                id={id}
                options={TYPES}
                value={v.type}
                onChange={(e) => patch(i, { type: e.target.value as Variable['type'] })}
              />
            )}
          </Field>
          {v.type === 'field-values' ? (
            <>
              <Field label="Source">
                {(id) => (
                  <Select
                    id={id}
                    options={SOURCES}
                    value={v.source ?? 'logs'}
                    onChange={(e) => patch(i, { source: e.target.value as Source })}
                  />
                )}
              </Field>
              <Field label="Field">
                {(id) => (
                  <TextInput
                    id={id}
                    mono
                    value={v.field ?? ''}
                    onChange={(e) => patch(i, { field: e.target.value })}
                  />
                )}
              </Field>
            </>
          ) : null}
          {v.type === 'custom' ? (
            <Field label="Options" note="comma-separated">
              {(id) => (
                <TextInput
                  id={id}
                  value={(v.options ?? []).join(', ')}
                  onChange={(e) => patch(i, { options: splitList(e.target.value) })}
                />
              )}
            </Field>
          ) : null}
          <Field label="Default" note="comma-separated, * = all">
            {(id) => (
              <TextInput
                id={id}
                value={(v.default ?? []).join(', ')}
                onChange={(e) => patch(i, { default: splitList(e.target.value) })}
              />
            )}
          </Field>
          <Checkbox checked={v.multi === true} onChange={(multi) => patch(i, { multi })}>
            Multi-select
          </Checkbox>
          <Button
            variant="ghost"
            size="sm"
            iconOnly
            icon={<Trash2 size={14} />}
            aria-label={`Remove variable ${v.name}`}
            onClick={() => set(local.filter((_, j) => j !== i))}
          />
        </div>
      ))}
      <div>
        <Button
          size="sm"
          icon={<Plus size={14} />}
          disabled={local.length >= MAX_VARIABLES}
          onClick={() =>
            set([
              ...local,
              {
                name: `var${local.length + 1}`,
                label: '',
                type: 'field-values',
                source: 'logs',
                field: 'service',
                multi: true,
                default: ['*'],
              },
            ])
          }
        >
          Add variable
        </Button>
      </div>
    </div>
  );
}
