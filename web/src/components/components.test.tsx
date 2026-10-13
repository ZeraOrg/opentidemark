// SPDX-License-Identifier: Apache-2.0

import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { Markdown } from './Markdown';
import { TqlText } from './TqlText';

describe('Markdown', () => {
  it('renders the safe subset with React elements', () => {
    const { container } = render(
      <Markdown
        source={[
          '# Runbook',
          '## Steps',
          '### Notes',
          'See **the** *docs* at [site](https://example.com) or [home](/events).',
          '- one',
          '1. first',
          '```',
          '<img src=x onerror=alert(1)>',
          '```',
        ].join('\n')}
      />,
    );
    expect(screen.getByRole('heading', { level: 2, name: 'Runbook' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 3, name: 'Steps' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { level: 4, name: 'Notes' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'site' })).toHaveAttribute('target', '_blank');
    expect(screen.getByRole('link', { name: 'home' })).not.toHaveAttribute('target');
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('pre')).toHaveTextContent('<img src=x onerror=alert(1)>');
    expect(container.querySelectorAll('ul li, ol li')).toHaveLength(2);
    expect(container.querySelector('strong')).toHaveTextContent('the');
    expect(container.querySelector('em')).toHaveTextContent('docs');
  });
});

describe('TqlText', () => {
  it('highlights every token and keeps the text intact', () => {
    const q = 'level:error | stats count()';
    const { container } = render(<TqlText q={q} wrap />);
    expect(container.textContent).toBe(q);
    expect(container.querySelector('.tql-field')).toHaveTextContent('level');
    expect(container.querySelector('.tql-function')).toHaveTextContent('count');
    expect(container.querySelector('code')).toHaveAttribute('title', q);
  });
});
