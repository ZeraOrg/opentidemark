// SPDX-License-Identifier: Apache-2.0
//
// The explorer's empty result (SPEC §16.3): "No events match" when a query or range excludes
// everything, or "No data yet" when no stream the caller can see holds any log rows — then
// an admin is pointed at the connections page and the ingestion docs.

import { useQuery } from '@tanstack/react-query';
import { ExternalLink, Inbox } from 'lucide-react';
import { streams as streamsApi } from '../../api/endpoints';
import type { StreamsResponse } from '../../api/types';
import { useApi } from '../../app/services';
import { hasRole, useMe } from '../../app/session';
import { Button, ButtonLink } from '../../components/controls';
import { EmptyState } from '../../components/layout';
import s from './EmptyResults.module.css';

export const INGESTION_DOCS_URL = 'https://zeraorg.github.io/opentidemark/ingestion/';

/** True once the streams list has loaded and no stream in it holds any log rows. */
export function hasNoLogData(resp: StreamsResponse | undefined): boolean {
  return resp !== undefined && resp.streams.every((st) => (st.stats?.logs?.rows ?? 0) === 0);
}

function NoDataYet() {
  const me = useMe();
  if (!hasRole(me.role, 'admin')) {
    return (
      <EmptyState
        icon={<Inbox size={20} aria-hidden="true" />}
        title="No data yet"
        hint="Ask an administrator to connect a log source."
      />
    );
  }
  return (
    <EmptyState
      icon={<Inbox size={20} aria-hidden="true" />}
      title="No data yet"
      hint={
        <>
          Point a client at OpenTidemark with a connection key. The installers create a connection called{' '}
          <code>bootstrap</code>; its key is <code>TIDEMARK_BOOTSTRAP_INGEST_KEY</code> in the environment
          file.
        </>
      }
      action={
        <div className={s.actions}>
          <ButtonLink to="/settings/connections" variant="primary" size="sm">
            Open connections
          </ButtonLink>
          <a
            href={INGESTION_DOCS_URL}
            target="_blank"
            rel="noopener noreferrer"
            className={s.link}
            aria-label="Ingestion docs (opens in a new tab)"
          >
            Ingestion docs
            <ExternalLink size={12} aria-hidden="true" />
          </a>
        </div>
      }
    />
  );
}

/** The empty state of the event stream; `onLast24h` widens the time range. */
export function EmptyResults({ onLast24h }: { onLast24h: () => void }) {
  const api = useApi();
  const streamsQ = useQuery({
    queryKey: ['streams'],
    queryFn: () => streamsApi.list(api),
    staleTime: 60_000,
  });
  if (hasNoLogData(streamsQ.data)) return <NoDataYet />;
  return (
    <EmptyState
      title="No events match"
      hint="Widen the time range or remove a filter."
      action={
        <Button size="sm" onClick={onLast24h}>
          Last 24 hours
        </Button>
      }
    />
  );
}
