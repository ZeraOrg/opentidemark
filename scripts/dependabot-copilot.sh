#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Dependabot follow-up for the dependabot-copilot workflow (SPEC §17.4).
#
#   scripts/dependabot-copilot.sh follow-up < <unified diff>
#       Prints one line per reason why a dependency update needs more than its own version
#       changes (and so a spec update or a code change), nothing for a routine update. Routine:
#       only go.mod require lines, go.sum, web/package.json, web/package-lock.json and
#       THIRD_PARTY_NOTICES.md change, with no major version step in go.mod or web/package.json
#       and no change of the go or toolchain directive.
#
#   scripts/dependabot-copilot.sh hand-off
#       For every open Dependabot pull request of $GITHUB_REPOSITORY whose ci run failed, or
#       whose update is not routine, opens one issue (label dependabot-fix) with the failed jobs'
#       annotations and assigns it to the Copilot coding agent, working on the Dependabot
#       branch. Closes dependabot-fix issues whose pull request is closed. Needs GH_TOKEN
#       (issues: write; pull requests, actions and checks: read) and COPILOT_TOKEN, a user token
#       that may assign Copilot (the API does not accept installation tokens).
# shellcheck disable=SC2016 # backticks in printf formats are Markdown code spans, not expansions
set -euo pipefail

readonly label="dependabot-fix"
readonly ci_workflow="ci.yml"
readonly copilot_login="copilot-swe-agent[bot]"
readonly title_prefix="Fix Dependabot update #"
readonly max_annotations_per_job=15
readonly max_annotation_chars=1500
readonly max_body_chars=60000

