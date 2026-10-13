// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import type { Alert, Span, Stream, SourceStats } from '../api/types';
import { passwordProblem } from './auth/password';
import {
  conditionText,
  groupText,
  isMuted,
  newRule,
  previewQuery,
  stateChip,
  stateVariant,
  withGroupBy,
} from './alerts/rules';
import { refreshMs, valuesLabel, variablePatch, variableValues } from './dashboards/vars';
import {
  dropsPartitions,
  estimateBytes,
  levelRuleSummary,
  purgeConfirmation,
  retentionFor,
  retentionPatchKey,
  streamRows,
  ttlPreview,
} from './streams/retention';
import { thresholdColor, thresholdToken } from './dashboards/PanelView';
import { layoutTrace, ticks } from './traces/tree';

const NOW = Date.parse('2026-10-13T10:00:00Z');

describe('password policy hints', () => {
  it.each([
    ['short', 'admin', undefined, 'Use at least 12 characters.'],
    ['x'.repeat(257), 'admin', undefined, 'Use at most 256 characters.'],
    ['Administrator', 'administrator', undefined, 'The password must not be the username.'],
    ['same password 1', 'u', 'same password 1', 'Choose a different password than the current one.'],
    ['correct horse battery', 'admin', 'old', null],
  ])('%j', (pw, user, current, want) => {
    expect(passwordProblem(pw, user, current)).toBe(want);
  });
});

function alert(overrides: Partial<Alert['doc']> = {}, state: Alert['state'] = 'ok'): Alert {
  const r = newRule();
  return {
    id: 'a1',
    name: 'x',
    enabled: true,
    doc: { ...r.doc, ...overrides },
    state,
    stateSince: null,
    lastEvalAt: null,
    lastValue: null,
    lastError: '',
    groups: [],
    version: 1,
    createdAt: '',
    updatedAt: '',
  };
}

describe('alert rules', () => {
  it('derives the state chip, muted only while ok', () => {
    const muted = { muteUntil: '2026-10-13T11:00:00Z' };
    expect(isMuted(alert(muted), NOW)).toBe(true);
    expect(isMuted(alert({ muteUntil: '2026-10-13T09:00:00Z' }), NOW)).toBe(false);
    expect(stateChip(alert(muted), NOW)).toEqual({ variant: 'muted', label: 'Muted' });
    expect(stateChip(alert(muted, 'firing'), NOW).label).toBe('Firing');
    expect(['firing', 'pending', 'error', 'resolved'].map((s) => stateVariant(s).label)).toEqual([
      'Firing',
      'Pending',
      'Error',
      'OK',
    ]);
  });

  it('formats conditions and groups', () => {
    expect(conditionText(alert({ condition: { op: '>', threshold: 25 }, for: '2m0s' }).doc)).toBe(
      '> 25 for 2m',
    );
    expect(conditionText(alert({ for: '0s' }).doc)).toBe('> 0');
    expect(groupText({ service: 'checkout-api', env: 'prod' })).toBe('service=checkout-api, env=prod');
  });

  it('adds span to the stats stage for the preview', () => {
    expect(previewQuery('level >= error | stats count() by service', '1m0s')).toBe(
      'level >= error | stats count() by service span 1m',
    );
    expect(previewQuery('x | stats count() span 5m', '1m')).toBe('x | stats count() span 5m');
    expect(previewQuery('level:error', '1m')).toBe('level:error');
  });

  it('keeps the by clause in sync with groupBy', () => {
    expect(withGroupBy('x | stats count() by a', ['service', 'env'])).toBe(
      'x | stats count() by service, env',
    );
    expect(withGroupBy('x | stats count() by a', [])).toBe('x | stats count()');
    expect(withGroupBy('x', ['a'])).toBe('x');
  });
});

describe('dashboard variables', () => {
  const doc = {
    variables: [{ name: 'svc', default: ['checkout-api'] }, { name: 'env' }],
  } as unknown as Parameters<typeof variableValues>[0];

  it('reads values from the URL with defaults', () => {
    expect(variableValues(doc, new URLSearchParams('var-env=prod,,dev'))).toEqual({
      svc: ['checkout-api'],
      env: ['prod', 'dev'],
    });
    expect(variableValues(undefined, new URLSearchParams())).toEqual({});
  });

  it('patches only non-default values', () => {
    const v = { name: 'svc', default: ['a'] } as unknown as Parameters<typeof variablePatch>[0];
    expect(variablePatch(v, ['a'])).toEqual({ 'var-svc': null });
    expect(variablePatch(v, ['a', 'b'])).toEqual({ 'var-svc': 'a,b' });
  });

  it('labels values and parses refresh intervals', () => {
    expect(valuesLabel([])).toBe('All');
    expect(valuesLabel(['*'])).toBe('All');
    expect(valuesLabel(['a', 'b'])).toBe('a');
    expect([
      refreshMs('30s'),
      refreshMs('5m'),
      refreshMs('1h'),
      refreshMs('off'),
      refreshMs(undefined),
    ]).toEqual([30_000, 300_000, 3_600_000, null, null]);
  });
});

const stats = (rows: number, bytes: number): SourceStats => ({
  rows,
  oldest: '',
  newest: '',
  ratePer5Min: 0,
  ratePer24h: 0,
  compressedBytes: bytes,
  uncompressedBytes: bytes * 10,
  compressionRatio: 10,
  estimated: false,
  topServices: [],
});

