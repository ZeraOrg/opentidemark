<!-- SPDX-License-Identifier: Apache-2.0 -->
# Guide for automation agents

A short guide for AI and DevOps agents that operate OpenTidemark: the stable identifiers to rely
on, idempotent API recipes, which operations are destructive and need explicit human
confirmation, and where to find machine-readable copies of this documentation.

## Ground rules

1. **Authenticate with an API token** (`Authorization: Bearer tmt_…`) of a dedicated user, with
   the smallest scope that works: `read` for diagnosis, `write` for dashboards/alerts/signals,
   `admin` only for provisioning. Never use a person's session or the default admin.
2. **Know your stream scope.** A token sees exactly the streams its owner sees. Start with
   `GET /api/v1/auth/me` and read `allStreams`, `streams` and `streamAccess`: empty results may
   mean "no access", not "no data". Only admins see every stream.
3. **Never print secrets.** Connection keys, API tokens and channel secrets are returned once;
   write them straight into a secret store. Logs and transcripts must show at most the prefix
   (`tmk_abcd…`).
4. **Read before write.** Every change starts with a `GET` of the current state, so you can show a
   diff and use the `version`/`updatedAt` concurrency token.
5. **Validate before apply.** Use `POST /api/v1/query/validate` for TQL, `POST /alerts/{id}/test`
   for rules, dry runs for purges, `tidemark migrate --dry-run` for upgrades.
6. **Respect limits.** 300 API requests per minute per token; back off on `429` using
   `Retry-After`, and on `503`.
7. **Report the request id** (`X-Request-Id`) with any failure.

## Machine-readable documentation

The documentation site publishes two plain-text files for agents and language models at its
root:

- `llms.txt` (<https://zeraorg.github.io/opentidemark/llms.txt>): an index of every page with its
  title, summary and link;
- `llms-full.txt` (<https://zeraorg.github.io/opentidemark/llms-full.txt>): the full Markdown of the
  user documentation and this manual in one file, without HTML comments.

Both are generated from the same sources as the site at every build, so they match the published
pages. The offline documentation bundle of each release (`tidemark_<version>_docs.zip`) contains
the same files.

## Stable identifiers

| Identifier | Stable? | Notes |
|---|---|---|
| Binary `tidemark`, commands, flags | yes | Product name is OpenTidemark; technical names keep `tidemark` |
| Variables `TIDEMARK_*`, headers `X-Tidemark-Key`, `X-Tidemark-CSRF`, `X-Tidemark-Signature` | yes | |
| Key prefixes `tmk_` (connection), `tmt_` (API token) | yes | Use them to detect what a secret is |
| Ids of users, teams, connections, dashboards, alerts, channels, signals, tokens | yes | UUIDv7 strings; never reuse across installations |
| Stream names | yes | `^[a-z0-9][a-z0-9._-]{0,63}$`; streams are addressed by name, also in team grants and connection `allowedStreams` |
| Dashboard and rule names | no | Not unique; look them up and keep them unique by convention |
| Connection and team names | yes, unique (case-insensitive) | Good lookup keys |
| `version` fields | change on every update | Concurrency token, not an identity |
| Audit actions (`connection.rotate`, `purge.request`, `alert.update`, `team.members`, `user.teams` …) | yes | Filter `GET /audit?action=` |
| Metric names `tidemark_*` | yes | See [Metrics](metrics.md#exported-metric-families) |
| TQL built-ins (`level`, `service`, `stream`, `connection_id`, …) | yes | [Query language](../query-language.md#fields) |
| ClickHouse® database `tidemark`, users `tidemark_writer`/`tidemark_reader` | yes | Never write to ClickHouse directly |

## Idempotent recipes

Natural keys: connection `name`, team `name`, stream `name`, dashboard `name` (by convention),
rule `name` (by convention), channel `name` (by convention), signal `name` + owner.

```bash
H=(-H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json')

# Ensure a stream exists with a given retention (POST, on 409 PATCH)
ensure_stream() {  # name days
  code=$(curl -sS -o /dev/null -w '%{http_code}' "${H[@]}" "$TM/api/v1/streams" \
         -d "{\"name\":\"$1\",\"retentionDays\":$2}")
  case $code in
    201) echo "created $1";;
    409) curl -sS --fail-with-body -X PATCH "${H[@]}" "$TM/api/v1/streams/$1" -d "{\"retentionDays\":$2}" >/dev/null && echo "updated $1";;
    *)   echo "error $code" >&2; return 1;;
  esac
}

# Ensure a connection exists (never rotates an existing key)
ensure_connection() {  # name json-body
  id=$(curl -sS "${H[@]}" "$TM/api/v1/connections" | jq -r --arg n "$1" '.[] | select(.name|ascii_downcase == ($n|ascii_downcase)) | .id')
  if [ -n "$id" ]; then
    curl -sS --fail-with-body -X PATCH "${H[@]}" "$TM/api/v1/connections/$id" -d "$2" >/dev/null && echo "$id"
  else
    curl -sS --fail-with-body "${H[@]}" "$TM/api/v1/connections" -d "$2" | tee >(jq -r .key > "$1.key") | jq -r .id
  fi
}

# Ensure a team exists with exactly these streams (members are managed separately)
ensure_team() {  # name stream-json-array, e.g. '["payments","payments-prod"]'
  id=$(curl -sS "${H[@]}" "$TM/api/v1/teams" | jq -r --arg n "$1" '.[] | select(.name|ascii_downcase == ($n|ascii_downcase)) | .id')
  [ -n "$id" ] || id=$(curl -sS --fail-with-body "${H[@]}" "$TM/api/v1/teams" -d "{\"name\":\"$1\"}" | jq -r .id)
  curl -sS --fail-with-body -X PUT "${H[@]}" "$TM/api/v1/teams/$id/streams" -d "{\"streams\":$2}" >/dev/null && echo "$id"
}

# Ensure an alert rule by name (create, or PUT with the current version)
ensure_rule() {  # file with {name, enabled, doc, streams?}
  name=$(jq -r .name "$1")
  cur=$(curl -sS "${H[@]}" "$TM/api/v1/alerts" | jq -c --arg n "$name" '[.[] | select(.name == $n)][0] // empty')
  if [ -z "$cur" ]; then
    curl -sS --fail-with-body "${H[@]}" "$TM/api/v1/alerts" --data-binary @"$1" | jq -r .id
  else
    id=$(echo "$cur" | jq -r .id); v=$(echo "$cur" | jq .version)
    jq --argjson v "$v" '. + {version: $v}' "$1" |
      curl -sS --fail-with-body -X PUT "${H[@]}" "$TM/api/v1/alerts/$id" --data-binary @- | jq -r .id
  fi
}
```

These helpers were assembled from calls that were each verified individually (`POST`/`PATCH`
streams with `409` on duplicates, `POST`/`PATCH` connections, `POST`/`PUT` alerts with
`version`, the team routes); the functions themselves were not run end to end.

Dashboards: [Dashboards › Dashboards as code](dashboards.md#as-code). Comparisons should use the
canonical forms the server returns (durations like `5m0s`, levels as names, sorted `streams` and
`allowedStreams`, masked secrets `"••••"`); sending `"••••"` back in a channel `PUT` keeps the
stored secret.

## Destructive operations — require confirmation

Ask a human before running any of these, show what will change, and prefer the safe variant:

| Operation | Effect | Safe variant / precondition |
|---|---|---|
| `DELETE /streams/{name}` | Deletes all data of the stream and revokes every team grant for it | Show `GET /streams` stats for it; requires `{"confirm":"<name>"}` |
| `POST /purge` with `dryRun:false` | Deletes rows | Run with `dryRun:true` first and report `rowsEstimate` |
| `PATCH /streams/{name}` or `PATCH /settings` lowering retention | Older data is deleted by TTL | Show the old and new days |
| `PATCH /settings` `{"access":{"denyByDefault":true}}` | Every user in no team loses read access with their next request | `GET /teams/access` first; `usersWithoutTeam` must be 0 |
| `PUT /teams/{id}/streams`, `PUT /teams/{id}/members`, `PUT /users/{id}/teams`, `DELETE /teams/{id}` | Replace the whole set; members can lose streams, restricted rules may become invisible to their authors | `GET` the team first and show the diff of `streams`/`members` |
| `DELETE /indexed-fields/{id}` | Drops column and index | Usually harmless, but queries get slower |
| `POST /connections/{id}/revoke`, `DELETE /connections/{id}`, `rotate` with `graceMinutes:0` | Clients stop ingesting immediately | Rotate with a grace period; check `lastUsedAt` |
| `PATCH /connections/{id}` narrowing `allowedStreams` | Data naming other streams silently lands in the default stream | Watch `tidemark_ingest_rerouted_total` afterwards |
| `DELETE /users/{id}`, `PATCH … {"disabled":true}` or `{"password":…}`, role changes | Access loss, tokens stop working (role changes only narrow them) | Prefer disable over delete |
| `DELETE /tokens/{id}` | Automation using it fails | Check `lastUsedAt` |
| `DELETE /dashboards/{id}`, `DELETE /alerts/{id}`, `DELETE /channels/{id}`, `DELETE /signals/{id}` | Configuration loss | `GET …/export` or `GET` the object first and keep it |
| `POST /alerts/{id}/mute` | Silences paging | Always set an `until`; never mute indefinitely |
| `tidemark restore --force` | Replaces the metadata database | Server stopped; a pre-restore copy is made automatically |
| `tidemark admin reset-password` | Locks out the current password holder | Confirm the account |
| Upgrades, `tidemark migrate` | Schema changes | `tidemark backup` and `migrate --dry-run` first |
| `docker compose down -v` | **Deletes all data volumes** | Never run; use `docker compose down` |

Read-only and always safe: every `GET`, `POST /query`, `POST /query/validate`,
`POST /dashboards/{id}/panels/{panelId}/query`, `POST /alerts/{id}/test` (no state change, no
notification), `POST /purge` with `dryRun:true`, `tidemark doctor`, `tidemark migrate --status`,
`tidemark migrate --dry-run`.

Low-risk writes that still change shared state: `POST /channels/{id}/test` sends a real test
notification to the channel's target; `GET /export` is audited.

## Diagnosis checklist for agents

```bash
curl -sS "$TM/readyz"                                                            # reachability
curl -sS "$TM/api/v1/auth/me" -H "Authorization: Bearer $TM_TOKEN" | jq '{role, streamAccess, allStreams, streams}'
curl -sS "$TM/api/v1/system" -H "Authorization: Bearer $TM_TOKEN" | jq '{version, queueDepth, counters}'   # admin
curl -sS "$TM/api/v1/streams" -H "Authorization: Bearer $TM_TOKEN" | jq '.streams[] | {name, paused, rows: .stats.logs.rows}'   # visible streams only
curl -sS "$TM/api/v1/alerts" -H "Authorization: Bearer $TM_TOKEN" | jq '.[] | select(.state != "ok") | {name, state, lastError, streams}'
```

Then follow the [incident runbook](operations.md#runbook).
