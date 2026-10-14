#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Prints the release notes of one version (SPEC §17.6): the body of the "## [<version>] - <date>"
# section of CHANGELOG.md, without leading and trailing blank lines, followed by an "Install"
# section for that version (assets, quick commands, verification). Fails when the changelog
# section is missing or empty, so a release cannot go out without user-facing notes.
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

v="$version"
cat <<NOTES

## Install

| Asset | Platform | Quick start |
|---|---|---|
| \`tidemark_${v}_docker.tar.gz\` or \`.zip\` | Docker Compose: Linux, Windows (Docker Desktop), macOS | \`tar -xzf tidemark_${v}_docker.tar.gz && cd tidemark_${v}_docker && ./install.sh docker\` (Windows: \`.\\install.ps1 -Mode Docker\`) |
| \`tidemark_${v}_linux_amd64.tar.gz\` | Linux x86-64, systemd service | \`tar -xzf tidemark_${v}_linux_amd64.tar.gz && cd tidemark_${v}_linux_amd64 && sudo ./deploy/install.sh native ./tidemark\` |
| \`tidemark_${v}_linux_arm64.tar.gz\` | Linux arm64, systemd service | \`tar -xzf tidemark_${v}_linux_arm64.tar.gz && cd tidemark_${v}_linux_arm64 && sudo ./deploy/install.sh native ./tidemark\` |
| \`tidemark_${v}_windows_amd64.zip\` | Windows 10/11, Server 2019+, Windows service | in the unpacked folder, as Administrator: \`.\\deploy\\install.ps1 -Mode Native -Binary .\\tidemark.exe\` |
| \`ghcr.io/zeraorg/tidemark:${v}\` | Container image, linux/amd64 and linux/arm64 | \`docker pull ghcr.io/zeraorg/tidemark:${v}\` |
| \`tidemark_${v}_docs.zip\` | Offline documentation (user guides and the operations manual, with search) | unzip, open \`tidemark_${v}_docs/index.html\` in a browser |

Every archive contains an \`INSTALL.md\` with the steps for its platform (ClickHouse setup, service
management, upgrade, uninstall). The full OpenTidemark installation guide is
<https://github.com/ZeraOrg/opentidemark/blob/v${v}/docs/installation.md>; the documentation site is
<https://zeraorg.github.io/opentidemark/>.

**Verify the download:**

\`\`\`bash
sha256sum -c checksums.txt --ignore-missing
gh attestation verify tidemark_${v}_linux_amd64.tar.gz --repo ZeraOrg/opentidemark
gh attestation verify oci://ghcr.io/zeraorg/tidemark:${v} --repo ZeraOrg/opentidemark
\`\`\`
NOTES
