<!-- SPDX-License-Identifier: Apache-2.0 -->
# Dashboards

Build OpenTidemark dashboards in the UI builder or as JSON documents, use variables, panel types
and templates, and manage dashboards as code: keep the JSON in git and import, export and update
it through the API with curl.

Reading dashboards needs the viewer role; creating, changing, importing and duplicating need
editor. UI: **Dashboards** (`/dashboards`).

## In the UI

- **Dashboards › New dashboard**, or pick one of the **Templates**: *Service overview (OTel)*,
  *Log volume & errors*, *HTTP (RED metrics from spans)*, *Ingestion health*. Templates ask for a
  *Service* and are instantiated with a multi-value `service` variable.
- *Edit* a dashboard, then *New panel*. The panel editor has two tabs:
    - **Builder**: source → filter (with autocomplete) → measure (count, avg, p95 … of a field) →
      group by (up to 3) → interval → visualisation. It generates TQL.
    - **Query**: raw TQL. Builder ⇒ Query is lossless; Query ⇒ Builder works when the query fits
      the builder's shape, otherwise the Builder tab is read-only.
- The preview refreshes 400 ms after every change.
- From the explorer, *Add to dashboard* pins the current query (choose the dashboard and panel
  type). Each panel has *Open in explorer*.
- Full-screen/TV mode cycles automatically; *Duplicate* makes a private copy.
- Panels and variable values are read within the viewer's stream scope and the scope picker's
  selection ([Users and access](users-access.md#teams)): two users can see different numbers on
  the same shared dashboard, and a user without access to a panel's streams sees an empty panel,
  not an error.

## The dashboard document {#document}

A dashboard is `{name, description, shared, doc}`; `doc` is a strict JSON document (unknown
fields are errors):

```json
{
  "version": 1,
  "timeRange": {"from": "now-6h", "to": "now"},
  "refresh": "30s",
  "variables": [
    {"name": "service", "label": "Service", "type": "field-values",
     "source": "logs", "field": "service", "multi": true, "default": ["*"]}
  ],
  "panels": [
    {"id": "errors", "title": "Errors by service", "type": "timeseries",
     "grid": {"x": 0, "y": 0, "w": 8, "h": 6},
     "query": "level >= error and service:$service | stats count() by service span auto",
     "options": {"stack": true, "style": "bars", "unit": "short", "legend": "bottom",
                 "thresholds": [{"value": 100, "color": "danger"}]}},
    {"id": "total", "title": "Events", "type": "stat",
     "grid": {"x": 8, "y": 0, "w": 4, "h": 3},
     "query": "service:$service | stats count()"},
    {"id": "notes", "title": "Runbook", "type": "markdown",
     "grid": {"x": 8, "y": 3, "w": 4, "h": 3},
     "options": {"content": "See the **on-call runbook**."}},
    {"id": "recent", "title": "Recent errors", "type": "logs",
     "grid": {"x": 0, "y": 6, "w": 12, "h": 6},
     "query": "level >= error and service:$service"}
  ]
}
```

| Field | Rules |
|---|---|
| `version` | `1` |
| `timeRange.from/to` | **required**; `now`, `now-<n><s|m|h|d|w>` or RFC 3339 |
| `refresh` | `""`, `off` or `<n>s|m|h` between 5 s and 24 h |
| `variables` | ≤ 20; `name` `^[A-Za-z_][A-Za-z0-9_]{0,63}$` unique; `label` ≤ 200; `type` `field-values` (needs `source` + `field`), `custom` (needs `options`) or `text`; `multi`; `default` (one value unless `multi`) |
| `panels` | ≤ 100; `id` `^[A-Za-z0-9_-]{1,64}$` unique; `title` ≤ 200; `grid` 12 columns: `x ≥ 0`, `w ≥ 1`, `x + w ≤ 12`, `0 ≤ y ≤ 10000`, `1 ≤ h ≤ 100` (row height 40 px) |
| `query` | ≤ 8 KiB, required except for `markdown`; must parse and check with the variable defaults |
| `options` | `stack`, `style` (`line`/`area`/`bars`), `unit` (`^[A-Za-z0-9_/%.-]{1,32}$`), `decimals` (0–10), `legend` (`bottom`/`right`/`hidden`), `thresholds` (≤ 20 × `{value, color}`; colours `accent`, `success`, `warning`, `danger`, `info`, `chart1`–`chart8`), `content` (markdown only, ≤ 32 KiB) |

### Panel types and the queries they need

| `type` | Shows | Query |
|---|---|---|
| `timeseries` | Line, area or bars over time | `stats` or `top` (use `span auto`) |
| `bar` | Categorical bars | `stats` or `top` |
| `stat` | Single value, sparkline, delta vs previous range | `stats` or `top` |
| `gauge` | Single value against thresholds | `stats` or `top` |
| `pie` | Shares | `stats` or `top` |
| `table` | Any result | any |
| `logs` | Live event list | logs filter without stats |
| `traces` | Trace list | spans filter without stats |
| `markdown` | Text (sanitised, no raw HTML) | none; `options.content` |

## Variables

`$name` in a panel query is replaced **on the parsed query**, never by string concatenation:

- one value ⇒ a literal (numbers stay numbers);
- several values ⇒ a value group: `service:$service` with `["checkout","web"]` becomes
  `(service:"checkout" or service:"web")`; `!=` combines with AND;
- an empty list or `*` removes the predicate (an enclosing `or` then matches everything);
- unknown variables are errors.

Types: `field-values` (values offered from facets of `source`/`field`), `custom` (fixed
`options`), `text` (free input).

Resolve a panel's final query, for example to debug or to run it from a script:

```bash
curl -sS "$TM/api/v1/dashboards/<DASHBOARD_ID>/panels/errors/query" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"vars":{"service":["checkout","web"]}}'
# ⇒ {"q":"level >= \"error\" and (service:\"checkout\" or service:\"web\") | stats count() by service span auto"}
```

## Dashboards as code {#as-code}

Keep one JSON file per dashboard in git in the export format `{name, description, doc}` and let
CI apply it. Use an API token with scope `write`.

### Export

```bash
curl -sS -H "Authorization: Bearer $TM_TOKEN" "$TM/api/v1/dashboards/<DASHBOARD_ID>/export" \
  -o dashboards/service-health.json
```

The response is `{name, description, doc}` with
`Content-Disposition: attachment; filename=dashboard-<name>.json`.

### Import (always creates a new, private dashboard)

```bash
curl -sS "$TM/api/v1/dashboards/import" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' --data-binary @dashboards/service-health.json | jq '{id, name, shared}'
```

### Idempotent apply (create or update by name)

Import always creates; for repeatable deployments, look the dashboard up by name and `PUT` it with
the current `version`:

```bash
#!/usr/bin/env bash
# apply-dashboard.sh <file.json> — create or update a shared dashboard by name
set -euo pipefail
FILE=$1
NAME=$(jq -r .name "$FILE")
AUTH=(-H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json')

ID=$(curl -sS "${AUTH[@]}" "$TM/api/v1/dashboards" | jq -r --arg n "$NAME" '[.[] | select(.name == $n)][0].id // empty')
if [ -z "$ID" ]; then
  jq '. + {shared: true}' "$FILE" | curl -sS --fail-with-body "${AUTH[@]}" -X POST "$TM/api/v1/dashboards" --data-binary @- | jq -r .id
else
  VERSION=$(curl -sS "${AUTH[@]}" "$TM/api/v1/dashboards/$ID" | jq .version)
  jq --argjson v "$VERSION" '. + {shared: true, version: $v}' "$FILE" |
    curl -sS --fail-with-body "${AUTH[@]}" -X PUT "$TM/api/v1/dashboards/$ID" --data-binary @- | jq -r .id
fi
```

- `PUT` with a stale `version` returns `409`; re-read and retry.
- Only the owner may `PUT`/`DELETE` a dashboard (admins also for shared ones). Run CI with a
  dedicated user so it owns the dashboards it manages.
- Names are not unique; keep them unique by convention if you apply by name.
- Validate in CI before applying: invalid documents return `400` with `details.field`
  (`doc.panels[0].query` …) or `invalid_query` with `details.panelId`, `pos`, `len`, `hint`.

### Templates as a starting point

```bash
curl -sS "$TM/api/v1/dashboards/templates?service=checkout" -H "Authorization: Bearer $TM_TOKEN" |
  jq '.[] | select(.id=="http-red") | {name, description, doc}' > dashboards/checkout-red.json
```

## Sharing

- `shared: true` makes a dashboard visible to every user (viewer and up); private dashboards are
  visible only to the owner. `GET /dashboards` lists own and shared dashboards.
- Imported and duplicated dashboards are private to the caller; share them with `PUT` (`shared:
  true`) or in the UI (*Visibility*).
- Dashboard URLs (`/dashboards/<id>`) can be shared; viewers need an account. There are no public
  or anonymous links.
- Duplicating (`POST /dashboards/{id}/duplicate {"name": "…"}`) gives every user a private copy
  to experiment with.
