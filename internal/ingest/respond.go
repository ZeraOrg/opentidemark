// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"bytes"
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		http.Error(w, `{"error":"encode"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes()) // the client may have gone away; nothing useful to do
}

// SplitLines splits NDJSON input into non-empty lines (CRLF tolerant), keeping 1-based
// line numbers.
func SplitLines(body []byte) []Line {
	var out []Line
	for i, raw := range bytes.Split(body, []byte("\n")) {
		line := bytes.TrimSpace(raw)
		if len(line) == 0 {
			continue
		}
		out = append(out, Line{Number: i + 1, Data: line})
	}
	return out
}

// Line is one NDJSON line.
type Line struct {
	Number int
	Data   []byte
}
