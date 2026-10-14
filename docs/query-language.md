<!-- SPDX-License-Identifier: Apache-2.0 -->
# OpenTidemark Query Language (TQL)

Search and analyse logs, traces and metrics with TQL, OpenTidemark's query language: KQL-style
`field:value` filters, Seq-style operators and pipe stages such as `stats`, `sort` and `top`.

TQL is the one query language of OpenTidemark: the explorer, dashboards and alerts all use the same
text. Filters are as easy as KQL (`level:error service:checkout*`), Seq-style operators work too
(`Level = 'Error'`), and pipe stages turn a filter into analytics.

## Overview by example

<!-- The example block below is kept identical to SPEC.md §10.1 and every line must parse and
     compile (tested). -->

```text
timeout                                   # free text in message + exception (case-insensitive)
"connection refused"                      # phrase
level:error                               # field:value (case-insensitive for strings)
level >= warning                          # level ordinal comparison
service:checkout*  and not env:dev        # wildcard, boolean logic (implicit AND between terms)
status_code:(500 or 502 or 503)           # value groups
status_code >= 500 and elapsed > 250      # numeric comparison (uses numeric property values)
user.email:*                              # field exists
not exception:*                           # field missing / empty
path:/\/api\/v[12]\/orders/               # regex
Service = 'payments-svc' and StatusCode != 200   # Seq-style operators accepted too
@trace_id:4bf92f3577b34da6a3ce929d0e0e4736
level:error | stats count() by service | sort count desc
level:error | top 10 exception_type
* | stats count() by level span 5m       # time series
elapsed:* | stats p95(elapsed), avg(elapsed) by service, route span 1m
source spans | service:checkout-api and status:error | stats count() by name
source metrics | name:http.server.request.duration | stats avg(value) by service span 1m
```

## Structure of a query

```
[source logs|spans|metrics |] <filter> [| stage] [| stage] …
```

- **Source.** `source spans | …` or `source metrics | …` selects the data set; without a source
  clause a query searches **logs**.
- **Filter.** An empty filter or `*` matches everything. Terms next to each other are combined
  with AND. Precedence: `not` > `and` > `or`; use parentheses to group.
- **Stages.** `where`, `stats`, `top`, `sort`, `limit` and `fields`, separated by `|`.

## Lexical rules

- Keywords are case-insensitive: `and or not in source stats by span sort asc desc limit fields
  where top as true false null`. `&&`, `||` and `!` work as well. Keywords are only reserved where
  the grammar expects them, so `source` can still be used as a property name elsewhere.
- **Field names** match `[A-Za-z_@][A-Za-z0-9_.\-@]*` (`Request.Path`, `@timestamp`,
  `http.status_code`); other names can be written in backticks: `` `weird key` ``.
- **Strings** use `'…'` or `"…"` with backslash escapes `\\ \' \" \n \t \*`. Unknown escapes are
  kept as written (`'\d+'` stays `\d+`).
- **Numbers**: `-?\d+(\.\d+)?([eE][+-]?\d+)?`. Comparisons with the span `duration` accept a
  unit suffix `ns us ms s m h d` (no unit = milliseconds).
