// SPDX-License-Identifier: Apache-2.0

package clickhouse

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidCluster is returned for cluster names that are not safe to put into DDL.
var ErrInvalidCluster = errors.New("invalid ClickHouse cluster name")

// clusterPattern validates TIDEMARK_CLICKHOUSE_CLUSTER (it is inlined into ON CLUSTER '…').
var clusterPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)

// ValidCluster reports whether name may be used as the ON CLUSTER target.
func ValidCluster(name string) bool { return clusterPattern.MatchString(name) }

var (
	createTablePattern = regexp.MustCompile(`^(CREATE TABLE IF NOT EXISTS )([A-Za-z0-9_]+)\.([A-Za-z0-9_]+)(\s*\()`)
	createViewPattern  = regexp.MustCompile(`^(CREATE MATERIALIZED VIEW IF NOT EXISTS [A-Za-z0-9_]+\.[A-Za-z0-9_]+)(\s+TO\s)`)
	createDBPattern    = regexp.MustCompile(`^(CREATE DATABASE IF NOT EXISTS [A-Za-z0-9_]+)$`)
	enginePattern      = regexp.MustCompile(`ENGINE = (Replacing|Aggregating|Summing)?MergeTree(\(([^)]*)\))?`)
)

// ReplicaPath is the Keeper path of a replicated table (one shard, N replicas; SPEC §17.5).
func ReplicaPath(table string) string { return "/clickhouse/tables/{shard}/{database}/" + table }

// RenderForCluster rewrites one migration statement for cluster mode: CREATE DATABASE/TABLE/
// MATERIALIZED VIEW run ON CLUSTER and MergeTree-family engines become their Replicated
// variants with the path of ReplicaPath and the {replica} macro. ALTER statements are left
// unchanged: metadata changes of replicated tables are replicated by ClickHouse itself.
// An empty cluster returns stmt unchanged.
func RenderForCluster(stmt, cluster string) (string, error) {
	if cluster == "" {
		return stmt, nil
	}
	if !ValidCluster(cluster) {
		return "", ErrInvalidCluster
	}
	on := " ON CLUSTER '" + cluster + "'"
	switch {
	case createTablePattern.MatchString(stmt):
		m := createTablePattern.FindStringSubmatch(stmt)
		out := createTablePattern.ReplaceAllString(stmt, "${1}${2}.${3}"+on+"${4}")
		return replicateEngine(out, m[3])
	case createViewPattern.MatchString(stmt):
		return createViewPattern.ReplaceAllString(stmt, "${1}"+on+"${2}"), nil
	case createDBPattern.MatchString(stmt):
		return stmt + on, nil
	case strings.HasPrefix(stmt, "CREATE"):
		return "", fmt.Errorf("statement cannot be rendered for cluster mode: %.60s", stmt)
	default:
		return stmt, nil
	}
}

func replicateEngine(stmt, table string) (string, error) {
	m := enginePattern.FindStringSubmatch(stmt)
	if m == nil {
		return "", fmt.Errorf("table %s: no MergeTree engine to replicate", table)
	}
	args := "'" + ReplicaPath(table) + "', '{replica}'"
	if strings.TrimSpace(m[3]) != "" {
		args += ", " + strings.TrimSpace(m[3])
	}
	return strings.Replace(stmt, m[0], "ENGINE = Replicated"+m[1]+"MergeTree("+args+")", 1), nil
}
