<!-- SPDX-License-Identifier: Apache-2.0 -->
# OpenTidemark Query Language (TQL)

Search and analyse logs, traces and metrics with TQL, OpenTidemark's query language: KQL-style
`field:value` filters, Seq-style operators and pipe stages such as `stats`, `sort` and `top`.

TQL is the one query language of OpenTidemark: the explorer, dashboards and alerts all use the same
text. Filters are as easy as KQL (`level:error service:checkout*`), Seq-style operators work too
(`Level = 'Error'`), and pipe stages turn a filter into analytics. This page is the complete
reference: every operator, field, function, stage and rule, with examples you can paste into the
search bar. Every example in a query block on this page is checked against the parser by the
project's tests.

<!-- Query blocks fenced as tql must each parse and compile (internal/tql doc test); use text
     fences for queries that are meant to fail. -->

**On this page:** [quick start](#quick-start) ·
[overview by example](#overview-by-example) · [how a query is built](#how-a-query-is-built) ·
[free text](#free-text) · [field filters](#field-filters) ·
[comparison operators](#comparison-operators) · [functions](#functions) ·
[combining conditions](#combining-conditions) · [fields](#fields) · [levels](#levels) ·
[time](#time) · [durations](#durations) · [sources](#sources) · [stages](#stages) ·
[aggregates](#aggregates) · [time series](#time-series-and-span) ·
[stage order](#stage-order) · [results](#results) ·
[dashboard variables](#dashboard-variables) · [writing values](#writing-values-lexical-rules) ·
[autocomplete](#autocomplete-and-the-editor) · [errors](#errors-and-how-to-fix-them) ·
[pitfalls](#pitfalls) · [examples](#examples) · [limits](#limits) · [grammar](#grammar) ·
[cheat sheet](#cheat-sheet)

## Quick start

Type a word to find events that mention it, add `field:value` pairs to narrow the result, and add
`| stats …` to count instead of list. Each line below is a complete query:

```tql
timeout                                   # events whose message or exception mentions "timeout"
timeout service:checkout                  # ...and that come from the checkout service
level:error service:checkout              # all errors of the checkout service
level:error | stats count() by service    # how many errors each service logged
level:error | stats count() by service span 5m   # the same, as a chart over time
```

The time range is not part of the query: it comes from the time picker in the explorer, the
dashboard's range, the alert's window or the `from`/`to` of an API request.

Looking for a query for a specific task? Jump to the [examples](#examples) near the end of this
page.

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

## How a query is built

```text
[source logs|spans|metrics |] <filter> [| stage] [| stage] …
```

A query has up to three parts, separated by the pipe character `|`:

1. **Source** (optional). `source spans | …` searches trace spans and `source metrics | …` metric
   points. Without a source clause, and with `source logs | …`, a query searches **log events**.
2. **Filter.** Which rows to keep. An empty filter or `*` keeps every row in the time range.
   After a source clause the filter is required: write `source spans | * | stats …`, not
   `source spans | stats …`.
3. **Stages** (optional). What to do with the rows: filter further (`where`), aggregate (`stats`,
   `top`), order (`sort`), cut (`limit`) or choose columns (`fields`). A query has at most five
   stages.

```tql
*                                                    # every log event in the time range
level:error                                          # a filter only
level:error | stats count() by service               # filter + one stage
source spans | status:error | top 10 name            # source + filter + stage
source metrics | name:process.cpu.time | stats avg(value) by service span 1m
```

Keywords (`and`, `or`, `not`, `in`, `like`, `ci`, `source`, `stats`, `by`, `span`, `auto`, `as`,
`sort`, `asc`, `desc`, `limit`, `fields`, `where`, `top`, `true`, `false`, `null`) are
case-insensitive: `LEVEL:error | STATS COUNT() BY service` works. A keyword is only special where
the grammar expects it, so `source`, `by` or `top` can still be property names elsewhere.

## Free text

A word or a quoted phrase that is not followed by `:` or an operator is **free text**. It matches
rows that contain it anywhere, ignoring case:

| Source | Free text searches |
|---|---|
| logs | the rendered message and the exception |
| spans | the span name and the status message |
| metrics | the metric name and its description |

```tql
timeout                          # contains "timeout", "Timeout", "TIMEOUT", …
"connection refused"             # the exact phrase, spaces included
timeout database                 # both words, anywhere, in any order
timeout or "connection refused"  # either one
not healthcheck                  # rows that do not mention healthcheck
```

- Words next to each other must **all** match (they are combined with AND).
- Free text is a substring search, so `time` also matches `timeout` and `runtime`. There are no
  wildcards in free text; to search a field with a pattern use `field:pattern*`.
- Quote text that contains spaces or any of `( ) [ ] , | : = < > ' "`: `"order #42 (retry)"`.
- **Plain-text fallback.** If a query does not parse and contains none of `: = < > | ( ) [ ] ' "`,
  a backtick, `&&` or a `!` used as an operator, the whole text is searched as one phrase, like
  Seq does. `Hello, world!` therefore finds the phrase "Hello, world!". The explorer and the API
  (`interpretedAsText: true`) tell you when this happened.
- Free text is not allowed after `stats`: a `where` stage after `stats` can only test aggregate
  results and group fields.

## Field filters

The colon form `field:value` is the everyday way to filter on a field. It is forgiving: text
comparisons ignore case and numbers match whether they were sent as numbers or as text.

| Form | Matches rows where … |
|---|---|
| `field:value` | the field equals the value, ignoring case |
| `field:42` | the field equals 42 as a number, or is the text `42` |
| `field:true`, `field:false` | the field is the text or boolean `true` / `false` |
| `field:null` | the field is missing |
| `field:*` | the field exists and is not empty |
| `not field:*` | the field is missing or empty |
| `field:abc*`, `field:*abc*`, `field:a?c*` | the value matches a wildcard pattern, ignoring case |
| `field:/regex/` | the value matches a regular expression |
| `field:(a or b)` | the value is any of several values (a value group) |
| `field:>=5`, `field:<"m"` | a range comparison, like `field >= 5` |

```tql
level:error
service:checkout-api
status_code:500                    # matches 500 sent as a number and "500" sent as text
cached:true
user.email:*                       # has an email
not exception:*                    # no exception attached
service:checkout*                  # starts with checkout
RequestPath:*/orders/*             # contains /orders/
host:web-?1*                       # ? is one character when the value also has a *
path:/\/api\/v[12]\/orders/        # regular expression
status_code:(500 or 502 or 503)    # any of three values
env:(prod or staging)
elapsed:>250                       # same as elapsed > 250
```

### Equality

`field:value` compares text **case-insensitively**: `service:Checkout` matches `checkout`,
`CHECKOUT` and `Checkout`. The whole value must match; use a wildcard (`service:check*`) or a
function (`contains(service, 'check')`) for parts of a value.

For a **number** the colon form matches both the numeric value and the same digits stored as text,
because the same property can arrive as `500` from one client and `"500"` from another. A number
on a built-in numeric field (such as `value` of a metric) compares numerically.

`true` and `false` match the text `true` / `false`, which is how boolean properties are stored.
`null` means "missing": `field:null` matches rows without the field. To search for the literal
word null, quote it: `field:"null"`.

### Exists and missing

`field:*` matches rows where the field is present and not empty; `not field:*` matches rows where
it is missing or empty. `has(field)` is the same as `field:*`.

```tql
exception:*                        # events with an exception
not user.id:*                      # anonymous requests
has(trace_id)                      # events that belong to a trace
```

### Wildcards

In a value after `:`, an unescaped `*` matches any number of characters (including none). When a
value contains a `*`, `?` matches exactly one character; in a value **without** `*`, `?` is just a
question mark. Wildcard matches ignore case.

```tql
service:checkout*                  # starts with
message:*timeout*                  # contains
RequestPath:*.json                 # ends with
host:web-??-*                      # web- + two characters + - + anything
message:"payment * declined"       # a * inside a quoted value is still a wildcard
message:100\*                      # \* is a literal star: matches the text 100*
```

Only the colon form uses wildcards. Values after comparison operators (`=`, `!=`, `<`, …), in `in`
lists and as function arguments are always taken literally.

### Regular expressions

`field:/pattern/` matches values against a regular expression in
[RE2 syntax](https://github.com/google/re2/wiki/Syntax) (the syntax of Go and ClickHouse: no
look-around or back-references). Regular expressions are **case-sensitive**; start the pattern
with `(?i)` to ignore case. Write a `/` inside the pattern as `\/`. A regex finds a match anywhere
in the value unless you anchor it with `^` and `$`.

```tql
path:/\/api\/v[12]\/orders/        # /api/v1/orders or /api/v2/orders, anywhere in path
message:/(?i)deadlock/             # case-insensitive
RequestId:/^[0-9a-f]{32}$/         # the whole value is 32 hex digits
match(message, 'user [0-9]+ locked')   # the function form, no / escaping needed
```

A value that starts with `/` is a regex only when its closing `/` is followed by a space, `)`, `|`
or the end of the query. Otherwise it is a plain value, so paths need no quotes:
`path:/api/v1/orders` is a plain value.

### Value groups

`field:(a or b or c)` is shorthand for `field:a or field:b or field:c`. Each value in the group can
use every colon-form feature (wildcards, regex, numbers). Up to 1,000 values per group.

```tql
status_code:(500 or 502 or 503 or 504)
service:(checkout* or payments*)
env:(prod or staging) level:error
```

Groups can also use `and`: `field:(a or b and c)` means `field:a or (field:b and field:c)`, with
`and` binding tighter than `or`.

### Range comparisons with the colon

`field:>5`, `field:>=5`, `field:<5` and `field:<=5` are the same as the comparison operators below
(`field > 5`, …). Spaces after the colon are allowed: `status_code: >= 500`.

## Comparison operators

TQL also accepts the operators Seq users know. Unlike the colon form, they are **exact**: text
comparisons are case-sensitive and patterns are not expanded.

| Operator | Meaning | Case |
|---|---|---|
| `=`, `==` | equal | case-sensitive; add `ci` to ignore case |
| `!=`, `<>` | not equal | case-sensitive; add `ci` to ignore case |
| `<`, `<=`, `>`, `>=` | less / greater than | numbers compare numerically, quoted text alphabetically |
| `like`, `not like` | SQL `LIKE` pattern: `%` any characters, `_` one character | case-sensitive; add `ci` to ignore case |
| `in [ … ]`, `not in [ … ]` | one of a list | case-sensitive |

```tql
Service = 'payments-svc'
Service = 'PAYMENTS-SVC' ci                  # ci: ignore case
StatusCode != 200
StatusCode <> 200
status_code >= 500 and status_code < 600
elapsed > 250
Customer.Name >= 'M'                         # quoted text: alphabetical comparison
message like '%timeout%'
message like '%TIMEOUT%' ci
message not like 'GET /health%'
env in ['prod', 'staging']
env not in ['dev', 'test']
StatusCode in [500, 502, 503]
level in [error, fatal]
```

### Numeric comparisons and missing values

`<`, `<=`, `>` and `>=` with a number compare the **numeric** value of the field. A row without
the field, or whose value is not a number, **never** matches a numeric comparison. As a
consequence, the negations match those rows: `not elapsed > 250` and `elapsed != 250` also match
events that have no `elapsed` at all. Combine with `field:*` to exclude them:

```tql
elapsed:* and not elapsed > 250              # has elapsed, and it is at most 250
```

With a **quoted** value the comparison is alphabetical text comparison: `version >= '2.0'`.

### `null`

`field = null` matches rows where the field is missing and `field != null` rows where it is
present. `null` works only with `=`/`==`/`!=`/`<>` (and as `field:null`); it is not allowed in an
`in` list.

```tql
user.id = null
user.id != null
```

### `like`

`like` uses SQL patterns: `%` matches any number of characters and `_` exactly one. Write `\%`
and `\_` for literal percent and underscore signs. `like` needs a text field. Without `ci` it is
case-sensitive.

```tql
RequestPath like '/api/%/orders'
host like 'web-__'
message like '%50\% off%'
```

### `in` lists

`field in [v1, v2, …]` matches any listed value exactly; `not in` matches none of them. Items are
separated by commas; text items can be quoted or bare words; up to 1,000 items. On a property,
the list compares numbers when **every** item is a number and text otherwise. `in` is
case-sensitive and does not take `ci`; for a case-insensitive list use a value group
(`env:(prod or staging)`).

```tql
StatusCode in [500, 502, 503]
env in [prod, staging]
service not in ['healthcheck', 'synthetic-monitor']
level in [warning, error, fatal]
```

`*` and `null` are not allowed in a list, and `@timestamp` cannot be used with `in`.

### `ci` (case-insensitive)

Append `ci` to `=`, `==`, `!=`, `<>`, `<`, `<=`, `>`, `>=`, `like` or `not like` to compare text
ignoring case. It has no effect on numbers, levels, durations and times.

```tql
Service = 'Payments-Svc' ci
Environment != 'PROD' ci
message like '%deadlock%' ci
```

## Functions

| Function | Matches rows where … |
|---|---|
| `contains(field, 'text')` | the field contains the text, ignoring case |
| `startswith(field, 'text')` | the field starts with the text, ignoring case |
| `endswith(field, 'text')` | the field ends with the text, ignoring case |
| `has(field)` | the field exists and is not empty (same as `field:*`) |
| `match(field, 'regex')` | the field matches the regular expression (RE2, case-sensitive unless `(?i)`) |

The text argument is taken literally: no wildcards, and no escaping of `*`, `%` or `_` needed.
`contains`, `startswith`, `endswith` and `match` need a field and a text (or number) argument;
`has` takes only a field.

```tql
contains(message, 'out of memory')
startswith(RequestPath, '/api/')
endswith(RequestPath, '.json')
has(exception)
match(RequestPath, '^/api/v[0-9]+/')
not contains(message, 'healthcheck')
```

## Combining conditions

| Write | Meaning |
|---|---|
| `a b` or `a and b` or `a && b` | both |
| `a or b` or `a || b` | either |
| `not a` or `!a` | not |
| `( … )` | grouping |

**Precedence:** `not` binds tightest, then `and` (including plain adjacency), then `or`. So
`a or b c` means `a or (b and c)`, and `not a b` means `(not a) and b`. Use parentheses when in
doubt; up to 32 levels of nesting.

```tql
level:error service:checkout                         # both
level:error and service:checkout                     # the same
level:error or level:fatal
level >= error and not service:healthcheck
(service:checkout or service:payments) and level:error
level:error && service:api || env:prod               # = (level:error and service:api) or env:prod
!exception:*
not (env:dev or env:test)
```

## Fields

### Built-in fields and properties

Every row has **built-in fields** (columns such as the level, the service or the message) and
**properties** (the structured data your application attached: `elapsed`, `RequestPath`,
`user.email`, OpenTelemetry attributes, …).

Field names are resolved like this:

1. A name that matches a built-in field or alias of the current source (ignoring case) is that
   built-in: `level`, `Level` and `LEVEL` are the same.
2. Any other name is a **property**. Property names are **case-sensitive**: `RequestPath` and
   `requestpath` are different properties.
3. A name starting with `@` is always a built-in: `@service`, `@trace_id`, `@level`, … An unknown
   `@name` is an error with a suggestion (`@srvice` → *did you mean 'service'?*).
4. `props.<key>` (logs) or `attrs.<key>` (spans, metrics) always means the property, even when a
   built-in has the same name: `props.level` is a property called `level`, not the event level.

```tql
@service:checkout                  # the built-in service, even if a property is called service
props.service:legacy-name          # the property called service
props.level:custom                 # a property called level
attrs.http.method:GET              # span or metric attribute http.method
source spans | attrs.http.response.status_code >= 500
```

Field names can contain letters, digits, `_`, `.`, `-` and `@`, and start with a letter, `_` or
`@`. Dots are part of the name: `user.email` is the property whose key is `user.email` (that is
how nested JSON and OpenTelemetry attributes are stored). For any other name, put it in backticks:

```tql
`order id`:42
`http status`:>=500
`weird:key`:*
```

### Built-in fields of logs

| Field | Aliases | Contents |
|---|---|---|
| `@timestamp` | `timestamp`, `@t` | time of the event |
| `level` | `@level`, `@l` | severity, see [Levels](#levels) |
| `message` | `@message`, `@m`, `msg` | the rendered message |
| `template` | `@mt`, `messagetemplate` | the message template (`User {UserId} logged in`) |
| `exception` | `@x` | exception type, message and stack trace |
| `service` | | service / application name |
| `environment` | `env` | deployment environment |
| `host` | | host or pod name |
| `stream` | | the stream the event was routed to |
| `trace_id` | `traceid`, `@tr` | trace id (lower-case hex) |
| `span_id` | `spanid`, `@sp` | span id (lower-case hex) |
| `event_type` | `@i` | event type: an 8-character hash of the message template, the same for every event of one template |
| `protocol` | | how the event arrived: `otlp`, `seq`, `elastic` or `native` |
| `connection_id` | `connectionid` | id of the ingestion connection that sent it |
| `id` | `@id` | the event's unique id (UUID) |

### Built-in fields of spans

| Field | Aliases | Contents |
|---|---|---|
| `@timestamp` | `start_time`, `timestamp` | span start time |
| `trace_id` | | trace id |
| `span_id` | | span id |
| `parent_span_id` | | parent span id (empty for root spans) |
| `service` | | service name |
| `name` | | operation name (`GET /orders/{id}`) |
| `kind` | | `server`, `client`, `internal`, `producer` or `consumer` |
| `status` | `status_code` | `ok`, `error` or `unset` |
| `status_message` | | status description |
| `duration` | `@duration` | span duration; milliseconds in results, see [Durations](#durations) |
| `stream` | | stream |
| `connection_id` | `connectionid` | ingestion connection |

### Built-in fields of metrics

| Field | Contents |
|---|---|
| `@timestamp` | time of the data point |
| `name` | metric name (`http.server.request.duration`) |
| `service` | service name |
| `kind` | `gauge`, `sum`, `histogram`, `exp_histogram` or `summary` |
| `unit` | unit as sent (`ms`, `s`, `By`, `1`, …) |
| `value` | the value of gauge and sum points |
| `count`, `sum`, `min`, `max` | count, sum, minimum and maximum of histogram and summary points |
| `stream` | stream |
| `connection_id` (`connectionid`) | ingestion connection |

Metric attributes are text; numeric comparisons and aggregates convert them to numbers when
possible.

### Ids

`trace_id`, `span_id`, `parent_span_id`, `connection_id` and `id` are stored in lower case. The
colon form lower-cases what you type, so `trace_id:4BF92F35…` works. The exact operators do not:
`trace_id = 'ABC…'` compares the upper-case text and finds nothing. Use the colon form for ids.

```tql
@trace_id:4bf92f3577b34da6a3ce929d0e0e4736
id:0f8fad5b-d9cb-469f-a165-70867728950e
connection_id:4f1c2d3e | stats count() by stream    # where one connection writes to
```

## Levels

OpenTidemark knows six levels, from least to most severe:

| Ordinal | Level | Also accepted (any case) |
|---|---|---|
| 0 | `Verbose` | `verbose`, `trace`, `trc`, `vrb`, `finest`, `finer` |
| 1 | `Debug` | `debug`, `dbg`, `fine` |
| 2 | `Information` | `information`, `info`, `inf`, `notice` |
| 3 | `Warning` | `warning`, `warn`, `wrn` |
| 4 | `Error` | `error`, `err`, `eror` |
| 5 | `Fatal` | `fatal`, `critical`, `crit`, `ftl`, `emergency`, `emerg`, `alert`, `panic` |

Every spelling in the table, and the ordinals `0` to `5`, can be used wherever a level is
expected. Levels compare by **severity**, not alphabetically:

```tql
level:error                        # exactly Error
level:warn                         # exactly Warning
level >= warning                   # Warning, Error and Fatal
level > information                # the same
level < warning                    # Verbose, Debug and Information
level:3                            # Warning, by ordinal
level >= 4                         # Error and Fatal
level in [error, fatal]
level:(error or fatal)
not level:(verbose or debug)
level:err*                         # wildcards match the level name, ignoring case
```

An unknown level is an error with a suggestion (`level:erorr` → *did you mean 'error'?*).
Results show levels by name (`Error`), also when you group by level.

## Time

The time range of a query comes from outside the query (time picker, dashboard, alert window,
API `from`/`to`). The range includes its start and excludes its end. A comparison on
`@timestamp` **narrows** that range further; it can never widen it.

```tql
@timestamp >= now-15m                         # the last 15 minutes of the selected range
@timestamp < now-1h                           # older than one hour
@timestamp >= '2026-10-13T08:00:00Z' and @timestamp < '2026-10-13T09:00:00Z'
@timestamp:>2026-10-13T08:00:00Z              # the colon form needs no quotes
@timestamp >= '2026-10-13'                    # from midnight UTC
```

**Relative times:** `now`, or `now` followed by `-` or `+`, a whole number and one unit, with no
spaces: `now-30s`, `now-15m`, `now-2h`, `now-7d`, `now-2w`, `now+1h`. Units: `s` seconds, `m`
minutes, `h` hours, `d` days, `w` weeks. `now` is the end of the query's time range (the current
time for "last …" ranges).

**Absolute times** (quote them after an operator, because they contain `:`):

| Format | Example |
|---|---|
| RFC 3339 / ISO 8601 with zone | `'2026-10-13T08:00:00Z'`, `'2026-10-13T10:00:00+02:00'`, `'2026-10-13T08:00:00.250Z'` |
| without zone (UTC) | `'2026-10-13T08:00:00'`, `'2026-10-13T08:00'`, `'2026-10-13 08:00:00'` |
| date only (midnight UTC) | `'2026-10-13'` |

Rules: `@timestamp` needs a comparison (`@timestamp:2026-10-13` alone is an error) and cannot be
used with `in`. Write relative times without spaces: `now - 15m` is read as `now` followed by
the free-text words `-` and `15m`.

## Durations

The span field `duration` is compared in **milliseconds** by default, and accepts a unit:
`ns`, `us`, `ms`, `s`, `m`, `h`, `d`. Decimals are allowed.

```tql
source spans | duration > 250                      # longer than 250 ms
source spans | duration > 2s
source spans | duration >= 1.5s and duration < 1m
source spans | duration < 500us
source spans | duration > '250ms'                  # a quoted duration works too
```

Units are allowed only for `duration`. For a property such as `elapsed`, compare plain numbers in
whatever unit your application logs (`elapsed > 250`). In results, span durations are in
milliseconds (`duration_ms` in event rows, and in aggregates such as `p95(duration)`).

## Sources

| Source clause | Searches | Free text searches | Properties |
|---|---|---|---|
| none, or `source logs` | log events | message + exception | `props.<key>` |
| `source spans` | trace spans | name + status message | `attrs.<key>` |
| `source metrics` | metric data points | name + description | `attrs.<key>` |

The source clause must come first and be followed by `|`. Each source has its own
[built-in fields](#fields).

```tql
source logs | level:error
source spans | service:checkout-api status:error
source spans | kind:server | stats p95(duration) by name
source spans | trace_id:4bf92f3577b34da6a3ce929d0e0e4736
source metrics | name:http.server.request.duration | stats avg(value) by service span 1m
source metrics | kind:sum name:http.server.requests | stats rate(value) by service span 1m
```

## Stages

Stages follow the filter, each after a `|`.

| Stage | Syntax | Does |
|---|---|---|
| `where` | `where <filter>` | before `stats`: filters rows (like adding `and`); after `stats`: filters aggregated rows |
| `stats` | `stats agg[, agg…] [by f[, f…]] [span <duration>|auto]` | aggregates, optionally grouped and bucketed in time |
| `top` | `top <n> <field> [by <group>]` | the n most frequent values of a field |
| `sort` | `sort <name> [asc|desc][, …]` | orders aggregated rows |
| `limit` | `limit <n>` | caps the number of rows |
| `fields` | `fields f[, f…]` | chooses the columns of event results |

### `where`

Before `stats` (or in a query without `stats`), `where` takes any filter and is combined with the
main filter by AND. It is handy for building a query step by step, and for dashboards that append
conditions.

```tql
level:error | where service:checkout
* | where elapsed > 250 | stats count() by route
```

After `stats` or `top`, `where` filters the **aggregated rows**, like SQL's `HAVING`. It can only
use the aggregate names and the `by` fields of that stage (with the comparison operators, the
colon form, `in`, `like` and functions), not free text or other fields:

```tql
level:error | stats count() by service | where count > 100
* | stats count() as errors by service | where errors >= 10 and errors < 1000
* | stats count() by service | where service:checkout*
* | stats count() by level, service | where level >= warning
* | stats p95(elapsed) as p95 by route | where p95 > 500 | sort p95 desc
```

### `stats`

`stats` turns rows into aggregated rows: one row in total, one per group (`by`), or one per group
and time bucket (`span`).

```tql
* | stats count()                                    # one number
* | stats count() by service                         # one row per service
* | stats count() by service, level                  # one row per combination
* | stats count(), count_distinct(user.id) by service
* | stats count() by service span 5m                 # a time series per service
* | stats count() as events, avg(elapsed) as avg_ms by route
```

- Up to 20 aggregates, separated by commas, and up to 10 `by` fields.
- `by` takes built-in fields or properties. A `by` field is a group even if some rows lack it
  (they form the group with an empty value).
- Name an aggregate with `as` (a field name or a backtick name); otherwise it is named after the
  function, or `function_field` as written: `count`, `count_user.id`, `p95_elapsed`,
  `avg_duration`, `rate`. Column names must be unique (ignoring case), and `@bucket` is reserved.
- A query has at most one `stats` or `top` stage.

### `top`

`top n field` lists the `n` most frequent values of a field with their counts. `top n field by
group` does so **per value of the group**. It is a shortcut:

| `top` | is the same as |
|---|---|
| `top 10 exception_type` | `stats count() by exception_type | sort count desc | limit 10` |
| `top 3 route by service` | `stats count() by service, route`, sorted by count, 3 rows per service |

```tql
level:error | top 10 exception_type
* | top 5 RequestPath by service
source spans | status:error | top 10 name
```

The result columns are the group field (if any), the field, and `count`. `top` is already sorted,
so `sort` is not allowed after it; `where` and `limit` are.

### `sort`

`sort` orders the rows of a `stats` or `top` result by aggregate names, `by` fields or, in a time
series, `@bucket`. The default direction is **ascending**; add `desc` for largest first. Several
keys are separated by commas.

```tql
* | stats count() by service | sort count desc
* | stats count() by service | sort service
* | stats avg(elapsed) as avg_ms, count() by route | sort avg_ms desc, count desc
```

Without `sort`, a table is ordered by its first aggregate, largest first, and a time series by
time. Event results are always newest first and cannot be sorted; `sort` needs a `stats` stage
before it.

### `limit`

`limit n` caps the number of rows. After `stats` it limits the aggregated rows; in a query
without `stats` it caps the number of events returned. `n` is a whole number from 1. The server
also has its own row limit, which `limit` cannot exceed.

```tql
* | stats count() by service | sort count desc | limit 5
level:error | limit 20
```

### `fields`

`fields` chooses which columns an **event** query returns: built-in fields and properties, in the
order written. Every row also keeps its time and id, so the explorer can page through results.
Up to 50 fields; not allowed together with `stats` or `top`.

```tql
level:error | fields @timestamp, service, message
source spans | * | fields name, duration, status
* | fields level, RequestPath, elapsed, props.user
```

The columns are named as written (`RequestPath`, `props.user`). `level` returns the level name.

## Aggregates

| Aggregate | Argument | Result | Default name |
|---|---|---|---|
| `count()` | none | number of rows | `count` |
| `count(f)` | any field | number of rows where `f` exists and is not empty | `count_f` |
| `count_distinct(f)` | any field | approximate number of distinct values | `count_distinct_f` |
| `sum(f)` | numeric | sum | `sum_f` |
| `avg(f)` | numeric | average | `avg_f` |
| `min(f)`, `max(f)` | numeric | smallest / largest value | `min_f`, `max_f` |
| `p50(f)`, `p75(f)`, `p90(f)`, `p95(f)`, `p99(f)` | numeric | percentile (approximate) | `p95_f`, … |
| `rate()` | none | rows per second of the bucket, or of the whole range without `span` | `rate` |
| `rate(value)` | `value`, metrics only | per-second rate of a counter | `rate_value` |
| `first(f)`, `last(f)` | any field | value of the earliest / latest row | `first_f`, `last_f` |

- **Numeric aggregates** (`sum`, `avg`, `min`, `max`, percentiles) skip rows where the field is
  missing or not a number. Text fields are converted where possible; `@timestamp` and `id` cannot
  be summed or averaged. `avg(level)` averages the level ordinal.
- **Span durations** aggregate in milliseconds: `source spans | * | stats p95(duration)` is in ms.
- **`count_distinct`** and the **percentiles** are fast approximations, accurate enough for
  monitoring and dashboards.
- **`rate()`** counts rows per second: `level:error | stats rate() span 1m` is errors per second
  in each minute.
- **`rate(value)`** is for counters (OpenTelemetry sums such as `http.server.requests`): it turns
  the ever-growing total into a per-second rate for each series and adds the series up. It must be
  the only aggregate of its `stats` stage. The increase between the last point of one bucket and
  the first point of the next is not counted.
- **`first`/`last`** return the field's value from the earliest / latest row of each group.

```tql
* | stats count(), count(exception) as with_exception by service
* | stats count_distinct(user.id) as users by service
* | stats avg(elapsed), p50(elapsed), p95(elapsed), p99(elapsed), max(elapsed) by route
source spans | * | stats p95(duration) as p95_ms by service, name | sort p95_ms desc | limit 20
level:error | stats rate() span 1m
source metrics | name:http.server.requests | stats rate(value) by service span 1m
source metrics | kind:histogram name:http.server.request.duration | stats sum(sum), sum(count) by service
source spans | * | stats first(name), last(name), count() by trace_id
```

## Time series and span

`span <duration>` buckets a `stats` result in time and makes the query a **time series**: one
point per bucket (and per group). The bucket start is the column `@bucket`.

```tql
* | stats count() span 1m
* | stats count() by level span 5m
level:error | stats count() by service span 1h
source spans | * | stats p95(duration) by service span 5m
* | stats count() by service span auto
```

- An explicit span needs a unit (`30s`, `5m`, `1h`, `1d`; also `90s` or `1.5m`) and must be a
  whole number of seconds, at least `1s`.
- `span auto` picks the smallest of `1s 5s 10s 30s 1m 5m 10m 30m 1h 3h 6h 12h 1d` that gives at
  most about 80 buckets for the time range (whole days beyond that). Dashboards use it so a panel
  stays readable at any range; charts that need a time series use it when no span is given.
- A time series can have at most 10,000 buckets: `span 1s` over a week is an error (*use a larger
  span or 'span auto'*).
- `sort @bucket` is allowed in a time series; results are in time order by default.

## Stage order

```text
[where …] → [stats … | top …] → [where …] [sort …] [limit …]
```

- Any number of `where` stages may come first; they filter rows.
- Then at most one `stats` or `top`.
- After it: `where` (on aggregated rows), `sort` and `limit`, in any order.
- `sort`, `limit` and `fields` may each appear once. `sort` needs `stats` or `top` before it and
  is not allowed after `top`. `limit` cannot come before `stats`. `fields` cannot be combined with
  `stats` or `top`.
- At most five stages in total.

These are valid:

```tql
level:error | where service:api | stats count() by route | where count > 10 | sort count desc | limit 5
* | top 10 service | where count > 100
level:error | fields message | limit 50
```

These are errors:

```text
* | limit 5 | stats count()               # limit must come after stats or top
level:error | sort @timestamp              # sort needs a stats or top stage before it
* | top 5 service | sort count             # top already sorts by count
* | stats count() | fields service         # fields cannot be combined with stats or top
* | stats count() | stats count()          # only one stats or top stage is allowed
```

## Results

What a query returns depends on its last stages:

| Query | Result | Columns |
|---|---|---|
| no `stats`/`top` | **events**, newest first, paged | all event fields, or time + id + the `fields` columns |
| `stats`/`top` without `span` | **table** | the `by` fields as written, then the aggregates by name |
| `stats … span …` | **time series** | `@bucket`, the `by` fields, the aggregates; one series per group |

```tql
level:error                                       # events
level:error | stats count() by service            # table: service, count
level:error | stats count() by service span 5m    # time series: @bucket, service, count
```

In the explorer, events appear in the event stream, and tables and time series as a table or a
chart.

## Dashboard variables

Dashboard queries can use variables: `$name` stands for the value(s) chosen in the dashboard's
variable bar. The dashboard replaces them before the query runs, so a query with variables only
works inside a dashboard.

```text
level:error service:$service
service:$service | stats count() by route span auto
env in [$env] | stats count() by service
source spans | service:$service duration > $min_ms
```

- A variable name starts with a letter or `_`, followed by letters, digits or `_`.
- Variables are values: write them where a value goes (after `:`, an operator, inside `[ ]` or
  as a function argument), unquoted. `'$service'` is the literal text `$service`.
- A value that is a plain number is used as a number, anything else as text. Values are inserted
  as they are, never parsed as TQL, so a value cannot change the structure of the query.
- **Several values:** `field:$v` and `field = $v` match any of them, `field != $v` none of them,
  and `[$v]` inserts all of them into the list. Range operators and `like` need exactly one value.
- **All:** when a variable is set to all values (`*` or nothing selected), the condition that uses
  it is dropped, so `service:$service` then matches every service.
- A variable can hold up to 1,000 values.

## Writing values (lexical rules)

**Bare values.** After `:`, a value can be written without quotes as long as it contains no
whitespace, `(`, `)` or `|`. Inside a bare value, `\` escapes `* ? ( ) | \` and a space; any other
backslash is kept, so Windows paths work unquoted:

```tql
path:C:\temp\app.log
RequestPath:/api/v1/orders
message:retry\ later               # an escaped space
version:1.2.3-beta
```

**Quoted strings** use `'…'` or `"…"`. Inside them `\\`, `\'`, `\"`, `\n` (newline), `\t` (tab)
and `\*` (literal star) are escapes; any other backslash sequence is kept as written, so `'\d+'`
stays `\d+`. Quote values that contain spaces or `|`:

```tql
message:"order failed | retrying"
service:'payments svc'
message = 'It\'s done'
match(message, '\d+ items')
```

**Numbers:** `42`, `-7`, `3.14`, `1e6`, `2.5E-3`. A number on the right of `:` also matches the same
digits stored as text.

**Field names:** letters, digits, `_`, `.`, `-` and `@`, starting with a letter, `_` or `@`;
other names in backticks (`` `order id` ``).

**Whitespace** separates terms and is optional around `:` and the operators: `level:error`,
`level: error` and `elapsed>250` all work.

## Autocomplete and the editor

The query editor in the explorer and in dashboards suggests, as you type:

- **fields:** built-in fields first, then the properties seen in the last 24 hours, the most
  frequent first (names that need them are inserted in backticks);
- **values** after `field:` or an operator: the 10 most frequent values of that field in the last
  hour with their counts, level names for `level`, and `*` for "exists";
- **operators** (`:` `=` `!=` `>` `>=` `<` `<=` `in` `like`), `and`/`or`/`not` and `|` after a term;
- **stages** after `|`, **aggregates** inside `stats`, then `by`, `span` and `as`, and span
  values (`auto 1m 5m 15m 1h 1d`) after `span`;
- **sort keys** from the preceding `stats` stage, then `asc`/`desc`.

Up to 50 suggestions are shown, filtered by what you have typed. The editor also has a syntax
cheat sheet with the examples from [Overview by example](#overview-by-example). The same
completions are available from the API (`GET /api/v1/tql/complete`).

## Errors and how to fix them

When a query is invalid, the editor underlines the problem and shows a message, often with a hint
(*did you mean 'error'?*). Misspelled sources, stages, aggregates, levels, sort keys, `@` fields
and names in a `where` after `stats` get a suggestion when they are close to a valid name. The API
returns `invalid_query` with the position (`pos`, `len`) and the `hint`.

| Message | Cause | Fix |
|---|---|---|
| `unknown level 'erorr'` | not a level name | use a [level](#levels): `level:error` |
| `unknown stage 'srt'` | not a stage | `where`, `stats`, `top`, `sort`, `limit`, `fields` |
| `unknown aggregate 'cnt'` | not an aggregate | see [Aggregates](#aggregates) |
| `unknown source 'traces'` | not a source | `logs`, `spans`, `metrics` |
| `unknown built-in field '@srvice' for source logs` | `@name` that does not exist | drop the `@` for a property, or fix the name |
| `expected '|' or end of query, found ':'` | an unquoted value with `:` after an operator | quote it: `@timestamp >= '2026-10-13T08:00:00Z'` |
| `expected a value after ':'` | `field:` with nothing after it | add a value, or `field:*` for "exists" |
| `unterminated string` | a quote without its closing quote | close the quote, or escape it (`\'`) |
| `@timestamp needs a comparison` | `@timestamp:value` | `@timestamp >= now-15m` |
| `invalid time '…'` | not a time literal | `now-15m` or `'2026-10-13T08:00:00Z'`, see [Time](#time) |
| `durations are only allowed for span duration` | a unit on a field other than `duration` | drop the unit (`elapsed > 2000`) |
| `invalid span duration '…'` | span without a unit or not positive | `span 5m`, `span auto` |
| `span must be a whole number of seconds (at least 1s)` | `span 500ms` | `span 1s` or larger |
| `span 1s gives 604800 buckets for this time range (max 10000)` | too many buckets | larger span, or `span auto` |
| `sort needs a stats or top stage before it` | `sort` on events | events are always newest first |
| `top already sorts by count` | `sort` after `top` | remove `sort`, or use `stats` + `sort` |
| `limit must come after stats or top` | `limit` before `stats` | move `limit` to the end |
| `fields cannot be combined with stats or top` | both in one query | choose columns with `by` instead |
| `only one stats or top stage is allowed` | two aggregations | combine them into one `stats` |
| `after stats, where can only use aggregate names or by fields; 'x' is neither` | a raw field after `stats` | move the condition before `stats`, or add the field to `by` |
| `free text is not allowed after stats` | a word in a `where` after `stats` | use `field:value` with a `by` field |
| `unknown sort key 'x'` | not an aggregate name or `by` field | sort by a name from the `stats` stage |
| `duplicate column name 'x'` | two aggregates or fields with one name | rename one with `as` |
| `x() needs a numeric field; @timestamp is not numeric` | numeric aggregate on a time or id | aggregate a number |
| `rate() takes a field only as rate(value) on source metrics` | `rate(field)` on logs/spans | `rate()` |
| `rate(value) must be the only aggregate in its stats stage` | `rate(value)` with others | use a separate query or panel |
| `null can only be compared with = or !=` | `field > null` | `field = null` / `field != null` |
| `null is not allowed in a list` | `null` in `in [ … ]` | `field = null or field in [ … ]` |
| `like needs a text field` | `like` on a numeric field | use comparisons or the colon form |
| `in cannot be used with @timestamp` | time in a list | use two comparisons |
| `invalid regular expression: …` | not valid RE2 | fix the pattern; RE2 has no look-around |
| `has() takes only a field` | `has(field, 'x')` | `contains(field, 'x')` |
| `contains() needs a field and a value` | missing argument | `contains(field, 'text')` |
| `unresolved variable $x` | a `$variable` outside a dashboard | replace it with a value |
| `expression is nested too deeply (max 32)` | too many parentheses | simplify the query |
| `too many values in group (max 1000)` | huge value group or list | split the query or use a wildcard |
| `query is too long (max 8192 bytes)` | query over 8 KiB | shorten it |

## Pitfalls

- **`:` ignores case, `=` does not.** `service:Checkout` matches `checkout`;
  `service = 'Checkout'` does not (add `ci`).
- **Ids with `=`.** Ids are stored in lower case. `trace_id = 'ABC…'` finds nothing; use
  `trace_id:ABC…`, which lower-cases the input.
- **Negated numeric conditions include rows without the field.** `not elapsed > 250` also matches
  events without `elapsed`; add `elapsed:*`.
- **Regex and level.** Regular expressions run against the level **name** (`Error`) and are
  case-sensitive: `level:/err/` matches nothing; use `level:err*`, `level:error` or
  `level >= error`.
- **`ci` after `in`.** `in` lists are case-sensitive and take no `ci`: `env in ['PROD'] ci` is
  read as the list followed by the free-text word `ci`. Use a value group: `env:(prod or staging)`.
- **`?` without `*`.** `host:web?1` searches for a literal question mark; `?` is a wildcard only
  when the value also contains `*` (`host:web?1*`).
- **A filter after `source`.** The part after `source spans |` is always the filter.
  `source spans | stats count()` is read as a search for the words *stats* and *count* (or fails
  to parse); write `source spans | * | stats count()`.
- **Unquoted times after operators.** `@timestamp >= 2026-10-13T08:00:00Z` fails at the first
  `:`; quote it, or use the colon form `@timestamp:>2026-10-13T08:00:00Z`.
- **Spaces in relative times.** `now - 15m` is not `now-15m`.
- **Property names are case-sensitive**, built-in names are not: `requestpath` is not
  `RequestPath`, but `SERVICE` is `service`.
- **Free text is a substring search** in the message and exception only. To search a property,
  use `field:*text*` or `contains(field, 'text')`.
- **The time range is outside the query.** If a query finds nothing, check the selected range
  first; `@timestamp` conditions can only narrow it.

## Examples

Real questions and the queries that answer them. Copy a query into the search bar, adjust the
names to your own services and properties, and pick a time range. Each example links to the part
of this page that explains it.

### Finding events

**Which events mention "timeout"?**

```tql
timeout
```

A plain word searches the message and the exception, ignoring case. See [Free text](#free-text).

**Which events contain the exact phrase "connection refused"?**

```tql
"connection refused"
```

Quotes keep the words together, in this order.

**What went wrong in the checkout service?**

```tql
level >= error service:checkout
```

`level >= error` keeps Error and Fatal events; the two conditions must both match. See
[Levels](#levels).

**Show me warnings and errors, but not the health checks.**

```tql
level >= warning not RequestPath:/health*
```

`not` removes events whose `RequestPath` starts with `/health`. See
[Combining conditions](#combining-conditions).

**Which events have an exception attached?**

```tql
exception:*
```

`field:*` means "the field is present and not empty". See
[Exists and missing](#exists-and-missing).

**Everything that happened during one request.**

```tql
trace_id:4bf92f3577b34da6a3ce929d0e0e4736
```

Every log event of the request shares its trace id. Use `:` for ids; it ignores upper and lower
case. See [Ids](#ids).

**Requests that took longer than one second.**

```tql
elapsed > 1000
```

Compares the numeric property `elapsed` (in whatever unit your application logs, here
milliseconds). Events without `elapsed` are not included. See
[Comparison operators](#comparison-operators).

**Errors from any of the payment services in production.**

```tql
level:error service:payments* env:(prod or production)
```

`payments*` matches every service whose name starts with "payments"; `(prod or production)`
accepts either value. See [Wildcards](#wildcards) and [Value groups](#value-groups).

**Requests from users who are not signed in.**

```tql
not user.id:*
```

Matches events where the `user.id` property is missing or empty.

**Only the columns I care about.**

```tql
level:error | fields @timestamp, service, message, RequestPath
```

`fields` chooses the columns of the result. See [fields](#fields_1).

### Counting and grouping

**How many errors did each service log?**

```tql
level:error | stats count() by service
```

`stats count() by service` returns one row per service with its count, largest first. See
[stats](#stats).

**What are the most common error messages?**

```tql
level >= error | top 10 template
```

`top 10 template` lists the ten most frequent message templates with their counts. Grouping by
`template` (rather than `message`) counts "User 7 not found" and "User 9 not found" as the same
error. See [top](#top).

**Which exceptions occur most often, per service?**

```tql
exception:* | top 5 exception_type by service
```

`by service` gives the five most frequent exception types for each service.

**How many different users were active in each service?**

```tql
* | stats count_distinct(user.id) as users by service | sort users desc
```

`count_distinct` counts unique values; `as users` names the column so `sort` can use it. See
[Aggregates](#aggregates).

**Which services logged more than 100 errors?**

```tql
level:error | stats count() by service | where count > 100
```

A `where` after `stats` filters the counted rows, not the events. See [where](#where).

**How many requests failed compared to the total?**

```tql
* | stats count() as total, count(exception) as failed by service
```

`count(exception)` only counts events that have an exception, so both numbers come from one query.

**Which hosts produce the most errors?**

```tql
level:error | stats count() by host | sort count desc | limit 10
```

`sort … desc` puts the largest first and `limit 10` keeps the first ten rows. See
[sort](#sort) and [limit](#limit).

### Charts over time

**Errors per service over time.**

```tql
level:error | stats count() by service span 5m
```

`span 5m` counts in five-minute buckets, which the explorer and dashboards draw as a chart. See
[Time series and span](#time-series-and-span).

**Errors per second.**

```tql
level:error | stats rate() span 1m
```

`rate()` divides each bucket's count by its length in seconds.

**A chart that fits any time range.**

```tql
* | stats count() by level span auto
```

`span auto` picks a bucket size that gives a readable chart for the selected range; dashboards
use it so a panel works for both "last hour" and "last 30 days".

### Performance

**Which endpoints are slowest?**

```tql
elapsed:* | stats p95(elapsed) as p95, count() by RequestPath | sort p95 desc | limit 20
```

`p95` is the time that 95 % of requests stay below, a better measure of "slow" than the average.
`elapsed:*` skips events that have no timing.

**How did latency develop over the day?**

```tql
elapsed:* | stats p50(elapsed), p95(elapsed), p99(elapsed) span 15m
```

Three percentiles in one chart: the typical, the slow and the slowest requests.

**Slowest operations from traces.**

```tql
source spans | kind:server | stats p95(duration) as p95_ms by service, name | sort p95_ms desc | limit 20
```

`source spans` searches trace spans instead of logs; `duration` is in milliseconds in results.
See [Sources](#sources) and [Durations](#durations).

**Spans that took longer than two seconds.**

```tql
source spans | duration > 2s
```

`duration` accepts a unit: `250ms`, `2s`, `1m`.

### Traces

**Which operations fail most often?**

```tql
source spans | status:error | top 10 name by service
```

`status:error` keeps failed spans; `top 10 name by service` ranks the operations of each service.

**Every span of one trace.**

```tql
source spans | trace_id:4bf92f3577b34da6a3ce929d0e0e4736
```

**Requests per second, per service.**

```tql
source spans | kind:server | stats rate() by service span 1m
```

Each server span is one handled request, so `rate()` of server spans is the request rate.

**HTTP 5xx responses by operation.**

```tql
source spans | attrs.http.response.status_code >= 500 | stats count() by name
```

`attrs.` reads an OpenTelemetry attribute of the span. See
[Built-in fields and properties](#built-in-fields-and-properties).

### Metrics

**Which metrics are being sent?**

```tql
source metrics | * | stats count() by name, kind, unit | sort count desc
```

After `source metrics |` comes a filter; `*` keeps everything. See [Pitfalls](#pitfalls).

**Average CPU use per service.**

```tql
source metrics | name:process.cpu.utilization | stats avg(value) by service span 1m
```

**Requests per second from a counter.**

```tql
source metrics | name:http.server.requests | stats rate(value) by service span 1m
```

Counters only ever grow; `rate(value)` turns them into "per second". See
[Aggregates](#aggregates).

**Peak memory per host.**

```tql
source metrics | name:system.memory.usage | stats max(value) by host span 5m
```

### Your data and ingestion

**Which services send the most data?**

```tql
* | stats count() by service, stream | sort count desc
```

**How does data arrive?**

```tql
* | stats count() by protocol span 1h
```

`protocol` is how each event was sent: `otlp`, `seq`, `elastic` or `native`.

**Where does one connection write to?**

```tql
connection_id:4f1c2d3e | stats count() by stream
```

**Events that arrive without a service name.**

```tql
not service:* | stats count() by protocol, host
```

Useful to find a client whose configuration is missing its service name.

### Dashboards with variables

Inside a dashboard, `$name` stands for the value chosen in the variable bar, so one panel works
for every service or environment. See [Dashboard variables](#dashboard-variables).

```text
level:error service:$service | stats count() by route span auto
env in [$env] | stats count() by service
```

When the variable is set to "All", its condition is dropped and the panel shows everything.

More scenarios (signals, live tail, exporting results, performance tips) are in the
[TQL cookbook](manual/querying.md).

## Limits

| Limit | Value |
|---|---|
| query length | 8 KiB (8,192 bytes) of UTF-8 text |
| nesting of parentheses | 32 levels |
| values in a value group or `in` list | 1,000 |
| stages | 5 |
| `by` fields | 10 |
| aggregates per `stats` | 20 |
| `fields` | 50 |
| time buckets per time series | 10,000 |
| events per page (explorer, API default) | 200 |

The server also limits every query's running time and number of rows
(`TIDEMARK_QUERY_TIMEOUT`, `TIDEMARK_QUERY_MAX_ROWS`, see [Configuration](configuration.md)).

## Grammar

The formal grammar, for tool builders. Words in quotes are keywords (case-insensitive);
`{ … }` repeats, `[ … ]` is optional.

```ebnf
query      = [ source_clause "|" ] filter { "|" stage } ;
source_clause = "source" ( "logs" | "spans" | "metrics" ) ;
filter     = "*" | or_expr | ε ;               (* empty or * = match all *)
or_expr    = and_expr { ("or" | "||") and_expr } ;
and_expr   = unary { [ "and" | "&&" ] unary } ;   (* adjacency = AND *)
unary      = ( "not" | "!" ) unary | primary ;
primary    = "(" or_expr ")" | field_term | compare | in_expr | func_call | free_text ;
field_term = field ":" value_expr ;
value_expr = value | "(" value { ("or"|"and") value } ")" | ( "<" | "<=" | ">" | ">=" ) value ;
value      = string | number | bare | regex | "*" | "true" | "false" | "null" ;
compare    = field op literal [ "ci" ] ;
op         = "=" | "==" | "!=" | "<>" | "<" | "<=" | ">" | ">=" | "like" | "not like" ;
in_expr    = field [ "not" ] "in" "[" literal { "," literal } "]" ;
func_call  = ( "contains" | "startswith" | "endswith" | "has" | "match" ) "(" field [ "," literal ] ")" ;
free_text  = string | bare ;                     (* a bare word not followed by ':' or op *)
stage      = stats | sort | limit | fields | where | top ;
stats      = "stats" agg { "," agg } [ "by" field { "," field } ] [ "span" ( duration | "auto" ) ] ;
agg        = agg_fn "(" [ field ] ")" [ "as" ident ] ;
agg_fn     = "count" | "count_distinct" | "sum" | "avg" | "min" | "max" | "p50" | "p75" | "p90"
           | "p95" | "p99" | "rate" | "first" | "last" ;
sort       = "sort" ident [ "asc" | "desc" ] { "," ident [ "asc" | "desc" ] } ;
limit      = "limit" integer ;
fields     = "fields" field { "," field } ;
where      = "where" or_expr ;
top        = "top" integer field [ "by" field ] ;
```

Precedence: `not` > `and` (including adjacency) > `or`.

## Cheat sheet

| I want … | Query |
|---|---|
| text anywhere in the message | `timeout`, `"connection refused"` |
| an exact field value (any case) | `service:checkout` |
| an exact field value (exact case) | `Service = 'Checkout'` |
| one of several values | `env:(prod or staging)`, `env in [prod, staging]` |
| a prefix / substring | `service:check*`, `RequestPath:*orders*`, `contains(f, 'x')` |
| a pattern | `path:/^\/api\/v\d+\//`, `match(f, 're')` |
| a field to exist / be missing | `user.id:*` / `not user.id:*` |
| numbers above a threshold | `elapsed > 250`, `status_code >= 500` |
| warnings and worse | `level >= warning` |
| the last 15 minutes of the range | `@timestamp >= now-15m` |
| slow spans | `source spans | duration > 2s` |
| exclude something | `not service:healthcheck`, `!exception:*` |
| count per group | `| stats count() by service` |
| a chart over time | `| stats count() by service span 5m` |
| the top N values | `| top 10 RequestPath` |
| percentiles | `| stats p95(elapsed), p99(elapsed) by route` |
| distinct users | `| stats count_distinct(user.id)` |
| filter on a count | `| stats count() by service | where count > 100` |
| largest first | `| sort count desc | limit 10` |
| only some columns | `| fields @timestamp, service, message` |
| per-second rate of a counter | `source metrics | name:x | stats rate(value) span 1m` |
