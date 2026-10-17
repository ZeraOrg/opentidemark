// SPDX-License-Identifier: Apache-2.0

package config

import "strings"

// Prefix of every configuration variable.
const Prefix = "TIDEMARK_"

// Variable names (SPEC §7).
const (
	VarHTTPAddr             = "TIDEMARK_HTTP_ADDR"
	VarSeqAddr              = "TIDEMARK_SEQ_ADDR"
	VarESAddr               = "TIDEMARK_ES_ADDR"
	VarOTLPHTTPAddr         = "TIDEMARK_OTLP_HTTP_ADDR"
	VarOTLPGRPCAddr         = "TIDEMARK_OTLP_GRPC_ADDR"
	VarTLSCertFile          = "TIDEMARK_TLS_CERT_FILE"
	VarTLSKeyFile           = "TIDEMARK_TLS_KEY_FILE"
	VarPublicURL            = "TIDEMARK_PUBLIC_URL"
	VarDataDir              = "TIDEMARK_DATA_DIR"
	VarCHAddr               = "TIDEMARK_CLICKHOUSE_ADDR"
	VarCHDatabase           = "TIDEMARK_CLICKHOUSE_DATABASE"
	VarCHWriterUser         = "TIDEMARK_CLICKHOUSE_WRITER_USER"
	VarCHWriterPassword     = "TIDEMARK_CLICKHOUSE_WRITER_PASSWORD" //nolint:gosec // variable name, not a credential
	VarCHReaderUser         = "TIDEMARK_CLICKHOUSE_READER_USER"
	VarCHReaderPassword     = "TIDEMARK_CLICKHOUSE_READER_PASSWORD" //nolint:gosec // variable name, not a credential
	VarCHTLS                = "TIDEMARK_CLICKHOUSE_TLS"
	VarAdminUsername        = "TIDEMARK_ADMIN_USERNAME"
	VarAdminPassword        = "TIDEMARK_ADMIN_PASSWORD"
	VarBootstrapIngestKey   = "TIDEMARK_BOOTSTRAP_INGEST_KEY"
	VarBatchMaxRows         = "TIDEMARK_BATCH_MAX_ROWS"
	VarBatchMaxWait         = "TIDEMARK_BATCH_MAX_WAIT"
	VarQueueMaxRows         = "TIDEMARK_QUEUE_MAX_ROWS"
	VarMaxBodyBytes         = "TIDEMARK_MAX_BODY_BYTES"
	VarMaxDecompressedBytes = "TIDEMARK_MAX_DECOMPRESSED_BYTES"
	VarQueryTimeout         = "TIDEMARK_QUERY_TIMEOUT"
	VarQueryMaxRows         = "TIDEMARK_QUERY_MAX_ROWS"
	VarSessionIdleTimeout   = "TIDEMARK_SESSION_IDLE_TIMEOUT"
	VarSessionAbsTimeout    = "TIDEMARK_SESSION_ABSOLUTE_TIMEOUT"
	VarTrustedProxies       = "TIDEMARK_TRUSTED_PROXIES"
	VarAllowPrivateWebhooks = "TIDEMARK_ALLOW_PRIVATE_WEBHOOKS"
	VarSMTPHost             = "TIDEMARK_SMTP_HOST"
	VarSMTPPort             = "TIDEMARK_SMTP_PORT"
	VarSMTPUsername         = "TIDEMARK_SMTP_USERNAME"
	VarSMTPPassword         = "TIDEMARK_SMTP_PASSWORD" //nolint:gosec // variable name, not a credential
	VarSMTPFrom             = "TIDEMARK_SMTP_FROM"
	VarSMTPStartTLS         = "TIDEMARK_SMTP_STARTTLS"
	VarLogLevel             = "TIDEMARK_LOG_LEVEL"
	VarMaxStreams           = "TIDEMARK_MAX_STREAMS"
	VarMetricsEnabled       = "TIDEMARK_METRICS_ENABLED"
	VarMetricsToken         = "TIDEMARK_METRICS_TOKEN"
	VarSecretKeyFile        = "TIDEMARK_SECRET_KEY_FILE"
	VarEnvFile              = "TIDEMARK_ENV_FILE"
	VarNodeID               = "TIDEMARK_NODE_ID"
	VarMetaDriver           = "TIDEMARK_META_DRIVER"
	VarMetaDSN              = "TIDEMARK_META_DSN"
	VarCHCluster            = "TIDEMARK_CLICKHOUSE_CLUSTER"
	VarCHInsertQuorum       = "TIDEMARK_CLICKHOUSE_INSERT_QUORUM"
	VarWALEnabled           = "TIDEMARK_WAL_ENABLED"
	VarWALDir               = "TIDEMARK_WAL_DIR"
	VarWALMaxBytes          = "TIDEMARK_WAL_MAX_BYTES"
	VarWALSync              = "TIDEMARK_WAL_SYNC"
	VarWALExitOnFailure     = "TIDEMARK_WAL_EXIT_ON_FAILURE"
	VarBackupDir            = "TIDEMARK_BACKUP_DIR"
	VarDrainSeconds         = "TIDEMARK_DRAIN_SECONDS"
)

