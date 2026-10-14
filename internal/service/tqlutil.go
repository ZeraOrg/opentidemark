// SPDX-License-Identifier: Apache-2.0

package service

import (
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/ZeraOrg/opentidemark/internal/model"
	"github.com/ZeraOrg/opentidemark/internal/tql"
)

// reSourceClause detects an explicit `source …` clause at the start of a query.
var reSourceClause = regexp.MustCompile(`(?i)^\s*source\s`)

// parseFor parses text and, when it has no explicit source clause, applies src (signals keep
// their source in a column; /traces searches spans). An explicit different source is an error.
func parseFor(src model.Source, text string) (*tql.Query, error) {
	q, err := tql.Parse(text)
	if err != nil {
		return nil, err
	}
	explicit := !q.InterpretedAsText && reSourceClause.MatchString(text)
	if explicit && q.Source != src {
		return nil, &tql.Error{Pos: 0, Len: len("source"), Msg: "this query must use source " + string(src)}
	}
	if !explicit && q.Source != src {
		cp := *q
		cp.Source = src
		q = &cp
	}
	return q, nil
}

// cursorJSON is the opaque cursor payload (SPEC §11.3).
type cursorJSON struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

// EncodeCursor returns the opaque base64url form of a keyset cursor.
func EncodeCursor(c *tql.Cursor) string {
	if c == nil {
		return ""
	}
	raw, err := json.Marshal(cursorJSON{T: c.Timestamp.UTC(), ID: c.ID})
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// maxCursorBytes bounds the decoded cursor size.
const maxCursorBytes = 512

// DecodeCursor parses an opaque cursor; "" yields nil.
func DecodeCursor(s string) (*tql.Cursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) > maxCursorBytes {
		return nil, invalid("cursor", "is not a valid cursor")
	}
	var c cursorJSON
	if err := json.Unmarshal(raw, &c); err != nil || c.T.IsZero() || c.ID == "" {
		return nil, invalid("cursor", "is not a valid cursor")
	}
	return &tql.Cursor{Timestamp: c.T, ID: c.ID}, nil
}

// reBareValue matches values that can be inserted into TQL without quotes.
var reBareValue = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

// tqlValue renders a facet value for insertion after `field:` (autocomplete): bare when
// safe, otherwise double-quoted with `\`, `"` and `*` escaped.
func tqlValue(v string) string {
	switch strings.ToLower(v) {
	case "and", "or", "not", "true", "false", "null":
		return `"` + v + `"`
	}
	if reBareValue.MatchString(v) {
		return v
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `*`, `\*`)
	return `"` + r.Replace(v) + `"`
}
