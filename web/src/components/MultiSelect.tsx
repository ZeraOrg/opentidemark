// SPDX-License-Identifier: Apache-2.0
//
// Multi-select (DESIGN §5.6): the chosen values as removable chips, a trigger that opens a
// checklist popover (searchable above SEARCH_THRESHOLD items, arrow keys move between rows,
// Escape returns focus to the trigger) and, optionally, a validated free-text entry for values
// that are not in the list yet (stream names granted before any data arrives).

import { ChevronDown, Plus, Search } from 'lucide-react';
import { useId, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { cx } from '../lib/cx';
import { Button, RemovableChip } from './controls';
import { Popover } from './overlay';
import s from './MultiSelect.module.css';

export interface MultiSelectItem {
  value: string;
  label: string;
  detail?: string;
}

/** Items above this count get a filter input. */
export const SEARCH_THRESHOLD = 8;

export interface CustomEntry {
  /** Returns a problem for an invalid entry, or null. */
  validate: (value: string) => string | null;
  placeholder: string;
  /** Label of the values that are not in `items` (e.g. "no data yet"). */
  note?: string;
}

export interface MultiSelectProps {
  items: readonly MultiSelectItem[];
  value: readonly string[];
  onChange: (next: string[]) => void;
  /** Accessible name of the control ("Streams", "Teams"). */
  label: string;
  placeholder?: string;
  /** Chips and rows in the data font. */
  mono?: boolean;
  custom?: CustomEntry;
  loading?: boolean;
  emptyText?: string;
  disabled?: boolean;
  id?: string;
  className?: string;
}

/** Moves focus between the checkboxes of a list with the arrow keys. */
export function moveFocus(e: KeyboardEvent<HTMLElement>, selector: string): void {
  if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
  const items = Array.from(e.currentTarget.querySelectorAll<HTMLElement>(selector));
  const i = items.findIndex((el) => el === document.activeElement);
  if (items.length === 0) return;
  e.preventDefault();
  const next = i < 0 ? 0 : (i + (e.key === 'ArrowDown' ? 1 : items.length - 1)) % items.length;
  items[next]?.focus();
}

function filterItems(items: readonly MultiSelectItem[], text: string): MultiSelectItem[] {
  const t = text.trim().toLowerCase();
  if (t === '') return [...items];
  return items.filter(
    (i) =>
      i.label.toLowerCase().includes(t) ||
      i.value.toLowerCase().includes(t) ||
      i.detail?.toLowerCase().includes(t),
  );
}

export function MultiSelect(props: MultiSelectProps) {
  const { items, value, onChange, label, mono, custom, loading, disabled, className } = props;
  const placeholder = props.placeholder ?? 'None';
  const [open, setOpen] = useState(false);
  const [filter, setFilter] = useState('');
  const [draft, setDraft] = useState('');
  const [draftError, setDraftError] = useState<string | null>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const listId = useId();
  const byValue = useMemo(() => new Map(items.map((i) => [i.value, i])), [items]);
  const shown = useMemo(() => filterItems(items, filter), [items, filter]);
  const searchable = items.length > SEARCH_THRESHOLD;
  const toggle = (v: string, on: boolean): void =>
    onChange(on ? [...value, v] : value.filter((x) => x !== v));
  const addDraft = (): void => {
    const v = draft.trim();
    if (v === '' || custom === undefined) return;
    const problem = custom.validate(v);
    if (problem !== null) {
      setDraftError(problem);
      return;
    }
    if (!value.includes(v)) onChange([...value, v]);
    setDraft('');
    setDraftError(null);
  };
  const close = (): void => {
    setOpen(false);
    trigger.current?.focus();
  };
  return (
    <Popover
      open={open}
      onOpenChange={(o) => (o ? setOpen(true) : close())}
      className={s.popover}
      anchorClassName={cx(s.anchor, className)}
      trigger={({ open: isOpen, toggle: toggleOpen }) => (
        // The whole control is the anchor, so the panel opens under its left edge.
        <div className={cx(s.box, mono === true && s.mono, disabled === true && s.disabled)}>
          {value.map((v) => (
            <RemovableChip key={v} label={`${label} ${v}`} onRemove={() => toggle(v, false)}>
              {byValue.get(v)?.label ?? v}
              {custom?.note !== undefined && !byValue.has(v) ? (
                <span className={s.note}>{custom.note}</span>
              ) : null}
            </RemovableChip>
          ))}
          <button
            ref={trigger}
            type="button"
            id={props.id}
            className={s.trigger}
            aria-haspopup="dialog"
            aria-expanded={isOpen}
            aria-label={`${label}: ${value.length === 0 ? placeholder : value.join(', ')}`}
            disabled={disabled}
            onClick={toggleOpen}
          >
            {value.length === 0 ? <span className={s.placeholder}>{placeholder}</span> : 'Add'}
            <ChevronDown size={14} aria-hidden="true" />
          </button>
        </div>
      )}
    >
      <div
        role="dialog"
        aria-label={label}
        className={s.panel}
        onKeyDown={(e) => moveFocus(e, 'input[type="checkbox"]')}
      >
        {searchable ? (
          <label className={s.search}>
            <Search size={14} aria-hidden="true" />
            <input
              type="search"
              value={filter}
              placeholder={`Filter ${label.toLowerCase()}`}
              aria-label={`Filter ${label.toLowerCase()}`}
              aria-controls={listId}
              onChange={(e) => setFilter(e.target.value)}
              data-autofocus
            />
          </label>
        ) : null}
        <fieldset className={s.group}>
          <legend className={s.legend}>
            {label}
            <span className={s.count}>
              {value.length} of {items.length}
            </span>
          </legend>
          {/* The scroll box is a div: a fieldset neither shrinks in a flex column nor scrolls reliably. */}
          <div id={listId} className={s.list}>
            {loading === true ? <div className={s.empty}>Loading…</div> : null}
            {!loading && shown.length === 0 ? (
              <div className={s.empty}>
                {filter === '' ? (props.emptyText ?? 'Nothing to choose from.') : 'No match.'}
              </div>
            ) : null}
            {shown.map((i) => (
              <label key={i.value} className={s.row}>
                <input
                  type="checkbox"
                  checked={value.includes(i.value)}
                  onChange={(e) => toggle(i.value, e.target.checked)}
                />
                <span className={s.rowLabel}>{i.label}</span>
                {i.detail !== undefined ? <span className={s.detail}>{i.detail}</span> : null}
              </label>
            ))}
          </div>
        </fieldset>
        {custom !== undefined ? (
          <div className={s.custom}>
            <div className={s.customRow}>
              <input
                className={cx(s.customInput, mono === true && s.mono)}
                value={draft}
                placeholder={custom.placeholder}
                aria-label={`Add ${label.toLowerCase()} by name`}
                aria-invalid={draftError !== null}
                onChange={(e) => {
                  setDraft(e.target.value);
                  setDraftError(null);
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    addDraft();
                  }
                }}
              />
              <Button
                size="sm"
                variant="ghost"
                icon={<Plus size={14} />}
                disabled={draft.trim() === ''}
                onClick={addDraft}
              >
                Add
              </Button>
            </div>
            {draftError !== null ? (
              <span className={s.error} role="alert">
                {draftError}
              </span>
            ) : null}
          </div>
        ) : null}
        <div className={s.foot}>
          <Button size="sm" variant="ghost" disabled={value.length === 0} onClick={() => onChange([])}>
            Clear
          </Button>
          <Button size="sm" variant="primary" onClick={close}>
            Done
          </Button>
        </div>
      </div>
    </Popover>
  );
}
