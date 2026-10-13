// SPDX-License-Identifier: Apache-2.0

package repotest

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// clickhouseUsers is the part of deploy/clickhouse/users.d/tidemark.xml this test reads.
type clickhouseUsers struct {
	Users struct {
		Entries []struct {
			XMLName xml.Name
			Grants  []string `xml:"grants>query"`
		} `xml:",any"`
	} `xml:"users"`
}

// ciGrant matches "GRANT … TO <user>;" lines of the CI workflow.
var ciGrant = regexp.MustCompile(`(?m)^\s*(GRANT .+?) TO (\w+);\s*$`)

// TestCIClickHouseGrantsMatchUsersD keeps the SQL that creates the ClickHouse users in CI
// (.github/workflows/ci.yml) in sync with deploy/clickhouse/users.d (SPEC §15.8, §18).
func TestCIClickHouseGrantsMatchUsersD(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "deploy", "clickhouse", "users.d", "tidemark.xml"))
	if err != nil {
		t.Fatal(err)
	}
	var cfg clickhouseUsers
	if err := xml.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{}
	for _, u := range cfg.Users.Entries {
		want[u.XMLName.Local] = slices.Sorted(slices.Values(u.Grants))
	}
	ci, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, m := range ciGrant.FindAllStringSubmatch(string(ci), -1) {
		got[m[2]] = append(got[m[2]], m[1])
	}
	if len(want) == 0 {
		t.Fatal("no users found in users.d")
	}
	for user, grants := range want {
		if g := slices.Sorted(slices.Values(got[user])); !slices.Equal(g, grants) {
			t.Errorf("%s: CI grants %q, users.d grants %q", user, g, grants)
		}
	}
	for user := range got {
		if _, ok := want[user]; !ok {
			t.Errorf("CI grants to unknown user %s", user)
		}
	}
}
