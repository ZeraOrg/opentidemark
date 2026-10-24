// SPDX-License-Identifier: Apache-2.0
//
// A tolerant TQL tokenizer for syntax highlighting and light-weight structure (pipe splitting,
// variable positions). It follows the lexical rules of SPEC §10.2 closely enough to colour every
// token class of DESIGN §3.2, but never rejects input: the server remains the parser.

export type TokenKind =
  | 'space'
  | 'keyword'
  | 'field'
  | 'operator'
  | 'punct'
  | 'value'
  | 'string'
  | 'number'
  | 'regex'
  | 'function'
  | 'pipe'
  | 'variable';

export interface Token {
  kind: TokenKind;
  from: number;
  to: number;
  text: string;
}

const KEYWORDS = new Set([
  'and',
  'or',
  'not',
  'in',
  'source',
  'stats',
  'by',
  'span',
  'sort',
  'asc',
  'desc',
  'limit',
  'fields',
  'where',
  'top',
  'as',
  'like',
  'ci',
  'true',
  'false',
  'null',
]);

export const STAGES = new Set(['stats', 'sort', 'limit', 'fields', 'where', 'top']);

export const FUNCTIONS = new Set([
  'count',
  'count_distinct',
  'sum',
  'avg',
  'min',
  'max',
  'p50',
  'p75',
  'p90',
  'p95',
  'p99',
  'rate',
  'first',
  'last',
  'hist_mean',
  'histogram_quantile',
  'contains',
  'startswith',
  'endswith',
  'has',
  'match',
]);

const OPERATORS = ['>=', '<=', '!=', '<>', '==', '&&', '||', '=', '<', '>', ':', '!'];
/** Arithmetic between aggregates in a stats stage (SPEC §10.5): `sum(a) / count()`. */
const ARITHMETIC = new Set(['+', '-', '*', '/']);
/** Operators after which a word is not a value: negation, boolean junctions and arithmetic. */
const NOT_VALUE_OPERATORS = new Set(['!', '&&', '||', ...ARITHMETIC]);
const WORD_STOP = new Set([
  ' ',
  '\t',
  '\n',
  '\r',
  '(',
  ')',
  '[',
  ']',
  ',',
  '|',
  ':',
  '=',
  '<',
  '>',
  "'",
  '"',
  '`',
]);
const NUMBER = /^-?\d+(\.\d+)?([eE][+-]?\d+)?(ns|us|ms|s|m|h|d)?$/;
const VARIABLE = /^\$[A-Za-z_][A-Za-z0-9_]*/;
/** Stages whose bare identifiers are field names or aggregate aliases. */
const FIELD_STAGES = new Set(['stats', 'sort', 'fields', 'top']);

function isSpace(c: string): boolean {
  return c === ' ' || c === '\t' || c === '\n' || c === '\r';
}

function readQuoted(q: string, i: number): number {
  const quote = q.charAt(i);
  let j = i + 1;
  while (j < q.length) {
    const c = q.charAt(j);
    if (c === '\\') {
      j += 2;
      continue;
    }
    if (c === quote) return j + 1;
    j++;
  }
  return q.length;
}

/** End of a regex literal starting at `i`, or -1 when the slash does not start a regex. */
function readRegex(q: string, i: number): number {
  let j = i + 1;
  while (j < q.length) {
    const c = q.charAt(j);
    if (c === '\\') {
      j += 2;
      continue;
    }
    if (c === '/') {
      const after = q.charAt(j + 1);
      return after === '' || isSpace(after) || after === ')' || after === '|' ? j + 1 : -1;
    }
    j++;
  }
  return -1;
}

/**
 * End of the word starting at `i`. In an aggregate list (`arith`) an arithmetic operator ends
 * the word (`2*count()`), except as the sign of a number (`-1`).
 */
function readWord(q: string, i: number, valuePos: boolean, arith: boolean): number {
  let j = i;
  while (j < q.length) {
    const c = q.charAt(j);
    if (c === '\\' && valuePos) {
      j += 2;
      continue;
    }
    if (WORD_STOP.has(c) && !(valuePos && (c === ':' || c === '=' || c === '<' || c === '>'))) break;
    if (c === '!' && (q.charAt(j + 1) === '=' || j === i)) break;
    if (c === '&' && q.charAt(j + 1) === '&') break;
    if (arith && j > i && ARITHMETIC.has(c)) break;
    j++;
  }
  return Math.max(j, i + 1);
}

function nextNonSpace(q: string, i: number): string {
  let j = i;
  while (j < q.length && isSpace(q.charAt(j))) j++;
  return q.slice(j, j + 2);
}

interface State {
  prev: Token | undefined;
  prevWord: string;
  stage: string;
  parenFn: boolean;
  /** In the aggregate list of a stats stage (before `by` / `span`), where `+ - * /` are arithmetic. */
  aggList: boolean;
}

/** Whether `+ - * /` here is arithmetic: in an aggregate list after a call's `)` or a number. */
function arithmeticHere(st: State): boolean {
  if (!st.aggList || st.parenFn) return false;
  return st.prev?.text === ')' || st.prev?.kind === 'number';
}