- **Bare values** (right of `:`) are any run of characters up to whitespace, `(`, `)` or `|`;
  `\` escapes `* ? ( ) | \` and space, other backslashes are literal (`path:C:\temp`). Quote
  values that contain `|`.
- **Wildcards.** An unescaped `*` in a value after `:` makes a wildcard, in bare and in quoted
  values; inside such a value `?` matches exactly one character. `\*` is a literal star. A lone
  `*` means "the field exists". Values after comparison operators, in lists and as function
  arguments are never wildcards.
- **Regular expressions**: `/…/` with `\/` as escape (RE2 syntax). A value starting with `/` is a
  regex only when its closing `/` is followed by whitespace, `)`, `|` or the end of the query;
  otherwise it is a plain value (`path:/api/v1/orders`).
- **Time literals** in `@timestamp` comparisons: `now`, `now-15m`, or ISO-8601 strings.
- **Dashboard variables**: `$name` is replaced by the dashboard's variable values before the
  query runs.
- If a query does not parse and contains no operators, quotes, colons, brackets or pipes, the
  whole text is searched as one phrase (like Seq); the API reports `interpretedAsText: true`.

## Filters

| Form | Meaning |
|---|---|
| `timeout`, `"connection refused"` | Free text: case-insensitive search in the message and exception (spans: name and status message; metrics: name and description). Adjacent words must all match. |
| `field:value` | Case-insensitive equality for text. A number also matches numerically (`status:500` matches `500` stored as text or number). `true`/`false` match the text `true`/`false`; `field:null` matches a missing field. |
| `field:abc*`, `field:a?c*` | Wildcard: `*` matches any characters; in a value with `*`, `?` matches one character. |
| `field:/re/` | Regular expression. |
| `field:*`, `not field:*` | Field exists and is not empty / field is missing or empty. |
| `field:(a or b and c)` | Value group, equivalent to `field:a or (field:b and field:c)`. At most 1,000 values. |
| `field:>=5`, `field >= 5` | Numeric comparison (`<`, `<=`, `>`, `>=`); a missing value never matches. Comparing with a quoted string compares text. |
| `field = 'x'`, `field != 'x'`, `field == 'x'`, `field <> 'x'` | Seq-style comparison, **case-sensitive** unless followed by `ci`. `= null` / `!= null` test for missing / present. |
| `field like 'a%'`, `field not like 'a%'` | SQL `LIKE` pattern, case-sensitive unless followed by `ci`. |
| `field in ['a', 'b']`, `field not in [1, 2]` | List membership (case-sensitive). |
| `contains(f, 'x')`, `startswith(f, 'x')`, `endswith(f, 'x')` | Case-insensitive substring tests. |
| `has(f)` | Same as `f:*`. |
| `match(f, 're')` | Regular expression. |
| `level >= warning` | Levels compare by severity: Verbose < Debug < Information < Warning < Error < Fatal (also `0`–`5` and common aliases such as `warn`, `err`, `critical`). |
| `@timestamp >= now-1h` | Narrows the request's time range (needs a comparison operator; `in` is not allowed). |

Numeric predicates never match events that lack the field, so `not x > 5` and `x != 5` match
events without `x`.

## Fields

Unprefixed names resolve first to a **built-in field** of the source (case-insensitive), else to
a property. Names starting with `@` are always built-ins (`@service`, `@trace_id`); an unknown
`@name` is an error with a suggestion. `props.<key>` / `attrs.<key>` always mean the property.
Property keys are case-sensitive.

| Source | Built-in fields (aliases) |
|---|---|
| logs | `@timestamp` (`timestamp`, `@t`) · `level` (`@level`, `@l`) · `message` (`@message`, `@m`, `msg`) · `template` (`@mt`, `messagetemplate`) · `exception` (`@x`) · `service` · `environment` (`env`) · `host` · `stream` · `trace_id` (`traceid`, `@tr`) · `span_id` (`spanid`, `@sp`) · `event_type` (`@i`) · `protocol` · `connection_id` (`connectionid`) · `id` (`@id`) |
| spans | `@timestamp` (`start_time`, `timestamp`) · `trace_id` · `span_id` · `parent_span_id` · `service` · `name` · `kind` · `status` (`status_code`) · `status_message` · `duration` (`@duration`, milliseconds in results) · `stream` · `connection_id` (`connectionid`) |
| metrics | `@timestamp` · `name` · `service` · `kind` · `unit` · `value` · `count` · `sum` · `min` · `max` · `stream` · `connection_id` (`connectionid`) |

`connection_id` is the id of the connection that sent the data (shown as `connectionId` on each
event), so `connection_id:<id> | stats count() by stream` shows where one connection writes.

## Stages

| Stage | Meaning |
|---|---|
| `where <filter>` | Before `stats`: further filtering (ANDed). After `stats`: filters on aggregate results (like SQL `HAVING`). |
| `stats agg[, agg…] [by f[, f…]] [span <dur>\|auto]` | Aggregation, optionally grouped and bucketed in time. |
| `top <n> <f> [by <g>]` | Shortcut for `stats count() by [g,] f`, sorted by count, `n` rows (per value of `g`). |
| `sort <name> [asc\|desc][, …]` | Orders aggregate results (events are always newest first). |
| `limit <n>` | Limits aggregate results. |
| `fields f[, f…]` | Chooses the columns of event results. |

**Aggregates:** `count()`, `count(f)` (events where `f` exists), `count_distinct(f)`, `sum`, `avg`,
`min`, `max`, percentiles `p50 p75 p90 p95 p99`, `rate()` (events per second of the bucket or
range), `first(f)` / `last(f)` (by time). For metrics, `rate(value)` turns cumulative counters into
a per-second rate and must be the only aggregate of its stage. Name results with `as`:
`stats p95(elapsed) as slow`; the default name is the function, or `function_field`
(`p95_elapsed`).

**Time buckets:** `span 5m` buckets results in time (whole seconds, at least `1s`); `span auto`
picks a span that gives a readable number of buckets for the time range.

**Stage order:** any `where` stages, then at most one `stats` or `top`, then `where`, `sort` and
`limit`. `sort`, `limit` and `fields` may appear once each; `sort` needs `stats` or `top` and is
not allowed after `top`; `limit` before `stats`, and `fields` together with `stats`/`top`, are
errors.

**Result kinds:** a query without `stats` returns **events**; `stats` without `span` returns a
**table**; `stats` with `span` returns a **time series**.

## Limits

Queries are at most 8 KiB of UTF-8 text, with at most 32 levels of nesting, 1,000 values in a list
or value group, 5 stages, 10 `by` fields, 20 aggregates per `stats`, 50 `fields` and 10,000 time
buckets. Queries run with a time limit and a row limit set by the server
(`TIDEMARK_QUERY_TIMEOUT`, `TIDEMARK_QUERY_MAX_ROWS`).

## Errors and autocomplete

Errors point to the position of the problem and often include a hint, for example
*unknown level 'erorr'* — *did you mean 'error'?*. The query editor offers completions for
fields, values (the most frequent values of the field left of `:`), operators, keywords, stages
and functions.
