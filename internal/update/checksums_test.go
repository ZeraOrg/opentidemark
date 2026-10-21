// SPDX-License-Identifier: Apache-2.0

package update

import (
	"errors"
	"strings"
	"testing"
)

func TestParseChecksums(t *testing.T) {
	a := strings.Repeat("a", 64)
	b := strings.Repeat("0123456789abcdef", 4)
	good := a + "  tidemark_1.0.0_linux_amd64.tar.gz\n" + b + "  tidemark_1.0.0_windows_amd64.zip\n"
	sums, err := ParseChecksums([]byte(good))
	if err != nil || len(sums) != 2 {
		t.Fatalf("ParseChecksums = %v, %v", sums, err)
	}
	if got, ok := sums.Lookup("tidemark_1.0.0_windows_amd64.zip"); !ok || got[0] != 0x01 || got[31] != 0xef {
		t.Errorf("Lookup = %x, %v", got, ok)
	}
	if _, ok := sums.Lookup("tidemark_1.0.0_linux_arm64.tar.gz"); ok {
		t.Error("Lookup found an unlisted asset")
	}
	bad := map[string]string{
		"empty":            "",
		"no final newline": strings.TrimSuffix(good, "\n"),
		"blank line":       good + "\n",
		"CRLF":             strings.ReplaceAll(good, "\n", "\r\n"),
		"upper-case hex":   strings.ToUpper(a) + "  x.zip\n",
		"short hash":       a[:63] + "  x.zip\n",
		"non-hex":          strings.Repeat("g", 64) + "  x.zip\n",
		"one space":        a + " x.zip\n",
		"binary marker":    a + " *x.zip\n",
		"path in name":     a + "  dir/x.zip\n",
		"traversal name":   a + "  ../x.zip\n",
		"no name":          a + "  \n",
		"duplicate":        a + "  x.zip\n" + b + "  x.zip\n",
		"trailing text":    a + "  x.zip extra\n",
	}
	for name, text := range bad {
		if _, err := ParseChecksums([]byte(text)); !errors.Is(err, ErrChecksums) {
			t.Errorf("%s: err = %v, want ErrChecksums", name, err)
		}
	}
}