follow_up() {
	awk '
	function major(v,   n) {
		sub(/^[^0-9]*/, "", v)
		n = split(v, p, ".")
		if (n == 0 || p[1] == "") return ""
		return (p[1] == "0" && n > 1) ? "0." p[2] : p[1]
	}
	function note(name, sign, ver) {
		if (sign == "-") old[name] = ver; else new[name] = ver
	}
	/^diff --git / { file = $4; sub(/^b\//, "", file); files[file] = 1; next }
	/^(---|\+\+\+) / { next }
	file == "go.mod" && /^[-+](go|toolchain) / {
		print "go.mod: " substr($0, 2) " (the Go release is pinned in SPEC §5)"; next
	}
	file == "go.mod" && /^[-+]\t?(require )?[a-z0-9][^ \t]* v[0-9]/ {
		line = substr($0, 2); sub(/^\t/, "", line); sub(/^require /, "", line)
		split(line, f, /[ \t]+/); note("go.mod " f[1], substr($0, 1, 1), f[2]); next
	}
	file == "web/package.json" && /^[-+][ \t]*"[^"]+":[ \t]*"[~^]?[0-9]/ {
		line = substr($0, 2); gsub(/[",]/, "", line); sub(/^[ \t]*/, "", line)
		split(line, f, /:[ \t]*/); note("web/package.json " f[1], substr($0, 1, 1), f[2]); next
	}
	END {
		routine["go.mod"]; routine["go.sum"]; routine["web/package.json"]
		routine["web/package-lock.json"]; routine["THIRD_PARTY_NOTICES.md"]
		for (x in files) if (!(x in routine)) print x ": not a routine dependency file (check SPEC.md for the versions it pins)"
		for (k in new) if ((k in old) && major(old[k]) != major(new[k]))
			print k ": major update " old[k] " -> " new[k] " (needs a deliberate review)"
	}' | sort
}

ci_state() { # <repo> <sha> → "<run id> <status> <conclusion>" of the newest pull-request ci run
	gh api "repos/$1/actions/workflows/$ci_workflow/runs?head_sha=$2&event=pull_request&per_page=1" \
		--jq '.workflow_runs[0] // {} | "\(.id // "-") \(.status // "none") \(.conclusion // "-")"'
}

failed_jobs() { # <repo> <run id> → markdown list of failed jobs with their annotations
	local repo="$1" run="$2" id name
	while IFS=$'\t' read -r id name; do
		[[ -n "$id" ]] || continue
		printf -- '- **%s**\n' "$name"
		gh api "repos/$repo/check-runs/$id/annotations?per_page=$max_annotations_per_job" \
			--jq ".[] | \"  - \(.title // \"annotation\"): \(.message | .[0:$max_annotation_chars] | gsub(\"\\n\"; \"\\n    \"))\""
	done < <(gh api "repos/$repo/actions/runs/$run/jobs?per_page=100" \
		--jq '.jobs[] | select(.conclusion == "failure" or .conclusion == "timed_out") | "\(.id)\t\(.name)"')
}

issue_body() { # <repo> <pr> <branch> <sha> <run id> <conclusion> <reasons>
	local repo="$1" pr="$2" branch="$3" sha="$4" run="$5" conclusion="$6" reasons="$7"
	{
		printf 'Dependabot pull request #%s (branch `%s`, head `%s`) cannot be applied as it is.\n\n' \
			"$pr" "$branch" "$sha"
		if [[ "$conclusion" != success ]]; then
			printf '### ci failed\n\n[ci run](https://github.com/%s/actions/runs/%s) concluded `%s`. Failed jobs and their annotations:\n\n' \
				"$repo" "$run" "$conclusion"
			failed_jobs "$repo" "$run"
			printf '\n'
		fi
		if [[ -n "$reasons" ]]; then
			printf '### Not a routine update\n\n'
			printf -- '- %s\n' "${reasons//$'\n'/$'\n'- }"
			printf '\n'
		fi
		printf '### Task\n\n'
		printf 'Work on top of the Dependabot branch `%s` and open a pull request into it, following ' "$branch"
		printf '`.github/copilot-instructions.md`. Keep the versions Dependabot proposes; make ci green and '
		printf 'update SPEC.md where it pins a changed version. If an update cannot be made to work, say so in '
		printf 'the pull request instead of weakening a check.\n'
	} | head -c "$max_body_chars"
}

hand_off_one() { # <repo> <pr> <branch> <sha> <existing titles>
	local repo="$1" pr="$2" branch="$3" sha="$4" existing="$5" state run status conclusion reasons body issue assigned
	if grep -qxF "$title_prefix$pr" <<<"$existing"; then
		echo "#$pr: already handed off"
		return
	fi
	state="$(ci_state "$repo" "$sha")"
	read -r run status conclusion <<<"$state"
	if [[ "$status" != completed || "$conclusion" == cancelled ]]; then
		echo "#$pr: ci not finished ($status $conclusion)"
		return
	fi
	reasons="$(gh api "repos/$repo/pulls/$pr" -H "Accept: application/vnd.github.diff" | follow_up)"
	if [[ "$conclusion" == success && -z "$reasons" ]]; then
		echo "#$pr: green routine update, nothing to do"
		return
	fi
	body="$(issue_body "$repo" "$pr" "$branch" "$sha" "$run" "$conclusion" "$reasons")"
	issue="$(gh api -X POST "repos/$repo/issues" -f title="$title_prefix$pr" -f body="$body" \
		-f "labels[]=$label" --jq .number)"
	assigned="$(jq -n --arg login "$copilot_login" --arg repo "$repo" --arg branch "$branch" \
		--arg instructions "Fix Dependabot pull request #$pr as described in issue #$issue." \
		'{assignees: [$login], agent_assignment: {target_repo: $repo, base_branch: $branch, custom_instructions: $instructions}}' |
		GH_TOKEN="$COPILOT_TOKEN" gh api -X POST "repos/$repo/issues/$issue/assignees" --input - \
			--jq '.assignees | length')"
	if [[ "$assigned" -eq 0 ]]; then
		echo "::error title=dependabot-copilot::Copilot was not assigned to issue #$issue (is the coding agent enabled for this repository?)"
		return 1
	fi
	echo "#$pr: handed to Copilot in issue #$issue"
}

close_finished() { # <repo>: close open dependabot-fix issues whose pull request is closed
	local repo="$1" issue title pr state
	while IFS=$'\t' read -r issue title; do
		pr="${title#"$title_prefix"}"
		[[ "$pr" =~ ^[0-9]+$ ]] || continue
		state="$(gh api "repos/$repo/pulls/$pr" --jq .state)"
		[[ "$state" == closed ]] || continue
		gh api -X POST "repos/$repo/issues/$issue/comments" \
			-f body="Dependabot pull request #$pr is closed, so this task is finished." >/dev/null
		gh api -X PATCH "repos/$repo/issues/$issue" -f state=closed >/dev/null
		echo "issue #$issue closed (#$pr is closed)"
	done < <(gh api --paginate "repos/$repo/issues?labels=$label&state=open&per_page=100" \
		--jq '.[] | select(.pull_request == null) | "\(.number)\t\(.title)"')
}

hand_off() {
	: "${GITHUB_REPOSITORY:?}" "${GH_TOKEN:?}"
	if [[ -z "${COPILOT_TOKEN:-}" ]]; then
		echo "::error title=dependabot-copilot::secret COPILOT_ASSIGN_TOKEN is not set, so failing Dependabot updates cannot be handed to Copilot"
		exit 1
	fi
	local repo="$GITHUB_REPOSITORY" existing pr branch sha
	gh api "repos/$repo/labels/$label" >/dev/null 2>&1 ||
		gh api -X POST "repos/$repo/labels" -f name="$label" -f color=0e8a16 \
			-f description="Dependabot update handed to the Copilot coding agent" >/dev/null
	existing="$(gh api --paginate "repos/$repo/issues?labels=$label&state=all&per_page=100" \
		--jq '.[] | select(.pull_request == null) | .title')"
	while IFS=$'\t' read -r pr branch sha; do
		[[ -n "$pr" ]] && hand_off_one "$repo" "$pr" "$branch" "$sha" "$existing"
	done < <(gh api --paginate "repos/$repo/pulls?state=open&base=main&per_page=100" \
		--jq '.[] | select(.user.login == "dependabot[bot]") | "\(.number)\t\(.head.ref)\t\(.head.sha)"')
	close_finished "$repo"
}

case "${1:-}" in
follow-up) follow_up ;;
hand-off) hand_off ;;
*)
	echo "usage: $0 follow-up < diff | hand-off" >&2
	exit 2
	;;
esac
