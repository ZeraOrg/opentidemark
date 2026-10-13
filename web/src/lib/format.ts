// SPDX-License-Identifier: Apache-2.0
//
// Number formatting for tables, stat tiles and stream statistics.

const SI = [
  { v: 1e12, s: 'T' },
  { v: 1e9, s: 'B' },
  { v: 1e6, s: 'M' },
  { v: 1e3, s: 'k' },
];

/** Compact count: 1.84 B, 622 M, 4.1 M, 880 k, 812. */
export function formatCount(n: number): string {
  if (!Number.isFinite(n)) return '—';
  const abs = Math.abs(n);
  for (const { v, s } of SI) {
    if (abs >= v) {
      const x = n / v;
      const digits = Math.abs(x) >= 100 ? 0 : Math.abs(x) >= 10 ? 1 : 2;
      return `${trimZeros(x.toFixed(digits))} ${s}`;
    }
  }
  return formatInteger(n);
}

/** Grouped integer: 12,771. */
export function formatInteger(n: number): string {
  return Math.round(n).toLocaleString('en-US');
}

/** A number with at most `decimals` decimals and grouping. */
export function formatNumber(n: number | null | undefined, decimals = 2): string {
  if (n === null || n === undefined || !Number.isFinite(n)) return '—';
  if (Number.isInteger(n)) return formatInteger(n);
  return n.toLocaleString('en-US', { maximumFractionDigits: decimals });
}

const BYTE_UNITS = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];

/** Binary byte size: 412 GiB, 3.9 TiB. */
export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  let i = 0;
  let x = n;
  while (x >= 1024 && i < BYTE_UNITS.length - 1) {
    x /= 1024;
    i++;
  }
  const digits = x >= 100 || i === 0 ? 0 : 1;
  return `${trimZeros(x.toFixed(digits))} ${BYTE_UNITS[i] ?? 'B'}`;
}

/** Events per second as a per-minute rate: 48.1 k/min. */
export function formatRatePerMin(perSecond: number): string {
  const perMin = perSecond * 60;
  if (perMin === 0) return '0';
  return `${formatCount(perMin)}/min`;
}

function trimZeros(s: string): string {
  return s.includes('.') ? s.replace(/\.?0+$/, '') : s;
}

/** Signed delta for comparisons: "+3", "-24", "0". */
export function formatDelta(n: number): string {
  if (n === 0) return '0';
  const s = formatNumber(Math.abs(n));
  return n > 0 ? `+${s}` : `-${s}`;
}

/** Formats a value with a panel unit (§13.1 `unit`): short, ms, s, %, bytes, or a free suffix. */
export function formatUnit(v: number | null | undefined, unit = '', decimals?: number): string {
  if (v === null || v === undefined || !Number.isFinite(v)) return '—';
  const d = decimals ?? (Math.abs(v) >= 100 ? 0 : 2);
  switch (unit) {
    case '':
    case 'none':
      return formatNumber(v, d);
    case 'short':
      return formatCount(v);
    case 'bytes':
      return formatBytes(v);
    case '%':
    case 'percent':
      return `${formatNumber(v, d)}%`;
    default:
      return `${formatNumber(v, d)} ${unit}`;
  }
}

/** Axis tick label: compact counts without a unit (1.2 k, 40, 0.5). */
export function formatTick(v: number): string {
  if (Math.abs(v) >= 1000) return formatCount(v);
  return formatNumber(v, 2);
}
