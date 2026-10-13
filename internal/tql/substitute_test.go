// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"errors"
	"strings"
	"testing"
)

func TestSubstitute(t *testing.T) {
	vars := map[string][]string{
		"one":   {"checkout"},
		"two":   {"a", "b"},
		"all":   {"*"},
		"mixed": {"a", "*"},
		"none":  {},
		"num":   {"500"},
		"lvl":   {"warning"},
		"dur":   {"250ms"},
		"evil":  {`x' OR 1=1 --`, "`; DROP"},
	}
	tests := []struct {
		in, want string
	}{
		{`service:$one`, `service:"checkout"`},
		{`service:$two`, `service:"a" or service:"b"`},
		{`service:$all`, `*`},
		{`service:$mixed`, `*`},
		{`service:$none`, `*`},
		{`level:error and service:$all`, `level:"error"`},
		{`service:$all and level:error`, `level:"error"`},
		{`level:error or service:$all`, `*`},
		{`not service:$all`, `*`},
		{`not service:$one`, `not service:"checkout"`},
		{`status:$num`, `status:500`},
		{`status: >= $num`, `status:>= 500`},
		{`level >= $lvl`, `level >= "warning"`},
		{`service = $two`, `service = "a" or service = "b"`},
		{`service != $two ci`, `service != "a" ci and service != "b" ci`},
		{`service = $all`, `*`},
		{`service in [$two, c]`, `service in ["a", "b", "c"]`},
		{`service not in [$all]`, `*`},
		{`service in [x]`, `service in ["x"]`},
		{`contains(message, $two)`, `contains(message, "a") or contains(message, "b")`},
		{`contains(message, $all)`, `*`},
		{`contains(message, 'x') and has(y)`, `contains(message, "x") and has(y)`},
		{`source spans | duration > $dur`, `source spans | duration > "250ms"`},
		{`x:$evil`, `x:"x' OR 1=1 --" or x:"` + "`; DROP" + `"`},
		{`* | stats count() by a | where a:$all`, `* | stats count() by a`},
		{`* | stats count() by a | where a:$one | sort a`, `* | stats count() by a | where a:"checkout" | sort a asc`},
		{`x:"literal $one"`, `x:"literal $one"`},
		{`level:error and service:$two`, `level:"error" and (service:"a" or service:"b")`},
		{`"free" and y:1`, `"free" and y:1`},
	}
	for _, tt := range tests {
		q, err := Parse(tt.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		before := q.String()
		s, err := Substitute(q, vars)
		if err != nil {
			t.Errorf("Substitute(%q): %v", tt.in, err)
			continue
		}
		if got := s.String(); got != tt.want {
			t.Errorf("Substitute(%q) = %s, want %s", tt.in, got, tt.want)
		}
		if q.String() != before {
			t.Errorf("Substitute mutated its input: %s", q.String())
		}
		if s.Text != s.String() {
			t.Errorf("Text not updated: %q", s.Text)
		}
		if _, err := Compile(s, goldenOptions()); err != nil {
			t.Errorf("Compile(Substitute(%q)): %v", tt.in, err)
		}
	}
}

func TestSubstituteErrors(t *testing.T) {
	many := make([]string, MaxValues+1)
	for i := range many {
		many[i] = "v"
	}
	vars := map[string][]string{"two": {"a", "b"}, "many": many, "half": many[:MaxValues/2+1]}
	tests := []struct {
		in  string
		pos int
		msg string
	}{
		{`x:$missing`, 2, "unknown variable $missing"},
		{`x > $two`, 4, "several values"},
		{`x: > $two`, 5, "several values"},
		{`x:$many`, 2, "too many values"},
		{`x in [$half, $half]`, 0, "too many values"},
		{`x = $missing`, 4, "unknown variable"},
		{`x in [$missing]`, 6, "unknown variable"},
		{`contains(x, $missing)`, 12, "unknown variable"},
		{`a:1 and x:$missing`, 10, "unknown variable"},
		{`x:$missing and a:1`, 2, "unknown variable"},
		{`not x:$missing`, 6, "unknown variable"},
		{`* | where x:$missing`, 12, "unknown variable"},
	}
	for _, tt := range tests {
		q, err := Parse(tt.in)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.in, err)
		}
		_, err = Substitute(q, vars)
		var e *Error
		if !errors.As(err, &e) || e.Pos != tt.pos || !strings.Contains(e.Msg, tt.msg) {
			t.Errorf("Substitute(%q) = %v; want pos %d %q", tt.in, err, tt.pos, tt.msg)
		}
	}
	if _, err := Substitute(nil, nil); err == nil {
		t.Error("nil query should fail")
	}
}
