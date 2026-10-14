// SPDX-License-Identifier: Apache-2.0

package model

import (
	"slices"
	"strings"
)

// StreamScope is the set of streams whose data a caller may read (SPEC §15.10). The zero value
// is unset and must be rejected by every reader (fail closed); use AllStreams, OnlyStreams or
// NoStreams. A scope is immutable: its methods never modify the receiver.
type StreamScope struct {
	set   bool
	all   bool
	names []string // sorted, unique; only meaningful when !all
}

// AllStreams is the scope of every stream (admins, system callers).
func AllStreams() StreamScope { return StreamScope{set: true, all: true} }

// NoStreams is the empty scope: queries return no rows.
func NoStreams() StreamScope { return StreamScope{set: true} }

// OnlyStreams is the scope of the given stream names (sorted and de-duplicated; empty names
// are ignored). Without names it equals NoStreams.
func OnlyStreams(names ...string) StreamScope {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	slices.Sort(out)
	return StreamScope{set: true, names: slices.Compact(out)}
}

// IsSet reports whether the scope was constructed (the zero value is not).
func (s StreamScope) IsSet() bool { return s.set }

// All reports whether the scope covers every stream.
func (s StreamScope) All() bool { return s.set && s.all }

// Empty reports whether the scope is set and covers no stream.
func (s StreamScope) Empty() bool { return s.set && !s.all && len(s.names) == 0 }

// Names returns a copy of the stream names of a restricted scope (nil for All or unset).
func (s StreamScope) Names() []string {
	if !s.set || s.all {
		return nil
	}
	return slices.Clone(s.names)
}

// Allows reports whether the scope covers the stream name.
func (s StreamScope) Allows(name string) bool {
	if !s.set {
		return false
	}
	if s.all {
		return true
	}
	_, ok := slices.BinarySearch(s.names, name)
	return ok
}

// Intersect returns the streams covered by both scopes; it is unset when either is unset.
func (s StreamScope) Intersect(o StreamScope) StreamScope {
	switch {
	case !s.set || !o.set:
		return StreamScope{}
	case s.all:
		return o
	case o.all:
		return s
	}
	out := []string{}
	for _, n := range s.names {
		if o.Allows(n) {
			out = append(out, n)
		}
	}
	return StreamScope{set: true, names: out}
}

// Subset reports whether every stream of s is covered by o. Unset scopes are never subsets
// and have no subsets.
func (s StreamScope) Subset(o StreamScope) bool {
	switch {
	case !s.set || !o.set:
		return false
	case o.all:
		return true
	case s.all:
		return false
	}
	for _, n := range s.names {
		if !o.Allows(n) {
			return false
		}
	}
	return true
}

// String renders the scope for logs and test output: "unset", "all" or "[a b]".
func (s StreamScope) String() string {
	switch {
	case !s.set:
		return "unset"
	case s.all:
		return "all"
	}
	return "[" + strings.Join(s.names, " ") + "]"
}
