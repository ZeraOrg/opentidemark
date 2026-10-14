// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/tidemark/internal/model"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// goldenTime is the fixed reference time for golden compilation.
var goldenTime = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func goldenOptions() Options {
	return Options{
		Database: "tidemark",
		From:     goldenTime.Add(-time.Hour),
		To:       goldenTime,
		IndexedFields: map[model.Source]map[string]string{
			model.SourceLogs: {"RequestPath": "f_requestpath"},
		},
		Streams: model.AllStreams(),
	}
}

func formatArg(a any) string {
	switch v := a.(type) {
	case string:
		return fmt.Sprintf("string %q", v)
	case time.Time:
		return "time " + v.Format(time.RFC3339Nano)
	default:
		return fmt.Sprintf("%T %v", v, v)
	}
}

func formatError(q string, err error) string {
	var e *Error
	if !errors.As(err, &e) {
		return "error (untyped): " + err.Error() + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "error: pos=%d len=%d msg=%q", e.Pos, e.Len, e.Msg)
	if e.Hint != "" {
		fmt.Fprintf(&b, " hint=%q", e.Hint)
	}
	b.WriteString("\n  | " + q + "\n  | " + strings.Repeat(" ", e.Pos) + strings.Repeat("^", max(e.Len, 1)) + "\n")
	return b.String()
}

func goldenEntry(line string) string {
	q, opts, whereOnly := line, goldenOptions(), false
	if rest, ok := strings.CutPrefix(q, "%cursor "); ok {
		q = rest
		opts.Cursor = &Cursor{Timestamp: goldenTime.Add(-time.Minute), ID: "0192f0a0-0000-7000-8000-000000000001"}
	}
	if rest, ok := strings.CutPrefix(q, "%streams "); ok {
		var names string
		names, q, _ = strings.Cut(rest, " ")
		opts.Streams = model.NoStreams()
		if names != "none" {
			opts.Streams = model.OnlyStreams(strings.Split(names, ",")...)
		}
	}
	if rest, ok := strings.CutPrefix(q, "%where "); ok {
		q, whereOnly = rest, true
	}
	var b strings.Builder
	b.WriteString("=== " + line + "\n")
	p, err := Parse(q)
	if err != nil {
		return b.String() + formatError(q, err)
	}
	if whereOnly {
		sql, args, err := CompileWhere(p, opts)
		if err != nil {
			return b.String() + formatError(q, err)
		}
		writeSQL(&b, sql, args)
		return b.String()
	}
	c, err := Compile(p, opts)
	if err != nil {
		return b.String() + formatError(q, err)
	}
	fmt.Fprintf(&b, "kind: %s  source: %s  limit: %d", c.Kind, c.Source, c.Limit)
	if c.Span > 0 {
		fmt.Fprintf(&b, "  span: %s", c.Span)
	}
	if c.InterpretedAsText {
		b.WriteString("  interpretedAsText")
	}
	b.WriteString("\ncanonical: " + p.String() + "\n")
	writeSQL(&b, c.SQL, c.Args)
	cols := make([]string, len(c.Columns))
	for i, col := range c.Columns {
		cols[i] = col.Name + ":" + string(col.Type)
	}
	b.WriteString("columns: " + strings.Join(cols, ", ") + "\n")
	return b.String()
}

func writeSQL(b *strings.Builder, sql string, args []any) {
	b.WriteString("sql: " + sql + "\nargs:\n")
	for i, a := range args {
		fmt.Fprintf(b, "  %d: %s\n", i, formatArg(a))
	}
}

func readQueries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(filepath.Clean(path)) //nolint:gosec // test input from testdata, not user controlled
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }() // read-only file; close error is irrelevant
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGolden(t *testing.T) {
	inputs, err := filepath.Glob(filepath.Join("testdata", "*.tql"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no golden inputs: %v", err)
	}
	for _, in := range inputs {
		name := strings.TrimSuffix(filepath.Base(in), ".tql")
		t.Run(name, func(t *testing.T) {
			var b strings.Builder
			for _, q := range readQueries(t, in) {
				entry := goldenEntry(q)
				if name == "errors" && !strings.Contains(entry, "\nerror: ") {
					t.Errorf("query %q in errors.tql did not fail:\n%s", q, entry)
				}
				if name != "errors" && strings.Contains(entry, "\nerror: ") {
					t.Errorf("query %q failed:\n%s", q, entry)
				}
				b.WriteString(entry + "\n")
			}
			golden := filepath.Join("testdata", name+".golden")
			if *update {
				if err := os.WriteFile(golden, []byte(b.String()), 0o600); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(filepath.Clean(golden)) //nolint:gosec // golden file in testdata
			if err != nil {
				t.Fatalf("%v (run go test -update)", err)
			}
			if got := b.String(); got != string(want) {
				t.Errorf("golden mismatch for %s; run `go test ./internal/tql -run TestGolden -update` and review the diff\n%s",
					golden, firstDiff(string(want), got))
			}
		})
	}
}

func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(wl), len(gl)); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n- %s\n+ %s", i+1, w, g)
		}
	}
	return ""
}
