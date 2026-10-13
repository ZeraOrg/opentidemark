// SPDX-License-Identifier: Apache-2.0
//
// Message-template rendering for the event stream (SPEC §9.2 rendering rules, DESIGN §5.9):
// the rendered message is split into literal text and property values so the UI can highlight
// the values that came from the template.

export type Segment = { kind: 'text'; text: string } | { kind: 'prop'; name: string; text: string };

type Token = { kind: 'text'; text: string } | { kind: 'hole'; name: string; raw: string };

const HOLE = /^\{([@$]?)([A-Za-z0-9_][A-Za-z0-9_.-]*)(?:,[-+]?\d+)?(?::[^}]*)?\}/;

/** Renders a property value the way the server does: strings bare, objects as compact JSON. */
export function renderValue(v: unknown): string {
  if (typeof v === 'string') return v;
  if (v === null || v === undefined) return 'null';
  if (typeof v === 'number' || typeof v === 'boolean' || typeof v === 'bigint') return String(v);
  try {
    return JSON.stringify(v);
  } catch {
    return String(v);
  }
}

interface TextPart {
  kind: 'text';
  text: string;
}

function isText(p: { kind: string }): p is TextPart {
  return p.kind === 'text';
}

/** Appends text, merging with a preceding text part. */
function pushText(out: { kind: string }[], text: string): void {
  if (text === '') return;
  const last = out[out.length - 1];
  if (last !== undefined && isText(last)) last.text += text;
  else {
    const part: TextPart = { kind: 'text', text };
    out.push(part);
  }
}

/** Splits a template into literal text and `{Name}` holes; `{{`/`}}` are escapes. */
export function tokenizeTemplate(template: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < template.length) {
    const ch = template.charAt(i);
    if ((ch === '{' || ch === '}') && template.charAt(i + 1) === ch) {
      pushText(out, ch);
      i += 2;
      continue;
    }
    if (ch === '{') {
      const m = HOLE.exec(template.slice(i));
      if (m) {
        out.push({ kind: 'hole', name: m[2] ?? '', raw: m[0] });
        i += m[0].length;
        continue;
      }
    }
    pushText(out, ch);
    i++;
  }
  return out;
}

/** Splits `template` into text and property segments using `props` (missing ⇒ token verbatim). */
export function renderTemplate(template: string, props: Record<string, unknown>): Segment[] {
  const out: Segment[] = [];
  for (const t of tokenizeTemplate(template)) {
    if (t.kind === 'text') pushText(out, t.text);
    else if (Object.prototype.hasOwnProperty.call(props, t.name))
      out.push({ kind: 'prop', name: t.name, text: renderValue(props[t.name]) });
    else pushText(out, t.raw);
  }
  return out;
}

/**
 * Segments for an event: template-based when a template exists and renders to the stored
 * message (otherwise the stored message wins, as `@m` does on ingest), else plain text.
 */
export function messageSegments(
  message: string,
  template: string,
  props: Record<string, unknown>,
): Segment[] {
  if (template === '') return message === '' ? [] : [{ kind: 'text', text: message }];
  const segs = renderTemplate(template, props);
  const rendered = segs.map((s) => s.text).join('');
  if (message !== '' && rendered !== message) return [{ kind: 'text', text: message }];
  return segs;
}

/** Names of the properties referenced by a template (highlighted in the detail view). */
export function templateProperties(template: string): Set<string> {
  const names = new Set<string>();
  for (const t of tokenizeTemplate(template)) if (t.kind === 'hole') names.add(t.name);
  return names;
}
