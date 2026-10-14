// SPDX-License-Identifier: Apache-2.0

package model

import (
	"slices"
	"testing"
)

func TestStreamScopeConstructors(t *testing.T) {
	tests := []struct {
		name               string
		s                  StreamScope
		set, all, empty    bool
		names              []string
		str                string
		allowsA, allowsZed bool
	}{
		{"zero", StreamScope{}, false, false, false, nil, "unset", false, false},
		{"all", AllStreams(), true, true, false, nil, "all", true, true},
		{"none", NoStreams(), true, false, true, nil, "[]", false, false},
		{"only empty", OnlyStreams(), true, false, true, []string{}, "[]", false, false},
		{"only blank", OnlyStreams(""), true, false, true, []string{}, "[]", false, false},
		{"only sorted dedup", OnlyStreams("b", "a", "b"), true, false, false, []string{"a", "b"}, "[a b]", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.s
			if s.IsSet() != tc.set || s.All() != tc.all || s.Empty() != tc.empty {
				t.Errorf("set/all/empty = %v/%v/%v", s.IsSet(), s.All(), s.Empty())
			}
			if got := s.Names(); !slices.Equal(got, tc.names) || (tc.names == nil) != (got == nil) {
				t.Errorf("Names = %#v, want %#v", got, tc.names)
			}
			if s.String() != tc.str {
				t.Errorf("String = %q, want %q", s.String(), tc.str)
			}
			if s.Allows("a") != tc.allowsA || s.Allows("zed") != tc.allowsZed {
				t.Errorf("Allows(a)=%v Allows(zed)=%v", s.Allows("a"), s.Allows("zed"))
			}
		})
	}
}

func TestStreamScopeNamesIsACopy(t *testing.T) {
	s := OnlyStreams("a", "b")
	n := s.Names()
	n[0] = "z"
	if !s.Allows("a") || s.Allows("z") {
		t.Error("Names exposed the internal slice")
	}
}

func TestStreamScopeIntersect(t *testing.T) {
	ab, bc := OnlyStreams("a", "b"), OnlyStreams("b", "c")
	tests := []struct {
		name string
		s, o StreamScope
		want string
	}{
		{"unset left", StreamScope{}, AllStreams(), "unset"},
		{"unset right", AllStreams(), StreamScope{}, "unset"},
		{"all all", AllStreams(), AllStreams(), "all"},
		{"all restricted", AllStreams(), ab, "[a b]"},
		{"restricted all", ab, AllStreams(), "[a b]"},
		{"overlap", ab, bc, "[b]"},
		{"disjoint", ab, OnlyStreams("x"), "[]"},
		{"none", ab, NoStreams(), "[]"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Intersect(tc.o).String(); got != tc.want {
				t.Errorf("Intersect = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestStreamScopeSubset(t *testing.T) {
	ab := OnlyStreams("a", "b")
	tests := []struct {
		name string
		s, o StreamScope
		want bool
	}{
		{"unset", StreamScope{}, AllStreams(), false},
		{"of unset", ab, StreamScope{}, false},
		{"all of all", AllStreams(), AllStreams(), true},
		{"all of restricted", AllStreams(), ab, false},
		{"restricted of all", ab, AllStreams(), true},
		{"equal", ab, OnlyStreams("b", "a"), true},
		{"smaller", OnlyStreams("a"), ab, true},
		{"larger", ab, OnlyStreams("a"), false},
		{"none of none", NoStreams(), NoStreams(), true},
		{"none of restricted", NoStreams(), ab, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.Subset(tc.o); got != tc.want {
				t.Errorf("%s ⊆ %s = %v, want %v", tc.s, tc.o, got, tc.want)
			}
		})
	}
}
