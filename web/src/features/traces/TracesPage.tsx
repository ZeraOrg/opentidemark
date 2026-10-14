// SPDX-License-Identifier: Apache-2.0
//
// Trace search (SPEC §11.3 GET /traces, §16.1): a spans filter and the matching traces with
// root span, duration, span and error counts and the services involved.

import { useQuery } from '@tanstack/react-query';
import { Activity } from 'lucide-react';
import { useEffect, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { errorMessage } from '../../api/client';
import { search } from '../../api/endpoints';
import { useDataScope } from '../../app/scope';
import { useApi, usePalette, useServices } from '../../app/services';
import { useTopbarStatus } from '../../app/shellContext';
import { patchParams, useTimeRange } from '../../app/url';
import { Button, Spinner, StatusChip } from '../../components/controls';
import { EmptyState, layoutStyles as l, Notice, Page, PageHead } from '../../components/layout';
import { QueryEditor } from '../../components/QueryEditor';
import { cx } from '../../lib/cx';
import { formatAge, formatClock, formatDuration } from '../../lib/time';
import { errorFromApi } from '../query/useQueryCheck';
import s from './traces.module.css';

const LIMIT = 100;

export default function TracesPage() {
  const api = useApi();
  const palette = usePalette();
  const { now } = useServices();
  const { range } = useTimeRange();
  const scope = useDataScope();
  const [params, setParams] = useSearchParams();
  const q = params.get('q') ?? '';
  const [draft, setDraft] = useState(q);
  useEffect(() => setDraft(q), [q]);
  const traces = useQuery({
    queryKey: ['traces', q, range.from, range.to, scope.key],
    queryFn: ({ signal }) => search.traces(api, { q, ...range, limit: LIMIT, ...scope.params }, signal),
  });
  const list = traces.data ?? [];
  const longest = Math.max(1, ...list.map((t) => t.durationMs));
  useTopbarStatus(traces.data === undefined ? null : `${list.length} traces`);
  const run = (v: string): void => setParams((p) => patchParams(p, { q: v.trim() === '' ? null : v.trim() }));
  return (
    <Page>
      <PageHead title="Traces" note="spans filter · newest matching traces first" />
      <div className={s.searchRow}>
        <QueryEditor
          value={draft}
          onChange={setDraft}
          onSubmit={run}
          source="spans"
          ariaLabel="Spans filter"
          placeholder="service:checkout-api and duration > 250ms"
          error={errorFromApi(q, traces.error)}
          className={s.searchBar}
          scope={scope.params}
        />
        <Button variant="primary" onClick={() => run(draft)}>
          Search
        </Button>
        <Button variant="ghost" onClick={() => run('status:error')}>
          Errors only
        </Button>
      </div>
      {traces.isError && errorFromApi(q, traces.error) === null ? (
        <Notice kind="danger">{errorMessage(traces.error)}</Notice>
      ) : null}
      {traces.isPending ? (
        <Spinner />
      ) : list.length === 0 ? (
        <EmptyState
          icon={<Activity size={20} aria-hidden="true" />}
          title="No traces in this range"
          hint="Send spans over OTLP or widen the time range."
        />
      ) : (
        <div className={cx(l.card, l.tableWrap)}>
          <table className={l.table}>
            <thead>
              <tr>
                <th>Start</th>
                <th>Root span</th>
                <th>Duration</th>
                <th className={l.num}>Spans</th>
                <th>Status</th>
                <th>Services</th>
              </tr>
            </thead>
            <tbody>
              {list.map((t) => (
                <tr key={t.traceId}>
                  <td className={l.monoCell} title={t.start}>
                    {formatClock(Date.parse(t.start))}{' '}
                    <span className={l.subtle}>{formatAge(Date.parse(t.start), now())}</span>
                  </td>
                  <td>
                    <Link to={`/traces/${encodeURIComponent(t.traceId)}`} className={s.rootLink}>
                      <span
                        className={s.dot}
                        style={{ background: `var(--tm-chart-${palette.index(t.rootService)})` }}
                        aria-hidden="true"
                      />
                      <span className={l.muted}>{t.rootService}</span> {t.rootName}
                    </Link>
                  </td>
                  <td className={s.durCell}>
                    <span
                      className={s.durBar}
                      style={{ width: `${Math.max(2, (t.durationMs / longest) * 100)}%` }}
                      aria-hidden="true"
                    />
                    <span className={l.monoCell}>{formatDuration(t.durationMs)}</span>
                  </td>
                  <td className={l.num}>{t.spanCount}</td>
                  <td>
                    {t.errorCount > 0 ? (
                      <StatusChip variant="firing">{t.errorCount} errors</StatusChip>
                    ) : (
                      <StatusChip variant="ok">ok</StatusChip>
                    )}
                  </td>
                  <td>
                    <span className={s.services}>
                      {t.services.map((svc) => (
                        <span key={svc} className={s.service}>
                          <span
                            className={s.dot}
                            style={{ background: `var(--tm-chart-${palette.index(svc)})` }}
                            aria-hidden="true"
                          />
                          {svc}
                        </span>
                      ))}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Page>
  );
}
