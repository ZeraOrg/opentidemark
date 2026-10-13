// SPDX-License-Identifier: Apache-2.0

package tql

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenKind classifies a lexical token.
type tokenKind int

const (
	tEOF      tokenKind = iota
	tError              // lexical error; Text holds the message
	tIdent              // identifier: [A-Za-z_@][A-Za-z0-9_.\-@]*
	tWord               // any other bare word (free text / literal)
	tNumber             // number with optional duration unit
	tString             // quoted string ('…' or "…"); Text is unescaped
	tBacktick           // backtick-quoted identifier; Text is unescaped
	tStar               // lone *
	tLParen             // (
	tRParen             // )
	tLBrack             // [
	tRBrack             // ]
	tComma              // ,
	tPipe               // |
	tColon              // :
	tOp                 // = == != <> < <= > >=
	tAndAnd             // &&
	tOrOr               // ||
	tBang               // !
	// value-mode only
	tRegex // /…/ ; Text is the pattern
	tBare  // bare value right of ':'
)

// token is a lexical token. Pos/End are byte offsets into the query.
type token struct {
	Kind    tokenKind
	Pos     int
	End     int
	Text    string  // unescaped text (strings, words, regex body, op)
	Wild    bool    // value contains an unescaped * (wildcard)
	Pattern string  // ILIKE pattern when Wild
	Num     float64 // value for tNumber
	Unit    string  // duration unit for tNumber ("" if none)
}

var (
	reIdent  = regexp.MustCompile(`^[A-Za-z_@][A-Za-z0-9_.\-@]*$`)
	reNumber = regexp.MustCompile(`^(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)(ns|us|ms|s|m|h|d)?$`)
	reVar    = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*$`)
)

// lexer produces tokens on demand at a given offset. It is stateless apart
// from the source text, so the parser can look ahead and switch modes freely.
type lexer struct{ src string }

func isSpaceAt(s string, i int) (bool, int) {
	r, n := utf8.DecodeRuneInString(s[i:])
	return unicode.IsSpace(r), n
}

func (lx lexer) skipSpace(pos int) int {
	for pos < len(lx.src) {
		sp, n := isSpaceAt(lx.src, pos)
		if !sp {
			break
		}
		pos += n
	}
	return pos
}

// next lexes one token in normal (expression) mode starting at pos.
func (lx lexer) next(pos int) token {
	s := lx.src
	pos = lx.skipSpace(pos)
	if pos >= len(s) {
		return token{Kind: tEOF, Pos: len(s), End: len(s)}
	}
	if t, ok := lx.punct(pos); ok {
		return t
	}
	switch s[pos] {
	case '\'', '"':
		return lx.quoted(pos, false)
	case '`':
		return lx.backtick(pos)
	}
	return lx.word(pos)
}

func (lx lexer) punct(pos int) (token, bool) {
	s := lx.src
	two := ""
	if pos+2 <= len(s) {
		two = s[pos : pos+2]
	}
	switch two {
	case "==", "!=", "<>", "<=", ">=":
		return token{Kind: tOp, Pos: pos, End: pos + 2, Text: two}, true
	case "&&":
		return token{Kind: tAndAnd, Pos: pos, End: pos + 2, Text: two}, true
	case "||":
		return token{Kind: tOrOr, Pos: pos, End: pos + 2, Text: two}, true
	}
	var k tokenKind
	switch s[pos] {
	case '(':
		k = tLParen
	case ')':
		k = tRParen
	case '[':
		k = tLBrack
	case ']':
		k = tRBrack
	case ',':
		k = tComma
	case '|':
		k = tPipe
	case ':':
		k = tColon
	case '=', '<', '>':
		k = tOp
	case '!':
		k = tBang
	default:
		return token{}, false
	}
	return token{Kind: k, Pos: pos, End: pos + 1, Text: s[pos : pos+1]}, true
}

// wordEnd returns the end of a bare word in normal mode.
func (lx lexer) wordEnd(pos int) int {
	s := lx.src
	i := pos
	for i < len(s) {
		c := s[i]
		if strings.IndexByte("()[],|:=<>'\"`", c) >= 0 {
			break
		}
		if (c == '!' && (i == pos || (i+1 < len(s) && s[i+1] == '='))) ||
			(c == '&' && i+1 < len(s) && s[i+1] == '&') {
			break
		}
		sp, n := isSpaceAt(s, i)
		if sp {
			break
		}
		i += n
	}
	return i
}

func (lx lexer) word(pos int) token {
	end := lx.wordEnd(pos)
	text := lx.src[pos:end]
	t := token{Kind: tWord, Pos: pos, End: end, Text: text}
	switch {
	case text == "*":
		t.Kind = tStar
	case reIdent.MatchString(text):
		t.Kind = tIdent
	default:
		if num, unit, ok := parseNumber(text); ok {
			t.Kind, t.Num, t.Unit = tNumber, num, unit
		}
	}
	return t
}

func parseNumber(text string) (float64, string, bool) {
	m := reNumber.FindStringSubmatch(text)
	if m == nil {
		return 0, "", false
	}
	f, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, "", false
	}
	return f, m[2], true
}

// litRune is a rune of a value together with whether it was escaped.
type litRune struct {
	r   rune
	esc bool
}

func unescapeString(c byte) (rune, bool) {
	switch c {
	case '\\':
		return '\\', true
	case '\'':
		return '\'', true
	case '"':
		return '"', true
	case 'n':
		return '\n', true
	case 't':
		return '\t', true
	case '*':
		return '*', true
	}
	return 0, false
}

