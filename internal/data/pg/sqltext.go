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
	return checkFunctions(first)
}

// blockedFunctions are functions a read can't call: their effects outlive
// the read-only transaction that is rolled back (session advisory locks,
// non-transactional logical decoding messages that reach CDC consumers),
// or they run SQL from a string, which would hide such a call from this
// check. They are executable by PUBLIC, so revoking them from Dock's roles
// alone wouldn't stop them. Names ending in _ are prefixes.
var blockedFunctions = []string{
	"pg_advisory_", "pg_try_advisory_", "pg_logical_emit_message",
	"query_to_xml", "query_to_xmlschema", "query_to_xml_and_xmlschema",
	"cursor_to_xml", "cursor_to_xmlschema", "ts_stat", "ts_rewrite", "dblink",
}

// checkFunctions rejects a statement that names a blocked function,
// however it is spelled (any case, schema-qualified, quoted). Identifiers
// with Unicode escapes (U&"...") are rejected outright, since they could
// spell one in a way this check doesn't decode.
func checkFunctions(sql string) error {
	return scanSQL(sql, func(kind tokenKind, text string) error {
		var name string
		switch kind {
		case tokenWord:
			name = strings.ToLower(text)
		case tokenQuoted:
			name = strings.ReplaceAll(text[1:len(text)-1], `""`, `"`)
		case tokenUnicodeQuoted:
			return fmt.Errorf("%w: identifiers with Unicode escapes (U&\"...\") aren't supported here", data.ErrInvalid)
		default:
			return nil
		}
		for _, b := range blockedFunctions {
			if name == b || strings.HasSuffix(b, "_") && strings.HasPrefix(name, b) {
				return fmt.Errorf("%w: %s can't be called here: its effects outlive the read, or it runs SQL from a string", data.ErrInvalid, name)
			}
		}
		return nil
	})
}

// redact replaces sql's literal values (strings in every form, dollar
// quotes, numbers) with ? and its comments with a space; identifiers,
// keywords, operators and parameters stay. Unterminated text is cut at
// the point it starts.
func redact(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	err := scanSQL(sql, func(kind tokenKind, text string) error {
		switch kind {
		case tokenString, tokenNumber:
			b.WriteByte('?')
		case tokenComment:
			b.WriteByte(' ')
		default:
			b.WriteString(text)
		}
		return nil
	})
	if err != nil {
		b.WriteString("?")
	}
	return b.String()
}

// tokenKind classifies what scanSQL found.
type tokenKind int

const (
	// tokenOther is anything else: whitespace, operators, punctuation,
	// parameters ($1), one byte or run at a time.
	tokenOther tokenKind = iota
	// tokenWord is an unquoted identifier or keyword.
	tokenWord
	// tokenQuoted is a quoted identifier, quotes included.
	tokenQuoted
	// tokenUnicodeQuoted is a U&"..." identifier.
	tokenUnicodeQuoted
	// tokenString is a string constant in any form ('...', E'...', B'...',
	// X'...', N'...', U&'...', $tag$...$tag$), prefix included.
	tokenString
	// tokenNumber is a numeric constant.
	tokenNumber
	// tokenComment is a -- or /* */ comment.
	tokenComment
)

// errUnterminated ends scanSQL at a quote, dollar quote or comment that
// never ends.
var errUnterminated = fmt.Errorf("%w: unterminated string, identifier or comment", data.ErrInvalid)

// scanSQL splits sql into tokens and calls fn with each, in order, until
// fn returns an error. Concatenating the texts gives sql back.
func scanSQL(sql string, fn func(kind tokenKind, text string) error) error {
	s := sql
	for i := 0; i < len(s); {
		c := s[i]
		kind, end := tokenOther, i+1
		switch {
		case c == '\'':
			j, ok := skipQuoted(s, i, '\'', false)
			if !ok {
				return errUnterminated
			}
			kind, end = tokenString, j
		case c == '"':
			j, ok := skipQuoted(s, i, '"', false)
			if !ok {
				return errUnterminated
			}
			kind, end = tokenQuoted, j
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			end = len(s)
			if j := strings.IndexByte(s[i:], '\n'); j >= 0 {
				end = i + j + 1
			}
			kind = tokenComment
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j, ok := skipBlockComment(s, i)
			if !ok {
				return errUnterminated
			}
			kind, end = tokenComment, j
		case c == '$':
			if tag, ok := dollarTag(s[i:]); ok {
				close := strings.Index(s[i+len(tag):], tag)
				if close < 0 {
					return errUnterminated
				}
				kind, end = tokenString, i+len(tag)+close+len(tag)
				break
			}
			for end < len(s) && s[end] >= '0' && s[end] <= '9' {
				end++ // a parameter, $1
			}
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			for end < len(s) && (isIdentChar(s[end]) && s[end] != '$' || s[end] == '.' ||
				(s[end] == '+' || s[end] == '-') && (s[end-1] == 'e' || s[end-1] == 'E')) {
				end++
			}
			kind = tokenNumber
		case isIdentChar(c):
			for end < len(s) && isIdentChar(s[end]) {
				end++
			}
			kind = tokenWord
			word := s[i:end]
			switch {
			case end < len(s) && s[end] == '\'' && len(word) == 1 && strings.ContainsRune("eEbBxXnN", rune(word[0])):
				j, ok := skipQuoted(s, end, '\'', word == "e" || word == "E")
				if !ok {
					return errUnterminated
				}
				kind, end = tokenString, j
			case (word == "u" || word == "U") && end+1 < len(s) && s[end] == '&' && (s[end+1] == '\'' || s[end+1] == '"'):
				j, ok := skipQuoted(s, end+1, s[end+1], false)
				if !ok {
					return errUnterminated
				}
				kind = tokenString
				if s[end+1] == '"' {
					kind = tokenUnicodeQuoted
				}
				end = j
			}
		}
		if err := fn(kind, s[i:end]); err != nil {
			return err
		}
		i = end
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
