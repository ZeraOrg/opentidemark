// SPDX-License-Identifier: Apache-2.0
//
// Syntax cheat-sheet entries (SPEC §10.1 examples, §10.7).

export interface CheatEntry {
  q: string;
  note: string;
}

export const CHEATSHEET: readonly CheatEntry[] = [
  { q: 'timeout', note: 'free text in message + exception' },
  { q: '"connection refused"', note: 'phrase' },
  { q: 'level:error', note: 'field:value (case-insensitive)' },
  { q: 'level >= warning', note: 'level ordinal comparison' },
  { q: 'service:checkout* and not env:dev', note: 'wildcards, boolean logic' },
  { q: 'status_code:(500 or 502 or 503)', note: 'value groups' },
  { q: 'status_code >= 500 and elapsed > 250', note: 'numeric comparison' },
  { q: 'user.email:*', note: 'field exists' },
  { q: 'not exception:*', note: 'field missing' },
  { q: 'path:/\\/api\\/v[12]\\/orders/', note: 'regex' },
  { q: "Service = 'payments-svc' and StatusCode != 200", note: 'Seq-style operators' },
  { q: 'level:error | stats count() by service | sort count desc', note: 'aggregate → table' },
  { q: 'level:error | top 10 exception_type', note: 'most frequent values' },
  { q: '* | stats count() by level span 5m', note: 'time series' },
  { q: 'elapsed:* | stats p95(elapsed), avg(elapsed) by service span 1m', note: 'percentiles' },
  { q: 'source spans | service:checkout-api and status:error | stats count() by name', note: 'spans' },
  {
    q: 'source metrics | name:http.server.request.duration | stats avg(value) by service span 1m',
    note: 'metrics',
  },
];
