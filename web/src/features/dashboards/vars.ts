// SPDX-License-Identifier: Apache-2.0
//
// Dashboard variables in the URL (SPEC §13.1, §16.1): `var-<name>=a,b` overrides the default of
// each variable; values missing from the URL fall back to the document defaults.

import type { DashboardDoc, Variable } from '../../api/types';

export const VAR_PREFIX = 'var-';

/** Current values of every variable of `doc`. */
export function variableValues(
  doc: DashboardDoc | undefined,
  params: URLSearchParams,
): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const v of doc?.variables ?? []) {
    const raw = params.get(VAR_PREFIX + v.name);
    out[v.name] = raw === null ? [...(v.default ?? ['*'])] : raw.split(',').filter((x) => x !== '');
  }
  return out;
}

/** The URL patch for a new value list (`null` when it equals the default). */
export function variablePatch(v: Variable, values: readonly string[]): Record<string, string | null> {
  const def = (v.default ?? ['*']).join(',');
  const next = values.join(',');
  return { [VAR_PREFIX + v.name]: next === def ? null : next };
}

/** Short label of a value list: "checkout-api", "All", or the first value. */
export function valuesLabel(values: readonly string[]): string {
  if (values.length === 0 || values.includes('*')) return 'All';
  return values[0] ?? '';
}

/** A Go-style refresh interval ("30s", "5m", "1h") in ms, or null for off/empty/invalid. */
export function refreshMs(refresh: string | undefined): number | null {
  const m = /^(\d+)([smh])$/.exec(refresh ?? '');
  if (!m) return null;
  const n = Number(m[1]);
  const unit = m[2] === 's' ? 1_000 : m[2] === 'm' ? 60_000 : 3_600_000;
  return n * unit;
}
