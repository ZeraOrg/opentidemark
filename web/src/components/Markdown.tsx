// SPDX-License-Identifier: Apache-2.0
//
// Renders the safe Markdown subset of lib/markdown.ts with React elements (no raw HTML, SPEC
// §15.7).

import type { ReactNode } from 'react';
import { parseMarkdown, type Inline } from '../lib/markdown';
import { layoutStyles as l } from './layout';

function inline(nodes: readonly Inline[], key: string): ReactNode[] {
  return nodes.map((n, i) => {
    const k = `${key}.${i}`;
    switch (n.kind) {
      case 'text':
        return n.text;
      case 'code':
        return <code key={k}>{n.text}</code>;
      case 'strong':
        return <strong key={k}>{inline(n.children, k)}</strong>;
      case 'em':
        return <em key={k}>{inline(n.children, k)}</em>;
      case 'link':
        return (
          <a
            key={k}
            href={n.href}
            rel="noopener noreferrer"
            target={n.href.startsWith('/') ? undefined : '_blank'}
          >
            {inline(n.children, k)}
          </a>
        );
    }
  });
}

export function Markdown({ source }: { source: string }) {
  return (
    <div className={l.markdown}>
      {parseMarkdown(source).map((b, i) => {
        const k = `b${i}`;
        switch (b.kind) {
          case 'heading':
            return b.level === 1 ? (
              <h2 key={k}>{inline(b.children, k)}</h2>
            ) : b.level === 2 ? (
              <h3 key={k}>{inline(b.children, k)}</h3>
            ) : (
              <h4 key={k}>{inline(b.children, k)}</h4>
            );
          case 'paragraph':
            return <p key={k}>{inline(b.children, k)}</p>;
          case 'list': {
            const items = b.items.map((it, j) => <li key={`${k}.${j}`}>{inline(it, `${k}.${j}`)}</li>);
            return b.ordered ? <ol key={k}>{items}</ol> : <ul key={k}>{items}</ul>;
          }
          case 'code':
            return (
              <pre key={k} className={l.code}>
                {b.text}
              </pre>
            );
        }
      })}
    </div>
  );
}
