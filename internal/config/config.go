// SPDX-License-Identifier: Apache-2.0

// Package config loads OpenTidemark's configuration (SPEC §7, §17.3): an optional env file,
// then environment variables, then command-line flags; secrets may come from <VAR>_FILE.
package config

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvFileName is the default env file inside the data directory.
const EnvFileName = "tidemark.env"

// Env looks up environment variables (injected for tests).
type Env interface {
	Lookup(key string) (string, bool)
	Keys() []string
}

// OSEnv reads the process environment.
type OSEnv struct{}

// Lookup implements Env.
func (OSEnv) Lookup(k string) (string, bool) { return os.LookupEnv(k) }

// Keys implements Env.
func (OSEnv) Keys() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		out = append(out, k)
	}
	return out
}

// MapEnv is an Env backed by a map.
type MapEnv map[string]string

// Lookup implements Env.
func (m MapEnv) Lookup(k string) (string, bool) { v, ok := m[k]; return v, ok }

// Keys implements Env.
func (m MapEnv) Keys() []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ClickHouse settings.
type ClickHouse struct {
	Addrs          []string
	Database       string
	WriterUser     string
	WriterPassword string
	ReaderUser     string
	ReaderPassword string
	TLS            bool
	Cluster        string // "" = single node; else ON CLUSTER + replicated engines (SPEC §17.5)
	InsertQuorum   string // "auto" or a replica count; used in cluster mode only
}

// Meta selects the metadata database (SPEC §8.4).
type Meta struct {
	Driver string // "sqlite" | "postgres"
	DSN    string // PostgreSQL DSN
}

// WAL configures the ingestion write-ahead log (SPEC §9.8).
type WAL struct {
	Enabled       bool
	Dir           string
	MaxBytes      int64
	Sync          string // "batch" | "none"
	ExitOnFailure bool   // a failed fsync stops the process so the supervisor restarts it
	MinFreeBytes  int64  // free-space floor of the WAL's file system; 0 = off
}

// SMTP settings.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	StartTLS bool
}

// Config is the validated configuration.
type Config struct {
	HTTPAddr               string
	SeqAddr                string
	ESAddr                 string
	OTLPHTTPAddr           string
	OTLPGRPCAddr           string
	TLSCertFile            string
	TLSKeyFile             string
	PublicURL              *url.URL
	DataDir                string
	ClickHouse             ClickHouse
	AdminUsername          string
	AdminPassword          string
	AdminPasswordIsDefault bool
	BootstrapIngestKey     string
	BatchMaxRows           int
	BatchMaxWait           time.Duration
	QueueMaxRows           int
	DeadLetterMaxBytes     int64 // cap of the dead-letter folder; 0 = unlimited
	MaxBodyBytes           int64
	MaxDecompressedBytes   int64
	QueryTimeout           time.Duration
	QueryMaxRows           int
	SessionIdleTimeout     time.Duration
	SessionAbsoluteTimeout time.Duration
	TrustedProxies         []netip.Prefix
	AllowPrivateWebhooks   bool
	SMTP                   SMTP
	LogLevel               slog.Level
	MaxStreams             int
	MetricsEnabled         bool
	MetricsToken           string
	SecretKeyFile          string
	EnvFile                string
	NodeID                 string
	Meta                   Meta
	WAL                    WAL
	BackupDir              string
	Drain                  time.Duration
	// Warnings lists non-fatal issues (unknown TIDEMARK_ variables) for logging.
	Warnings []string
}

// TLSEnabled reports whether listeners use TLS.
func (c Config) TLSEnabled() bool { return c.TLSCertFile != "" }

// SecureCookies reports whether cookies get the Secure flag (PUBLIC_URL is https).
func (c Config) SecureCookies() bool { return c.PublicURL.Scheme == "https" }

// ErrInvalid wraps every validation error.
var ErrInvalid = errors.New("invalid configuration")

