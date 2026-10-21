<!-- SPDX-License-Identifier: Apache-2.0 -->
# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's **Report a vulnerability** form on
<https://github.com/ZeraOrg/opentidemark/security/advisories/new> (Security → Advisories in the
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

## Verifying releases

Every release (after 0.4.0) signs its `checksums.txt` with [minisign](https://jedisct1.github.io/minisign/);
the signature is the release asset `checksums.txt.minisig`. Its trusted comment is
`opentidemark v<version> checksums.txt`, which binds the signature to that one release.
OpenTidemark trusts these public keys, and builds them into the binary:

| Key | Key id | Public key |
|---|---|---|
| CI key (signs every release) | `E3D864A9712D58F3` | `RWTzWC1xqWTY457VdX92WvxpPeXXOUhpiGzch3af0NEoe9ENK8bEbiDE` |
| Offline backup key (replaces the CI key if it ever has to be rotated) | `A9028169BE5C889E` | `RWSeiFy+aYECqVbvFDNIW//gdsHLox72jDPcY3swGgJm/WQTA+0s9nwP` |

The CI key is held only by the release workflow, in a protected environment that runs on `main`
alone; the backup key never leaves offline storage. A signature by either key is valid. To verify a
download, in the directory with the release files:

```bash
minisign -Vm checksums.txt -P RWTzWC1xqWTY457VdX92WvxpPeXXOUhpiGzch3af0NEoe9ENK8bEbiDE
sha256sum -c checksums.txt --ignore-missing
gh attestation verify <file> --repo ZeraOrg/opentidemark   # optional: build provenance
```

minisign must print `Trusted comment: opentidemark v<version> checksums.txt` for the version you
downloaded. On Windows, compare `Get-FileHash <file> -Algorithm SHA256` with `checksums.txt` after
the `minisign` check. If the CI key is ever replaced, the release notes say so, the next release is
signed with the backup key and ships a new embedded key; such a change is never made silently.

## ⚠️ Default credentials

A fresh install creates the user **`admin`** with password **`admin`**. **Change it immediately**
after your first login (Settings → Users), or set `TIDEMARK_ADMIN_PASSWORD` before the first start.
Until you change it, OpenTidemark shows a warning banner and logs a warning on every start.
