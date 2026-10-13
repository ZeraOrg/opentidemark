#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Prints the release notes of one version (SPEC §17.6): the body of the "## [<version>] - <date>"
# section of CHANGELOG.md, without leading and trailing blank lines. Fails when the section is
# missing or empty, so a release cannot go out without user-facing notes.
#
#   scripts/release-notes.sh <version> [<changelog>]
set -euo pipefail

version="${1:-}"
changelog="${2:-CHANGELOG.md}"
[[ -n "$version" ]] || { echo "usage: release-notes.sh <version> [<changelog>]" >&2; exit 2; }
[[ -f "$changelog" ]] || { echo "release-notes: $changelog not found" >&2; exit 1; }

notes="$(awk -v head="## [$version] - " '
  index($0, head) == 1 { found = 1; inside = 1; next }
  inside && (/^## / || /^\[[^]]+\]: /) { inside = 0 }
  inside { body = body $0 "\n" }
  END {
    if (!found) exit 3
    sub(/^\n+/, "", body); sub(/\n+$/, "", body)
    printf "%s", body
  }
' "$changelog")" || {
  echo "release-notes: $changelog has no section \"## [$version] - <YYYY-MM-DD>\"; add it before releasing $version" >&2
  exit 1
}
if [[ -z "${notes//[[:space:]]/}" ]]; then
  echo "release-notes: the $version section of $changelog is empty" >&2
  exit 1
fi
printf '%s\n' "$notes"