// Load builds the configuration from args (without the program name) and env.
func Load(args []string, env Env) (Config, error) {
	flags, err := parseFlags(args)
	if err != nil {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	vals, warnings, err := layer(flags, env)
	if err != nil {
		return Config{}, err
	}
	cfg, err := build(vals)
	if err != nil {
		return Config{}, err
	}
	cfg.Warnings = warnings
	return cfg, nil
}

// parseFlags returns the explicitly set flags keyed by variable name.
func parseFlags(args []string) (map[string]string, error) {
	fs := flag.NewFlagSet("tidemark", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	ptrs := map[string]*string{}
	for _, v := range allVars {
		ptrs[v] = fs.String(FlagName(v), "", "sets "+v)
	}
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	out := map[string]string{}
	fs.Visit(func(f *flag.Flag) {
		for v, p := range ptrs {
			if FlagName(v) == f.Name {
				out[v] = *p
			}
		}
	})
	return out, nil
}

// layer merges defaults < env file < environment < flags and resolves _FILE secrets.
func layer(flags map[string]string, env Env) (map[string]string, []string, error) {
	envVals := map[string]string{}
	var warnings []string
	for _, k := range env.Keys() {
		if !strings.HasPrefix(k, Prefix) {
			continue
		}
		if !isKnown(k) {
			warnings = append(warnings, "unknown variable "+k)
			continue
		}
		envVals[k], _ = env.Lookup(k)
	}
	pick := func(k string) (string, bool) {
		if v, ok := flags[k]; ok {
			return v, true
		}
		v, ok := envVals[k]
		return v, ok
	}
	dataDir, ok := pick(VarDataDir)
	if !ok {
		dataDir = defaultDataDir(env)
	}
	envFile, explicit := pick(VarEnvFile)
	if !explicit {
		envFile = filepath.Join(dataDir, EnvFileName)
	}
	fileVals, err := readEnvFile(envFile, explicit)
	if err != nil {
		return nil, nil, err
	}
	vals := map[string]string{VarSecretKeyFile: filepath.Join(dataDir, "secret.key"), VarDataDir: dataDir, VarEnvFile: envFile,
		VarWALDir: filepath.Join(dataDir, "wal"), VarBackupDir: filepath.Join(dataDir, "backups"), VarNodeID: defaultNodeID()}
	for k, v := range defaults {
		vals[k] = v
	}
	merged := map[string]string{}
	for _, src := range []map[string]string{fileVals, envVals, flags} {
		for k, v := range src {
			merged[k] = v
		}
	}
	if err := resolveSecretFiles(merged); err != nil {
		return nil, nil, err
	}
	for k, v := range merged {
		vals[k] = v
	}
	if _, set := merged[VarAdminPassword]; !set {
		vals[adminPasswordDefaultMarker] = "1"
	}
	return vals, warnings, nil
}

// adminPasswordDefaultMarker records that the admin password was not configured.
const adminPasswordDefaultMarker = "__admin_password_default"

func resolveSecretFiles(vals map[string]string) error {
	for _, k := range secretVars {
		path, hasFile := vals[k+"_FILE"]
		if !hasFile {
			continue
		}
		delete(vals, k+"_FILE")
		if _, direct := vals[k]; direct {
			return fmt.Errorf("%w: both %s and %s_FILE are set", ErrInvalid, k, k)
		}
		b, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return fmt.Errorf("%w: %s_FILE: %w", ErrInvalid, k, err)
		}
		vals[k] = strings.TrimRight(string(b), "\r\n")
	}
	return nil
}

// readEnvFile parses KEY=VALUE lines ("export " prefix, # comments and quotes allowed).
// A missing file is only an error when it was configured explicitly.
func readEnvFile(path string, required bool) (map[string]string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("%w: env file: %w", ErrInvalid, err)
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			return nil, fmt.Errorf("%w: env file %s line %d: expected KEY=VALUE", ErrInvalid, path, n)
		}
		k, v = strings.TrimSpace(k), unquote(strings.TrimSpace(v))
		if strings.HasPrefix(k, Prefix) && isKnown(k) {
			out[k] = v
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: env file: %w", ErrInvalid, err)
	}
	return out, nil
}

// defaultNodeID is the host name (TIDEMARK_NODE_ID default); "tidemark" when it is unknown.
func defaultNodeID() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "tidemark"
	}
	return h
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}
