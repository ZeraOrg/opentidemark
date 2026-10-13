// SPDX-License-Identifier: Apache-2.0
//
// CodeMirror 6 support for TQL (SPEC §16.3, DESIGN §3.2, §5.7): token highlighting from the
// tolerant tokenizer, the error squiggle as a mark decoration (`.cm-tql-error`), a single-line
// input filter and the completion source backed by GET /tql/complete (byte offsets ⇄ UTF-16).

import type {
  Completion as CmCompletion,
  CompletionContext,
  CompletionResult,
} from '@codemirror/autocomplete';
import { EditorState, RangeSetBuilder, StateEffect, StateField, type Extension } from '@codemirror/state';
import { Decoration, EditorView, ViewPlugin, type DecorationSet, type ViewUpdate } from '@codemirror/view';
import type { Completion } from '../api/types';
import { byteToUtf16, utf16ToByte } from '../lib/utf8';
import { tokenize } from './tokenize';

const markCache = new Map<string, Decoration>();

function mark(kind: string): Decoration {
  let d = markCache.get(kind);
  if (d === undefined) {
    d = Decoration.mark({ class: `tql-${kind}` });
    markCache.set(kind, d);
  }
  return d;
}

/** Decorations for every non-space token of `text`. */
export function highlightDecorations(text: string): DecorationSet {
  const b = new RangeSetBuilder<Decoration>();
  for (const t of tokenize(text)) {
    if (t.kind === 'space' || t.to <= t.from) continue;
    b.add(t.from, t.to, mark(t.kind));
  }
  return b.finish();
}

/** Syntax highlighting (token classes `tql-keyword`, `tql-field`, …). */
export const tqlHighlight: Extension = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = highlightDecorations(view.state.doc.toString());
    }
    update(u: ViewUpdate): void {
      if (u.docChanged) this.decorations = highlightDecorations(u.state.doc.toString());
    }
  },
  { decorations: (v) => v.decorations },
);

/** UTF-16 range of the current query error, or null. */
export const setQueryError = StateEffect.define<{ from: number; to: number } | null>();

const errorMark = Decoration.mark({ class: 'cm-tql-error' });

export const queryErrorField = StateField.define<DecorationSet>({
  create: () => Decoration.none,
  update(value, tr) {
    let next = value.map(tr.changes);
    for (const e of tr.effects) {
      if (!e.is(setQueryError)) continue;
      const r = e.value;
      const len = tr.state.doc.length;
      if (r === null || len === 0) {
        next = Decoration.none;
      } else {
        const from = Math.max(0, Math.min(r.from, len - 1));
        const to = Math.max(from + 1, Math.min(r.to, len));
        next = Decoration.set([errorMark.range(from, to)]);
      }
    }
    return next;
  },
  provide: (f) => EditorView.decorations.from(f),
});

/** Keeps the editor single-line: pasted newlines become spaces. */
export const singleLine: Extension = EditorState.transactionFilter.of((tr) => {
  if (!tr.docChanged) return tr;
  const text = tr.newDoc.toString();
  if (!text.includes('\n')) return tr;
  const flat = text.replace(/\s*\n\s*/g, ' ');
  return {
    changes: { from: 0, to: tr.startState.doc.length, insert: flat },
    selection: { anchor: Math.min(flat.length, tr.newSelection.main.head) },
    effects: tr.effects,
  };
});

export type CompletionFetcher = (q: string, bytePos: number, signal: AbortSignal) => Promise<Completion>;

/** Converts an API completion (byte offsets) into a CodeMirror result (UTF-16 offsets). */
export function toCompletionResult(q: string, c: Completion): CompletionResult {
  const options: CmCompletion[] = c.suggestions.map((s, i) => ({
    label: s.label,
    apply: s.insertText,
    detail: s.detail,
    type: s.kind,
    boost: -i,
  }));
  return { from: byteToUtf16(q, c.from), to: byteToUtf16(q, c.to), options, filter: false };
}

/** A completion source that asks the server (§10.7) for suggestions at the cursor. */
export function tqlCompletionSource(fetch: CompletionFetcher) {
  return async (ctx: CompletionContext): Promise<CompletionResult | null> => {
    const q = ctx.state.doc.toString();
    const before = q.slice(0, ctx.pos);
    if (!ctx.explicit && before !== '' && !/[\w@.$:|\s(=<>!*-]$/.test(before)) return null;
    const ctrl = new AbortController();
    ctx.addEventListener('abort', () => ctrl.abort());
    try {
      const c = await fetch(q, utf16ToByte(q, ctx.pos), ctrl.signal);
      if (ctx.aborted || c.suggestions.length === 0) return null;
      return toCompletionResult(q, c);
    } catch {
      return null;
    }
  };
}
