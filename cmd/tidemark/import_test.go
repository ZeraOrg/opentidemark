// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/ZeraOrg/opentidemark/internal/importer"
)

func TestParseImport(t *testing.T) {
	env := map[string]string{"ES_PW": "s3cret", "SEQ_KEY": "k3y"}
	getenv := func(k string) string { return env[k] }
	query := filepath.Join(t.TempDir(), "q.json")
	if err := os.WriteFile(query, []byte(`{"term":{"service":"api"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		kind    string
		args    []string
		wantErr string
		check   func(t *testing.T, cfg map[string]any, files, rest []string)
	}{
		{name: "clef files and config flags", kind: "clef",
			args: []string{"a.clef", "--stream", "app", "--data-dir", "/d", "b.gz", "--log-level=debug"},
			check: func(t *testing.T, _ map[string]any, files, rest []string) {
				if !reflect.DeepEqual(files, []string{"a.clef", "b.gz"}) || !reflect.DeepEqual(rest, []string{"--data-dir", "/d", "--log-level=debug"}) {
					t.Fatalf("files %v rest %v", files, rest)
				}
			}},
		{name: "clef needs files", kind: "clef", args: []string{"--stream", "app"}, wantErr: "usage"},
		{name: "elastic with env secret and query file", kind: "elastic",
			args: []string{"--url", "https://es:9200", "--index", "logs-*", "--auth-type", "basic", "--username", "u",
				"--password-env", "ES_PW", "--from", "2026-01-01T00:00:00Z", "--query-file", query, "--page-size", "100"},
			check: func(t *testing.T, cfg map[string]any, _, _ []string) {
				auth, _ := cfg["auth"].(map[string]any)
				if auth["password"] != "s3cret" || cfg["index"] != "logs-*" || cfg["pageSize"] != float64(100) ||
					cfg["from"] != "2026-01-01T00:00:00Z" || cfg["query"] == nil {
					t.Fatalf("config %v", cfg)
				}
			}},
		{name: "missing env secret", kind: "elastic", args: []string{"--url", "http://es", "--index", "x", "--password-env", "NOPE"}, wantErr: "NOPE"},
		{name: "bad time", kind: "seq", args: []string{"--url", "http://seq", "--from", "yesterday"}, wantErr: "RFC 3339"},
		{name: "seq flags", kind: "seq", args: []string{"--url", "http://seq", "--api-key-env", "SEQ_KEY", "--signals", "--no-events", "--filter", "@Level = 'Error'"},
			check: func(t *testing.T, cfg map[string]any, _, _ []string) {
				if cfg["apiKey"] != "k3y" || cfg["importSignals"] != true || cfg["importEvents"] != false || cfg["filter"] != "@Level = 'Error'" {
					t.Fatalf("config %v", cfg)
				}
			}},
		{name: "flag of another kind", kind: "seq", args: []string{"--url", "http://seq", "--index", "x"}, wantErr: "not supported"},
		{name: "kibana takes one file", kind: "kibana", args: []string{"a.ndjson", "b.ndjson"}, wantErr: "usage"},
		{name: "kibana cannot detach", kind: "kibana", args: []string{"a.ndjson", "--detach"}, wantErr: "not supported"},
		{name: "stray argument", kind: "elastic", args: []string{"--url", "http://es", "--index", "x", "oops"}, wantErr: "unexpected"},
		{name: "bad page size", kind: "seq", args: []string{"--url", "http://seq", "--page-size", "0"}, wantErr: "page-size"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req, rest, err := parseImport(tc.kind, tc.args, getenv)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]any
			if len(req.Config) > 0 {
				if err := json.Unmarshal(req.Config, &cfg); err != nil {
					t.Fatal(err)
				}
			}
			tc.check(t, cfg, req.Files, rest)
		})
	}
}

// TestImportCommandDetached queues a clef-file job, lists, inspects and cancels it without a
// ClickHouse server (the server would run a detached job).
func TestImportCommandDetached(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "events.clef")
	if err := os.WriteFile(file, []byte(`{"@t":"2026-01-01T00:00:00Z","@m":"hello"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--data-dir", dir, "--clickhouse-addr", "127.0.0.1:1"}
	var out bytes.Buffer
	if err := run(append([]string{"import", "clef", file, "--stream", "app", "--detach"}, base...), nil, &out, &out); err != nil {
		t.Fatalf("import: %v\n%s", err, out.String())
	}
	id := regexp.MustCompile(`import job (\S+) \(clef-file\) queued`).FindStringSubmatch(out.String())
	if id == nil {
		t.Fatalf("output %q", out.String())
	}
	out.Reset()
	if err := run(append([]string{"import", "list"}, base...), nil, &out, &out); err != nil || !strings.Contains(out.String(), id[1]) {
		t.Fatalf("list: %v %q", err, out.String())
	}
	out.Reset()
	if err := run(append([]string{"import", "status", id[1]}, base...), nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	var job map[string]any
	if err := json.Unmarshal(out.Bytes(), &job); err != nil || job["requestedBy"] != "system:cli" || job["kind"] != importer.KindCLEFFile ||
		strings.Contains(out.String(), dir) {
		t.Fatalf("status %v %s (server paths must not be shown)", err, out.String())
	}
	out.Reset()
	if err := run(append([]string{"import", "cancel", id[1]}, base...), nil, &out, &out); err != nil || !strings.Contains(out.String(), "cancel") {
		t.Fatalf("cancel: %v %q", err, out.String())
	}
	for _, args := range [][]string{{"import"}, {"import", "status"}, {"import", "nope"}} {
		if err := run(append(args, base...), nil, &out, &out); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}
