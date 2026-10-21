// SPDX-License-Identifier: Apache-2.0
//
// System (SPEC §11.5 GET /system): the update card (§17.7), version, uptime, pipeline queue
// depths, ingest counters and ClickHouse version and disks.

import { useQuery } from '@tanstack/react-query';
import { errorMessage } from '../../api/client';
import { admin } from '../../api/endpoints';
import { useApi } from '../../app/services';
import { Spinner } from '../../components/controls';
import { Card, layoutStyles as l, Notice } from '../../components/layout';
import { formatBytes, formatInteger } from '../../lib/format';
import s from './settings.module.css';
import UpdateCard from './UpdateCard';

/** "3 d 4 h", "5 h 12 min", "42 s" */
export function formatUptime(seconds: number): string {
  const d = Math.floor(seconds / 86_400);
  const h = Math.floor((seconds % 86_400) / 3_600);
  const m = Math.floor((seconds % 3_600) / 60);
  if (d > 0) return `${d} d ${h} h`;
  if (h > 0) return `${h} h ${m} min`;
  if (m > 0) return `${m} min`;
  return `${seconds} s`;
}

export default function SystemPage() {
  const api = useApi();
  const sys = useQuery({ queryKey: ['system'], queryFn: () => admin.system(api), refetchInterval: 15_000 });
  return (
    <div className={s.page}>
      <div className={s.head}>
        <h1>System</h1>
        <span className={l.subtle}>refreshes every 15 s</span>
      </div>
      {sys.isError ? <Notice kind="danger">{errorMessage(sys.error)}</Notice> : null}
      {sys.isPending ? <Spinner /> : null}
      <div className={s.cards}>
        <UpdateCard />
        {sys.data !== undefined ? (
          <>
            <Card title="OpenTidemark">
              <dl className={l.kv}>
                <dt>Version</dt>
                <dd>{sys.data.version}</dd>
                <dt>Started</dt>
                <dd>{new Date(sys.data.startedAt).toLocaleString()}</dd>
                <dt>Uptime</dt>
                <dd>{formatUptime(sys.data.uptimeSeconds)}</dd>
                <dt>Streams</dt>
                <dd>{sys.data.streams}</dd>
              </dl>
            </Card>
            <Card title="Pipeline queues">
              <dl className={l.kv}>
                {Object.entries(sys.data.queueDepth).map(([k, v]) => (
                  <div key={k} className={s.contents}>
                    <dt>{k}</dt>
                    <dd>{formatInteger(v)}</dd>
                  </div>
                ))}
              </dl>
            </Card>
            <Card title="Counters">
              <dl className={l.kv}>
                {Object.entries(sys.data.counters).map(([k, v]) => (
                  <div key={k} className={s.contents}>
                    <dt>{k}</dt>
                    <dd>{formatInteger(v)}</dd>
                  </div>
                ))}
              </dl>
            </Card>
            <Card title="ClickHouse">
              {sys.data.clickhouseError !== undefined ? (
                <Notice kind="danger">{sys.data.clickhouseError}</Notice>
              ) : null}
              {sys.data.clickhouse !== undefined ? (
                <dl className={l.kv}>
                  <dt>Version</dt>
                  <dd>{sys.data.clickhouse.version}</dd>
                  {sys.data.clickhouse.disks.map((d) => (
                    <div key={d.name} className={s.contents}>
                      <dt>disk {d.name}</dt>
                      <dd>
                        {formatBytes(d.freeBytes)} free of {formatBytes(d.totalBytes)}
                        <span className={s.diskBar} aria-hidden="true">
                          <span
                            style={{
                              width: `${d.totalBytes > 0 ? ((d.totalBytes - d.freeBytes) / d.totalBytes) * 100 : 0}%`,
                            }}
                          />
                        </span>
                      </dd>
                    </div>
                  ))}
                </dl>
              ) : null}
            </Card>
          </>
        ) : null}
      </div>
    </div>
  );
}
