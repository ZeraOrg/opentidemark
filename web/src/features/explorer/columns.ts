// SPDX-License-Identifier: Apache-2.0
//
// Event-list columns the user can show (SPEC §16.3): any property, plus the built-in fields that
// are not already fixed columns. A built-in name always means the built-in field.

import type { LogEvent } from '../../api/types';
import { renderValue } from '../../lib/template';

const BUILTIN_COLUMNS: Readonly<Record<string, (ev: LogEvent) => string>> = {
  environment: (ev) => ev.environment,
  host: (ev) => ev.host,
  trace_id: (ev) => ev.traceId,
  span_id: (ev) => ev.spanId,
  event_type: (ev) => ev.eventType,
};

/** Built-in fields that can be shown as columns, in display order. */
export const BUILTIN_COLUMN_FIELDS: readonly string[] = Object.keys(BUILTIN_COLUMNS);

/** Whether a field of the event detail can be shown as a column. */
export function canShowAsColumn(field: string, builtin: boolean): boolean {
  return !builtin || field in BUILTIN_COLUMNS;
}

/** Fields get a filter input in the Columns dialog above this many. */
export const COLUMN_FILTER_THRESHOLD = 12;

/** Shown columns shrink from 140 px to 72 px before the list scrolls horizontally (DESIGN §5.9). */
const PINNED_WIDTH = 140;
const PINNED_MIN_WIDTH = 72;
const FIXED_WIDTHS = [148, 56, 148];
const MESSAGE_MIN_WIDTH = 160;
const COLUMN_GAP = 12;
const ROW_PADDING = 32;

/** The grid template of an event row: fixed columns, shown columns, message. */
export function rowTemplate(pinned: number): string {
  const cols = Array.from({ length: pinned }, () => `minmax(${PINNED_MIN_WIDTH}px, ${PINNED_WIDTH}px)`);
  return [...FIXED_WIDTHS.map((w) => `${w}px`), ...cols, `minmax(${MESSAGE_MIN_WIDTH}px, 1fr)`].join(' ');
}

/** The narrowest width at which every column keeps its minimum; the list scrolls sideways below it. */
export function rowMinWidth(pinned: number): number {
  const columns = FIXED_WIDTHS.length + pinned + 1;
  return (
    FIXED_WIDTHS.reduce((a, b) => a + b, 0) +
    pinned * PINNED_MIN_WIDTH +
    MESSAGE_MIN_WIDTH +
    (columns - 1) * COLUMN_GAP +
    ROW_PADDING
  );
}

/** The Columns dialog's lists: the shown columns in order, and the other candidates matching `filter`. */
export function columnChoices(
  candidates: readonly string[],
  pinned: readonly string[],
  filter: string,
): { shown: string[]; available: string[] } {
  const t = filter.trim().toLowerCase();
  const available = [...new Set(candidates)].filter(
    (c) => !pinned.includes(c) && c.toLowerCase().includes(t),
  );
  return { shown: [...pinned], available };
}

/** The text of a column cell; empty when the event has no value for the field. */
export function columnValue(ev: LogEvent, field: string): string {
  const builtin = BUILTIN_COLUMNS[field];
  if (builtin) return builtin(ev);
  const v = ev.properties[field];
  return v === undefined ? '' : renderValue(v);
}
