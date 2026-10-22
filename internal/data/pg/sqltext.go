package pg

import (
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/data"
)

// readKeywords are the statements Query accepts. Postgres enforces read-only
// anyway (dock_reader, READ ONLY transaction); this rejects session-changing
// statements (SET, PREPARE, LISTEN, ...) and writes with a clear error before
// anything reaches the database.
var readKeywords = map[string]bool{
	"select": true, "with": true, "values": true, "table": true,
	"explain": true, "show": true,
}

// checkReadStatement accepts sql if it is exactly one statement (a trailing
// semicolon and comments are fine) starting with a read keyword. The
// extended protocol rejects several statements too; this gives a clearer
// error and runs before connecting.
func checkReadStatement(sql string) error {
	first, rest, err := splitFirst(sql)
	if err != nil {
		return err
	}
	if strings.TrimSpace(first) == "" {
		return fmt.Errorf("%w: the query is empty", data.ErrInvalid)
	}
	if rest != "" {
		return fmt.Errorf("%w: run one statement at a time", data.ErrInvalid)
	}
	kw := firstKeyword(first)
	if !readKeywords[kw] {
		if kw == "" {
			kw = "this statement"
		}
		return fmt.Errorf("%w: only read statements (SELECT, WITH, VALUES, TABLE, EXPLAIN, SHOW) can run here, not %s", data.ErrInvalid, strings.ToUpper(kw))
	}
	return nil
}

// splitFirst returns the first statement of sql and any remaining
// statement text (empty when only whitespace and comments follow the first
// semicolon). It understands quotes, dollar quotes and comments.
func splitFirst(sql string) (first, rest string, err error) {
	s := sql
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'':
			// E'...' strings allow backslash escapes.
			escapes := i > 0 && (s[i-1] == 'e' || s[i-1] == 'E') && (i < 2 || !isIdentChar(s[i-2]))
			j, ok := skipQuoted(s, i, '\'', escapes)
			if !ok {
				return "", "", fmt.Errorf("%w: unterminated string", data.ErrInvalid)
			}
			i = j
		case c == '"':
			j, ok := skipQuoted(s, i, '"', false)
			if !ok {
				return "", "", fmt.Errorf("%w: unterminated quoted identifier", data.ErrInvalid)
			}
			i = j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				i = len(s)
			} else {
				i += j + 1
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j, ok := skipBlockComment(s, i)
			if !ok {
				return "", "", fmt.Errorf("%w: unterminated comment", data.ErrInvalid)
			}
			i = j
		case c == '$' && (i == 0 || !isIdentChar(s[i-1])):
			tag, ok := dollarTag(s[i:])
			if !ok {
				i++
				continue
			}
			end := strings.Index(s[i+len(tag):], tag)
			if end < 0 {
				return "", "", fmt.Errorf("%w: unterminated dollar-quoted string", data.ErrInvalid)
			}
			i += len(tag) + end + len(tag)
		case c == ';':
			if onlyComments(s[i+1:]) {
				return s[:i], "", nil
			}
			return s[:i], s[i+1:], nil
		default:
			i++
		}
	}
	return s, "", nil
}

// skipQuoted returns the index after the quoted text starting at s[i].
// Doubled quotes stay inside; with escapes, so do backslash-escaped ones.
func skipQuoted(s string, i int, q byte, escapes bool) (int, bool) {
	for j := i + 1; j < len(s); j++ {
		switch s[j] {
		case '\\':
			if escapes {
				j++
			}
		case q:
			if j+1 < len(s) && s[j+1] == q {
				j++
				continue
			}
			return j + 1, true
		}
	}
	return 0, false
}

// skipBlockComment returns the index after the (possibly nested) /* */
// comment starting at s[i].
func skipBlockComment(s string, i int) (int, bool) {
	depth := 0
	for j := i; j+1 < len(s); j++ {
		switch {
		case s[j] == '/' && s[j+1] == '*':
			depth++
			j++
		case s[j] == '*' && s[j+1] == '/':
			depth--
			j++
			if depth == 0 {
				return j + 1, true
			}
		}
	}
	return 0, false
}

// dollarTag returns the opening tag ($$ or $name$) at the start of s.
// Positional parameters ($1) aren't tags.
func dollarTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		c := s[j]
		if c == '$' {
			return s[:j+1], true
		}
		digit := c >= '0' && c <= '9'
		if !isIdentChar(c) || j == 1 && digit {
			return "", false
		}
	}
	return "", false
}

// onlyComments reports whether s has nothing but whitespace, comments and
// semicolons.
func onlyComments(s string) bool {
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == ';':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return true
			}
			i += j + 1
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j, ok := skipBlockComment(s, i)
			if !ok {
				return false
			}
			i = j
		default:
			return false
		}
	}
	return true
}

// firstKeyword returns the first word of a statement, lower case, skipping
// whitespace, comments and opening parentheses.
func firstKeyword(s string) string {
	for i := 0; i < len(s); {
		switch c := s[i]; {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '(':
			i++
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				return ""
			}
			i += j + 1
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j, ok := skipBlockComment(s, i)
			if !ok {
				return ""
			}
			i = j
		default:
			j := i
			for j < len(s) && isIdentChar(s[j]) {
				j++
			}
			return strings.ToLower(s[i:j])
		}
	}
	return ""
}

// isIdentChar reports whether c can continue an unquoted identifier.
func isIdentChar(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// quoteLiteral renders v as a SQL string literal for display (changeset
// previews). Execution always binds values as parameters.
func quoteLiteral(v string) string {
	if strings.ContainsRune(v, '\\') {
		return "E'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}

// arrayLiteral renders values as a Postgres array in text input format,
// every element quoted, for binding as one parameter (filter op in).
func arrayLiteral(values []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, v := range values {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for j := 0; j < len(v); j++ {
			if v[j] == '"' || v[j] == '\\' {
				b.WriteByte('\\')
			}
			b.WriteByte(v[j])
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// appName is application_name for an actor: "dock:<actor>", limited to
// printable ASCII without quotes so it can sit in a SET statement, at most
// 63 bytes (Postgres' limit).
func appName(actor string) string {
	b := []byte("dock:" + actor)
	for i, c := range b {
		if c < 0x20 || c > 0x7e || c == '\'' || c == '\\' {
			b[i] = '_'
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	return string(b)
}