// quoted lexes a '…' or "…" string. In value mode an unescaped * makes the
// string a wildcard pattern.
func (lx lexer) quoted(pos int, valueMode bool) token {
	s := lx.src
	q := s[pos]
	var runes []litRune
	i := pos + 1
	for i < len(s) {
		c := s[i]
		if c == q {
			return finishValue(token{Kind: tString, Pos: pos, End: i + 1}, runes, valueMode)
		}
		if c == '\\' && i+1 < len(s) {
			if r, ok := unescapeString(s[i+1]); ok {
				runes = append(runes, litRune{r, true})
				i += 2
				continue
			}
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		runes = append(runes, litRune{r, false})
		i += n
	}
	return token{Kind: tError, Pos: pos, End: len(s), Text: "unterminated string"}
}

func (lx lexer) backtick(pos int) token {
	s := lx.src
	var b strings.Builder
	for i := pos + 1; i < len(s); i++ {
		switch {
		case s[i] == '`' && i+1 < len(s) && s[i+1] == '`':
			b.WriteByte('`')
			i++
		case s[i] == '`':
			if b.Len() == 0 {
				return token{Kind: tError, Pos: pos, End: i + 1, Text: "empty field name"}
			}
			return token{Kind: tBacktick, Pos: pos, End: i + 1, Text: b.String()}
		default:
			b.WriteByte(s[i])
		}
	}
	return token{Kind: tError, Pos: pos, End: len(s), Text: "unterminated quoted field name"}
}

// finishValue fills Text, Wild and Pattern from the collected runes.
func finishValue(t token, runes []litRune, valueMode bool) token {
	var text strings.Builder
	wild := false
	for _, lr := range runes {
		text.WriteRune(lr.r)
		if valueMode && lr.r == '*' && !lr.esc {
			wild = true
		}
	}
	t.Text = text.String()
	if !wild {
		return t
	}
	var p strings.Builder
	for _, lr := range runes {
		switch {
		case lr.r == '*' && !lr.esc:
			p.WriteByte('%')
		case lr.r == '?' && !lr.esc:
			p.WriteByte('_')
		case lr.r == '%' || lr.r == '_' || lr.r == '\\':
			p.WriteByte('\\')
			p.WriteRune(lr.r)
		default:
			p.WriteRune(lr.r)
		}
	}
	t.Wild, t.Pattern = true, p.String()
	return t
}

// nextValue lexes one token in value mode (right of ':'), starting at pos.
func (lx lexer) nextValue(pos int) token {
	s := lx.src
	pos = lx.skipSpace(pos)
	if pos >= len(s) {
		return token{Kind: tEOF, Pos: len(s), End: len(s)}
	}
	switch s[pos] {
	case '(':
		return token{Kind: tLParen, Pos: pos, End: pos + 1, Text: "("}
	case ')':
		return token{Kind: tRParen, Pos: pos, End: pos + 1, Text: ")"}
	case '|':
		return token{Kind: tPipe, Pos: pos, End: pos + 1, Text: "|"}
	case '\'', '"':
		return lx.quoted(pos, true)
	case '<', '>':
		if pos+1 < len(s) && s[pos+1] == '=' {
			return token{Kind: tOp, Pos: pos, End: pos + 2, Text: s[pos : pos+2]}
		}
		return token{Kind: tOp, Pos: pos, End: pos + 1, Text: s[pos : pos+1]}
	case '/':
		if t, ok := lx.regex(pos); ok {
			return t
		}
	}
	return lx.bare(pos)
}

func isValueDelim(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	if s[i] == '(' || s[i] == ')' || s[i] == '|' {
		return true
	}
	sp, _ := isSpaceAt(s, i)
	return sp
}

// regex lexes /…/ when the closing slash is followed by a delimiter;
// otherwise the text is a bare value (e.g. a path like /api/v1).
func (lx lexer) regex(pos int) (token, bool) {
	s := lx.src
	var b strings.Builder
	for i := pos + 1; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '/':
			b.WriteByte('/')
			i++
		case s[i] == '/':
			if !isValueDelim(s, i+1) {
				return token{}, false
			}
			return token{Kind: tRegex, Pos: pos, End: i + 1, Text: b.String()}, true
		default:
			b.WriteByte(s[i])
		}
	}
	return token{}, false
}

func isBareEscape(c byte) bool { return strings.IndexByte("*?()|\\ ", c) >= 0 }

// bare lexes a bare value: a run of characters up to whitespace, '(', ')' or
// '|'. A backslash escapes one of * ? ( ) | \ and space.
func (lx lexer) bare(pos int) token {
	s := lx.src
	var runes []litRune
	i := pos
	for !isValueDelim(s, i) {
		if s[i] == '\\' && i+1 < len(s) && isBareEscape(s[i+1]) {
			runes = append(runes, litRune{rune(s[i+1]), true})
			i += 2
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		runes = append(runes, litRune{r, false})
		i += n
	}
	t := finishValue(token{Kind: tBare, Pos: pos, End: i}, runes, true)
	if !t.Wild && t.Text == s[pos:i] {
		if num, unit, ok := parseNumber(t.Text); ok {
			t.Kind, t.Num, t.Unit = tNumber, num, unit
		}
	}
	return t
}

// tokens lexes the whole input in normal mode (used by autocomplete).
func (lx lexer) tokens() []token {
	var out []token
	pos := 0
	for {
		t := lx.next(pos)
		out = append(out, t)
		if t.Kind == tEOF || t.End <= pos {
			return out
		}
		pos = t.End
	}
}
