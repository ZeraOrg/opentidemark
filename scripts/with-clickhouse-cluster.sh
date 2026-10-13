#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Runs a command with a throw-away 1-shard, 3-replica ClickHouse cluster on loopback, each server
# embedding ClickHouse Keeper (Raft quorum of 3), for the replicated-schema integration test
# (SPEC §17.5, §18):
#
#   scripts/with-clickhouse-cluster.sh go test -tags integration -run TestIntegrationCluster ./internal/store/clickhouse
#
# Binary: $CLICKHOUSE_BIN, /opt/clickhouse/clickhouse or `clickhouse` on PATH. Users come from
# deploy/ha/clickhouse/users.d (the writer holds GRANT CLUSTER).
# Exports TIDEMARK_TEST_CLICKHOUSE_{ADDR,WRITER_PASSWORD,READER_PASSWORD,CLUSTER,CLUSTER_ADDRS}
# (ADDR = the first replica), runs the command and removes the cluster afterwards.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  echo "usage: $0 <command> [args...]" >&2
  exit 2
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
  echo "with-clickhouse-cluster: no ClickHouse binary found; the cluster test will skip" >&2
  exec "$@"
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tidemark-chc.XXXXXX")"
CLUSTER=tidemark_cluster
REPLICAS=3
PIDS=()

cleanup() {
  for pid in "${PIDS[@]}"; do
    kill "${pid}" 2>/dev/null || true
  done
  for pid in "${PIDS[@]}"; do
    for _ in $(seq 1 100); do
      kill -0 "${pid}" 2>/dev/null || break
      sleep 0.1
    done
    kill -9 "${pid}" 2>/dev/null || true
  done
  rm -rf "${WORK}"
}
trap cleanup EXIT INT TERM

free_port() {
  python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'
}

random_secret() {
  head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n'
}

declare -a TCP HTTP INTER KEEPER RAFT
for i in $(seq 1 "${REPLICAS}"); do
  TCP[i]="$(free_port)"; HTTP[i]="$(free_port)"; INTER[i]="$(free_port)"
  KEEPER[i]="$(free_port)"; RAFT[i]="$(free_port)"
done

replicas_xml=""
zookeeper_xml=""
raft_xml=""
for i in $(seq 1 "${REPLICAS}"); do
  replicas_xml+="<replica><host>127.0.0.1</host><port>${TCP[i]}</port></replica>"
  zookeeper_xml+="<node><host>127.0.0.1</host><port>${KEEPER[i]}</port></node>"
  raft_xml+="<server><id>${i}</id><hostname>127.0.0.1</hostname><port>${RAFT[i]}</port></server>"
done

TIDEMARK_CLICKHOUSE_WRITER_PASSWORD="$(random_secret)"
TIDEMARK_CLICKHOUSE_READER_PASSWORD="$(random_secret)"
export TIDEMARK_CLICKHOUSE_WRITER_PASSWORD TIDEMARK_CLICKHOUSE_READER_PASSWORD

for i in $(seq 1 "${REPLICAS}"); do
  D="${WORK}/node${i}"
  mkdir -p "${D}/data" "${D}/tmp" "${D}/user_files" "${D}/log" "${D}/users.d" "${D}/keeper" "${D}/backups"
  cp "${ROOT}/deploy/ha/clickhouse/users.d/"*.xml "${D}/users.d/"
  cat >"${D}/users.xml" <<'EOF'
<clickhouse>
  <profiles><default/></profiles>
  <quotas><default/></quotas>
  <users/>
</clickhouse>
EOF
  cat >"${D}/config.xml" <<EOF
