// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHADefaults(t *testing.T) {
	c, err := load(t, nil, MapEnv{})
	if err != nil {
		t.Fatal(err)
	}
	if c.NodeID == "" || c.Meta.Driver != "sqlite" || c.HA() || c.ClickHouse.Cluster != "" || c.ClickHouse.InsertQuorum != "auto" ||
		!c.WAL.Enabled || c.WAL.MaxBytes != 10737418240 || c.WAL.Sync != "batch" || c.Drain != 10*time.Second {
		t.Errorf("defaults: %+v", c)
	}
	if c.WAL.Dir != filepath.Join(c.DataDir, "wal") || c.BackupDir != filepath.Join(c.DataDir, "backups") {
		t.Errorf("paths: %s %s", c.WAL.Dir, c.BackupDir)
	}
}

func TestHASettings(t *testing.T) {
	dsnFile := filepath.Join(t.TempDir(), "dsn")
	if err := os.WriteFile(dsnFile, []byte("postgres://tm@db/tidemark\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := load(t, nil, MapEnv{VarNodeID: "node-2", VarMetaDriver: "postgres", VarMetaDSN + "_FILE": dsnFile,
		VarCHCluster: "tidemark_cluster", VarCHInsertQuorum: "2", VarWALEnabled: "false", VarWALSync: "none",
		VarWALMaxBytes: "1048576", VarDrainSeconds: "0", VarWALDir: "/var/wal", VarBackupDir: "/var/bk"})
	if err != nil {
		t.Fatal(err)
	}
	if c.NodeID != "node-2" || !c.HA() || c.Meta.DSN != "postgres://tm@db/tidemark" || c.ClickHouse.Cluster != "tidemark_cluster" ||
		c.ClickHouse.InsertQuorum != "2" || c.WAL.Enabled || c.WAL.Sync != "none" || c.WAL.MaxBytes != 1<<20 || c.Drain != 0 {
		t.Errorf("parsed: %+v", c)
	}
}

// TestNodeIDMustStartWithLetterOrDigit pins the SPEC §7 rule for TIDEMARK_NODE_ID: a letter or
// digit first, then up to 63 of [A-Za-z0-9._-].
func TestNodeIDMustStartWithLetterOrDigit(t *testing.T) {
	const maxLen = 64
	tests := []struct {
		id    string
		valid bool
	}{
		{"a", true},
		{"7", true},
		{"tm-node1", true},
		{"Node_2.example-com", true},
		{"a" + strings.Repeat("-", maxLen-1), true},
		{"-node", false},
		{".node", false},
		{"_node", false},
		{"bad id!", false},
		{"a" + strings.Repeat("b", maxLen), false},
	}
	for _, tc := range tests {
		c, err := load(t, nil, MapEnv{VarNodeID: tc.id})
		switch {
		case tc.valid && (err != nil || c.NodeID != tc.id):
			t.Errorf("%q: want accepted, got %v", tc.id, err)
		case !tc.valid && (!errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "must start with a letter or digit")):
			t.Errorf("%q: want rejected with the SPEC §7 rule, got %v", tc.id, err)
		}
	}
}

func TestHAValidation(t *testing.T) {
	tests := map[string]MapEnv{
		VarMetaDriver:     {VarMetaDriver: "mysql"},
		VarMetaDSN:        {VarMetaDriver: "postgres"},
		VarNodeID:         {VarNodeID: "bad id!"},
		VarCHCluster:      {VarCHCluster: "x'; DROP"},
		VarCHInsertQuorum: {VarCHInsertQuorum: "most"},
		VarWALSync:        {VarWALSync: "always"},
		VarWALMaxBytes:    {VarWALMaxBytes: "10"},
		VarDrainSeconds:   {VarDrainSeconds: "9999"},
	}
	for key, env := range tests {
		_, err := load(t, nil, env)
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: %v", key, err)
		}
	}
}
