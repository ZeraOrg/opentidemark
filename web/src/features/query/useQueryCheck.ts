// SPDX-License-Identifier: Apache-2.0
//
// Live query validation for every TQL editor (SPEC §11.3 POST /query/validate, §16.3): the draft
// is checked 250 ms after the last keystroke; the byte offsets of an error are mapped to UTF-16
// positions of the draft so CodeMirror can underline the right characters.

import { useQuery } from '@tanstack/react-query';
import { isApiError } from '../../api/client';
import { search } from '../../api/endpoints';
import type { ResultKind, ValidateResponse } from '../../api/types';
import { useApi } from '../../app/services';
import type { QueryError } from '../../components/QueryEditor';
import { useDebounced } from '../../lib/hooks';
import { byteRangeToUtf16 } from '../../lib/utf8';

export const VALIDATE_DEBOUNCE_MS = 250;

export interface QueryCheck {
  error: QueryError | null;
  kind: ResultKind | null;
  interpretedAsText: boolean;
  /** The checked text (results belong to it, not necessarily to the current draft). */
  checked: string;
}

/** Converts a validation response for `q` into editor state. */
export function toCheck(q: string, v: ValidateResponse | undefined): QueryCheck {
  if (v === undefined) return { error: null, kind: null, interpretedAsText: false, checked: q };
  if (v.ok || v.error === undefined) {
    return { error: null, kind: v.kind ?? null, interpretedAsText: v.interpretedAsText, checked: q };
  }
  const r = byteRangeToUtf16(q, v.error.pos, Math.max(1, v.error.len));
  return {
    error: { ...r, message: v.error.message, hint: v.error.hint, pos: v.error.pos, len: v.error.len },
    kind: null,
    interpretedAsText: false,
    checked: q,
  };
}

/** Maps a POST /query `invalid_query` failure onto the editor. */
export function errorFromApi(q: string, e: unknown): QueryError | null {
  if (!isApiError(e) || e.code !== 'invalid_query') return null;
  const r = e.queryRange(q);
  if (r === null) return null;
  const pos = typeof e.details.pos === 'number' ? e.details.pos : undefined;
  const len = typeof e.details.len === 'number' ? e.details.len : undefined;
  return { ...r, message: e.message, hint: e.hint, pos, len };
}

export function useQueryCheck(draft: string, enabled = true): QueryCheck {
  const api = useApi();
  const q = useDebounced(draft, VALIDATE_DEBOUNCE_MS);
  const res = useQuery({
    queryKey: ['validate', q],
    queryFn: ({ signal }) => search.validate(api, q, signal),
    enabled: enabled && q.trim() !== '',
    staleTime: 60_000,
  });
  if (q !== draft) return { error: null, kind: null, interpretedAsText: false, checked: q };
  return toCheck(q, res.data);
}
