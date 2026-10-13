// SPDX-License-Identifier: Apache-2.0

// Package logging builds the structured slog logger. String attributes are scrubbed of
// connection keys and API tokens (tmk_…/tmt_… are reduced to their 8-character prefix).
package logging

import (
	"io"
	"log/slog"

	"github.com/ZeraOrg/tidemark/internal/auth"
)

// New returns a JSON logger at level writing to w.
func New(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level, ReplaceAttr: redact}))
}

func redact(_ []string, a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		a.Value = slog.StringValue(auth.RedactInText(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			a.Value = slog.StringValue(auth.RedactInText(err.Error()))
		}
	}
	return a
}
