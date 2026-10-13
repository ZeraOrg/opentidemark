// SPDX-License-Identifier: Apache-2.0

package logging

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	l := New(&buf, slog.LevelDebug)
	l.Info("got key tmk_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcd", "header", "Bearer tmt_ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210zyxw",
		"err", errors.New("bad key tmk_secretsecretsecret"), "n", 3)
	out := buf.String()
	for _, secret := range []string{"ABCDEFGHIJKLMNOP", "ZYXWVUTSRQ", "secretsecret"} {
		if strings.Contains(out, secret) {
			t.Errorf("leaked %q in %s", secret, out)
		}
	}
	if !strings.Contains(out, "tmk_ABCD…") || !strings.Contains(out, `"n":3`) {
		t.Errorf("unexpected output %s", out)
	}
	buf.Reset()
	New(&buf, slog.LevelWarn).Info("hidden")
	if buf.Len() != 0 {
		t.Error("level not applied")
	}
}
