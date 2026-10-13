// SPDX-License-Identifier: Apache-2.0

import { describe, expect, it } from 'vitest';
import { ApiClient } from '../../api/client';
import { imports } from '../../api/endpoints';
import type { ImportJob } from '../../api/types';
import { jsonResponse, scriptedFetch } from '../../test/fakes';
import {
  jobProgress,
  jobSource,
  jobStream,
  parseQueryDsl,
  retentionWarning,
  uploadProblem,
} from './ImportPage';

function job(config: ImportJob['config'], rowsImported = 0, rowsTotal = 0): ImportJob {
  return {
    id: 'i',
    kind: 'elastic',
    name: 'n',
    status: 'running',
    config,
    rowsImported,
    rowsTotal,
    errors: 0,
    lastError: '',
    warnings: [],
    cancelRequested: false,
    requestedBy: 'user:u1',
    createdAt: '',
    updatedAt: '',
  };
}

const file = (name: string, size: number): File => {
  const f = new File(['x'], name);
  Object.defineProperty(f, 'size', { value: size });
  return f;
};

describe('import job helpers', () => {
  it('describes sources and streams', () => {
    const es = job({ url: 'https://es', auth: { type: 'none' }, index: 'logs-*', stream: 'legacy' });
    expect(jobSource(es)).toBe('https://es · logs-*');
    expect(jobStream(es)).toBe('legacy');
    expect(jobSource(job({ url: 'https://seq' }))).toBe('https://seq');
    expect(
      jobSource(
        job({
          files: [
            { name: 'a.ndjson', size: 1 },
            { name: 'b.gz', size: 2 },
          ],
          node: 'n',
        }),
      ),
    ).toBe('a.ndjson, b.gz');
    expect(jobSource(job({ file: { name: 'export.ndjson', size: 1 } }))).toBe('export.ndjson');
    expect(jobSource(job({}))).toBe('—');
    expect(jobStream(job({}))).toBe('');
  });

  it('computes progress only with a known total', () => {
    expect(jobProgress(job({}, 5, 0))).toBeNull();
    expect(jobProgress(job({}, 5, 10))).toBe(0.5);
    expect(jobProgress(job({}, 12, 10))).toBe(1);
  });

  it('checks upload limits', () => {
    expect(uploadProblem('clef-file', [])).toBe('Choose a file.');
    expect(uploadProblem('clef-file', [file('a', 10)])).toBeNull();
    expect(
      uploadProblem(
        'clef-file',
        Array.from({ length: 17 }, (_, i) => file(`f${i}`, 1)),
      ),
    ).toMatch(/at most 16/);
    expect(uploadProblem('clef-file', [file('a', 1024 ** 3 + 1)])).toMatch(/1 GiB/);
    expect(uploadProblem('kibana', [file('a', 1), file('b', 1)])).toMatch(/exactly one/);
    expect(uploadProblem('kibana', [file('a', 64 * 1024 ** 2 + 1)])).toMatch(/64 MiB/);
    expect(uploadProblem('kibana', [file('a', 1)])).toBeNull();
  });

  it.each([
    ['', {}],
    ['{"term":{"level":"error"}}', { query: { term: { level: 'error' } } }],
    ['{}', { error: 'The query must be a non-empty JSON object.' }],
    ['[1]', { error: 'The query must be a non-empty JSON object.' }],
    ['{', { error: 'The query is not valid JSON.' }],
  ])('parseQueryDsl(%j)', (text, want) => {
    expect(parseQueryDsl(text)).toEqual(want);
  });

  it('warns when the range starts before the retention', () => {
    const now = Date.parse('2026-10-13T00:00:00Z');
    expect(retentionWarning('2026-09-01T00:00:00Z', 30, now)).toBe(true);
    expect(retentionWarning('2026-10-01T00:00:00Z', 30, now)).toBe(false);
    expect(retentionWarning('not a date', 30, now)).toBe(false);
  });
});

describe('imports.upload', () => {
  it('sends the job part first, then one file part per file', async () => {
    const { fetch, calls } = scriptedFetch(() => jsonResponse({}, 201));
    const api = new ApiClient({ fetch, cookies: () => 'tm_csrf=t' });
    await imports.upload(api, { kind: 'clef-file', config: { stream: 'api' } }, [
      file('a.ndjson', 1),
      file('b.gz', 1),
    ]);
    const form = calls[0]?.init.body as FormData;
    expect(calls[0]?.url).toBe('/api/v1/imports');
    expect([...form.keys()]).toEqual(['job', 'file', 'file']);
    expect(JSON.parse(String(form.get('job')))).toEqual({ kind: 'clef-file', config: { stream: 'api' } });
    expect(form.getAll('file').map((f) => (f as File).name)).toEqual(['a.ndjson', 'b.gz']);
  });
});