/** Whether a word here is an arithmetic operand (not a field in a call or an alias after `as`). */
function arithmeticOperand(st: State): boolean {
  return st.aggList && !st.parenFn && st.prevWord !== 'as';
}

function classifyWord(word: string, q: string, end: number, st: State): TokenKind {
  const lower = word.toLowerCase();
  const prevKind = st.prev?.kind;
  const prevText = st.prev?.text ?? '';
  const valuePos = prevKind === 'operator' && !NOT_VALUE_OPERATORS.has(prevText);
  if (valuePos) return NUMBER.test(word) ? 'number' : 'value';
  if (st.prev?.kind === 'pipe' && STAGES.has(lower)) return 'keyword';
  const ahead = nextNonSpace(q, end);
  if (FUNCTIONS.has(lower) && ahead.startsWith('(')) return 'function';
  if (st.parenFn) return NUMBER.test(word) ? 'number' : 'field';
  if (lower === 'source' && st.prev === undefined && !/^[:=<>!]/.test(ahead)) return 'keyword';
  if (st.prevWord === 'source' && ['logs', 'spans', 'metrics'].includes(lower)) return 'value';
  if (st.prevWord === 'span' || st.prevWord === 'limit' || st.prevWord === 'top') {
    return NUMBER.test(word) || lower === 'auto' ? 'number' : 'field';
  }
  if (KEYWORDS.has(lower) && !/^[:=<>!]/.test(ahead)) return 'keyword';
  if (/^(:|=|<|>|!=)/.test(ahead)) return 'field';
  if (NUMBER.test(word)) return 'number';
  if (FIELD_STAGES.has(st.stage)) return 'field';
  return 'value';
}

/** Splits `q` into highlight tokens covering every character. */
export function tokenize(q: string): Token[] {
  const out: Token[] = [];
  const st: State = { prev: undefined, prevWord: '', stage: '', parenFn: false, aggList: false };
  const push = (kind: TokenKind, from: number, to: number): void => {
    const t: Token = { kind, from, to, text: q.slice(from, to) };
    out.push(t);
    if (kind === 'space') return;
    const lower = t.text.toLowerCase();
    if (kind === 'keyword' && STAGES.has(lower) && st.prev?.kind === 'pipe') {
      st.stage = lower;
      st.aggList = lower === 'stats';
    } else if (kind === 'keyword' && (lower === 'by' || lower === 'span')) {
      st.aggList = false;
    }
    st.prev = t;
    st.prevWord =
      kind === 'keyword' || kind === 'field' || kind === 'value' || kind === 'number' ? lower : '';
  };
  let i = 0;
  while (i < q.length) {
    const c = q.charAt(i);
    if (isSpace(c)) {
      let j = i;
      while (j < q.length && isSpace(q.charAt(j))) j++;
      push('space', i, j);
      i = j;
      continue;
    }
    if (c === '|' && q.charAt(i + 1) !== '|') {
      st.stage = '';
      st.aggList = false;
      push('pipe', i, i + 1);
      i++;
      continue;
    }
    if (c === "'" || c === '"') {
      const end = readQuoted(q, i);
      push('string', i, end);
      i = end;
      continue;
    }
    if (c === '`') {
      const end = readQuoted(q, i);
      push('field', i, end);
      i = end;
      continue;
    }
    if (c === '$') {
      const m = VARIABLE.exec(q.slice(i));
      if (m) {
        push('variable', i, i + m[0].length);
        i += m[0].length;
        continue;
      }
    }
    if (ARITHMETIC.has(c) && arithmeticHere(st)) {
      push('operator', i, i + 1);
      i++;
      continue;
    }
    if (c === '/' && st.prev?.kind === 'operator') {
      const end = readRegex(q, i);
      if (end > 0) {
        push('regex', i, end);
        i = end;
        continue;
      }
    }
    if (c === '(' || c === ')' || c === '[' || c === ']' || c === ',') {
      if (c === '(') st.parenFn = st.prev?.kind === 'function';
      if (c === ')') st.parenFn = false;
      push('punct', i, i + 1);
      i++;
      continue;
    }
    const op = OPERATORS.find((o) => q.startsWith(o, i));
    if (op !== undefined) {
      push('operator', i, i + op.length);
      i += op.length;
      continue;
    }
    const valuePos = st.prev?.kind === 'operator';
    const end = readWord(q, i, valuePos && st.prev?.text === ':', arithmeticOperand(st));
    const word = q.slice(i, end);
    push(classifyWord(word, q, end, st), i, end);
    i = end;
  }
  return out;
}

/** Splits a query into its pipe-separated parts (outside quotes), with their start offsets. */
export function splitPipes(q: string): { text: string; from: number }[] {
  const parts: { text: string; from: number }[] = [];
  let start = 0;
  for (const t of tokenize(q)) {
    if (t.kind === 'pipe') {
      parts.push({ text: q.slice(start, t.from), from: start });
      start = t.to;
    }
  }
  parts.push({ text: q.slice(start), from: start });
  return parts;
}

/** Names of the `$variables` used in a query. */
export function variablesOf(q: string): string[] {
  const names = new Set<string>();
  for (const t of tokenize(q)) if (t.kind === 'variable') names.add(t.text.slice(1));
  return [...names];
}
