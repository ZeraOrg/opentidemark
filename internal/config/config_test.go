// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func load(t *testing.T, args []string, env MapEnv) (Config, error) {
	t.Helper()
	if _, ok := env[VarDataDir]; !ok {
		env[VarDataDir] = t.TempDir()
	}
	return Load(args, env)
}

func TestDefaults(t *testing.T) {
	c, err := load(t, nil, MapEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.SeqAddr != ":5341" || c.ESAddr != ":9200" || c.OTLPHTTPAddr != ":4318" ||
		c.OTLPGRPCAddr != ":4317" || c.PublicURL.String() != "http://localhost:8080" ||
		c.ClickHouse.Addrs[0] != "localhost:9000" || c.ClickHouse.Database != "tidemark" ||
		c.ClickHouse.WriterUser != "tidemark_writer" || c.ClickHouse.ReaderUser != "tidemark_reader" ||
		c.AdminUsername != "admin" || c.AdminPassword != "admin" || !c.AdminPasswordIsDefault ||
		c.BatchMaxRows != 10000 || c.BatchMaxWait != time.Second || c.QueueMaxRows != 200000 ||
		c.MaxBodyBytes != 10485760 || c.MaxDecompressedBytes != 104857600 || c.QueryTimeout != 30*time.Second ||
		c.QueryMaxRows != 10000 || c.SessionIdleTimeout != 12*time.Hour || c.SessionAbsoluteTimeout != 168*time.Hour ||
		c.SMTP.Port != 587 || !c.SMTP.StartTLS || c.LogLevel != slog.LevelInfo || c.MaxStreams != 200 ||
		c.MetricsEnabled || c.AllowPrivateWebhooks || c.TLSEnabled() || c.SecureCookies() {
		t.Errorf("defaults: %+v", c)
	}
	if c.SecretKeyFile != filepath.Join(c.DataDir, "secret.key") || c.EnvFile != filepath.Join(c.DataDir, EnvFileName) {
		t.Errorf("paths %s %s", c.SecretKeyFile, c.EnvFile)
	}
}

func TestOSDefaultDataDir(t *testing.T) {
	got := defaultDataDir(MapEnv{"ProgramData": `D:\PD`, "HOME": "/home/u"})
	switch runtime.GOOS {
	case "linux":
		if got != "/var/lib/tidemark" {
			t.Error(got)
		}
	case "windows":
		if got != `D:\PD\Tidemark` {
			t.Error(got)
		}
	default:
		if got != filepath.Join("/home/u", ".tidemark") {
			t.Error(got)
		}
	}
}

func TestPrecedence(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, EnvFileName)
	content := "# comment\nexport TIDEMARK_HTTP_ADDR=:1111\nTIDEMARK_SEQ_ADDR=\"\"\nTIDEMARK_LOG_LEVEL='debug'\nTIDEMARK_ES_ADDR=:3333\nOTHER=x\n"
	if err := os.WriteFile(envFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	env := MapEnv{VarDataDir: dir, VarESAddr: ":4444", VarOTLPGRPCAddr: ":5555"}
	c, err := Load([]string{"--otlp-grpc-addr", ":6666", "--max-streams=7"}, env)
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":1111" || c.SeqAddr != "" || c.LogLevel != slog.LevelDebug || c.ESAddr != ":4444" ||
		c.OTLPGRPCAddr != ":6666" || c.MaxStreams != 7 {
		t.Errorf("precedence: %+v", c)
	}
}

func TestEnvFileErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(nil, MapEnv{VarDataDir: dir, VarEnvFile: filepath.Join(dir, "missing.env")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("explicit missing env file: %v", err)
	}
	bad := filepath.Join(dir, "bad.env")
	_ = os.WriteFile(bad, []byte("NOEQUALS\n"), 0o600)
	if _, err := Load(nil, MapEnv{VarDataDir: dir, VarEnvFile: bad}); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Errorf("bad env file: %v", err)
	}
}

func TestSecretFiles(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	_ = os.WriteFile(pw, []byte("s3cret-writer\n"), 0o600)
	c, err := load(t, nil, MapEnv{VarCHWriterPassword + "_FILE": pw, VarAdminPassword + "_FILE": pw})
	if err != nil {
		t.Fatal(err)
	}
	if c.ClickHouse.WriterPassword != "s3cret-writer" || c.AdminPassword != "s3cret-writer" || c.AdminPasswordIsDefault {
		t.Errorf("%q %q", c.ClickHouse.WriterPassword, c.AdminPassword)
	}
	if _, err := load(t, nil, MapEnv{VarMetricsToken: "x", VarMetricsToken + "_FILE": pw}); err == nil {
		t.Error("both direct and _FILE accepted")
	}
	if _, err := load(t, nil, MapEnv{VarMetricsToken + "_FILE": filepath.Join(dir, "nope")}); err == nil {
		t.Error("missing secret file accepted")
	}
}

