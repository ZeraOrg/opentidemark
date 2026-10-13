// SPDX-License-Identifier: Apache-2.0
//
// Time range handling for the global time picker (SPEC §11.1, DESIGN §5.13). Ranges are kept as
// the strings the API accepts — `now`, `now-<n><s|m|h|d|w>` or RFC 3339 — so URLs stay relative.

export interface TimeRange {
  from: string;
  to: string;
}

export const DEFAULT_RANGE: TimeRange = { from: 'now-15m', to: 'now' };

const UNIT_MS: Record<string, number> = {
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
  d: 86_400_000,
  w: 604_800_000,
};

const RELATIVE = /^now(?:-(\d+)([smhdw]))?$/;

/** Reports whether `s` is a valid `from`/`to` value. */
export function isValidTime(s: string): boolean {
  const t = s.trim();
  if (RELATIVE.test(t)) return true;
  return /^\d{4}-\d{2}-\d{2}T/.test(t) && !Number.isNaN(Date.parse(t));
}

/** Resolves a range value to epoch milliseconds relative to `now`. */
export function resolveTime(s: string, now: number): number {
  const t = s.trim();
  const m = RELATIVE.exec(t);
  if (m) {
    const n = m[1] === undefined ? 0 : Number(m[1]);
    const unit = UNIT_MS[m[2] ?? 's'] ?? 0;
    return now - n * unit;
  }
  const parsed = Date.parse(t);
  return Number.isNaN(parsed) ? now : parsed;
}

export interface Preset {
  label: string;
  short: string;
  range: (now: Date) => TimeRange;
}

function startOfDay(d: Date): Date {
  const s = new Date(d);
  s.setHours(0, 0, 0, 0);
  return s;
}

const rel =
  (from: string): (() => TimeRange) =>
  () => ({ from, to: 'now' });

export const PRESETS: Preset[] = [
  { label: 'Last 5 minutes', short: '5m', range: rel('now-5m') },
  { label: 'Last 15 minutes', short: '15m', range: rel('now-15m') },
  { label: 'Last 1 hour', short: '1h', range: rel('now-1h') },
  { label: 'Last 6 hours', short: '6h', range: rel('now-6h') },
  { label: 'Last 24 hours', short: '24h', range: rel('now-24h') },
  { label: 'Last 7 days', short: '7d', range: rel('now-7d') },
  { label: 'Last 30 days', short: '30d', range: rel('now-30d') },
  {
    label: 'Today',
    short: 'today',
    range: (now) => ({ from: startOfDay(now).toISOString(), to: 'now' }),
  },
  {
    label: 'Yesterday',
    short: 'yday',
    range: (now) => {
      const today = startOfDay(now);
      const y = new Date(today);
      y.setDate(y.getDate() - 1);
      return { from: y.toISOString(), to: today.toISOString() };
    },
  },
];

const RELATIVE_LABEL: Record<string, string> = { s: 'second', m: 'minute', h: 'hour', d: 'day', w: 'week' };

/** Human label for a range: "Last 15 minutes" or an absolute span. */
export function rangeLabel(range: TimeRange): string {
  const m = RELATIVE.exec(range.from.trim());
  if (m && range.to.trim() === 'now' && m[1] !== undefined && m[2] !== undefined) {
    const n = Number(m[1]);
    if (m[2] === 'h' && n === 24) return 'Last 24 hours';
    const unit = RELATIVE_LABEL[m[2]] ?? m[2];
    return `Last ${n} ${unit}${n === 1 ? '' : 's'}`;
  }
  return `${formatShortDateTime(range.from)} – ${formatShortDateTime(range.to)}`;
}

/** Compact label for the mobile time button ("15m"). */
export function rangeShortLabel(range: TimeRange): string {
  const m = RELATIVE.exec(range.from.trim());
  if (m && range.to.trim() === 'now' && m[1] !== undefined && m[2] !== undefined) return `${m[1]}${m[2]}`;
  return 'range';
}

function formatShortDateTime(s: string): string {
  if (RELATIVE.test(s.trim())) return s.trim();
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s;
  return `${d.toLocaleDateString(undefined, { day: '2-digit', month: 'short' })} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function pad(n: number, width = 2): string {
  return String(n).padStart(width, '0');
}

/** HH:mm:ss.SSS in local time. */
export function formatClock(ms: number, withMillis = true): string {
  const d = new Date(ms);
  const base = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  return withMillis ? `${base}.${pad(d.getMilliseconds(), 3)}` : base;
}

/** YYYY-MM-DD HH:mm in local time. */
export function formatDateTime(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** Short relative age: "2 s ago", "14 min ago", "41 d ago". */
export function formatAge(ms: number, now: number): string {
  const s = Math.max(0, Math.round((now - ms) / 1000));
  if (s < 2) return 'now';
  if (s < 60) return `${s} s ago`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m} min ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} h ago`;
  return `${Math.round(h / 24)} d ago`;
}

/** Formats a duration in milliseconds: "38 ms", "1.2 s", "4 min". */
export function formatDuration(ms: number): string {
  if (ms < 1) return `${ms.toFixed(2)} ms`;
  if (ms < 10_000) return `${Math.round(ms).toLocaleString('en-US')} ms`;
  if (ms < 120_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.round(ms / 60_000)} min`;
}

/** Formats a span in milliseconds as a TQL duration ("5m", "1h", "30s"). */
export function formatSpan(ms: number): string {
  if (ms % 86_400_000 === 0) return `${ms / 86_400_000}d`;
  if (ms % 3_600_000 === 0) return `${ms / 3_600_000}h`;
  if (ms % 60_000 === 0) return `${ms / 60_000}m`;
  return `${Math.round(ms / 1000)}s`;
}
