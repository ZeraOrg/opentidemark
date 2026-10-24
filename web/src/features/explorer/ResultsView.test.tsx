// SPDX-License-Identifier: Apache-2.0

import { render, screen } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { QueryResponse } from '../../api/types';
import { Chart } from '../../components/Chart';
import { Providers, testServices } from '../../test/fakes';
import { TableResult } from './ResultsView';

const stats = { elapsedMs: 3, rowsRead: 10, bytesRead: 100, interpretedAsText: false };

describe('aggregate values without observations (null)', () => {
  it('leave an empty table cell and no proportion bar', () => {
    const resp = {
      kind: 'table',
      columns: [
        { name: 'route', type: 'string' },
        { name: 'histogram_quantile_0_95', type: 'number' },
      ],
      rows: [
        ['GET /cart', 0.42],
        ['other', null],
      ],
      stats,
    } as QueryResponse;
    render(<TableResult resp={resp} />);
    expect(screen.getByRole('columnheader', { name: 'histogram_quantile_0_95' })).toBeInTheDocument();
    const cells = screen.getAllByRole('cell').map((c) => c.textContent);
    expect(cells).toEqual(['GET /cart', '0.42', '', 'other', '', '']);
  });

  it('mount a chart with a gap in every series', () => {
    render(
      <Providers services={testServices()}>
        <Chart
          data={{
            x: [60_000, 120_000, 180_000],
            series: [
              { label: 'hist_mean', values: [0.12, null, 0.14] },
              { label: 'histogram_quantile_0_95', values: [null, null, null] },
            ],
          }}
          height={120}
          ariaLabel="Latency"
          unit="s"
        />
      </Providers>,
    );
    expect(screen.getByRole('img', { name: 'Latency: 2 series, 3 points' })).toBeInTheDocument();
    const legend = screen.getByRole('list', { name: 'Legend' });
    expect(legend).toHaveTextContent('hist_mean');
    expect(legend).toHaveTextContent('histogram_quantile_0_95');
  });
});
