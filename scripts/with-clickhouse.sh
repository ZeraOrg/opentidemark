#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Runs a command with a throw-away ClickHouse server for the integration tests (SPEC §18).
#
#   scripts/with-clickhouse.sh go test -tags integration ./...
#
# When TIDEMARK_TEST_CLICKHOUSE_ADDR is already set the command runs unchanged. Otherwise a
# local ClickHouse binary ($CLICKHOUSE_BIN, /opt/clickhouse/clickhouse or `clickhouse` on PATH)
# is started on a free loopback port with a temporary data directory and the OpenTidemark users of
# deploy/clickhouse/users.d (random passwords), the TIDEMARK_TEST_CLICKHOUSE_* variables are
# exported for the command, and the server is stopped and removed afterwards. Without a binary
# the command still runs and the integration tests skip themselves.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: $0 <command> [args...]" >&2
  exit 2
fi

if [[ -n "${TIDEMARK_TEST_CLICKHOUSE_ADDR:-}" ]]; then
  exec "$@"
fi

find_clickhouse() {
  if [[ -n "${CLICKHOUSE_BIN:-}" && -x "${CLICKHOUSE_BIN}" ]]; then
    echo "${CLICKHOUSE_BIN}"
  elif [[ -x /opt/clickhouse/clickhouse ]]; then
    echo /opt/clickhouse/clickhouse
  elif command -v clickhouse >/dev/null 2>&1; then
    command -v clickhouse
  fi
}

CH="$(find_clickhouse)"
if [[ -z "${CH}" ]]; then
  echo "with-clickhouse: no ClickHouse binary found and TIDEMARK_TEST_CLICKHOUSE_ADDR unset; integration tests will skip" >&2
  exec "$@"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tidemark-ch.XXXXXX")"
PID=""

cleanup() {
  if [[ -n "${PID}" ]] && kill -0 "${PID}" 2>/dev/null; then
    kill "${PID}" 2>/dev/null || true
    for _ in $(seq 1 100); do
      kill -0 "${PID}" 2>/dev/null || break
      sleep 0.1
    done
    kill -9 "${PID}" 2>/dev/null || true
  fi
  rm -rf "${WORK}"
}
trap cleanup EXIT INT TERM

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

random_secret() {
  head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

PORT="$(free_port)"
mkdir -p "${WORK}/data" "${WORK}/tmp" "${WORK}/user_files" "${WORK}/log" "${WORK}/users.d" "${WORK}/backups"
cp "${ROOT}/deploy/clickhouse/users.d/"*.xml "${WORK}/users.d/"

cat >"${WORK}/config.xml" <<EOF
<clickhouse>
  <logger>
    <level>warning</level>
    <log>${WORK}/log/clickhouse.log</log>
    <errorlog>${WORK}/log/clickhouse.err.log</errorlog>
  </logger>
  <listen_host>127.0.0.1</listen_host>
  <tcp_port>${PORT}</tcp_port>
  <path>${WORK}/data/</path>
  <tmp_path>${WORK}/tmp/</tmp_path>
  <user_files_path>${WORK}/user_files/</user_files_path>
  <user_directories>
    <users_xml><path>${WORK}/users.xml</path></users_xml>
  </user_directories>
  <mark_cache_size>268435456</mark_cache_size>
  <storage_configuration>
    <disks>
      <backups><type>local</type><path>${WORK}/backups/</path></backups>
    </disks>
  </storage_configuration>
  <backups><allowed_disk>backups</allowed_disk></backups>
</clickhouse>
EOF

cat >"${WORK}/users.xml" <<'EOF'
<clickhouse>
  <profiles><default/></profiles>
  <quotas><default/></quotas>
  <users/>
</clickhouse>
EOF

TIDEMARK_CLICKHOUSE_WRITER_PASSWORD="$(random_secret)"
TIDEMARK_CLICKHOUSE_READER_PASSWORD="$(random_secret)"
export TIDEMARK_CLICKHOUSE_WRITER_PASSWORD TIDEMARK_CLICKHOUSE_READER_PASSWORD

"${CH}" server --config-file="${WORK}/config.xml" >"${WORK}/log/stdout.log" 2>&1 &
PID=$!

ready=""
for _ in $(seq 1 120); do
  if "${CH}" client --host 127.0.0.1 --port "${PORT}" --user tidemark_writer \
    --password "${TIDEMARK_CLICKHOUSE_WRITER_PASSWORD}" --query "SELECT 1" >/dev/null 2>&1; then
    ready=1
    break
  fi
  if ! kill -0 "${PID}" 2>/dev/null; then
    break
  fi
  sleep 0.5
done
if [[ -z "${ready}" ]]; then
  echo "with-clickhouse: ClickHouse did not start; log follows" >&2
  cat "${WORK}/log/stdout.log" "${WORK}/log/clickhouse.err.log" >&2 2>/dev/null || true
  exit 1
fi
echo "with-clickhouse: ClickHouse $("${CH}" client --host 127.0.0.1 --port "${PORT}" --user tidemark_writer \
  --password "${TIDEMARK_CLICKHOUSE_WRITER_PASSWORD}" --query "SELECT version()") on 127.0.0.1:${PORT}" >&2

export TIDEMARK_TEST_CLICKHOUSE_ADDR="127.0.0.1:${PORT}"
export TIDEMARK_TEST_CLICKHOUSE_WRITER_PASSWORD="${TIDEMARK_CLICKHOUSE_WRITER_PASSWORD}"
export TIDEMARK_TEST_CLICKHOUSE_READER_PASSWORD="${TIDEMARK_CLICKHOUSE_READER_PASSWORD}"
unset TIDEMARK_CLICKHOUSE_WRITER_PASSWORD TIDEMARK_CLICKHOUSE_READER_PASSWORD

set +e
"$@"
status=$?
set -e
exit "${status}"
