// SPDX-License-Identifier: Apache-2.0
//
// A deliberately small Markdown subset for markdown panels (SPEC §13.1, §15.7): headings,
// paragraphs, lists, fenced code, inline code, bold, italic and links. The parser produces a
// tree that is rendered with React elements; raw HTML is never interpreted (it stays text) and
// links are limited to http(s), mailto and same-origin paths.

export type Inline =
  | { kind: 'text'; text: string }
  | { kind: 'code'; text: string }
  | { kind: 'strong'; children: Inline[] }
  | { kind: 'em'; children: Inline[] }
  | { kind: 'link'; href: string; children: Inline[] };

export type Block =
  | { kind: 'heading'; level: 1 | 2 | 3; children: Inline[] }
  | { kind: 'paragraph'; children: Inline[] }
  | { kind: 'list'; ordered: boolean; items: Inline[][] }
  | { kind: 'code'; text: string };

/** Returns the href when it is safe to link to, else null. */
export function safeHref(href: string): string | null {
  const h = href.trim();
  if (/^https?:\/\//i.test(h) || /^mailto:/i.test(h)) return h;
  if (h.startsWith('/') && !h.startsWith('//')) return h;
  return null;
}

const INLINE =
  /(`[^`]+`)|(\*\*[^*]+\*\*)|(\*[^*\s][^*]*\*|(?<![A-Za-z0-9])_[^_]+_(?![A-Za-z0-9]))|(\[[^\]]+\]\([^)\s]+\))/;

/** Parses inline markup. */
export function parseInline(text: string): Inline[] {
  const out: Inline[] = [];
  let rest = text;
  while (rest !== '') {
    const m = INLINE.exec(rest);
    if (!m) {
      out.push({ kind: 'text', text: rest });
      break;
    }
    if (m.index > 0) out.push({ kind: 'text', text: rest.slice(0, m.index) });
    const tok = m[0];
    if (m[1] !== undefined) out.push({ kind: 'code', text: tok.slice(1, -1) });
    else if (m[2] !== undefined) out.push({ kind: 'strong', children: parseInline(tok.slice(2, -2)) });
    else if (m[3] !== undefined) out.push({ kind: 'em', children: parseInline(tok.slice(1, -1)) });
    else {
      const close = tok.indexOf('](');
      const label = tok.slice(1, close);
      const href = safeHref(tok.slice(close + 2, -1));
      if (href === null) out.push({ kind: 'text', text: label });
      else out.push({ kind: 'link', href, children: parseInline(label) });
    }
    rest = rest.slice(m.index + tok.length);
  }
  return out;
}

const LIST_ITEM = /^\s*(?:([-*+])|(\d+)[.)])\s+(.*)$/;

/** Parses a Markdown document into blocks. */
export function parseMarkdown(src: string): Block[] {
  const lines = src.replace(/\r\n?/g, '\n').split('\n');
  const blocks: Block[] = [];
  let para: string[] = [];
  const flush = (): void => {
    if (para.length > 0) blocks.push({ kind: 'paragraph', children: parseInline(para.join(' ')) });
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i] ?? '';
    if (line.trimStart().startsWith('```')) {
      flush();
      const code: string[] = [];
      i++;
      while (i < lines.length && !(lines[i] ?? '').trimStart().startsWith('```')) code.push(lines[i++] ?? '');
      blocks.push({ kind: 'code', text: code.join('\n') });
      continue;
    }
    const h = /^(#{1,3})\s+(.*)$/.exec(line);
    if (h) {
      flush();
      const level = (h[1] ?? '#').length as 1 | 2 | 3;
      blocks.push({ kind: 'heading', level, children: parseInline(h[2] ?? '') });
      continue;
    }
    const li = LIST_ITEM.exec(line);
    if (li) {
      flush();
      const ordered = li[2] !== undefined;
      const items: Inline[][] = [];
      while (i < lines.length) {
        const m = LIST_ITEM.exec(lines[i] ?? '');
        if (!m || (m[2] !== undefined) !== ordered) break;
        items.push(parseInline(m[3] ?? ''));
        i++;
      }
      i--;
      blocks.push({ kind: 'list', ordered, items });
      continue;
    }
    if (line.trim() === '') flush();
    else para.push(line.trim());
  }
  flush();
  return blocks;
}
