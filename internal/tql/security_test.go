// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
)

// trustedQuoted lists the only quoted fragments the compiler itself emits.
var trustedQuoted = regexp.MustCompile("''|'delta'|`count`|`sum`|`min`|`max`")

// assertSafeSQL fails when SQL text contains anything that could only come
// from user input: quotes outside trusted fragments, comments, statement
// separators, or the marker text.
func assertSafeSQL(t testing.TB, q, sql string, args []any, marker string) {
	t.Helper()
	rest := trustedQuoted.ReplaceAllString(sql, "")
	for _, bad := range []string{"'", "\"", "`", "--", ";", "/*", "\\"} {
		if strings.Contains(rest, bad) {
			t.Fatalf("query %q produced SQL containing %q:\n%s", q, bad, sql)
		}
	}
	if marker != "" && strings.Contains(strings.ToLower(sql), strings.ToLower(marker)) {
		t.Fatalf("query %q leaked marker %q into SQL:\n%s", q, marker, sql)
	}
	if n := strings.Count(sql, "?"); n != len(args) {
		t.Fatalf("query %q: %d placeholders for %d args", q, n, len(args))
	}
}

func TestSQLInjection(t *testing.T) {
	const m = "zqinj"
	payloads := []string{
		`'` + m + `' OR 1=1 --`, `"` + m + `"; DROP TABLE events; --`, "`" + m + "`", m + `\' --`,
		m + `ʼ OR ʼ1ʼ=ʼ1`, m + "\u0000", m + "/* c */", m + "; SELECT 1", m + `\`,
	}
	templates := []string{
		`%s`, `x:%s`, `x = %s`, `x != %s ci`, `x > %s`, `x like %s`, `x in [%s, %s]`,
		`contains(x, %s)`, `startswith(x, %s)`, `match(x, %s)`, `x:/%s/`, `x:%s*`,
		"`%s`:1", "`%s` > 3", `* | stats count() by ` + "`%s`", `* | stats avg(` + "`%s`)" + ` as ` + "`%s`",
		"* | stats count() by `%s` | where `%s`:1 | sort `%s`", "* | top 3 `%s` by `%s`", "* | fields `%s`",
		`source spans | %s`, `source metrics | ` + "`%s`" + `:%s | stats rate(value)`,
	}
	o := goldenOptions()
	o.IndexedFields = map[model.Source]map[string]string{model.SourceLogs: {"k": "f_k"}}
	compiled := 0
	for _, p := range payloads {
		quotedS := `'` + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(p) + `'`
		backtick := strings.ReplaceAll(p, "`", "``")
		for _, tmpl := range templates {
			arg := quotedS
			if strings.Contains(tmpl, "`%s`") {
				arg = backtick
			}
			if strings.Contains(tmpl, "/%s/") {
				arg = strings.ReplaceAll(regexp.QuoteMeta(p), "/", `\/`)
			}
			q := strings.ReplaceAll(tmpl, "%s", arg)
			parsed, err := Parse(q)
			if err != nil {
				continue // rejected input is safe
			}
			c, err := Compile(parsed, o)
			if err != nil {
				continue
			}
			assertSafeSQL(t, q, c.SQL, c.Args, m)
			compiled++
			found := false
			for _, a := range c.Args {
				if s, ok := a.(string); ok && strings.Contains(s, m) {
					found = true
				}
			}
			if !found && !strings.Contains(tmpl, "stats count() by `%s` | where") {
				t.Errorf("query %q: payload not found in args %v", q, c.Args)
			}
		}
	}
	if minCompiled := len(payloads) * len(templates) * 3 / 4; compiled < minCompiled {
		t.Errorf("only %d of %d injection queries compiled; the test is not exercising the compiler", compiled, len(payloads)*len(templates))
	}
}

func TestInjectionThroughOptions(t *testing.T) {
	q := mustParse(t, "x")
	for _, db := range []string{"tidemark; DROP", "a.b", "`x`", "1abc", strings.Repeat("a", 65)} {
		if _, err := Compile(q, Options{Database: db, From: goldenTime.Add(-time.Hour), To: goldenTime,
			Streams: model.AllStreams()}); err == nil {
			t.Errorf("database %q accepted", db)
		}
	}
	for _, col := range []string{"f_x; DROP", "props['x']", "F_UPPER", "f_", "message"} {
		o := goldenOptions()
		o.IndexedFields = map[model.Source]map[string]string{model.SourceLogs: {"x": col}}
		if _, err := Compile(q, o); err == nil {
			t.Errorf("indexed column %q accepted", col)
		}
	}
	o := goldenOptions()
	o.Cursor = &Cursor{Timestamp: goldenTime, ID: "x'); DROP TABLE events; --"}
	c := compileQ(t, "x", o)
	assertSafeSQL(t, "cursor", c.SQL, c.Args, "DROP")
	o = goldenOptions()
	o.Streams = model.OnlyStreams("a'); DROP TABLE events; --", "b`;/* c */", "ok")
	c = compileQ(t, "x", o)
	assertSafeSQL(t, "streams", c.SQL, c.Args, "DROP")
	if !strings.Contains(c.SQL, " AND stream IN (?, ?, ?)") {
		t.Errorf("stream scope not parameterised: %s", c.SQL)
	}
}
