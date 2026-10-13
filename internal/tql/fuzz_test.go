// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"errors"
	"testing"

	"github.com/ZeraOrg/tidemark/internal/model"
)

// FuzzParse checks that no input panics, that errors are positioned inside the
// query, that canonical printing round-trips, and that compiled SQL never
// contains user-controlled text (SPEC §18).
func FuzzParse(f *testing.F) {
	seeds := []string{
		"", "*", "timeout", `"connection refused"`, "level:error", "level >= warning",
		"service:checkout*  and not env:dev", "status_code:(500 or 502 or 503)", "user.email:*",
		`path:/\/api\/v[12]\/orders/`, "Service = 'payments-svc' and StatusCode != 200",
		"level:error | stats count() by service | sort count desc", "level:error | top 10 exception_type",
		"* | stats count() by level span 5m", "elapsed:* | stats p95(elapsed), avg(elapsed) by service, route span 1m",
		"source spans | service:checkout-api and status:error | stats count() by name",
		"source metrics | name:x | stats rate(value) by service span 1m | where rate_value > 1",
		"x in [1, 'a'] and contains(m, 'x') ci", "`we'ird`:\"a'b\" -- ;", "a:(x or y and z)", "!a && (b || c)",
		"@timestamp > now-15m", "x:$var", "* | fields a, b | limit 3", "x:\\*\\(", "x like '%a%' ci",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	o := goldenOptions()
	o.IndexedFields = map[model.Source]map[string]string{model.SourceLogs: {"k": "f_k"}}
	f.Fuzz(func(t *testing.T, in string) {
		q, err := Parse(in)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("Parse(%q) returned untyped error %v", in, err)
			}
			if e.Pos < 0 || e.Pos > len(in) || e.Len < 0 || e.Pos+e.Len > len(in) {
				t.Fatalf("Parse(%q) error out of range: %+v", in, e)
			}
			return
		}
		canon := q.String()
		again, err := Parse(canon)
		if err != nil {
			t.Fatalf("canonical %q of %q does not parse: %v", canon, in, err)
		}
		if again.String() != canon {
			t.Fatalf("canonical not stable: %q -> %q", canon, again.String())
		}
		_ = Check(q)
		c, err := Compile(q, o)
		if err == nil {
			assertSafeSQL(t, in, c.SQL, c.Args, "")
		}
		if w, args, err := CompileWhere(q, o); err == nil {
			assertSafeSQL(t, in, w, args, "")
		}
		if s, err := Substitute(q, map[string][]string{"var": {"a", "b"}}); err == nil {
			_, _ = Compile(s, o) // must not panic
		}
		_ = Complete(in, len(in)/2, model.SourceLogs, nil)
	})
}
