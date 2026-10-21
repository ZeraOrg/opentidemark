// SPDX-License-Identifier: Apache-2.0

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateSettings(t *testing.T) {
	tests := []struct {
		name      string
		env       MapEnv
		wantCheck bool
		wantProxy string // proxy host, "" = none
		wantErr   bool
	}{
		{"defaults", MapEnv{}, true, "", false},
		{"opt out", MapEnv{VarUpdateCheck: "false"}, false, "", false},
		{"proxy", MapEnv{VarUpdateProxy: "http://proxy.internal:3128"}, true, "proxy.internal:3128", false},
		{"proxy with credentials", MapEnv{VarUpdateProxy: "https://u:s3cret@proxy.internal"}, true, "proxy.internal", false},
		{"bad check", MapEnv{VarUpdateCheck: "maybe"}, true, "", true},
		{"bad proxy scheme", MapEnv{VarUpdateProxy: "socks5://u:s3cret@proxy:1080"}, true, "", true},
		{"proxy with path", MapEnv{VarUpdateProxy: "http://u:s3cret@proxy/x"}, true, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := load(t, nil, tc.env)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalid) || strings.Contains(err.Error(), "s3cret") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			host := ""
			if c.Update.Proxy != nil {
				host = c.Update.Proxy.Host
			}
			if c.Update.Check != tc.wantCheck || host != tc.wantProxy {
				t.Fatalf("update = %+v", c.Update)
			}
		})
	}
}

func TestUpdateProxyFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proxy")
	if err := os.WriteFile(path, []byte("http://u:p@proxy.internal:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := load(t, nil, MapEnv{VarUpdateProxy + "_FILE": path})
	if err != nil {
		t.Fatal(err)
	}
	if c.Update.Proxy == nil || c.Update.Proxy.Host != "proxy.internal:8080" || c.Update.Proxy.User.Username() != "u" {
		t.Fatalf("proxy = %v", c.Update.Proxy)
	}
}