// allVars lists every variable; each also has a kebab-case flag.
var allVars = []string{
	VarHTTPAddr, VarSeqAddr, VarESAddr, VarOTLPHTTPAddr, VarOTLPGRPCAddr, VarTLSCertFile, VarTLSKeyFile,
	VarPublicURL, VarDataDir, VarCHAddr, VarCHDatabase, VarCHWriterUser, VarCHWriterPassword, VarCHReaderUser,
	VarCHReaderPassword, VarCHTLS, VarAdminUsername, VarAdminPassword, VarBootstrapIngestKey, VarBatchMaxRows,
	VarBatchMaxWait, VarQueueMaxRows, VarMaxBodyBytes, VarMaxDecompressedBytes, VarQueryTimeout, VarQueryMaxRows,
	VarSessionIdleTimeout, VarSessionAbsTimeout, VarTrustedProxies, VarAllowPrivateWebhooks, VarSMTPHost,
	VarSMTPPort, VarSMTPUsername, VarSMTPPassword, VarSMTPFrom, VarSMTPStartTLS, VarLogLevel, VarMaxStreams,
	VarMetricsEnabled, VarMetricsToken, VarSecretKeyFile, VarEnvFile,
	VarNodeID, VarMetaDriver, VarMetaDSN, VarCHCluster, VarCHInsertQuorum, VarWALEnabled, VarWALDir, VarWALMaxBytes,
	VarWALSync, VarWALExitOnFailure, VarBackupDir, VarDrainSeconds,
}

// secretVars may be supplied as <VAR>_FILE (Docker/Kubernetes secrets).
var secretVars = []string{
	VarCHWriterPassword, VarCHReaderPassword, VarAdminPassword, VarBootstrapIngestKey, VarSMTPPassword,
	VarMetricsToken, VarMetaDSN,
}

// defaults are the values used when a variable is not set (OS-specific ones are added in Load).
var defaults = map[string]string{
	VarHTTPAddr: ":8080", VarSeqAddr: ":5341", VarESAddr: ":9200", VarOTLPHTTPAddr: ":4318", VarOTLPGRPCAddr: ":4317",
	VarPublicURL: "http://localhost:8080", VarCHAddr: "localhost:9000", VarCHDatabase: "tidemark",
	VarCHWriterUser: "tidemark_writer", VarCHReaderUser: "tidemark_reader", VarCHTLS: "false",
	VarAdminUsername: "admin", VarAdminPassword: "admin", VarBatchMaxRows: "10000", VarBatchMaxWait: "1s",
	VarQueueMaxRows: "200000", VarMaxBodyBytes: "10485760", VarMaxDecompressedBytes: "104857600",
	VarQueryTimeout: "30s", VarQueryMaxRows: "10000", VarSessionIdleTimeout: "12h", VarSessionAbsTimeout: "168h",
	VarAllowPrivateWebhooks: "false", VarSMTPPort: "587", VarSMTPStartTLS: "true", VarLogLevel: "info",
	VarMaxStreams: "200", VarMetricsEnabled: "false",
	VarMetaDriver: "sqlite", VarCHInsertQuorum: "auto", VarWALEnabled: "true", VarWALMaxBytes: "10737418240",
	VarWALSync: "batch", VarWALExitOnFailure: "true", VarDrainSeconds: "10",
}

// FlagName returns the CLI flag of a variable: TIDEMARK_HTTP_ADDR ⇒ http-addr.
func FlagName(v string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(v, Prefix), "_", "-"))
}

// isKnown reports whether v (or v minus a _FILE suffix for secrets) is a known variable.
func isKnown(v string) bool {
	for _, k := range allVars {
		if k == v {
			return true
		}
	}
	for _, k := range secretVars {
		if k+"_FILE" == v {
			return true
		}
	}
	return false
}
