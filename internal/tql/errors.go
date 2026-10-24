// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"errors"
	"fmt"
	"strings"
)

// Limits enforced by the parser and checker (SPEC §10.3).
const (
	MaxQueryBytes = 8 << 10 // maximum query length in bytes
	MaxDepth      = 32      // maximum nesting of parentheses / unary operators
	MaxValues     = 1000    // maximum values in an `in` list or value group
	MaxStages     = 5       // maximum pipe stages
	MaxByFields   = 10      // maximum `by` fields in stats
	MaxAggregates = 20      // maximum aggregates in one stats stage, counting those inside arithmetic
	MaxAggTerms   = 50      // maximum aggregates and numbers in one stats stage
	MaxFields     = 50      // maximum fields in a `fields` stage
	MaxBuckets    = 10000   // maximum time buckets a timeseries may produce
)

// Error is a query error with a byte position into the query text.
// It is returned for every syntax and semantic problem and is mapped to the
// API error code `invalid_query` with details {pos,len,hint} (SPEC §11.1).
type Error struct {
	Pos  int    // byte offset of the offending token
	Len  int    // length in bytes of the offending token (0 at end of input)
	Msg  string // human-readable message
	Hint string // optional suggestion, e.g. "did you mean 'error'?"
}

// Error implements the error interface.
func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tql: %s (at %d)", e.Msg, e.Pos)
	if e.Hint != "" {
		b.WriteString(" — ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

// Sentinel errors for invalid compile options (not query errors).
var (
	// ErrInvalidOptions is wrapped by Compile when Options are unusable
	// (missing time range, bad database name, bad indexed column, …).
	ErrInvalidOptions = errors.New("tql: invalid compile options")
)

func errAt(pos, n int, format string, args ...any) *Error {
	return &Error{Pos: pos, Len: n, Msg: fmt.Sprintf(format, args...)}
}

func errTok(t token, format string, args ...any) *Error {
	return errAt(t.Pos, t.End-t.Pos, format, args...)
}

func (e *Error) withHint(h string) *Error {
	e.Hint = h
	return e
}

// didYouMean returns a hint naming the closest candidate (edit distance ≤ 2),
// or "" when nothing is close.
func didYouMean(got string, candidates []string) string {
	best, bestD := "", 3
	g := strings.ToLower(got)
	for _, c := range candidates {
		d := editDistance(g, strings.ToLower(c))
		if d < bestD || (d == bestD && best != "" && c < best) {
			best, bestD = c, d
		}
	}
	if best == "" {
		return ""
	}
	return fmt.Sprintf("did you mean '%s'?", best)
}

func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
