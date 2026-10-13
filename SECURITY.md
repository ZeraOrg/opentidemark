<!-- SPDX-License-Identifier: Apache-2.0 -->
# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's **Report a vulnerability** form on
<https://github.com/zeraOrg/tidemark/security/advisories/new> (Security → Advisories in the
public repository). Do not open a public issue or pull request for a vulnerability.

We aim to acknowledge reports within 72 hours. Fixes are released through the public repository;
the advisory credits the reporter unless they ask otherwise.

## Hardening summary

- **Every ingestion connection needs its own API key.** You can restrict each key by protocol,
  source IP range (CIDR), expiry and rate. Keys can be revoked or rotated.
- Passwords are hashed with argon2id. Logins are throttled and accounts lock out after repeated
  failures. Sessions use HttpOnly + SameSite=Strict cookies, and requests are protected against
  CSRF.
- A strict Content-Security-Policy and other security headers are applied. There is no CORS.
- All SQL is parameterized. Queries run as a read-only ClickHouse user with time and memory
  limits.
- Request bodies are size-limited, including protection against decompression bombs.
- Alert webhooks are protected against SSRF.
- Secrets are encrypted at rest. An audit log records security-relevant actions.
- Containers run as non-root with a read-only filesystem. ClickHouse is never exposed publicly by
  default.

## ⚠️ Default credentials

A fresh install creates the user **`admin`** with password **`admin`**. **Change it immediately**
after your first login (Settings → Users), or set `TIDEMARK_ADMIN_PASSWORD` before the first start.
Until you change it, Tidemark shows a warning banner and logs a warning on every start.