func TestValidation(t *testing.T) {
	tests := []struct {
		name string
		env  MapEnv
		args []string
		want string
	}{
		{"bad addr", MapEnv{VarHTTPAddr: "8080"}, nil, VarHTTPAddr},
		{"empty http addr", MapEnv{VarHTTPAddr: ""}, nil, VarHTTPAddr},
		{"bad port", MapEnv{VarSeqAddr: ":99999"}, nil, VarSeqAddr},
		{"tls half", MapEnv{VarTLSCertFile: "c.pem"}, nil, VarTLSCertFile},
		{"public url", MapEnv{VarPublicURL: "ftp://x"}, nil, VarPublicURL},
		{"public url relative", MapEnv{VarPublicURL: "/x"}, nil, VarPublicURL},
		{"ch addr", MapEnv{VarCHAddr: "a:1,b"}, nil, VarCHAddr},
		{"ch db", MapEnv{VarCHDatabase: "tm-1"}, nil, VarCHDatabase},
		{"ch users", MapEnv{VarCHReaderUser: ""}, nil, VarCHWriterUser},
		{"ch tls", MapEnv{VarCHTLS: "maybe"}, nil, VarCHTLS},
		{"batch rows", MapEnv{VarBatchMaxRows: "0"}, nil, VarBatchMaxRows},
		{"batch rows text", MapEnv{VarBatchMaxRows: "lots"}, nil, VarBatchMaxRows},
		{"batch wait", MapEnv{VarBatchMaxWait: "1ms"}, nil, VarBatchMaxWait},
		{"batch wait text", MapEnv{VarBatchMaxWait: "soon"}, nil, VarBatchMaxWait},
		{"queue < batch", MapEnv{VarQueueMaxRows: "10", VarBatchMaxRows: "100"}, nil, VarQueueMaxRows},
		{"decompressed < body", MapEnv{VarMaxBodyBytes: "4096", VarMaxDecompressedBytes: "2048"}, nil, VarMaxDecompressedBytes},
		{"abs < idle", MapEnv{VarSessionIdleTimeout: "2h", VarSessionAbsTimeout: "1h"}, nil, VarSessionAbsTimeout},
		{"proxies", MapEnv{VarTrustedProxies: "10.0.0.0/8, nope"}, nil, VarTrustedProxies},
		{"log level", MapEnv{VarLogLevel: "trace"}, nil, VarLogLevel},
		{"admin user", MapEnv{VarAdminUsername: "bad user"}, nil, VarAdminUsername},
		{"admin password empty", MapEnv{VarAdminPassword: ""}, nil, VarAdminPassword},
		{"bootstrap short", MapEnv{VarBootstrapIngestKey: "short"}, nil, VarBootstrapIngestKey},
		{"smtp port", MapEnv{VarSMTPPort: "0"}, nil, VarSMTPPort},
		{"max streams", MapEnv{VarMaxStreams: "0"}, nil, VarMaxStreams},
		{"unknown flag", MapEnv{}, []string{"--nope"}, "nope"},
		{"stray arg", MapEnv{}, []string{"serve"}, "unexpected argument"},
	}
	for _, tt := range tests {
		_, err := load(t, tt.args, tt.env)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", tt.name, err)
		}
	}
}

func TestParsedValues(t *testing.T) {
	c, err := load(t, nil, MapEnv{
		VarTrustedProxies: "10.0.0.0/8, 192.168.1.7, 2001:db8::/32", VarPublicURL: "https://tm.example.com/",
		VarCHAddr: "ch1:9000, ch2:9000", VarTLSCertFile: "c.pem", VarTLSKeyFile: "k.pem", VarLogLevel: "WARN",
		VarMetricsEnabled: "true", VarBootstrapIngestKey: "tmk_0123456789abcdefghijklmnop", "TIDEMARK_TYPO": "1",
		VarAllowPrivateWebhooks: "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 3 || c.TrustedProxies[1].String() != "192.168.1.7/32" || !c.SecureCookies() ||
		c.PublicURL.String() != "https://tm.example.com" || len(c.ClickHouse.Addrs) != 2 || !c.TLSEnabled() ||
		c.LogLevel != slog.LevelWarn || !c.MetricsEnabled || !c.AllowPrivateWebhooks {
		t.Errorf("%+v", c)
	}
	if len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "TIDEMARK_TYPO") {
		t.Errorf("warnings %v", c.Warnings)
	}
	for _, lvl := range []string{"debug", "error"} {
		if _, err := load(t, nil, MapEnv{VarLogLevel: lvl}); err != nil {
			t.Error(err)
		}
	}
}

func TestFlagNames(t *testing.T) {
	if FlagName(VarHTTPAddr) != "http-addr" || FlagName(VarSessionAbsTimeout) != "session-absolute-timeout" {
		t.Error("flag names")
	}
	if !isKnown(VarMetricsToken+"_FILE") || isKnown(VarHTTPAddr+"_FILE") || isKnown("TIDEMARK_NOPE") {
		t.Error("isKnown")
	}
	if len(OSEnv{}.Keys()) == 0 {
		t.Error("OSEnv keys")
	}
	if _, ok := (OSEnv{}).Lookup("TIDEMARK_DEFINITELY_UNSET"); ok {
		t.Error("OSEnv lookup")
	}
}
