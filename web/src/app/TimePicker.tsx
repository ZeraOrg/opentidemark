// SPDX-License-Identifier: Apache-2.0
//
// Global time picker (DESIGN §5.13): relative presets, absolute from/to (ISO or `now-15m`
// syntax) with a two-month calendar; the selection is written to the URL (`from`, `to`).

import { ChevronDown, ChevronLeft, ChevronRight, Clock } from 'lucide-react';
import { useState } from 'react';
import { Button, TextInput } from '../components/controls';
import { Popover } from '../components/overlay';
import { cx } from '../lib/cx';
import { isValidTime, pad, PRESETS, rangeLabel, rangeShortLabel, type TimeRange } from '../lib/time';
import s from './TimePicker.module.css';

const WEEKDAYS = ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'];

function sameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** Days of the month grid starting on Monday (leading blanks are null). */
export function monthDays(year: number, month: number): (Date | null)[] {
  const first = new Date(year, month, 1);
  const lead = (first.getDay() + 6) % 7;
  const count = new Date(year, month + 1, 0).getDate();
  const out: (Date | null)[] = new Array<Date | null>(lead).fill(null);
  for (let d = 1; d <= count; d++) out.push(new Date(year, month, d));
  return out;
}

function Month({
  year,
  month,
  from,
  to,
  onPick,
}: {
  year: number;
  month: number;
  from: Date | null;
  to: Date | null;
  onPick: (d: Date) => void;
}) {
  const title = new Date(year, month, 1).toLocaleDateString(undefined, { month: 'long', year: 'numeric' });
  return (
    <div className={s.month}>
      <div className={s.monthTitle}>{title}</div>
      <div className={s.grid} role="grid" aria-label={title}>
        {WEEKDAYS.map((w) => (
          <span key={w} className={s.weekday}>
            {w}
          </span>
        ))}
        {monthDays(year, month).map((d, i) =>
          d === null ? (
            <span key={`b${i}`} />
          ) : (
            <button
              key={d.getDate()}
              type="button"
              className={cx(
                s.day,
                from !== null && sameDay(d, from) && s.edge,
                to !== null && sameDay(d, to) && s.edge,
                from !== null && to !== null && d > from && d < to && s.inRange,
              )}
              onClick={() => onPick(d)}
            >
              {d.getDate()}
            </button>
          ),
        )}
      </div>
    </div>
  );
}

function toLocalIso(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:00${tzOffset(d)}`;
}

function tzOffset(d: Date): string {
  const off = -d.getTimezoneOffset();
  const sign = off >= 0 ? '+' : '-';
  const abs = Math.abs(off);
  return `${sign}${pad(Math.floor(abs / 60))}:${pad(abs % 60)}`;
}

function parseDate(s: string): Date | null {
  if (!/^\d{4}-/.test(s)) return null;
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? null : d;
}

function Panel({
  range,
  onApply,
  close,
}: {
  range: TimeRange;
  onApply: (r: TimeRange) => void;
  close: () => void;
}) {
  const [from, setFrom] = useState(range.from);
  const [to, setTo] = useState(range.to);
  const now = new Date();
  const [view, setView] = useState(() => new Date(now.getFullYear(), now.getMonth() - 1, 1));
  const fromDate = parseDate(from);
  const toDate = parseDate(to);
  const valid = isValidTime(from) && isValidTime(to);
  const apply = (r: TimeRange): void => {
    onApply(r);
    close();
  };
  const pick = (d: Date): void => {
    if (fromDate === null || toDate !== null) {
      setFrom(toLocalIso(d));
      setTo('');
    } else if (d < fromDate) {
      setFrom(toLocalIso(d));
    } else {
      const end = new Date(d);
      end.setHours(23, 59, 0, 0);
      setTo(toLocalIso(end));
    }
  };
  const next = new Date(view.getFullYear(), view.getMonth() + 1, 1);
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone;
  return (
    <div className={s.panel}>
      <div className={s.presets} role="menu" aria-label="Relative ranges">
        {PRESETS.map((p) => {
          const r = p.range(new Date());
          const active = r.from === range.from && r.to === range.to;
          return (
            <button
              key={p.label}
              type="button"
              role="menuitemradio"
              aria-checked={active}
              className={s.preset}
              onClick={() => apply(r)}
            >
              {p.label}
            </button>
          );
        })}
      </div>
      <div className={s.absolute}>
        <div className={s.inputs}>
          <label className={s.label}>
            From
            <TextInput
              mono
              value={from}
              onChange={(e) => setFrom(e.target.value)}
              aria-invalid={!isValidTime(from)}
            />
          </label>
          <label className={s.label}>
            To
            <TextInput
              mono
              value={to}
              onChange={(e) => setTo(e.target.value)}
              aria-invalid={!isValidTime(to)}
            />
          </label>
        </div>
        <div className={s.calNav}>
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<ChevronLeft size={14} />}
            aria-label="Previous month"
            onClick={() => setView(new Date(view.getFullYear(), view.getMonth() - 1, 1))}
          />
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={<ChevronRight size={14} />}
            aria-label="Next month"
            onClick={() => setView(next)}
          />
        </div>
        <div className={s.months}>
          <Month
            year={view.getFullYear()}
            month={view.getMonth()}
            from={fromDate}
            to={toDate}
            onPick={pick}
          />
          <Month
            year={next.getFullYear()}
            month={next.getMonth()}
            from={fromDate}
            to={toDate}
            onPick={pick}
          />
        </div>
        <div className={s.footer}>
          <span className={s.tz}>Times in {tz}</span>
          <Button
            variant="primary"
            size="sm"
            disabled={!valid}
            onClick={() => apply({ from: from.trim(), to: to.trim() })}
          >
            Apply
          </Button>
        </div>
      </div>
    </div>
  );
}

export function TimePicker({
  range,
  onChange,
  compact,
}: {
  range: TimeRange;
  onChange: (r: TimeRange) => void;
  compact?: boolean;
}) {
  return (
    <Popover
      className={s.popover}
      trigger={({ open, toggle }) => (
        <button
          type="button"
          className={cx(s.trigger, compact === true && s.compact)}
          aria-haspopup="dialog"
          aria-expanded={open}
          aria-label={`Time range: ${rangeLabel(range)}`}
          onClick={toggle}
        >
          <Clock size={16} aria-hidden="true" />
          <span>{compact === true ? rangeShortLabel(range) : rangeLabel(range)}</span>
          {compact === true ? null : <ChevronDown size={14} aria-hidden="true" />}
        </button>
      )}
    >
      {(close) => <Panel range={range} onApply={onChange} close={close} />}
    </Popover>
  );
}