function stream(name: string, s?: Stream['stats']): Stream {
  return {
    id: name,
    name,
    description: '',
    retentionDays: 30,
    spansRetentionDays: null,
    metricsRetentionDays: 90,
    levelRules: [],
    paused: false,
    createdAt: '',
    updatedAt: '',
    ...(s === undefined ? {} : { stats: s }),
  };
}

describe('streams and retention', () => {
  it('builds one row per stream and source', () => {
    const rows = streamRows([stream('prod', { logs: stats(10, 100), spans: stats(5, 50) }), stream('empty')]);
    expect(rows.map((r) => `${r.stream.name}:${r.source ?? '-'}`)).toEqual([
      'prod:logs',
      'prod:spans',
      'empty:-',
    ]);
  });

  it('reads and patches the right retention field', () => {
    const st = stream('a');
    expect([retentionFor(st, 'logs'), retentionFor(st, 'spans'), retentionFor(st, 'metrics')]).toEqual([
      30,
      null,
      90,
    ]);
    expect([retentionPatchKey('logs'), retentionPatchKey('spans'), retentionPatchKey('metrics')]).toEqual([
      'retentionDays',
      'spansRetentionDays',
      'metricsRetentionDays',
    ]);
  });

  it('summarises level rules and previews the TTL', () => {
    const rules = [
      { levels: ['Verbose', 'Debug'], days: 3 },
      { levels: ['Information'], days: 14 },
      { levels: [], days: 1 },
    ];
    expect(levelRuleSummary(rules.slice(0, 2))).toBe('verbose/debug 3 d · info 14 d');
    expect(ttlPreview("o'brien", 30, rules)).toBe(
      [
        '-- generated TTL (preview)',
        "TTL toDateTime(timestamp) + toIntervalDay(3) DELETE\n    WHERE stream = 'o\\'brien' AND level_num IN (0,1),",
        "    toDateTime(timestamp) + toIntervalDay(14) DELETE\n    WHERE stream = 'o\\'brien' AND level_num = 2,",
        "    toDateTime(timestamp) + toIntervalDay(30) DELETE\n    WHERE stream = 'o\\'brien'",
      ].join('\n'),
    );
  });

  it('confirms purges and estimates sizes', () => {
    expect(purgeConfirmation('prod')).toBe('purge prod');
    expect(purgeConfirmation('')).toBe('purge all');
    expect(estimateBytes(5, stats(10, 100))).toBe(50);
    expect(estimateBytes(5, stats(0, 100))).toBeNull();
    expect(estimateBytes(5, null)).toBeNull();
  });

  it('drops partitions only for full UTC days without stream or filter', () => {
    const day = 86_400_000;
    expect(dropsPartitions('', '', 0, 2 * day)).toBe(true);
    expect(dropsPartitions('', '', day / 2, day * 1.5)).toBe(false);
    expect(dropsPartitions('prod', '', 0, 2 * day)).toBe(false);
    expect(dropsPartitions('', 'level:debug', 0, 2 * day)).toBe(false);
  });
});

function span(
  id: string,
  parent: string,
  startMs: number,
  durationMs: number,
  service = 'api',
  status = 'Ok',
): Span {
  return {
    traceId: 't',
    spanId: id,
    parentSpanId: parent,
    stream: 'default',
    service,
    name: id,
    kind: 'server',
    start: new Date(NOW + startMs).toISOString(),
    durationMs,
    statusCode: status,
    statusMessage: '',
    attributes: {},
    resourceAttributes: {},
    events: [],
    links: [],
  };
}

describe('trace layout', () => {
  it('orders spans depth-first and finds the critical path', () => {
    const spans = [
      span('root', '', 0, 100),
      span('db', 'root', 10, 20, 'db'),
      span('pay', 'root', 40, 55, 'payments', 'Error'),
      span('fraud', 'pay', 45, 30, 'fraud'),
      span('orphan', 'missing', 5, 5, 'worker'),
    ];
    const t = layoutTrace(spans);
    expect(t.rows.map((r) => `${'  '.repeat(r.depth)}${r.span.spanId}`)).toEqual([
      'root',
      '  db',
      '  pay',
      '    fraud',
      'orphan',
    ]);
    expect(t.duration).toBe(100);
    expect(t.services).toEqual(['api', 'db', 'payments', 'fraud', 'worker']);
    expect([...t.critical].sort()).toEqual(['db', 'fraud', 'pay', 'root']);
    expect(t.rows.find((r) => r.span.spanId === 'pay')).toMatchObject({
      error: true,
      childCount: 1,
      offset: 40,
      end: 95,
    });
  });

  it('handles an empty trace and makes ticks', () => {
    expect(layoutTrace([])).toMatchObject({ rows: [], duration: 0, services: [] });
    expect(ticks(1000)).toEqual([0, 250, 500, 750, 1000]);
  });
});

describe('panel thresholds', () => {
  const panel = {
    options: {
      thresholds: [
        { value: 2, color: 'warning' },
        { value: 5, color: 'danger' },
      ],
    },
  } as unknown as Parameters<typeof thresholdToken>[0];

  it('picks the highest threshold reached', () => {
    expect(thresholdToken(panel, 1)).toBeNull();
    expect(thresholdToken(panel, 3)).toBe('warning');
    expect(thresholdToken(panel, 9)).toBe('danger');
    expect(thresholdToken(panel, null)).toBeNull();
    expect(thresholdColor(panel, 9)).toBe('var(--tm-color-danger)');
    expect(thresholdColor(panel, 0)).toBeNull();
  });
});
