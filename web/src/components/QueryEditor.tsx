// SPDX-License-Identifier: Apache-2.0
//
// The TQL query bar (SPEC §16.3, DESIGN §5.7): CodeMirror with TQL highlighting, server-side
// autocomplete (§10.7), the error squiggle with a popover (and a "did you mean" fix), history on
// ↑/↓, Enter / Ctrl+Enter to run and a free-text notice.

import {
  acceptCompletion,
  autocompletion,
  completionStatus,
  startCompletion,
} from '@codemirror/autocomplete';
import { defaultKeymap, history, historyKeymap } from '@codemirror/commands';
import { EditorState, Prec } from '@codemirror/state';
import { EditorView, keymap, placeholder as cmPlaceholder } from '@codemirror/view';
import { AlertCircle, Search } from 'lucide-react';
import { useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { search } from '../api/endpoints';
import type { ScopeParams, Source } from '../api/types';
import { useApi } from '../app/services';
import { cx } from '../lib/cx';
import {
  queryErrorField,
  setQueryError,
  singleLine,
  tqlCompletionSource,
  tqlHighlight,
} from '../tql/codemirror';
import s from './QueryEditor.module.css';

export interface QueryError {
  from: number;
  to: number;
  message: string;
  hint?: string;
  /** Byte position and length as reported by the API (shown to the user). */
  pos?: number;
  len?: number;
}

export interface QueryEditorProps {
  value: string;
  onChange: (v: string) => void;
  onSubmit?: (v: string) => void;
  ariaLabel: string;
  source?: Source;
  error?: QueryError | null;
  /** `text` = interpreted as free text (warning border). */
  tone?: 'text' | null;
  placeholder?: string;
  history?: readonly string[];
  readOnly?: boolean;
  size?: 'md' | 'sm';
  trailing?: ReactNode;
  className?: string;
  /** Receives the editor so the shell can focus it with `/`. */
  onView?: (view: EditorView | null) => void;
  showIcon?: boolean;
  /** Narrows completions (field names and values) to the selected streams (SPEC §11.3). */
  scope?: ScopeParams;
}

/** Extracts "did you mean 'x'?" suggestions from an error hint. */
export function didYouMean(hint: string | undefined): string | null {
  const m = /did you mean\s+['"`]?([^'"`?]+)['"`]?\s*\?/i.exec(hint ?? '');
  return m?.[1]?.trim() ?? null;
}

/** Moves through the query history when the caret is at the start (↑) or end (↓). */
function stepHistory(v: EditorView, dir: 1 | -1, pos: { current: number }, list: readonly string[]): boolean {
  if (completionStatus(v.state) !== null) return false;
  const head = v.state.selection.main.head;
  if (dir === 1 && head !== 0 && pos.current < 0) return false;
  if (dir === -1 && head !== v.state.doc.length) return false;
  const next = pos.current + dir;
  if (next < -1 || next >= list.length) return false;
  const text = next === -1 ? '' : (list[next] ?? '');
  v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: text } });
  pos.current = next;
  return true;
}

export function QueryEditor(props: QueryEditorProps) {
  const {
    value,
    ariaLabel,
    source = 'logs',
    error,
    tone,
    placeholder,
    readOnly,
    size = 'md',
    trailing,
    className,
    showIcon = true,
  } = props;
  const api = useApi();
  const host = useRef<HTMLDivElement>(null);
  const wrap = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const latest = useRef(props);
  latest.current = props;
  const sourceRef = useRef(source);
  sourceRef.current = source;
  const scopeRef = useRef(props.scope);
  scopeRef.current = props.scope;
  const historyPos = useRef(-1);
  const [focused, setFocused] = useState(false);
  const [errorLeft, setErrorLeft] = useState(0);

  const extensions = useMemo(
    () => [
      history(),
      singleLine,
      tqlHighlight,
      queryErrorField,
      EditorView.lineWrapping,
      EditorView.contentAttributes.of({ 'aria-label': ariaLabel, spellcheck: 'false' }),
      autocompletion({
        override: [
          tqlCompletionSource((q, pos, signal) =>
            search.complete(api, { q, pos, source: sourceRef.current, ...scopeRef.current }, signal),
          ),
        ],
        icons: false,
        maxRenderedOptions: 50,
        optionClass: (c) => `cm-tql-opt-${c.type ?? 'field'}`,
        addToOptions: [
          {
            position: 10,
            render: (c) => {
              const el = document.createElement('span');
              el.className = 'cm-tql-kind';
              el.textContent = c.type ?? '';
              return el;
            },
          },
        ],
      }),
      Prec.highest(
        keymap.of([
          { key: 'Tab', run: acceptCompletion },
          {
            key: 'Mod-Enter',
            run: (v) => {
              latest.current.onSubmit?.(v.state.doc.toString());
              return true;
            },
          },
          {
            key: 'Enter',
            run: (v) => {
              if (completionStatus(v.state) === 'active') return false;
              latest.current.onSubmit?.(v.state.doc.toString());
              return true;
            },
          },
          {
            key: 'ArrowUp',
            run: (v) => stepHistory(v, 1, historyPos, latest.current.history ?? []),
          },
          {
            key: 'ArrowDown',
            run: (v) => stepHistory(v, -1, historyPos, latest.current.history ?? []),
          },
          { key: 'Ctrl-Space', run: startCompletion },
        ]),
      ),
      keymap.of([...defaultKeymap, ...historyKeymap]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) {
          historyPos.current = -1;
          latest.current.onChange(u.state.doc.toString());
        }
        if (u.focusChanged) setFocused(u.view.hasFocus);
      }),
      EditorState.readOnly.of(readOnly === true),
      EditorView.editable.of(readOnly !== true),
      ...(placeholder !== undefined ? [cmPlaceholder(placeholder)] : []),
    ],
    // The editor is created once per configuration; value changes are synced below.
    [api, ariaLabel, placeholder, readOnly],
  );

  useEffect(() => {
    const parent = host.current;
    if (parent === null) return undefined;
    const view = new EditorView({
      parent,
      state: EditorState.create({ doc: latest.current.value, extensions }),
    });
    viewRef.current = view;
    latest.current.onView?.(view);
    return () => {
      latest.current.onView?.(null);
      viewRef.current = null;
      view.destroy();
    };
  }, [extensions]);

  useEffect(() => {
    const view = viewRef.current;
    if (view === null) return;
    const current = view.state.doc.toString();
    if (current !== value) {
      view.dispatch({
        changes: { from: 0, to: current.length, insert: value },
        selection: { anchor: value.length },
      });
    }
  }, [value]);

  const errFrom = error?.from;
  const errTo = error?.to;
  useLayoutEffect(() => {
    const view = viewRef.current;
    if (view === null) return;
    view.dispatch({
      effects: setQueryError.of(
        errFrom === undefined || errTo === undefined ? null : { from: errFrom, to: errTo },
      ),
    });
    if (errFrom !== undefined && wrap.current !== null) {
      const box = wrap.current.getBoundingClientRect();
      const c = view.coordsAtPos(Math.min(errFrom, view.state.doc.length));
      setErrorLeft(c === null ? 0 : Math.max(0, Math.min(c.left - box.left - 8, box.width - 360)));
    }
  }, [errFrom, errTo, value]);

  const fix = didYouMean(error?.hint);
  const applyFix = (): void => {
    if (error === null || error === undefined || fix === null) return;
    const v = viewRef.current;
    if (v === null) return;
    v.dispatch({ changes: { from: error.from, to: error.to, insert: fix } });
    v.focus();
  };

  return (
    <div ref={wrap} className={cx(s.wrap, className)}>
      <div
        className={cx(
          s.bar,
          size === 'sm' && s.sm,
          focused && s.focused,
          error && s.error,
          !error && tone === 'text' && s.text,
          readOnly === true && s.readOnly,
        )}
      >
        {showIcon ? <Search size={14} className={s.icon} aria-hidden="true" /> : null}
        <div ref={host} className={s.host} />
        {trailing !== undefined ? <div className={s.trailing}>{trailing}</div> : null}
      </div>
      {error ? (
        <div className={s.errorPop} role="alert" style={{ left: errorLeft }}>
          <AlertCircle size={16} aria-hidden="true" />
          <div>
            <div className={s.errorMsg}>
              <strong>{error.message}</strong>
              {fix !== null ? (
                <>
                  {' — did you mean '}
                  <button type="button" className={s.fix} onClick={applyFix}>
                    {fix}
                  </button>
                  ?
                </>
              ) : null}
            </div>
            <div className={s.errorMeta}>
              {error.pos !== undefined ? `pos ${error.pos} · len ${error.len ?? 1}` : null}
              {error.hint !== undefined && fix === null ? ` · ${error.hint}` : null}
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