<clickhouse>
  <logger>
    <level>warning</level>
    <log>${D}/log/clickhouse.log</log>
    <errorlog>${D}/log/clickhouse.err.log</errorlog>
  </logger>
  <listen_host>127.0.0.1</listen_host>
  <tcp_port>${TCP[i]}</tcp_port>
  <http_port>${HTTP[i]}</http_port>
  <interserver_http_host>127.0.0.1</interserver_http_host>
  <interserver_http_port>${INTER[i]}</interserver_http_port>
  <path>${D}/data/</path>
  <tmp_path>${D}/tmp/</tmp_path>
  <user_files_path>${D}/user_files/</user_files_path>
  <user_directories>
    <users_xml><path>${D}/users.xml</path></users_xml>
  </user_directories>
  <mark_cache_size>268435456</mark_cache_size>
  <macros><shard>01</shard><replica>node${i}</replica></macros>
  <remote_servers><${CLUSTER}><shard><internal_replication>true</internal_replication>${replicas_xml}</shard></${CLUSTER}></remote_servers>
  <zookeeper>${zookeeper_xml}</zookeeper>
  <distributed_ddl><path>/clickhouse/task_queue/ddl</path></distributed_ddl>
  <keeper_server>
    <tcp_port>${KEEPER[i]}</tcp_port>
    <server_id>${i}</server_id>
    <enable_ipv6>false</enable_ipv6>
    <log_storage_path>${D}/keeper/log</log_storage_path>
    <snapshot_storage_path>${D}/keeper/snapshots</snapshot_storage_path>
    <coordination_settings><operation_timeout_ms>10000</operation_timeout_ms><session_timeout_ms>30000</session_timeout_ms></coordination_settings>
    <raft_configuration>${raft_xml}</raft_configuration>
  </keeper_server>
  <storage_configuration>
    <disks><backups><type>local</type><path>${D}/backups/</path></backups></disks>
  </storage_configuration>
  <backups><allowed_disk>backups</allowed_disk></backups>
</clickhouse>
EOF
  "${CH}" server --config-file="${D}/config.xml" >"${D}/log/stdout.log" 2>&1 &
  PIDS+=($!)
done

query() {
  "${CH}" client --host 127.0.0.1 --port "$1" --user tidemark_writer \
    --password "${TIDEMARK_CLICKHOUSE_WRITER_PASSWORD}" --query "$2"
}

ready=""
for _ in $(seq 1 240); do
  ok=0
  for i in $(seq 1 "${REPLICAS}"); do
    if query "${TCP[i]}" "SELECT 1" >/dev/null 2>&1 &&
      "${CH}" keeper-client --host 127.0.0.1 --port "${KEEPER[i]}" --query "ls /" >/dev/null 2>&1; then
      ok=$((ok + 1))
    fi
  done
  if [[ "${ok}" -eq "${REPLICAS}" ]]; then
    ready=1
    break
  fi
  sleep 0.5
done
if [[ -z "${ready}" ]]; then
  echo "with-clickhouse-cluster: cluster did not become ready; logs follow" >&2
  for i in $(seq 1 "${REPLICAS}"); do
    cat "${WORK}/node${i}/log/stdout.log" "${WORK}/node${i}/log/clickhouse.err.log" >&2 2>/dev/null || true
  done
  exit 1
fi

ADDRS=""
for i in $(seq 1 "${REPLICAS}"); do
  ADDRS+="${ADDRS:+,}127.0.0.1:${TCP[i]}"
done
echo "with-clickhouse-cluster: ClickHouse $(query "${TCP[1]}" "SELECT version()") cluster ${CLUSTER} on ${ADDRS}" >&2

export TIDEMARK_TEST_CLICKHOUSE_ADDR="127.0.0.1:${TCP[1]}"
export TIDEMARK_TEST_CLICKHOUSE_CLUSTER="${CLUSTER}"
export TIDEMARK_TEST_CLICKHOUSE_CLUSTER_ADDRS="${ADDRS}"
export TIDEMARK_TEST_CLICKHOUSE_WRITER_PASSWORD="${TIDEMARK_CLICKHOUSE_WRITER_PASSWORD}"
export TIDEMARK_TEST_CLICKHOUSE_READER_PASSWORD="${TIDEMARK_CLICKHOUSE_READER_PASSWORD}"
unset TIDEMARK_CLICKHOUSE_WRITER_PASSWORD TIDEMARK_CLICKHOUSE_READER_PASSWORD

set +e
"$@"
status=$?
set -e
exit "${status}"
