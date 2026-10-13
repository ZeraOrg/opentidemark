// SPDX-License-Identifier: Apache-2.0
//
// Preview-only variable substitution for unsaved panel queries (SPEC §16.4 panel editor).
// Saved panels are always resolved on the server's AST (POST /dashboards/{id}/panels/{p}/query);
// this token-level replacement only feeds the live preview of a panel being edited. Values are
// inserted as quoted string literals, never as raw TQL.

import { tokenize } from './tokenize';

/** Quotes a value as a TQL double-quoted string (`\` and `"` escaped). */
export function quoteValue(v: string): string {
  return `"${v.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
}

function literal(values: readonly string[]): string {
  const vals = values.filter((v) => v !== '');
  if (vals.length === 0 || vals.includes('*')) return '*';
  if (vals.length === 1)
    return /^-?\d+(\.\d+)?$/.test(vals[0] ?? '') ? (vals[0] ?? '') : quoteValue(vals[0] ?? '');
  return `(${vals.map((v) => (/^-?\d+(\.\d+)?$/.test(v) ? v : quoteValue(v))).join(' or ')})`;
}

/** Replaces `$name` tokens with the given values (missing variables are left untouched). */
export function substitutePreview(q: string, vars: Record<string, readonly string[]>): string {
  let out = '';
  let last = 0;
  for (const t of tokenize(q)) {
    if (t.kind !== 'variable') continue;
    const values = vars[t.text.slice(1)];
    if (values === undefined) continue;
    out += q.slice(last, t.from) + literal(values);
    last = t.to;
  }
  return out + q.slice(last);
}
