// SPDX-License-Identifier: Apache-2.0
//
// Read-only TQL with the query-bar token colours (DESIGN §3.2): rule lists, generated TQL,
// signal queries.

import { tokenize } from '../tql/tokenize';
import { cx } from '../lib/cx';
import s from './TqlText.module.css';

export function TqlText({ q, className, wrap }: { q: string; className?: string; wrap?: boolean }) {
  return (
    <code className={cx(s.tql, wrap === true && s.wrap, className)} title={q}>
      {tokenize(q).map((t) =>
        t.kind === 'space' ? (
          t.text
        ) : (
          <span key={t.from} className={`tql-${t.kind}`}>
            {t.text}
          </span>
        ),
      )}
    </code>
  );
}
