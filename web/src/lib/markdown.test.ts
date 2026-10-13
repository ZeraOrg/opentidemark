// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { parseInline, parseMarkdown, safeHref } from './markdown';

describe('safeHref', () => {
  it.each([
    ['https://example.com/a', 'https://example.com/a'],
    ['HTTP://x', 'HTTP://x'],
    ['mailto:ops@example.com', 'mailto:ops@example.com'],
    ['/dashboards/1', '/dashboards/1'],
    ['//evil.example', null],
    ['javascript:alert(1)', null],
    ['data:text/html,x', null],
    ['relative/path', null],
  ])('%s', (href, want) => {
    expect(safeHref(href)).toBe(want);
  });
});

describe('parseInline', () => {
  it('parses code, strong, em and links', () => {
    expect(parseInline('a `b` **c** *d* _e_ [f](https://x.io)')).toEqual([
      { kind: 'text', text: 'a ' },
      { kind: 'code', text: 'b' },
      { kind: 'text', text: ' ' },
      { kind: 'strong', children: [{ kind: 'text', text: 'c' }] },
      { kind: 'text', text: ' ' },
      { kind: 'em', children: [{ kind: 'text', text: 'd' }] },
      { kind: 'text', text: ' ' },
      { kind: 'em', children: [{ kind: 'text', text: 'e' }] },
      { kind: 'text', text: ' ' },
      { kind: 'link', href: 'https://x.io', children: [{ kind: 'text', text: 'f' }] },
    ]);
  });

  it('turns unsafe links into their label and keeps HTML as text', () => {
    expect(parseInline('[x](javascript:alert(1)) <b>hi</b>')).toEqual([
      { kind: 'text', text: 'x' },
      { kind: 'text', text: ') <b>hi</b>' },
    ]);
  });

  it('does not treat snake_case as emphasis', () => {
    expect(parseInline('user_id and order_id')).toEqual([{ kind: 'text', text: 'user_id and order_id' }]);
  });
});

describe('parseMarkdown', () => {
  it('parses headings, paragraphs, lists and code fences', () => {
    const src = [
      '# Title',
      '## Sub',
      '### Small',
      'para one',
      'continues',
      '',
      '- a',
      '* b',
      '1. one',
      '2) two',
      '```',
      '<script>x</script>',
      '```',
      'end',
    ].join('\r\n');
    expect(parseMarkdown(src)).toEqual([
      { kind: 'heading', level: 1, children: [{ kind: 'text', text: 'Title' }] },
      { kind: 'heading', level: 2, children: [{ kind: 'text', text: 'Sub' }] },
      { kind: 'heading', level: 3, children: [{ kind: 'text', text: 'Small' }] },
      { kind: 'paragraph', children: [{ kind: 'text', text: 'para one continues' }] },
      {
        kind: 'list',
        ordered: false,
        items: [[{ kind: 'text', text: 'a' }], [{ kind: 'text', text: 'b' }]],
      },
      {
        kind: 'list',
        ordered: true,
        items: [[{ kind: 'text', text: 'one' }], [{ kind: 'text', text: 'two' }]],
      },
      { kind: 'code', text: '<script>x</script>' },
      { kind: 'paragraph', children: [{ kind: 'text', text: 'end' }] },
    ]);
  });

  it('closes an unterminated fence at the end', () => {
    expect(parseMarkdown('```\ncode')).toEqual([{ kind: 'code', text: 'code' }]);
  });

  it('returns nothing for blank input', () => {
    expect(parseMarkdown('\n\n')).toEqual([]);
  });
});
