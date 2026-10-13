// SPDX-License-Identifier: Apache-2.0

package normalize

import "strings"

// Render renders a Serilog/Seq message template with the given properties (SPEC §9.2).
// Supported tokens: {Name}, {@Name}, {$Name}, {Name:fmt}, {Name,align}, positional {0};
// "{{" and "}}" are escapes. Format and alignment are ignored. Missing properties and
// malformed tokens are rendered verbatim.
func Render(template string, props map[string]any) string {
	var b strings.Builder
	b.Grow(len(template))
	for i := 0; i < len(template); {
		c := template[i]
		switch {
		case c == '{' && i+1 < len(template) && template[i+1] == '{':
			b.WriteByte('{')
			i += 2
		case c == '}' && i+1 < len(template) && template[i+1] == '}':
			b.WriteByte('}')
			i += 2
		case c == '{':
			end := strings.IndexByte(template[i:], '}')
			if end < 0 {
				b.WriteString(template[i:])
				return b.String()
			}
			token := template[i : i+end+1]
			b.WriteString(renderToken(token, props))
			i += end + 1
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// renderToken renders one "{…}" token or returns it verbatim.
func renderToken(token string, props map[string]any) string {
	name, ok := tokenName(token[1 : len(token)-1])
	if !ok {
		return token
	}
	v, found := props[name]
	if !found {
		return token
	}
	return Text(v)
}

// tokenName extracts and validates the property name of a token body.
func tokenName(body string) (string, bool) {
	if strings.HasPrefix(body, "@") || strings.HasPrefix(body, "$") {
		body = body[1:]
	}
	if cut := strings.IndexAny(body, ",:"); cut >= 0 {
		body = body[:cut]
	}
	if body == "" {
		return "", false
	}
	for _, r := range body {
		if r != '_' && (r < '0' || r > '9') && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return "", false
		}
	}
	return body, true
}
