#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Runs a CI step and, when it fails, publishes the tail of its output as GitHub annotations
# (SPEC §17.4), so failures can be read from check-run annotations without access to the logs.
#
#   scripts/ci-annotate.sh "<title>" -- <command> [args...]
#
# The output is printed as usual. GitHub keeps at most 4096 characters per annotation and ten
# annotations per level and step, so the last MAX_PARTS chunks of at most CHUNK_BYTES each are
# published, split on line boundaries where possible. They use the warning level because problem
# matchers (setup-go registers one for `file.go:line:` output) can fill the ten error slots first;
# the runner still reports the step's failure as an error. The exit status is the command's.
set -uo pipefail

readonly CHUNK_BYTES=4000
readonly MAX_PARTS=9

if [[ $# -lt 3 || "$2" != "--" ]]; then
  echo "usage: $0 <title> -- <command> [args...]" >&2
  exit 2
fi
title="$1"
shift 2

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

"$@" 2>&1 | tee "$work/output"
status="${PIPESTATUS[0]}"
if [[ "$status" -eq 0 ]]; then
  exit 0
fi

# escape encodes workflow-command data: % first, then CR and LF.
escape() {
  sed -e 's/%/%25/g' -e 's/\r/%0D/g' "$1" | awk 'BEGIN { ORS = "%0A" } { print }'
}

# Title properties additionally escape ':' and ','.
safe_title="$(printf '%s' "$title" | sed -e 's/%/%25/g' -e 's/:/%3A/g' -e 's/,/%2C/g')"

sed -e $'s/\x1b\\[[0-9;]*m//g' "$work/output" | tail -c $((CHUNK_BYTES * MAX_PARTS)) > "$work/tail"
if [[ ! -s "$work/tail" ]]; then
  echo "(no output)" > "$work/tail"
fi
mkdir "$work/parts"
split -C "$CHUNK_BYTES" -d -a 3 "$work/tail" "$work/parts/p"
all=("$work"/parts/p*)
skip=$((${#all[@]} > MAX_PARTS ? ${#all[@]} - MAX_PARTS : 0))
parts=("${all[@]:skip}")
total="${#parts[@]}"
for i in "${!parts[@]}"; do
  printf '::warning title=%s failed (exit %s) %d/%d::%s\n' "$safe_title" "$status" $((i + 1)) "$total" "$(escape "${parts[$i]}")"
done
exit "$status"
