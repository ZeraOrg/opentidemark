#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Go toolchain security fixes (SPEC §15.9, §17.4). Dependabot does not update the Go release, so
# vulnerabilities in the standard library are fixed here.
#
#   scripts/go-toolchain-fix.sh run
#       Runs govulncheck (must be on PATH). When it reports standard-library or toolchain
#       vulnerabilities and a newer patch release of the pinned Go minor version fixes all of
#       them (and its golang:<version>-bookworm image is published), it applies that release
#       (see apply) on branch security/go-toolchain-go<version>, pushes it and opens a pull
#       request labelled security-autofix, which the dependabot-copilot workflow merges once ci
#       is green. Needs GITHUB_REPOSITORY, a checkout whose credentials may push and
#       COPILOT_TOKEN (a user token, so the push and the pull request start ci).
#
#   scripts/go-toolchain-fix.sh apply <version> <advisories>
#       Moves go.mod `toolchain`, the Dockerfile golang image and SPEC.md (§5, §17.2, a new spec
#       version and changelog row) to Go <version> (e.g. 1.27.5) and adds a CHANGELOG.md entry.
set -euo pipefail

readonly label="security-autofix"
readonly branch_prefix="security/go-toolchain-go"

current_version() { sed -n 's/^toolchain go//p' go.mod; }

