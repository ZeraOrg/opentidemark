// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Validation bounds.
const (
	maxBatchRows    = 1_000_000
	minBatchWait    = 10 * time.Millisecond
	maxBatchWait    = time.Minute
	minBodyBytes    = 1024
	maxBodyBytes    = 1 << 30
	maxQueryTimeout = time.Hour
	maxQueryRows    = 1_000_000
	maxStreamsLimit = 10_000
	minBootstrapKey = 24
	maxPort         = 65535
)

var (
	usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._@-]{1,64}$`)
	databasePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// parser accumulates the first conversion error.
type parser struct {
	vals map[string]string
	errs []error
}

func (p *parser) fail(k string, err error) { p.errs = append(p.errs, fmt.Errorf("%s: %w", k, err)) }

func (p *parser) str(k string) string { return strings.TrimSpace(p.vals[k]) }

func (p *parser) intRange(k string, lo, hi int64) int64 {
	n, err := strconv.ParseInt(p.str(k), 10, 64)
	if err != nil {
		p.fail(k, errors.New("must be an integer"))
		return 0
	}
	if n < lo || n > hi {
		p.fail(k, fmt.Errorf("must be between %d and %d", lo, hi))
	}
	return n
}

func (p *parser) duration(k string, lo, hi time.Duration) time.Duration {
	d, err := time.ParseDuration(p.str(k))
	if err != nil {
		p.fail(k, errors.New("must be a duration such as 30s or 12h"))
		return 0
	}
	if d < lo || d > hi {
		p.fail(k, fmt.Errorf("must be between %v and %v", lo, hi))
	}
	return d
}

func (p *parser) boolean(k string) bool {
	b, err := strconv.ParseBool(p.str(k))
	if err != nil {
		p.fail(k, errors.New("must be true or false"))
	}
	return b
}

func (p *parser) addr(k string, optional bool) string {
	v := p.str(k)
	if v == "" && optional {
		return ""
	}
	if err := checkHostPort(v); err != nil {
		p.fail(k, err)
	}
	return v
}

func checkHostPort(v string) error {
	_, port, err := net.SplitHostPort(v)
	if err != nil {
		return errors.New("must be host:port")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > maxPort {
		return errors.New("invalid port")
	}
	return nil
}

func build(vals map[string]string) (Config, error) {
	p := &parser{vals: vals}
	c := Config{
		HTTPAddr: p.addr(VarHTTPAddr, false), SeqAddr: p.addr(VarSeqAddr, true), ESAddr: p.addr(VarESAddr, true),
		OTLPHTTPAddr: p.addr(VarOTLPHTTPAddr, true), OTLPGRPCAddr: p.addr(VarOTLPGRPCAddr, true),
		TLSCertFile: p.str(VarTLSCertFile), TLSKeyFile: p.str(VarTLSKeyFile),
		AdminUsername: p.str(VarAdminUsername), AdminPassword: vals[VarAdminPassword],
		AdminPasswordIsDefault: vals[adminPasswordDefaultMarker] == "1",
		BootstrapIngestKey:     p.str(VarBootstrapIngestKey),
		MetricsToken:           p.str(VarMetricsToken),
		SecretKeyFile:          p.str(VarSecretKeyFile), EnvFile: p.str(VarEnvFile),
	}
	c.PublicURL = parsePublicURL(p)
	c.DataDir = absPath(p, VarDataDir)
	c.ClickHouse = parseClickHouse(p)
	parseLimits(p, &c)
	parseSecurity(p, &c)
	c.SMTP = SMTP{Host: p.str(VarSMTPHost), Port: int(p.intRange(VarSMTPPort, 1, maxPort)), Username: p.str(VarSMTPUsername),
		Password: vals[VarSMTPPassword], From: p.str(VarSMTPFrom), StartTLS: p.boolean(VarSMTPStartTLS)}
	c.LogLevel = parseLogLevel(p)
	c.MaxStreams = int(p.intRange(VarMaxStreams, 1, maxStreamsLimit))
	c.MetricsEnabled = p.boolean(VarMetricsEnabled)
	parseHA(p, &c)
	crossChecks(p, &c)
	if len(p.errs) > 0 {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalid, errors.Join(p.errs...))
	}
	return c, nil
}

// HA, durability and upgrade-safety settings (SPEC §7, §8.5, §9.8, §17.5).
const (
	maxDrainSeconds = 600
	minWALBytes     = 1 << 20
	maxWALBytes     = 1 << 50
	maxQuorum       = 64
)

var (
	nodeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	clusterPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

func parseHA(p *parser, c *Config) {
	c.NodeID = p.str(VarNodeID)
	if !nodeIDPattern.MatchString(c.NodeID) {
		p.fail(VarNodeID, errors.New("must be 1-64 characters of [A-Za-z0-9._-]"))
	}
	c.Meta = Meta{Driver: strings.ToLower(p.str(VarMetaDriver)), DSN: p.vals[VarMetaDSN]}
	switch c.Meta.Driver {
	case "sqlite":
	case "postgres":
		if c.Meta.DSN == "" {
			p.fail(VarMetaDSN, errors.New("is required when TIDEMARK_META_DRIVER=postgres"))
		}
	default:
		p.fail(VarMetaDriver, errors.New("must be sqlite or postgres"))
	}
	c.ClickHouse.Cluster = p.str(VarCHCluster)
	if c.ClickHouse.Cluster != "" && !clusterPattern.MatchString(c.ClickHouse.Cluster) {
		p.fail(VarCHCluster, errors.New("must match ^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$"))
	}
	c.ClickHouse.InsertQuorum = strings.ToLower(p.str(VarCHInsertQuorum))
	if c.ClickHouse.InsertQuorum != "auto" {
		p.intRange(VarCHInsertQuorum, 1, maxQuorum)
	}
	c.WAL = WAL{Enabled: p.boolean(VarWALEnabled), MaxBytes: p.intRange(VarWALMaxBytes, minWALBytes, maxWALBytes),
		Sync: strings.ToLower(p.str(VarWALSync))}
	c.WAL.Dir = absPath(p, VarWALDir)
	if c.WAL.Sync != "batch" && c.WAL.Sync != "none" {
		p.fail(VarWALSync, errors.New("must be batch or none"))
	}
	c.BackupDir = absPath(p, VarBackupDir)
	c.Drain = time.Duration(p.intRange(VarDrainSeconds, 0, maxDrainSeconds)) * time.Second
}

// HA reports whether the node runs in HA mode (shared PostgreSQL metadata).
func (c Config) HA() bool { return c.Meta.Driver == "postgres" }

func parsePublicURL(p *parser) *url.URL {
	u, err := url.Parse(p.str(VarPublicURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		p.fail(VarPublicURL, errors.New("must be an absolute http(s) URL"))
		return &url.URL{Scheme: "http", Host: "localhost"}
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u
}

func absPath(p *parser, k string) string {
	v := p.str(k)
	if v == "" {
		p.fail(k, errors.New("must not be empty"))
		return ""
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		p.fail(k, err)
		return v
	}
	return abs
}

func parseClickHouse(p *parser) ClickHouse {
	ch := ClickHouse{Database: p.str(VarCHDatabase), WriterUser: p.str(VarCHWriterUser), ReaderUser: p.str(VarCHReaderUser),
		WriterPassword: p.vals[VarCHWriterPassword], ReaderPassword: p.vals[VarCHReaderPassword], TLS: p.boolean(VarCHTLS)}
	for _, a := range strings.Split(p.str(VarCHAddr), ",") {
		a = strings.TrimSpace(a)
		if err := checkHostPort(a); err != nil {
			p.fail(VarCHAddr, err)
			continue
		}
		ch.Addrs = append(ch.Addrs, a)
	}
	if !databasePattern.MatchString(ch.Database) {
		p.fail(VarCHDatabase, errors.New("must be an identifier ([A-Za-z_][A-Za-z0-9_]*)"))
	}
	if ch.WriterUser == "" || ch.ReaderUser == "" {
		p.fail(VarCHWriterUser, errors.New("writer and reader users must be set"))
	}
	return ch
}

func parseLimits(p *parser, c *Config) {
	c.BatchMaxRows = int(p.intRange(VarBatchMaxRows, 1, maxBatchRows))
	c.BatchMaxWait = p.duration(VarBatchMaxWait, minBatchWait, maxBatchWait)
	c.QueueMaxRows = int(p.intRange(VarQueueMaxRows, 1, maxBatchRows*100))
	c.MaxBodyBytes = p.intRange(VarMaxBodyBytes, minBodyBytes, maxBodyBytes)
	c.MaxDecompressedBytes = p.intRange(VarMaxDecompressedBytes, minBodyBytes, maxBodyBytes*4)
	c.QueryTimeout = p.duration(VarQueryTimeout, time.Second, maxQueryTimeout)
	c.QueryMaxRows = int(p.intRange(VarQueryMaxRows, 1, maxQueryRows))
}

func parseSecurity(p *parser, c *Config) {
	c.SessionIdleTimeout = p.duration(VarSessionIdleTimeout, time.Minute, 365*24*time.Hour)
	c.SessionAbsoluteTimeout = p.duration(VarSessionAbsTimeout, time.Minute, 365*24*time.Hour)
	c.AllowPrivateWebhooks = p.boolean(VarAllowPrivateWebhooks)
	for _, s := range strings.Split(p.str(VarTrustedProxies), ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		pre, err := parsePrefix(s)
		if err != nil {
			p.fail(VarTrustedProxies, fmt.Errorf("invalid CIDR %q", s))
			continue
		}
		c.TrustedProxies = append(c.TrustedProxies, pre)
	}
}

func parsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		pre, err := netip.ParsePrefix(s)
		return pre.Masked(), err
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), nil
}

func parseLogLevel(p *parser) slog.Level {
	switch strings.ToLower(p.str(VarLogLevel)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		p.fail(VarLogLevel, errors.New("must be debug, info, warn or error"))
		return slog.LevelInfo
	}
}

func crossChecks(p *parser, c *Config) {
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		p.fail(VarTLSCertFile, errors.New("TLS cert and key files must be set together"))
	}
	if c.QueueMaxRows < c.BatchMaxRows {
		p.fail(VarQueueMaxRows, errors.New("must be at least TIDEMARK_BATCH_MAX_ROWS"))
	}
	if c.MaxDecompressedBytes < c.MaxBodyBytes {
		p.fail(VarMaxDecompressedBytes, errors.New("must be at least TIDEMARK_MAX_BODY_BYTES"))
	}
	if c.SessionAbsoluteTimeout < c.SessionIdleTimeout {
		p.fail(VarSessionAbsTimeout, errors.New("must be at least the idle timeout"))
	}
	if !usernamePattern.MatchString(c.AdminUsername) {
		p.fail(VarAdminUsername, errors.New("must be 1-64 characters of [A-Za-z0-9._@-]"))
	}
	if c.AdminPassword == "" {
		p.fail(VarAdminPassword, errors.New("must not be empty"))
	}
	if c.BootstrapIngestKey != "" && len(c.BootstrapIngestKey) < minBootstrapKey {
		p.fail(VarBootstrapIngestKey, fmt.Errorf("must be at least %d characters", minBootstrapKey))
	}
}
