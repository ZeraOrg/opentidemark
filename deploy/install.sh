#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# OpenTidemark installer for Linux (and macOS for the Docker mode) — SPEC §17.2, §17.3.
#
#   ./install.sh docker [--no-start]
#       Creates deploy/.env from .env.example on first run (random ClickHouse passwords and a
#       bootstrap ingest key, mode 0600), then runs `docker compose up -d --build`.
#
#   sudo ./install.sh native <path-to-tidemark-binary> [--start] [--no-self-update]
#       Creates the system user `tidemark`, /var/lib/tidemark, /etc/tidemark/tidemark.env (random
#       secrets on first run), installs the binary to /usr/local/bin and the systemd unit
#       tidemark.service, and enables it, plus the update helper (tidemark-update.path/.service)
#       unless --no-self-update is given (which also removes an installed helper). ClickHouse must
#       be set up separately with the users from deploy/clickhouse/users.d and the passwords
#       written to tidemark.env; --start starts the service right away.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLACEHOLDER="generate-me"
KEY_PREFIX="tmk_"
KEY_LENGTH=40

usage() {
  sed -n '4,16p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2
  exit 2
}

# random_hex prints 64 hex characters (256 bits) from the kernel CSPRNG.
random_hex() {
  head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

# random_key prints a connection key in the SPEC §15.3 format: tmk_ + 40 base62 characters.
random_key() {
  local chars=""
  while [[ ${#chars} -lt ${KEY_LENGTH} ]]; do
    chars+="$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')"
  done
  printf '%s%s' "${KEY_PREFIX}" "${chars:0:${KEY_LENGTH}}"
}

# write_env_file renders an env file from a template: every "generate-me" becomes a fresh secret
# and the commented bootstrap key line gets a generated key. The file is created with mode 0600.
write_env_file() {
  local template="$1" target="$2" tmp
  tmp="$(mktemp "${target}.XXXXXX")"
  chmod 0600 "${tmp}"
  while IFS= read -r line || [[ -n "${line}" ]]; do
    if [[ "${line}" == *"=${PLACEHOLDER}" ]]; then
      line="${line%"${PLACEHOLDER}"}$(random_hex)"
    elif [[ "${line}" == "#TIDEMARK_BOOTSTRAP_INGEST_KEY=" ]]; then
      line="TIDEMARK_BOOTSTRAP_INGEST_KEY=$(random_key)"
    fi
    printf '%s\n' "${line}"
  done <"${template}" >"${tmp}"
  mv "${tmp}" "${target}"
}

install_docker() {
  local start=1
  for arg in "$@"; do
    case "${arg}" in
      --no-start) start=0 ;;
      *) usage ;;
    esac
  done
  command -v docker >/dev/null 2>&1 || { echo "install: docker is not installed" >&2; exit 1; }
  local env_file="${SCRIPT_DIR}/.env"
  if [[ -f "${env_file}" ]]; then
    echo "install: keeping existing ${env_file}"
  else
    write_env_file "${SCRIPT_DIR}/.env.example" "${env_file}"
    echo "install: created ${env_file} with random ClickHouse passwords and a bootstrap ingest key"
  fi
  if [[ "${start}" -eq 1 ]]; then
    (cd "${SCRIPT_DIR}" && docker compose up -d --build)
    echo "install: open http://localhost:8080 and sign in as admin / admin, then change the password."
    echo "install: the bootstrap ingest key is TIDEMARK_BOOTSTRAP_INGEST_KEY in ${env_file}."
  fi
}

# install_update_helper installs the units that let admins update from the UI (SPEC §17.7): the
# service stages releases in /var/lib/tidemark/update, the root helper installs them.
install_update_helper() {
  install -d -m 0750 -o tidemark -g tidemark /var/lib/tidemark/update
  install -m 0644 -o root -g root "${SCRIPT_DIR}/tidemark-update.service" /etc/systemd/system/tidemark-update.service
  install -m 0644 -o root -g root "${SCRIPT_DIR}/tidemark-update.path" /etc/systemd/system/tidemark-update.path
  systemctl daemon-reload
  systemctl enable --now tidemark-update.path
}

# remove_update_helper removes the helper units of an earlier install (--no-self-update).
remove_update_helper() {
  if [[ -f /etc/systemd/system/tidemark-update.path ]]; then
    systemctl disable --now tidemark-update.path
    rm -f /etc/systemd/system/tidemark-update.path /etc/systemd/system/tidemark-update.service
    echo "install: removed the update helper; update with this installer"
  fi
  systemctl daemon-reload
}

install_native() {
  [[ $# -ge 1 ]] || usage
  local binary="$1" start=0 self_update=1
  shift
  for arg in "$@"; do
    case "${arg}" in
      --start) start=1 ;;
      --no-self-update) self_update=0 ;;
      *) usage ;;
    esac
  done
  [[ "$(id -u)" -eq 0 ]] || { echo "install: native mode must run as root" >&2; exit 1; }
  [[ -x "${binary}" ]] || { echo "install: ${binary} is not an executable" >&2; exit 1; }
  command -v systemctl >/dev/null 2>&1 || { echo "install: systemd is required" >&2; exit 1; }

  if ! id -u tidemark >/dev/null 2>&1; then
    useradd --system --user-group --home-dir /var/lib/tidemark --no-create-home \
      --shell /usr/sbin/nologin --comment "Tidemark" tidemark
  fi
  install -d -m 0750 -o tidemark -g tidemark /var/lib/tidemark
  install -d -m 0750 -o root -g tidemark /etc/tidemark
  install -m 0755 -o root -g root "${binary}" /usr/local/bin/tidemark

  local env_file=/etc/tidemark/tidemark.env
  if [[ -f "${env_file}" ]]; then
    echo "install: keeping existing ${env_file}"
  else
    write_env_file "${SCRIPT_DIR}/.env.example" "${env_file}"
    # Native installs talk to a local ClickHouse; the data directory comes from the unit.
    sed -i 's|^#TIDEMARK_CLICKHOUSE_ADDR=clickhouse:9000$|TIDEMARK_CLICKHOUSE_ADDR=localhost:9000|' "${env_file}"
    chown root:tidemark "${env_file}"
    chmod 0640 "${env_file}"
    echo "install: created ${env_file}; configure ClickHouse users with the passwords it contains"
  fi

  install -m 0644 -o root -g root "${SCRIPT_DIR}/tidemark.service" /etc/systemd/system/tidemark.service
  if [[ "${self_update}" -eq 1 ]]; then
    install_update_helper
  else
    remove_update_helper
  fi
  if [[ "${start}" -eq 1 ]]; then
    systemctl enable --now tidemark.service
  else
    systemctl enable tidemark.service
    echo "install: start with: systemctl start tidemark"
  fi
}

[[ $# -ge 1 ]] || usage
mode="$1"
shift
case "${mode}" in
  docker) install_docker "$@" ;;
  native) install_native "$@" ;;
  *) usage ;;
esac
