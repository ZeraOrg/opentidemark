<!-- SPDX-License-Identifier: Apache-2.0 -->
# Alerting

Configure OpenTidemark alerting: notification channels (webhook, Slack, Microsoft Teams, email over
SMTP), the anatomy of an alert rule, testing, muting and history, the webhook payload and its
HMAC-SHA256 signature, and ready-made rules for common SRE signals.

Roles: viewers read rules, history and channels (secrets masked); editors create, change, test,
mute and delete rules; admins manage channels. UI: **Alerts** (`/alerts`) and **Settings ›
Channels**.

## How evaluation works

```text
every 10 s (alerting leader only):
  for each enabled rule where now ≥ lastEvalAt + every:
    run the rule query over [now − window, now]  (60 s timeout)
    one value per group ─▶ state machine per group ─▶ history row per transition
                                                   └▶ notifications (unless muted)
```

States per group: `ok → pending` (condition true, shorter than `for`) `→ firing` `→ ok`
(resolved). With `for: 0s` a true condition goes straight to `firing`. Three consecutive failed
evaluations (query error, invalid document) replace all groups with one `error` state; the next
success starts afresh. Notifications are sent on transitions to `firing` and `error` and from
`firing`/`error` back to `ok`; identical notifications (rule, group, state) within one minute are
dropped. Muted rules keep evaluating and recording history but send nothing.

In HA mode exactly one node (the `alerting` lease holder) evaluates.

## Channels

| Kind | Config | Notes |
|---|---|---|
| `webhook` | `{"url", "secret"?, "headers"?}` | JSON POST; optional HMAC signature; ≤ 20 custom headers (not `Host`, `Content-Length`, `Content-Type`, `Transfer-Encoding`, `Connection`, `User-Agent`, `X-Tidemark-Signature`) |
| `slack` | `{"url"}` | Slack incoming webhook; Block Kit message with an *Open in OpenTidemark* button |
| `teams` | `{"url"}` | Microsoft Teams Workflows/incoming webhook; Adaptive Card 1.4 |
| `email` | `{"to": ["ops@example.com", …]}` (1–20 bare addresses) | SMTP server from `TIDEMARK_SMTP_*`; required before email channels can be created |