version_ge() { # <a> <b>: a >= b (dotted versions)
	[[ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n1)" == "$1" ]]
}

apply() {
	local new="$1" advisories="$2" cur today
	cur="$(current_version)"
	[[ -n "$cur" ]] || { echo "go.mod has no toolchain directive" >&2; exit 1; }
	today="$(date -u +%F)"
	sed -i "s/^toolchain go.*/toolchain go$new/" go.mod
	sed -i -E "s/golang:[0-9.]+-bookworm/golang:$new-bookworm/" Dockerfile
	CUR="$cur" NEW="$new" TODAY="$today" ADVISORIES="$advisories" python3 - <<'EOF'
import os, re
cur, new, today, adv = (os.environ[k] for k in ("CUR", "NEW", "TODAY", "ADVISORIES"))

with open("SPEC.md", encoding="utf-8") as f:
    spec = f.read()
head, sep, log = spec.partition("\n## 21. Changelog")
if not sep:
    raise SystemExit("SPEC.md: changelog section not found")
head = head.replace(f"toolchain go{cur}", f"toolchain go{new}").replace(
    f"golang:{cur}-bookworm", f"golang:{new}-bookworm")
m = re.search(r"\*\*Spec version:\*\* (\d+)\.(\d+)\.(\d+) · \*\*Last updated:\*\* [0-9-]+", head)
if not m:
    raise SystemExit("SPEC.md: spec version line not found")
version = f"{m[1]}.{m[2]}.{int(m[3]) + 1}"
head = head[:m.start()] + f"**Spec version:** {version} · **Last updated:** {today}" + head[m.end():]
row = (f"| {version} | {today} | Go toolchain go{new} (Dockerfile `golang:{new}-bookworm`, §5, §17) "
       f"for the standard-library advisories {adv} reported by govulncheck; applied automatically "
       f"by `scripts/go-toolchain-fix.sh` before the 7-day rule (§15.9) because it fixes a "
       f"vulnerability and the fix is only available by upgrading. |\n")
first = re.search(r"^\| \d+\.\d+\.\d+ \|", log, re.M)
if not first:
    raise SystemExit("SPEC.md: no changelog rows")
log = log[:first.start()] + row + log[first.start():]
with open("SPEC.md", "w", encoding="utf-8") as f:
    f.write(head + sep + log)

with open("CHANGELOG.md", encoding="utf-8") as f:
    changelog = f.read()
entry = f"- Built with Go {new}, which fixes standard-library vulnerabilities ({adv}).\n"
m = re.search(r"^## \[Unreleased\]\n", changelog, re.M)
if not m:
    raise SystemExit("CHANGELOG.md: no [Unreleased] section")
end = re.search(r"^## \[", changelog[m.end():], re.M)
section_end = m.end() + (end.start() if end else len(changelog) - m.end())
section = changelog[m.end():section_end]
s = re.search(r"^### Security\n\n", section, re.M)
if s:
    section = section[:s.end()] + entry + section[s.end():]
else:
    section = "\n### Security\n\n" + entry + section
changelog = changelog[:m.end()] + section + changelog[section_end:]
with open("CHANGELOG.md", "w", encoding="utf-8") as f:
    f.write(changelog)
EOF
}

run() {
	: "${GITHUB_REPOSITORY:?}" "${COPILOT_TOKEN:?secret COPILOT_ASSIGN_TOKEN is not set}"
	local repo="$GITHUB_REPOSITORY" cur minor findings needed advisories latest branch pr
	cur="$(current_version)"
	minor="${cur%.*}"
	findings="$(govulncheck -format json ./... | jq -rs '
		[.[] | .finding? // empty | select(.trace[0].module == "stdlib" or .trace[0].module == "toolchain")
		 | {osv, fixed: (.fixed_version // "" | ltrimstr("v"))}] | unique | .[] | "\(.osv)\t\(.fixed)"')"
	if [[ -z "$findings" ]]; then
		echo "go$cur: no standard-library vulnerabilities"
		return
	fi
	advisories="$(cut -f1 <<<"$findings" | sort -u | paste -sd, - | sed 's/,/, /g')"
	if cut -f2 <<<"$findings" | grep -qx ''; then
		echo "::warning title=go-toolchain-fix::no fixed Go release yet for $advisories"
		return
	fi
	needed="$(cut -f2 <<<"$findings" | sort -V | tail -n1)"
	latest="$(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | jq -r --arg p "go$minor." '
		[.[] | select(.stable and (.version | startswith($p))) | .version | ltrimstr("go")] | .[]' |
		sort -V | tail -n1)"
	if [[ -z "$latest" ]] || ! version_ge "$latest" "$needed"; then
		echo "::error title=go-toolchain-fix::$advisories need Go $needed, which is not a patch release of go$minor; move the Go release deliberately (SPEC §5)"
		exit 1
	fi
	if ! curl -fsS -o /dev/null "https://hub.docker.com/v2/repositories/library/golang/tags/$latest-bookworm"; then
		echo "Go $latest fixes $advisories, but golang:$latest-bookworm is not published yet; next run"
		return
	fi
	branch="$branch_prefix$latest"
	if git ls-remote --exit-code --heads origin "$branch" >/dev/null; then
		echo "$branch already exists"
		return
	fi
	apply "$latest" "$advisories"
	git checkout --quiet -b "$branch"
	git add go.mod Dockerfile SPEC.md CHANGELOG.md
	git -c user.name="github-actions[bot]" \
		-c user.email="41898282+github-actions[bot]@users.noreply.github.com" \
		commit --quiet -m "Go toolchain go$latest for $advisories" \
		-m "Applied by scripts/go-toolchain-fix.sh (SPEC §15.9, §17.4): govulncheck reports standard-library vulnerabilities in go$cur that go$latest fixes."
	git push --quiet origin "HEAD:refs/heads/$branch"
	export GH_TOKEN="$COPILOT_TOKEN"
	gh api "repos/$repo/labels/$label" >/dev/null 2>&1 ||
		gh api -X POST "repos/$repo/labels" -f name="$label" -f color=b60205 \
			-f description="Security fix opened automatically; merged when ci is green" >/dev/null
	pr="$(gh api -X POST "repos/$repo/pulls" -f title="Go toolchain go$latest for $advisories" \
		-f head="$branch" -f base=main --jq .number \
		-f body="govulncheck reports standard-library vulnerabilities ($advisories) in go$cur; go$latest fixes them. Opened by \`scripts/go-toolchain-fix.sh\` and merged automatically by the dependabot-copilot workflow once ci is green.")"
	gh api -X POST "repos/$repo/issues/$pr/labels" -f "labels[]=$label" >/dev/null
	echo "#$pr: go$cur -> go$latest ($advisories)"
}

case "${1:-}" in
run) run ;;
apply) apply "${2:?version}" "${3:?advisories}" ;;
*)
	echo "usage: $0 run | apply <version> <advisories>" >&2
	exit 2
	;;
esac
