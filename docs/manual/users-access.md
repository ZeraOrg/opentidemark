<!-- SPDX-License-Identifier: Apache-2.0 -->
# Users and access

Manage who can use OpenTidemark and what they can do: user accounts and the viewer, editor and
admin roles, API tokens for automation, and teams that decide which streams' data each user can
see.

## Model

- **Roles decide what a user may do.** Every user has exactly one global role.
- **Teams decide which streams' data a user may see.** A team has members and is granted stream
  names; a user's *stream scope* is the union of the grants of their teams (see
  [Teams and stream access](#teams)).
- **API tokens** act for their owner with at most the role of their scope, and see the same
  streams as their owner.
- **Connection keys** are not users: they can only ingest, never read. A connection can be limited
  to the streams it may write to ([Ingestion](ingestion.md#connections)).

The number of user accounts is not limited.

## Roles {#roles}

| Capability | viewer | editor | admin |
|---|:---:|:---:|:---:|
| Search, histogram, facets, fields, event detail, live tail, traces, metrics browser | ✓ | ✓ | ✓ |
| Read dashboards, alert rules and history, channels (secrets masked), streams and mappings, indexed fields | ✓ | ✓ | ✓ |
| Own account, own API tokens | ✓ | ✓ | ✓ |
| Create/change/delete signals, dashboards, alert rules; test and mute rules | | ✓ | ✓ |
| Export (`GET /export`, up to 100,000 events, audited) | | ✓ | ✓ |
| Users, teams, connections and keys, snippets | | | ✓ |
| Streams (create, retention, pause, delete), indexed fields, purge | | | ✓ |
| Channels (create, change, delete, test) | | | ✓ |
| Settings (retention, deny by default), audit log, system, imports | | | ✓ |

Ownership rules on top of roles:

- Signals and dashboards are private to their owner unless `shared`. Only the owner (or an admin
  for shared items) may change or delete them; others get `403` (or `404` when they cannot see the
  item).
- The last admin cannot be deleted or demoted.
- Admins see every stream regardless of teams; every other role sees only its stream scope.

The full route × role table is in [API for automation](api.md#endpoints).

## Users

UI: **Settings › Users**. Admins see the users table with a *Teams* column and assign teams in the
edit dialog; every role sees the **Your account** card there, which states which streams the
caller sees (every stream as admin, the streams of their teams, every stream while in no team, or
none) and offers the password change.

```bash
# Create a user (admin)
curl -sS "$TM/api/v1/users" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"username":"jdoe","displayName":"Jane Doe","role":"editor","password":"<INITIAL_PASSWORD>"}'

# Change role, disable, or set a new password (admin); omitted fields stay unchanged
curl -sS -X PATCH "$TM/api/v1/users/<USER_ID>" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"role":"viewer"}'
curl -sS -X PATCH "$TM/api/v1/users/<USER_ID>" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"disabled":true}'

# Replace the user's teams (admin); unknown team ⇒ 400 details.field = teamIds
curl -sS -X PUT "$TM/api/v1/users/<USER_ID>/teams" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"teamIds":["<TEAM_ID>"]}'

# Delete (admin)
curl -sS -X DELETE "$TM/api/v1/users/<USER_ID>" -H "Authorization: Bearer $TM_TOKEN"
```

User objects of the `/users` routes carry `teams: [{id, name}]`.

- Passwords follow the policy: 12–256 characters, not the user name, not a common password.
- Disabling a user revokes all their sessions and stops their API tokens from authenticating.
- A password change revokes the user's other sessions.
- A password reset by an admin revokes all the user's sessions and API tokens.
- Prefer **disable** over delete for leavers: the audit trail keeps resolving the user id.
  Deleting a user removes their team memberships (audited as `user.delete` with the teams).
  Their dashboards and signals, private ones included, pass to the admin who deletes the user.

Changing your own password:

```bash
curl -sS "$TM/api/v1/auth/password" -b cookies.txt -H "X-Tidemark-CSRF: <tm_csrf cookie value>" \
  -H 'Content-Type: application/json' -d '{"currentPassword":"<OLD>","newPassword":"<NEW>"}'
```

`GET /api/v1/auth/me` returns, for the caller:

| Field | Meaning |
|---|---|
| `id`, `username`, `displayName`, `role` | The identity a session or token resolves to |
| `mustChangePassword`, `defaultCredentials` | Password hygiene flags (the UI shows a banner for both) |
| `allStreams` | `true` when the caller sees every stream |
| `streams` | The visible stream names when not all, else `[]` |
| `teams` | `[{id, name}]` of the caller's teams, by name |
| `streamAccess` | How the scope was computed: `admin`, `teams`, `default-all` or `default-none` |

Use it in scripts to check which identity a token resolves to and what it can see.

## API tokens {#api-tokens}

API tokens are the only supported way to automate the API. They avoid CSRF handling, carry no
session timeout, and can be scoped and expired.

| Field | Meaning |
|---|---|
| `name` | 1–128 characters, shown in the token list and audit log |
| `scope` | `read` (viewer), `write` (editor) or `admin` (admin) |
| `expiresAt` | Optional RFC 3339 time in the future |

Effective role = the lower of the scope's role and the owner's **current** role. An editor's
`admin` token acts as editor. A token sees exactly the streams its owner sees; the scope limits
what it may do, not what it may read.

UI: **Settings › API tokens › New API token**. The token is shown once; copy it into your secret
store.

The API creates tokens only for the **caller**, so the first token must be created from a
signed-in session (UI, or the cookie flow below). After that, tokens can create further tokens.

```bash
# 1. Sign in once and keep the cookies (session + CSRF)
curl -sS -c cookies.txt "$TM/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d '{"username":"automation","password":"<PASSWORD>"}' >/dev/null
CSRF=$(awk '$6=="tm_csrf"{print $7}' cookies.txt)

# 2. Create a token (cookie requests that change state need X-Tidemark-CSRF)
curl -sS -b cookies.txt "$TM/api/v1/tokens" -H "X-Tidemark-CSRF: $CSRF" \
  -H 'Content-Type: application/json' \
  -d '{"name":"ci-dashboards","scope":"write","expiresAt":"2027-01-31T00:00:00Z"}' | jq -r .token

# 3. Sign out
curl -sS -b cookies.txt -X POST "$TM/api/v1/auth/logout" -H "X-Tidemark-CSRF: $CSRF"
```

Manage tokens:

```bash
curl -sS "$TM/api/v1/tokens" -H "Authorization: Bearer $TM_TOKEN"            # own tokens (no secrets)
curl -sS -X DELETE "$TM/api/v1/tokens/<TOKEN_ID>" -H "Authorization: Bearer $TM_TOKEN"
```

Practices:

- A dedicated user per automation (`ci-dashboards`, `alert-sync`) with the role it needs, so the
  audit log names the system, not a person, and leaving employees do not break pipelines. Put that
  user into the teams whose streams the automation must read.
- `read` tokens for exporters and reporting, `write` for dashboards/alerts as code, `admin` only
  for provisioning (connections, streams, users, teams).
- Expire tokens and rotate them by creating the replacement first, switching consumers, then
  deleting the old one.
- At most 100 tokens per user.

## Teams and stream access {#teams}

Teams restrict **which streams' data** a user can read. They do not change what a user may do;
that remains the role.

### How access is computed

For every request the server computes the caller's **stream scope** from the metadata (no cache,
so changes apply to the next request on every node):

| Caller | Scope | `streamAccess` |
|---|---|---|
| User whose own role is admin (also through an API token of an admin, whatever the token scope) | All streams | `admin` |
| Member of one or more teams | Union of the streams granted to those teams (a team without grants adds nothing: a member of only such teams sees no stream) | `teams` |
| Member of no team | All streams | `default-all` |
| Member of no team while the setting `access.denyByDefault` is `true` | No stream | `default-none` |
| API token | Same as its owner; the role is capped by the token scope | as the owner |

The scope is enforced on the server for every data read: queries (including every dashboard
panel), histogram, facets, field lists, autocomplete, event by id (`404` when not visible), event
context, export (an empty scope yields an empty file), live tail (re-evaluated on every poll),
trace search and trace view (only visible spans and events; `404` when none), metric names, the
stream list and stream statistics (visible streams only), and field mappings (`404` outside the
scope). An empty scope returns empty results, never an error. A failing scope lookup fails the
request; it never falls back to all streams.

The **scope picker** in the top bar of Events, Traces, Metrics and dashboards narrows the view
further to some of the visible streams and, for logs, to environments; it can never widen access.
A user who sees no stream gets the empty state *You don't have access to any streams yet* instead
of those views, and no query is sent. The API equivalents are the `streams` and `environments`
(`env`) selection parameters ([API for automation](api.md#search-and-analytics)).

Not covered by the scope (accepted exposures): the names of indexed fields (`GET /indexed-fields`)
and the text of shared signals.

**Environment is not a security boundary.** The environment selection filters logs by the
`environment` field only. To isolate production data, send it to its own stream and grant that
stream only to the teams that need it.

### Managing teams

UI: **Settings › Teams** (admin). The *Access* card holds the **Deny by default** switch, spells
out its consequence and shows the number of users in no team (turning it on while such users
exist asks for confirmation, because they lose access at once). The teams table lists name,
description, members, streams (*none · members see nothing* for a team without grants) and the
last update. *New team* asks for the name (1–64 characters) and a description. A team's page has
three cards that save on their own and show *Unsaved changes* / *Saved*: **Details** (rename,
description, *Delete team* with typed confirmation), **Members** (searchable multi-select of
users) and **Streams** (the known streams plus typed names, which are validated and marked *no
data yet* when no stream of that name exists).

API (admin):

```bash
# Create a team
curl -sS "$TM/api/v1/teams" -H "Authorization: Bearer $TM_TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Payments","description":"Payments squad"}' | tee team.json | jq .id
TEAM=$(jq -r .id team.json)

# Replace its members and its streams (PUT replaces the whole set; duplicates are ignored)
curl -sS -X PUT "$TM/api/v1/teams/$TEAM/members" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"userIds":["<USER_ID>","<USER_ID>"]}'
curl -sS -X PUT "$TM/api/v1/teams/$TEAM/streams" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"streams":["payments","payments-prod"]}'

# Rename or describe
curl -sS -X PATCH "$TM/api/v1/teams/$TEAM" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"description":"Payments squad and on-call"}'

# Who is still outside every team, and is deny by default on?
curl -sS "$TM/api/v1/teams/access" -H "Authorization: Bearer $TM_TOKEN"
# ⇒ {"denyByDefault":false,"usersWithoutTeam":3}

# Deny access to users without a team (the body must be exactly this object)
curl -sS -X PATCH "$TM/api/v1/settings" -H "Authorization: Bearer $TM_TOKEN" \
  -H 'Content-Type: application/json' -d '{"access":{"denyByDefault":true}}'

# Delete a team (memberships and grants go with it)
curl -sS -X DELETE "$TM/api/v1/teams/$TEAM" -H "Authorization: Bearer $TM_TOKEN"    # ⇒ 204
```

Responses: `GET /teams` lists `[{id, name, description, memberCount, streams, createdAt,
updatedAt}]` by name; `POST`, `GET /teams/{id}`, `PATCH` and the two `PUT` routes return the full
team `{id, name, description, members: [{id, username, displayName, role}], streams, createdAt,
updatedAt}`. Errors: an unknown user ⇒ `400` with `details.field = userIds`; a duplicate name
(case-insensitive) ⇒ `409 conflict`.

Limits: team names 1–64 characters (trimmed), unique ignoring case; description ≤ 1024
characters; at most 500 teams, 10,000 members and 1,000 streams per team; stream names must be
valid (`^[a-z0-9][a-z0-9._-]{0,63}$`) but need not exist yet, so grants can precede the data.

### Alert rules, connections and teams

- Alert rules carry a `streams` list outside the rule document (`null` = every stream). A caller
  who sees every stream may leave it `null`; everyone else must choose a non-empty subset of their
  own streams (the editor's *Streams* field, default: the editor's whole scope). The evaluator
  reads the rule's stored streams, so a rule keeps evaluating after its author loses access.
  A rule is visible, testable, mutable and deletable only to callers whose scope covers its
  streams; otherwise it is `404`. Rules created before teams existed have `null`
  ([Alerting](alerting.md#rule-anatomy)).
- Connections can be limited on the **write** side with `allowedStreams`, so a team's ingest key
  cannot write into another team's streams ([Ingestion](ingestion.md#connections)).

### Lifecycle, audit and upgrade notes

- Deleting a user removes their memberships; deleting a team removes its grants; deleting a
  stream revokes the grants for its name (audited as `stream.delete` with `revokedFromTeams`).
  Data that re-creates the stream later does not bring the grants back.
- Teams are part of the metadata export, so `tidemark backup` and `restore` carry them.
- Existing installations upgrade with no teams, so every non-admin keeps seeing all streams until
  an admin creates teams or turns on deny by default.
- Running a binary from before teams (metadata migration 0007) against the upgraded database
  ignores the tables: every user sees every stream again.
- Audit actions: `team.create` `{name}`, `team.update` `{name, previousName?}`, `team.delete`
  `{name, members, streams}`, `team.members` and `team.streams` `{added, removed}`, `user.teams`
  `{added, removed}`, `user.delete` with the user's `teams`, `transferredTo`, `dashboards` and `signals` (counts), `settings.update` with
  `access.denyByDefault`, `alert.create|update` with `streams`, `connection.create|update` with
  `allowedStreams`.

### Rollout procedure

1. Map streams to owners. Move sensitive data into dedicated streams first (connection
   `defaultStream`, `allowedStreams` or a pinned `@stream`; see
   [Ingestion › Routing to streams](ingestion.md#routing-to-streams)).
2. Create the teams and grant streams. Grants may name streams that have no data yet.
3. Add every non-admin user to at least one team; check that `GET /teams/access` (or the
   *Access* card) reports zero users without a team.
4. Turn on **Deny by default**.
5. Review alert rules created before the rollout: they have `streams: null` (all streams), which
   only callers who see every stream can keep when they save the rule.
6. Give automation users the team memberships their tokens need.
