#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Reviewed Go licence exceptions (SPEC §19.1), listed in scripts/licence-exceptions.txt.
#
#   scripts/licence-exceptions.sh modules   # print the module paths, one per line
#   scripts/licence-exceptions.sh verify    # fail unless each LICENSE has the reviewed SHA-256
#   scripts/licence-exceptions.sh rows      # print THIRD_PARTY_NOTICES.md table rows
#
# A module that is no longer a dependency also fails `verify`, so stale exceptions get removed.
set -euo pipefail

readonly LIST="$(dirname "$0")/licence-exceptions.txt"
readonly GO="${GO:-go}"

if [[ $# -ne 1 ]]; then
  echo "usage: $0 modules|verify|rows" >&2
  exit 2
fi
mode="$1"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# module_info prints "<version> <dir>" of a module in the build list, downloading it if needed.
module_info() {
  "$GO" mod download "$1"
  "$GO" list -m -f '{{.Version}} {{.Dir}}' "$1"
}

status=0
while read -r module spdx want; do
  if [[ -z "$module" || "$module" == \#* ]]; then
    continue
  fi
  case "$mode" in
    modules)
      echo "$module"
      ;;
    verify)
      info="$(module_info "$module")"
      got="$(sha256 "${info#* }/LICENSE")"
      if [[ "$got" != "$want" ]]; then
        echo "licence exception $module ($spdx): LICENSE SHA-256 is $got, reviewed $want" >&2
        status=1
      fi
      ;;
    rows)
      info="$(module_info "$module")"
      echo "| $module | ${info%% *} | $spdx (reviewed exception) |"
      ;;
    *)
      echo "unknown mode: $mode" >&2
      exit 2
      ;;
  esac
done < "$LIST"
exit "$status"
