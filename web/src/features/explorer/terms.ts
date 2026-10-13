// SPDX-License-Identifier: Apache-2.0
//
// Builds TQL terms from facet clicks and property actions (SPEC §16.3: click = include,
// alt-click = exclude; "filter =" / "exclude" in the event detail) and appends them to the
// query's filter part (before any pipe stage).

import { splitPipes } from '../../tql/tokenize';

const IDENT = /^[A-Za-z_@][A-Za-z0-9_.\-@]*$/;
const BARE = /^[A-Za-z0-9_.@\-/]+$/;
const RESERVED = new Set(['and', 'or', 'not', 'in', 'true', 'false', 'null', 'like', 'ci']);

/** A field reference: the name when it is a valid identifier, else backtick-quoted. */
export function fieldRef(name: string): string {
  return IDENT.test(name) ? name : `\`${name.replace(/`/g, '')}\``;
}

/** A value literal: bare when safe, else double-quoted with `\`, `"` and `*` escaped. */
export function valueLiteral(value: string): string {
  if (value !== '' && BARE.test(value) && !RESERVED.has(value.toLowerCase()) && !value.startsWith('/'))
    return value;
  return `"${value.replace(/\\/g, '\\\\').replace(/"/g, '\\"').replace(/\*/g, '\\*')}"`;
}

/** `field:value` or `not field:value`. */
export function term(field: string, value: string, exclude = false): string {
  const t = `${fieldRef(field)}:${valueLiteral(value)}`;
  return exclude ? `not ${t}` : t;
}

/** Adds `t` to the filter of `q` (ANDed), keeping any pipe stages after it. */
export function appendTerm(q: string, t: string): string {
  const parts = splitPipes(q);
  const first = parts[0];
  if (first === undefined) return t;
  let filterIdx = 0;
  if (/^\s*source\s+(logs|spans|metrics)\s*$/i.test(first.text)) {
    if (parts.length === 1) return `${first.text.trim()} | ${t}`;
    filterIdx = 1;
  }
  const filter = parts[filterIdx]?.text.trim() ?? '';
  const nextFilter = filter === '' || filter === '*' ? t : `${filter} and ${t}`;
  const out = parts.map((p, i) => (i === filterIdx ? nextFilter : p.text.trim()));
  if (filterIdx === 0 && parts.length === 1) return nextFilter;
  return out.join(' | ');
}

/** Reports whether a query has pipe stages (other than a leading source clause). */
export function hasStages(q: string): boolean {
  const parts = splitPipes(q);
  const lead = /^\s*source\s+(logs|spans|metrics)\s*$/i.test(parts[0]?.text ?? '') ? 1 : 0;
  return parts.length > lead + 1;
}
