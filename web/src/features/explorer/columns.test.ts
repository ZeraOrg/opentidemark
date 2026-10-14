// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { LogEvent } from '../../api/types';
import {
  BUILTIN_COLUMN_FIELDS,
  canShowAsColumn,
  columnChoices,
  columnValue,
  rowMinWidth,
  rowTemplate,
} from './columns';

const ev: LogEvent = {
  id: 'e1',
  timestamp: '2026-10-13T12:00:00Z',
  stream: 'default',
  level: 'Information',
  service: 'checkout',
  environment: 'prod',
  host: 'web-1',
  messageTemplate: 'Order {OrderId} placed',
  message: 'Order 42 placed',
  exception: '',
  eventType: 'a1b2c3d4',
  traceId: '4bf92f3577b34da6a3ce929d0e0e4736',
  spanId: '00f067aa0ba902b7',
  protocol: 'seq',
  connectionId: 'c1',
  properties: { OrderId: 42, host: 'shadowed', Tags: ['a', 'b'], Note: null },
};

describe('event-list columns', () => {
  it.each([
    ['OrderId', false, true],
    ['host', true, true],
    ['trace_id', true, true],
    ['environment', true, true],
    ['service', true, false],
    ['level', true, false],
    ['@timestamp', true, false],
    ['template', true, false],
  ])('canShowAsColumn(%j, builtin=%j) = %j', (field, builtin, want) => {
    expect(canShowAsColumn(field, builtin)).toBe(want);
  });

  it.each([
    ['environment', 'prod'],
    ['host', 'web-1'],
    ['trace_id', '4bf92f3577b34da6a3ce929d0e0e4736'],
    ['span_id', '00f067aa0ba902b7'],
    ['event_type', 'a1b2c3d4'],
    ['OrderId', '42'],
    ['Tags', '["a","b"]'],
    ['Note', 'null'],
    ['Missing', ''],
  ])('columnValue(%j) = %j', (field, want) => {
    expect(columnValue(ev, field)).toBe(want);
  });

  it('lists every built-in column field', () => {
    expect(BUILTIN_COLUMN_FIELDS).toEqual(['environment', 'host', 'trace_id', 'span_id', 'event_type']);
  });
});

describe('row layout with shown columns', () => {
  it('lets shown columns shrink before the message and computes the scroll width', () => {
    expect(rowTemplate(0)).toBe('148px 56px 148px minmax(160px, 1fr)');
    expect(rowTemplate(2)).toBe(
      '148px 56px 148px minmax(72px, 140px) minmax(72px, 140px) minmax(160px, 1fr)',
    );
    expect(rowMinWidth(0)).toBe(148 + 56 + 148 + 160 + 3 * 12 + 32);
    expect(rowMinWidth(6)).toBe(148 + 56 + 148 + 6 * 72 + 160 + 9 * 12 + 32);
  });
});

describe('columnChoices', () => {
  it('lists shown columns in order and filters the rest, keeping pinned fields outside the range', () => {
    const r = columnChoices(['host', 'OrderId', 'UserId', 'host'], ['UserId', 'gone'], 'o');
    expect(r.shown).toEqual(['UserId', 'gone']);
    expect(r.available).toEqual(['host', 'OrderId']);
    expect(columnChoices(['a'], ['a'], '').available).toEqual([]);
  });
});
