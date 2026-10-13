// SPDX-License-Identifier: Apache-2.0
//
// Go duration strings (SPEC §14.1: alert `window`/`every`/`for` are stored canonically as
// "5m0s") and their short display form ("5m").

const UNITS: Record<string, number> = {
  ns: 1e-6,
  us: 1e-3,
  µs: 1e-3,
  ms: 1,
  s: 1_000,
  m: 60_000,
  h: 3_600_000,
};

const PART = /(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g;

/** Parses a Go duration ("1h30m", "5m0s", "250ms", "0s") to milliseconds; null when invalid. */
export function parseGoDuration(s: string): number | null {
  const t = s.trim();
  if (t === '0') return 0;
  if (t === '') return null;
  let total = 0;
  let consumed = 0;
  for (const m of t.matchAll(PART)) {
    if (m.index !== consumed) return null;
    total += Number(m[1]) * (UNITS[m[2] ?? ''] ?? 0);
    consumed += m[0].length;
  }
  return consumed === t.length ? total : null;
}

/** Formats milliseconds as the shortest Go duration: 300000 → "5m", 5400000 → "1h30m". */
export function formatGoDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms <= 0) return '0s';
  let rest = Math.round(ms);
  let out = '';
  const h = Math.floor(rest / 3_600_000);
  rest -= h * 3_600_000;
  const m = Math.floor(rest / 60_000);
  rest -= m * 60_000;
  const s = Math.floor(rest / 1_000);
  rest -= s * 1_000;
  if (h > 0) out += `${h}h`;
  if (m > 0) out += `${m}m`;
  if (s > 0) out += `${s}s`;
  if (rest > 0) out += `${rest}ms`;
  return out;
}

/** Short display of a stored Go duration ("5m0s" → "5m"); unparsable input is returned as is. */
export function shortDuration(s: string): string {
  const ms = parseGoDuration(s);
  return ms === null ? s : formatGoDuration(ms);
}
