// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/meta"
)

func TestCmdFlagsSplit(t *testing.T) {
	f := cmdFlags{"out": true, "meta-only": false}
	own, rest, err := f.split([]string{"--out", "/b", "--data-dir", "/d", "--meta-only", "--log-level=debug"})
	if err != nil || own["out"] != "/b" || own["meta-only"] != "true" || !reflect.DeepEqual(rest, []string{"--data-dir", "/d", "--log-level=debug"}) {
		t.Fatalf("split: %v %v %v", own, rest, err)
	}
	if own, _, _ := f.split([]string{"--out=/x"}); own["out"] != "/x" {
		t.Errorf("--out=: %v", own)
	}
	for _, bad := range [][]string{{"--out"}, {"--meta-only=yes"}} {
		if _, _, err := f.split(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestOpsCommands(t *testing.T) {
	dir := t.TempDir()
	db, err := meta.Open(context.Background(), filepath.Join(dir, meta.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	base := []string{"--data-dir", dir, "--clickhouse-addr", "127.0.0.1:1"}
	var out bytes.Buffer
	usage := [][]string{{"backup"}, {"restore"}, {"migrate", "--status", "--dry-run"}}
	for _, args := range usage {
		if err := run(args, nil, &out, &out); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	backups := t.TempDir()
	if err := run(append([]string{"backup", "--out", backups, "--meta-only"}, base...), nil, &out, &out); err != nil {
		t.Fatalf("backup: %v\n%s", err, out.String())
	}
	entries, _ := os.ReadDir(backups)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "tidemark-backup-") {
		t.Fatalf("backup dir: %v", entries)
	}
	from := filepath.Join(backups, entries[0].Name())
	if err := run(append([]string{"restore", "--from", from, "--force"}, base...), nil, &out, &out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	out.Reset()
	if err := run(append([]string{"migrate", "--status"}, base...), nil, &out, &out); err == nil || !strings.Contains(out.String(), "applied") {
		t.Errorf("migrate --status: %v %q", err, out.String())
	}
	out.Reset()
	if err := run(append([]string{"doctor", "--json"}, base...), nil, &out, &out); err == nil || !strings.Contains(out.String(), `"status": "fail"`) {
		t.Errorf("doctor: %v %q", err, out.String())
	}
}