URLs must be `https` and resolve to public addresses unless `TIDEMARK_ALLOW_PRIVATE_WEBHOOKS=true`
([SSRF protection](security-hardening.md#ssrf)). URLs, secrets and header values are encrypted at
rest and returned as `"••••"`. On `PUT`, sending `"••••"` keeps the stored value.

Delivery: up to 2 workers, 10 s timeout per attempt, 1 attempt + 3 retries (1 s, 2 s, 4 s) on
network errors, `408`, `429` and `5xx`; other statuses fail permanently. HTTP `2xx` is success.
Webhook requests carry `User-Agent: Tidemark-Alerting/1`.

### SMTP settings

```bash
# .env or tidemark.env, then restart
TIDEMARK_SMTP_HOST=smtp.example.com
TIDEMARK_SMTP_PORT=587
TIDEMARK_SMTP_USERNAME=opentidemark@example.com
TIDEMARK_SMTP_PASSWORD_FILE=/run/secrets/smtp_password
TIDEMARK_SMTP_FROM=OpenTidemark <opentidemark@example.com>
TIDEMARK_SMTP_STARTTLS=true            # STARTTLS required; PLAIN auth only after STARTTLS or to localhost
```

Mails have the subject `[STATE] rule name` and plain-text + HTML bodies.

### Create and test channels

```bash
# Webhook with HMAC secret (admin)
curl -sS "$TM/api/v1/channels" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' -d '{
  "name": "ops-webhook", "kind": "webhook",
  "config": {"url": "https://hooks.example.com/opentidemark", "secret": "<HMAC_SECRET>",
             "headers": {"X-Env": "prod"}}}' | tee channel.json | jq '{id, config}'
# ⇒ config values masked: {"url":"••••","secret":"••••","headers":{"X-Env":"••••"}}

# Slack, Teams, email
curl -sS "$TM/api/v1/channels" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"slack-oncall","kind":"slack","config":{"url":"https://hooks.slack.com/services/<…>"}}'
curl -sS "$TM/api/v1/channels" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"teams-ops","kind":"teams","config":{"url":"<TEAMS_WORKFLOW_URL>"}}'
curl -sS "$TM/api/v1/channels" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"ops-mail","kind":"email","config":{"to":["ops@example.com"]}}'

# Send a test notification (state "test", no retries)
curl -sS -X POST "$TM/api/v1/channels/$(jq -r .id channel.json)/test" -H "Authorization: Bearer $TM_TOKEN"
# ⇒ {"ok":true}
```

Deleting a channel that a rule uses returns `409`.

## Rule anatomy

```json
{
  "name": "Checkout errors",
  "enabled": true,
  "streams": ["checkout"],
  "doc": {
    "description": "Checkout error spike",
    "query": "service:checkout-api and level >= error | stats count()",
    "condition": {"op": ">", "threshold": 25},
    "window": "5m",
    "every": "1m",
    "for": "2m",
    "severity": "critical",
    "channels": ["<CHANNEL_ID>"],
    "noData": "ok",
    "groupBy": [],
    "muteUntil": null
  }
}
```

| Field | Rules | UI label |
|---|---|---|
| `query` | ≤ 8 KiB; exactly **one** `stats` stage with **one** numeric aggregate; no `top`, no `span`; by-fields must equal `groupBy` in order | *Query* |
| `condition.op` / `threshold` | `>`, `>=`, `<`, `<=`, `==`, `!=`; finite number | *Condition*, *Threshold* |
| `window` | 10 s – 7 d (Go duration); the query range is `[now − window, now]` | *Window* |
| `every` | 10 s – 24 h; **required** | *Evaluate every* |
| `for` | 0 – 24 h; how long the condition must hold before `firing` | *For* |
| `severity` | `info`, `warning` (default), `critical` | *Severity* |
| `noData` | `ok` (default: no data = condition false), `keep` (state unchanged), `alerting` (no data = condition true) | *No data* |
| `groupBy` | One state per group; ≤ 1,000 groups evaluated | *Group by* |
| `channels` | ≤ 20 existing channel ids | *Channels* |
| `description` | ≤ 1024 | |
| `muteUntil` | Set through the mute endpoint | |
| `streams` (outside `doc`) | `null` = every stream, else 1–100 valid stream names (stored sorted); see below | *Streams* |

A group with no row (or a NULL/NaN value) is "no data" and handled by `noData`. For grouped rules,
groups that disappear from the result are treated as no data, and `ok` groups are forgotten.

### Streams and teams

A rule reads the streams listed in `streams`; the list sits next to `doc`, so older binaries
still parse the document. `null` (or an absent field) means every stream and is accepted only
from callers who see every stream; everyone else must send a non-empty subset of their own
visible streams (else `400` with `details.field = streams`). The editor drawer shows *All
streams* for callers who see every stream and otherwise a required *Streams* choice that
defaults to the editor's whole scope and feeds the query bar's completions. The evaluator uses
the **stored** streams, so a rule keeps evaluating after its author loses access to them. A
caller lists, reads, tests, mutes, changes and deletes a rule only when its streams lie within
the caller's scope (a `null` rule only when they see every stream); otherwise the rule is `404`.
Rules created before teams existed have `null`. Rule objects returned by the API carry `streams`;
`alert.create` and `alert.update` audit it ([Users and access](users-access.md#teams)).

## Create, test, mute

```bash
# Create (editor)
curl -sS "$TM/api/v1/alerts" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' -d '{
  "name": "Errors by service", "enabled": true,
  "doc": {"description": "Any error event per service",
          "query": "level >= error | stats count() by service",
          "condition": {"op": ">", "threshold": 0},
          "window": "5m", "every": "1m", "for": "0s", "severity": "critical",
          "channels": ["<CHANNEL_ID>"], "noData": "ok", "groupBy": ["service"]}}' | tee rule.json | jq '{id, state, version}'

RULE=$(jq -r .id rule.json)

# Dry evaluation now: no state change, no notification
curl -sS -X POST "$TM/api/v1/alerts/$RULE/test" -H "Authorization: Bearer $TM_TOKEN" | jq '{state, samples, transitions}'

# Mute for two hours, then unmute
curl -sS "$TM/api/v1/alerts/$RULE/mute" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"until\":\"$(date -u -d '+2 hours' +%Y-%m-%dT%H:%M:%SZ)\"}" | jq .doc.muteUntil
curl -sS "$TM/api/v1/alerts/$RULE/mute" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"until":null}'

# History (newest first, default 100, max 1000)
curl -sS "$TM/api/v1/alerts/$RULE/history?limit=20" -H "Authorization: Bearer $TM_TOKEN"
```

- `mute` accepts a time in the future up to one year; it bumps `version`.
- `PUT /alerts/{id}` needs the full body `{name, enabled, doc, streams?, version}`; a stale
  `version` ⇒ `409`.
- The rule above has no `streams`, so it reads every stream; only callers who see every stream
  can create it. A restricted editor adds `"streams": ["checkout"]` (a subset of their own).
- The UI drawer (**Alerts › New rule**) shows a *Preview* chart: the rule query with `span
  <every>` over the last 3 h, the threshold as a dashed line, and shaded buckets where the
  condition holds.

## Webhook payload and signature

```json
{
  "rule": {"id": "01a13700-…", "name": "Errors by service", "severity": "critical"},
  "state": "firing",
  "previousState": "ok",
  "value": 1,
  "threshold": 0,
  "group": {"service": "inventory"},
  "at": "2026-10-13T20:43:01.300070427Z",
  "url": "https://tidemark.example.com/alerts/01a13700-…",
  "message": "firing: value 1, condition > 0"
}
```

- `state` is `firing`, `ok` (resolved), `error`, or `test` from the channel test.
- `value` is `null` when there was no data; `group` is `{}` for ungrouped rules.
- `url` uses `TIDEMARK_PUBLIC_URL`.

When the channel has a `secret`, the request carries

```text
X-Tidemark-Signature: sha256=<hex HMAC-SHA256 of the raw request body, key = secret>
```

Verify it over the **raw bytes** before parsing the JSON, with a constant-time comparison:

=== "Python"

    ```python
    import hashlib, hmac

    def verify(secret: bytes, body: bytes, header: str) -> bool:
        expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
        return hmac.compare_digest(expected, header or "")
    ```

=== "Go"

    ```go
    func verify(secret, body []byte, header string) bool {
        m := hmac.New(sha256.New, secret)
        m.Write(body)
        want := "sha256=" + hex.EncodeToString(m.Sum(nil))
        return hmac.Equal([]byte(want), []byte(header))
    }
    ```

=== "Node.js"

    ```js
    const crypto = require("crypto");
    function verify(secret, rawBody, header) {
      const want = "sha256=" + crypto.createHmac("sha256", secret).update(rawBody).digest("hex");
      return header && want.length === header.length &&
        crypto.timingSafeEqual(Buffer.from(want), Buffer.from(header));
    }
    ```

The signature covers only the body; there is no timestamp header. Use the `at` field to reject
stale or replayed deliveries (for example older than 5 minutes) and deduplicate on
`rule.id` + `group` + `state` + `at`.

## Example rules for SRE signals

Each `doc` below is a complete rule document; wrap it as `{"name": …, "enabled": true, "doc": …}`
and add `"streams": […]` when the rule should read (or may only read) some streams. All seven
were accepted by `POST /alerts` on a test installation. The HMAC snippets above were executed for
Python; the Go and Node.js versions and the SMTP, Slack and Teams channel examples were written
from the source and not run.

```json
{"description": "Error burst per service",
 "query": "level >= error | stats count() by service",
 "condition": {"op": ">", "threshold": 50}, "window": "5m", "every": "1m", "for": "5m",
 "severity": "critical", "channels": ["<CHANNEL_ID>"], "noData": "ok", "groupBy": ["service"]}
```

```json
{"description": "p95 server latency above 800 ms",
 "query": "source spans | kind:server | stats p95(duration) by service",
 "condition": {"op": ">", "threshold": 800}, "window": "10m", "every": "1m", "for": "10m",
 "severity": "warning", "channels": ["<CHANNEL_ID>"], "noData": "ok", "groupBy": ["service"]}
```

```json
{"description": "Server span errors per service",
 "query": "source spans | kind:server and status:error | stats count() by service",
 "condition": {"op": ">", "threshold": 10}, "window": "5m", "every": "1m", "for": "3m",
 "severity": "critical", "channels": ["<CHANNEL_ID>"], "noData": "ok", "groupBy": ["service"]}
```

```json
{"description": "Heartbeat: checkout stopped logging",
 "query": "service:checkout-api | stats count()",
 "condition": {"op": "<", "threshold": 1}, "window": "10m", "every": "1m", "for": "5m",
 "severity": "critical", "channels": ["<CHANNEL_ID>"], "noData": "alerting", "groupBy": []}
```

```json
{"description": "Fatal events anywhere",
 "query": "level:fatal | stats count()",
 "condition": {"op": ">", "threshold": 0}, "window": "2m", "every": "1m", "for": "0s",
 "severity": "critical", "channels": ["<CHANNEL_ID>"], "noData": "ok", "groupBy": []}
```

```json
{"description": "Queue depth gauge too high",
 "query": "source metrics | name:queue.depth | stats max(value) by service",
 "condition": {"op": ">", "threshold": 10000}, "window": "5m", "every": "1m", "for": "10m",
 "severity": "warning", "channels": ["<CHANNEL_ID>"], "noData": "keep", "groupBy": ["service"]}
```

```json
{"description": "Failed SSH logins (syslog via Collector)",
 "query": "\"Failed password\" | stats count() by hostname",
 "condition": {"op": ">", "threshold": 20}, "window": "10m", "every": "1m", "for": "0s",
 "severity": "warning", "channels": ["<CHANNEL_ID>"], "noData": "ok", "groupBy": ["hostname"]}
```

Tips:

- Use `for` to ride out short spikes and `window` ≥ 2 × `every` so evaluations overlap.
- For absence detection, use `noData: "alerting"` together with a `count()` condition.
- Keep groups bounded (≤ 1,000); group by `service`, not by request id.
- Alert on OpenTidemark itself from an external Prometheus® ([Metrics](metrics.md#alert-rules)),
  not only from rules inside OpenTidemark.
