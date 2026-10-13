// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want Level
		ok   bool
	}{
		{"verbose", Verbose, true}, {"TRACE", Verbose, true}, {" trc ", Verbose, true}, {"vrb", Verbose, true},
		{"finest", Verbose, true}, {"finer", Verbose, true}, {"0", Verbose, true},
		{"debug", Debug, true}, {"DBG", Debug, true}, {"fine", Debug, true}, {"1", Debug, true},
		{"information", Information, true}, {"Info", Information, true}, {"inf", Information, true},
		{"notice", Information, true}, {"2", Information, true},
		{"warning", Warning, true}, {"WARN", Warning, true}, {"wrn", Warning, true}, {"3", Warning, true},
		{"error", Error, true}, {"err", Error, true}, {"eror", Error, true}, {"4", Error, true},
		{"fatal", Fatal, true}, {"critical", Fatal, true}, {"crit", Fatal, true}, {"ftl", Fatal, true},
		{"emergency", Fatal, true}, {"emerg", Fatal, true}, {"alert", Fatal, true}, {"panic", Fatal, true},
		{"5", Fatal, true},
		{"", Information, false}, {"bogus", Information, false}, {"6", Information, false}, {"-1", Information, false},
	}
	for _, tt := range tests {
		got, ok := ParseLevel(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseLevel(%q) = %v,%v want %v,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
	if ParseLevelOrDefault("nope") != Information {
		t.Error("default should be Information")
	}
}

func TestSyslogLevel(t *testing.T) {
	want := []Level{Fatal, Fatal, Fatal, Error, Warning, Information, Information, Debug}
	for sev, w := range want {
		got, ok := SyslogLevel(sev)
		if !ok || got != w {
			t.Errorf("SyslogLevel(%d) = %v,%v want %v", sev, got, ok, w)
		}
	}
	for _, sev := range []int{-1, 8, 100} {
		if _, ok := SyslogLevel(sev); ok {
			t.Errorf("SyslogLevel(%d) should not be ok", sev)
		}
	}
}

func TestOTLPLevel(t *testing.T) {
	tests := []struct {
		n    int32
		text string
		want Level
	}{
		{1, "", Verbose}, {4, "", Verbose}, {5, "", Debug}, {8, "", Debug}, {9, "", Information},
		{12, "", Information}, {13, "", Warning}, {16, "", Warning}, {17, "", Error}, {20, "", Error},
		{21, "", Fatal}, {24, "", Fatal}, {0, "warn", Warning}, {0, "", Information}, {25, "error", Error},
		{-3, "", Information},
	}
	for _, tt := range tests {
		if got := OTLPLevel(tt.n, tt.text); got != tt.want {
			t.Errorf("OTLPLevel(%d,%q) = %v want %v", tt.n, tt.text, got, tt.want)
		}
	}
}

func TestLevelString(t *testing.T) {
	names := []string{"Verbose", "Debug", "Information", "Warning", "Error", "Fatal"}
	for i, l := range AllLevels() {
		if l.String() != names[i] || !l.Valid() {
			t.Errorf("level %d: %q", i, l.String())
		}
	}
	if Level(42).String() != "Information" || Level(42).Valid() {
		t.Error("out of range level")
	}
}

func TestNormalizeStream(t *testing.T) {
	tests := map[string]string{
		"":                "default",
		"   ":             "default",
		"API":             "api",
		"my stream/1":     "my-stream-1",
		"_hidden":         "hidden",
		"..":              "default",
		"logs-app.prod_1": "logs-app.prod_1",
		"Ünïcode":         "n-code",
		"filebeat-8.11.0": "filebeat-8.11.0",
	}
	for in, want := range tests {
		got := NormalizeStream(in)
		if got != want {
			t.Errorf("NormalizeStream(%q) = %q want %q", in, got, want)
		}
		if !ValidStreamName(got) {
			t.Errorf("NormalizeStream(%q) = %q is not valid", in, got)
		}
	}
	long := NormalizeStream("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz")
	if len(long) != MaxStreamNameLen || !ValidStreamName(long) {
		t.Errorf("long name %q", long)
	}
}

func TestValidStreamName(t *testing.T) {
	valid := []string{"a", "default", "api-1", "x.y_z"}
	invalid := []string{"", "-a", "A", "a b", "a'b", "a;drop", string(make([]byte, 65))}
	for _, s := range valid {
		if !ValidStreamName(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range invalid {
		if ValidStreamName(s) {
			t.Errorf("%q should be invalid", s)
		}
	}
}

func TestComputeEventType(t *testing.T) {
	a := ComputeEventType("User {Id} logged in", "User 1 logged in")
	b := ComputeEventType("User {Id} logged in", "User 2 logged in")
	if a != b || len(a) != 8 {
		t.Errorf("template hash mismatch %q %q", a, b)
	}
	if ComputeEventType("", "x") == ComputeEventType("", "y") {
		t.Error("message fallback should differ")
	}
	// FNV-1a 32 of the empty string is 0x811c9dc5.
	if got := ComputeEventType("", ""); got != "811c9dc5" {
		t.Errorf("empty = %s", got)
	}
}

func TestSourceValid(t *testing.T) {
	for _, s := range []Source{SourceLogs, SourceSpans, SourceMetrics} {
		if !s.Valid() {
			t.Errorf("%s invalid", s)
		}
	}
	if Source("x").Valid() {
		t.Error("x valid")
	}
}
