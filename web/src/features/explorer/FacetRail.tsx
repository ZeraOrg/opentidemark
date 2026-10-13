// SPDX-License-Identifier: Apache-2.0
//
// Explorer rail (DESIGN §5.11): signal chips, field facets with top values and proportional
// bars (click = include, Alt/Option-click = exclude) and the searchable field list.

import { ChevronDown, ChevronRight, Search } from 'lucide-react';
import { useState, type MouseEvent } from 'react';
import type { Facet, FieldInfo, Signal } from '../../api/types';
import { SignalChip, Spinner } from '../../components/controls';
import { cx } from '../../lib/cx';
import { formatInteger } from '../../lib/format';
import s from './FacetRail.module.css';

const VISIBLE_VALUES = 5;

export interface FacetRailProps {
  signals: readonly Signal[];
  activeSignals: readonly string[];
  onToggleSignal: (id: string) => void;
  facets: readonly Facet[] | undefined;
  facetsLoading: boolean;
  fields: readonly FieldInfo[];
  openFields: readonly string[];
  onToggleField: (name: string) => void;
  onTerm: (field: string, value: string, exclude: boolean) => void;
  className?: string;
}

function fieldType(f: FieldInfo | undefined, name: string): string {
  if (name === 'level') return 'built-in';
  if (f === undefined) return '';
  if (f.builtin) return 'built-in';
  return f.numeric ? 'number' : 'string';
}

function FacetBlock({
  facet,
  info,
  onTerm,
  onToggle,
}: {
  facet: Facet;
  info: FieldInfo | undefined;
  onTerm: FacetRailProps['onTerm'];
  onToggle: () => void;
}) {
  const [all, setAll] = useState(false);
  const values = all ? facet.values : facet.values.slice(0, VISIBLE_VALUES);
  const max = Math.max(1, ...facet.values.map((v) => v.count));
  const isLevel = facet.field === 'level';
  return (
    <div className={s.facet}>
      <button type="button" className={s.facetHead} aria-expanded="true" onClick={onToggle}>
        <ChevronDown size={14} aria-hidden="true" />
        <span className={s.facetName}>{facet.field}</span>
        <span className={s.facetType}>{fieldType(info, facet.field)}</span>
      </button>
      {facet.values.length === 0 ? <div className={s.none}>no values in range</div> : null}
      <ul className={s.values}>
        {values.map((v) => (
          <li key={v.value}>
            <button
              type="button"
              className={s.value}
              title={`${facet.field}:${v.value} — click to filter, Alt-click to exclude`}
              onClick={(e: MouseEvent) => onTerm(facet.field, v.value, e.altKey)}
            >
              <span className={s.valueText}>{v.value === '' ? '(empty)' : v.value}</span>
              <span className={s.count}>{formatInteger(v.count)}</span>
              <span
                className={cx(s.bar, isLevel && s[`lvl-${v.value.toLowerCase()}`])}
                style={{ width: `${(v.count / max) * 100}%` }}
                aria-hidden="true"
              />
            </button>
          </li>
        ))}
      </ul>
      {facet.values.length > VISIBLE_VALUES ? (
        <button type="button" className={s.more} onClick={() => setAll(!all)}>
          {all ? 'fewer' : `more… (${facet.values.length - VISIBLE_VALUES})`}
        </button>
      ) : null}
      {facet.sampled ? <div className={s.none}>sampled from the newest 1,000,000 events</div> : null}
    </div>
  );
}

export function FacetRail(props: FacetRailProps) {
  const {
    signals,
    activeSignals,
    onToggleSignal,
    facets,
    facetsLoading,
    fields,
    openFields,
    onToggleField,
    onTerm,
  } = props;
  const [filter, setFilter] = useState('');
  const byName = new Map(fields.map((f) => [f.name, f]));
  const shown = new Set((facets ?? []).map((f) => f.field));
  const rest = fields.filter(
    (f) => !shown.has(f.name) && f.name.toLowerCase().includes(filter.trim().toLowerCase()),
  );
  return (
    <aside className={cx(s.rail, props.className)} aria-label="Signals and fields">
      <section className={s.section}>
        <h2 className={s.sectionHead}>
          Signals <span>{signals.length}</span>
        </h2>
        {signals.length === 0 ? (
          <div className={s.none}>No saved signals yet. Use “Save as signal”.</div>
        ) : (
          <div className={s.chips}>
            {signals.map((sig) => (
              <SignalChip
                key={sig.id}
                active={activeSignals.includes(sig.id)}
                onToggle={() => onToggleSignal(sig.id)}
                title={sig.tql}
              >
                {sig.name}
              </SignalChip>
            ))}
          </div>
        )}
      </section>
      <section className={s.section}>
        <h2 className={s.sectionHead}>
          Fields <span>{fields.length}</span>
        </h2>
        <label className={s.search}>
          <Search size={14} aria-hidden="true" />
          <input
            type="search"
            placeholder="Filter fields"
            aria-label="Filter fields"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </label>
        {facetsLoading && facets === undefined ? (
          <div className={s.loading}>
            <Spinner size={14} />
          </div>
        ) : null}
        {(facets ?? [])
          .filter((f) => f.field.toLowerCase().includes(filter.trim().toLowerCase()))
          .map((f) => (
            <FacetBlock
              key={f.field}
              facet={f}
              info={byName.get(f.field)}
              onTerm={onTerm}
              onToggle={() => onToggleField(f.field)}
            />
          ))}
        <ul className={s.fieldList}>
          {rest.map((f) => (
            <li key={f.name}>
              <button
                type="button"
                className={s.facetHead}
                aria-expanded={openFields.includes(f.name)}
                onClick={() => onToggleField(f.name)}
              >
                <ChevronRight size={14} aria-hidden="true" />
                <span className={s.facetName}>{f.name}</span>
                <span className={s.facetType}>{fieldType(f, f.name)}</span>
              </button>
            </li>
          ))}
        </ul>
      </section>
    </aside>
  );
}
