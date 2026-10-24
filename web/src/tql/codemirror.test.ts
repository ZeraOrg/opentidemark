// SPDX-License-Identifier: Apache-2.0

import { CompletionContext } from '@codemirror/autocomplete';
import { EditorState } from '@codemirror/state';
import { EditorView } from '@codemirror/view';
import { describe, expect, it, vi } from 'vitest';
import type { Completion } from '../api/types';
import { CHEATSHEET } from './cheatsheet';
import {
  highlightDecorations,
  queryErrorField,
  setQueryError,
  singleLine,
  tqlCompletionSource,
  toCompletionResult,
  typeOverSelection,
} from './codemirror';
import { tokenize } from './tokenize';

function classes(text: string): string[] {
  const out: string[] = [];
  highlightDecorations(text).between(0, text.length, (from, to, d) => {
    out.push(`${String(d.spec.class)}@${from}-${to}`);
  });
  return out;
}

describe('highlightDecorations', () => {
  it('marks every non-space token with its class', () => {
    expect(classes('level:error | stats count()')).toEqual([
      'tql-field@0-5',
      'tql-operator@5-6',
      'tql-value@6-11',
      'tql-pipe@12-13',
      'tql-keyword@14-19',
      'tql-function@20-25',
      'tql-punct@25-26',
      'tql-punct@26-27',
    ]);
  });
});

describe('queryErrorField', () => {
  function errorRanges(state: EditorState): string[] {
    const out: string[] = [];
    state.field(queryErrorField).between(0, state.doc.length, (from, to) => {
      out.push(`${from}-${to}`);
    });
    return out;
  }

  it('sets, clamps and clears the error mark', () => {
    let state = EditorState.create({ doc: 'level:', extensions: [queryErrorField] });
    state = state.update({ effects: setQueryError.of({ from: 6, to: 9 }) }).state;
    expect(errorRanges(state)).toEqual(['5-6']);
    state = state.update({ effects: setQueryError.of({ from: 0, to: 0 }) }).state;
    expect(errorRanges(state)).toEqual(['0-1']);
    state = state.update({ effects: setQueryError.of(null) }).state;
    expect(errorRanges(state)).toEqual([]);
  });

  it('ignores errors on an empty document', () => {
    let state = EditorState.create({ doc: '', extensions: [queryErrorField] });
    state = state.update({ effects: setQueryError.of({ from: 0, to: 1 }) }).state;
    expect(errorRanges(state)).toEqual([]);
  });
});

describe('singleLine', () => {
  it('replaces pasted newlines with spaces', () => {
    const state = EditorState.create({ doc: '', extensions: [singleLine] });
    const next = state.update({ changes: { from: 0, insert: 'level:error\n  | stats count()' } }).state;
    expect(next.doc.toString()).toBe('level:error | stats count()');
  });

  it('leaves single-line edits and selection-only transactions alone', () => {
    const state = EditorState.create({ doc: 'a', extensions: [singleLine] });
    expect(state.update({ changes: { from: 1, insert: 'b' } }).state.doc.toString()).toBe('ab');
    expect(state.update({ selection: { anchor: 0 } }).state.doc.toString()).toBe('a');
  });
});

describe('typeOverSelection', () => {
  function beforeInput(view: EditorView, init: InputEventInit): InputEvent {
    const event = new InputEvent('beforeinput', { bubbles: true, cancelable: true, ...init });
    view.contentDOM.dispatchEvent(event);
    return event;
  }

  it('replaces a selection with the typed text itself and leaves other input to the browser', () => {
    const view = new EditorView({
      parent: document.body,
      state: EditorState.create({
        doc: 'level:error',
        selection: { anchor: 0, head: 11 },
        extensions: [typeOverSelection],
      }),
    });
    const over = beforeInput(view, { inputType: 'insertText', data: 'x' });
    expect(over.defaultPrevented).toBe(true);
    expect(view.state.doc.toString()).toBe('x');
    expect(view.state.selection.main.head).toBe(1);
    const plain = beforeInput(view, { inputType: 'insertText', data: 'y' });
    expect(plain.defaultPrevented).toBe(false);
    view.dispatch({ selection: { anchor: 0, head: 1 } });
    const composition = beforeInput(view, { inputType: 'insertCompositionText', data: 'z' });
    expect(composition.defaultPrevented).toBe(false);
    expect(view.state.doc.toString()).toBe('x');
    view.destroy();
  });
});

const completion: Completion = {
  from: 7,
  to: 9,
  suggestions: [
    { kind: 'value', label: 'error', insertText: 'error', detail: '12 k' },
    { kind: 'value', label: 'warning', insertText: 'warning' },
  ],
};

describe('toCompletionResult', () => {
  it('converts byte offsets to UTF-16 and keeps server order', () => {
    // "ä" is two UTF-8 bytes but one UTF-16 unit.
    const r = toCompletionResult('lävel:er', completion);
    expect(r.from).toBe(6);
    expect(r.to).toBe(8);
    expect(r.filter).toBe(false);
    expect(r.options.map((o) => [o.label, o.apply, o.type, o.boost])).toEqual([
      ['error', 'error', 'value', -0],
      ['warning', 'warning', 'value', -1],
    ]);
  });
});

describe('tqlCompletionSource', () => {
  function context(doc: string, explicit: boolean): CompletionContext {
    const state = EditorState.create({ doc });
    return new CompletionContext(state, doc.length, explicit);
  }

  it('asks the server with the byte position of the cursor', async () => {
    const fetch = vi.fn(() => Promise.resolve(completion));
    const r = await tqlCompletionSource(fetch)(context('level:er', true));
    expect(fetch).toHaveBeenCalledWith('level:er', 8, expect.any(AbortSignal));
    expect(r?.options).toHaveLength(2);
  });

  it('stays quiet after characters that cannot start a suggestion', async () => {
    const fetch = vi.fn(() => Promise.resolve(completion));
    expect(await tqlCompletionSource(fetch)(context('"quoted"', false))).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('returns null for empty answers and failures', async () => {
    const empty = vi.fn(() => Promise.resolve({ from: 0, to: 0, suggestions: [] }));
    expect(await tqlCompletionSource(empty)(context('x', false))).toBeNull();
    const failing = vi.fn(() => Promise.reject(new Error('down')));
    expect(await tqlCompletionSource(failing)(context('x', true))).toBeNull();
  });
});

describe('CHEATSHEET', () => {
  it('only contains queries the highlighter can tokenize completely', () => {
    for (const e of CHEATSHEET) {
      expect(
        tokenize(e.q)
          .map((t) => t.text)
          .join(''),
      ).toBe(e.q);
    }
  });
});
