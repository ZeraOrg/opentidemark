// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ZeraOrg/opentidemark/internal/meta"
)

func TestVersionAndUnknown(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"version"}, nil, &out, &out); err != nil || !strings.HasPrefix(out.String(), "tidemark ") {
		t.Errorf("version: %v %q", err, out.String())
	}
	if err := run([]string{"bogus"}, nil, &out, &out); err == nil {
		t.Error("unknown command accepted")
	}
	if err := run([]string{"admin"}, nil, &out, &out); err == nil {
		t.Error("admin without subcommand accepted")
	}
	if err := run([]string{"admin", "reset-password"}, nil, &out, &out); err == nil {
		t.Error("reset without username accepted")
	}
	if runtime.GOOS != "windows" {
		if err := run([]string{"service", "install"}, nil, &out, &out); err == nil {
			t.Error("service command outside Windows accepted")
		}
	}
}

func TestServeRejectsBadConfig(t *testing.T) {
	var out bytes.Buffer
	err := serveContext(context.Background(), []string{"--http-addr", "nope", "--data-dir", t.TempDir()}, &out, false)
	if err == nil || !strings.Contains(err.Error(), "TIDEMARK_HTTP_ADDR") {
		t.Errorf("bad config: %v", err)
	}
}

func TestAdminResetPassword(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := meta.Open(ctx, filepath.Join(dir, meta.DBFileName))
	if err != nil {
		t.Fatal(err)
	}
	users := meta.NewUsers(db)
	u := meta.User{Username: "admin", PasswordHash: "x", Role: meta.RoleAdmin, FailedLogins: 9}
	_ = users.Create(ctx, &u)
	_ = db.Close()

	var out bytes.Buffer
	if err := run([]string{"admin", "reset-password", "--username", "admin", "--data-dir", dir}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 || len(lines[1]) != 24 {
		t.Fatalf("output %q", out.String())
	}
	out.Reset()
	stdin := strings.NewReader("Chosen-Password-2026\n")
	if err := run([]string{"admin", "reset-password", "--username=admin", "--password-stdin", "--data-dir", dir}, stdin, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "must be changed") {
		t.Errorf("output %q", out.String())
	}
	db, _ = meta.Open(ctx, filepath.Join(dir, meta.DBFileName))
	defer db.Close()
	got, _ := meta.NewUsers(db).Get(ctx, u.ID)
	if !got.MustChangePassword || got.FailedLogins != 0 || got.PasswordHash == "x" {
		t.Errorf("user after reset %+v", got)
	}
	if err := run([]string{"admin", "reset-password", "--username", "nobody", "--data-dir", dir}, nil, &out, &out); err == nil {
		t.Error("unknown user accepted")
	}
	if err := run([]string{"admin", "reset-password", "--username", "admin", "--password-stdin", "--data-dir", dir}, strings.NewReader("weak\n"), &out, &out); err == nil {
		t.Error("weak password accepted")
	}
}
