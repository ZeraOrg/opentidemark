// SPDX-License-Identifier: Apache-2.0

package importer

import (
	"errors"
	"regexp"
	"strings"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// errNotAFilter is returned for filters that only parse as a free-text fallback or that use
// pipe stages.
var errNotAFilter = errors.New("not a TQL filter")

// seqBuiltins maps Seq's built-in properties to TQL built-ins (SPEC §10.4).
var seqBuiltins = map[string]string{
	"@level": "@level", "@message": "@message", "@messagetemplate": "@template", "@exception": "@exception",
	"@eventtype": "@event_type", "@timestamp": "@timestamp", "@traceid": "@trace_id", "@spanid": "@span_id",
	"@id": "@id",
}

// reSeqIdent matches an @-identifier at the scanner position.
var reSeqIdent = regexp.MustCompile(`^@[A-Za-z][A-Za-z0-9_]*`)

// Seq null checks: `X is not null` ⇒ `X:*`, `X is null` ⇒ `not X:*`.
var (
	reIsNotNull = regexp.MustCompile(`(?i)(^|[\s(])([A-Za-z_@][A-Za-z0-9_.@-]*)\s+is\s+not\s+null\b`)
	reIsNull    = regexp.MustCompile(`(?i)(^|[\s(])([A-Za-z_@][A-Za-z0-9_.@-]*)\s+is\s+null\b`)
)

// reExists rewrites Lucene's _exists_:field to TQL's field:*.
var reExists = regexp.MustCompile(`(?i)\b_exists_:([A-Za-z_@][A-Za-z0-9_.@-]*)`)

// TranslateSeq converts a strict Seq filter expression to TQL. Seq's operators (=, <>, like,
// in […], and/or/not, ci) are TQL syntax already; built-in names are mapped and doubled single
// quotes inside strings become backslash escapes, and `is [not] null` becomes an existence
// test. The result is validated (SPEC §9.9).
func TranslateSeq(filter string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(filter); {
		switch c := filter[i]; c {
		case '\'', '"':
			n := copySeqString(&b, filter[i:], c)
			i += n
		case '@':
			id := reSeqIdent.FindString(filter[i:])
			if id == "" {
				b.WriteByte(c)
				i++
				continue
			}
			i += len(id)
			if mapped, ok := seqBuiltins[strings.ToLower(id)]; ok {
				id = mapped
			}
			b.WriteString(id)
		default:
			b.WriteByte(c)
			i++
		}
	}
	out := reIsNotNull.ReplaceAllString(strings.TrimSpace(b.String()), "$1$2:*")
	out = reIsNull.ReplaceAllString(out, "${1}not $2:*")
	return out, checkFilter(out)
}

// copySeqString copies one quoted string starting at s[0] and returns the bytes consumed.
// Inside single quotes Seq escapes a quote by doubling it.
func copySeqString(b *strings.Builder, s string, q byte) int {
	b.WriteByte(q)
	for i := 1; i < len(s); i++ {
		switch {
		case s[i] == q && q == '\'' && i+1 < len(s) && s[i+1] == q:
			b.WriteString(`\'`)
			i++
		case s[i] == '\\':
			b.WriteString(`\\`)
		case s[i] == q:
			b.WriteByte(q)
			return i + 1
		default:
			b.WriteByte(s[i])
		}
	}
	return len(s)
}

// TranslateKQL converts a Kibana query (KQL, or simple Lucene) to TQL. KQL field:value terms,
// phrases, wildcards, value groups, ranges and boolean operators are TQL syntax; Lucene's
// _exists_:f becomes f:*. Anything else (nested queries, [a TO b] ranges) fails validation.
func TranslateKQL(query string) (string, error) {
	out := strings.TrimSpace(reExists.ReplaceAllString(query, "$1:*"))
	if out == "" {
		out = "*"
	}
	return out, checkFilter(out)
}

// checkFilter accepts text when it parses as a logs filter without stages.
func checkFilter(text string) error {
	if strings.TrimSpace(text) == "" {
		return errNotAFilter
	}
	q, err := tql.Parse(text)
	if err != nil {
		return err
	}
	if q.InterpretedAsText || len(q.Stages) > 0 || (q.Source != "" && q.Source != model.SourceLogs) {
		return errNotAFilter
	}
	return tql.Check(q)
}
