package mysql

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/data"
)

// quoteIdent quotes an identifier with backticks, which mean the same in
// every sql_mode (ANSI_QUOTES changes only double quotes).
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// quoteTable quotes `schema`.`table`.
func quoteTable(schema, name string) string { return quoteIdent(schema) + "." + quoteIdent(name) }

// quoteString renders s as a string literal that means the same in every
// sql_mode: quotes doubled when s has no backslash and no control bytes,
// else a hex literal with a utf8mb4 introducer (so the column's collation
// still applies). Values that aren't UTF-8 become plain hex literals,
// which are binary strings.
func quoteString(s string) string {
	if !utf8.ValidString(s) {
		return "X'" + hex.EncodeToString([]byte(s)) + "'"
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c == '\\' || c < 0x20 || c == 0x7f {
			return "_utf8mb4 X'" + hex.EncodeToString([]byte(s)) + "'"
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// literal renders a value for a column of type typeName (DATA_TYPE):
// numbers bare (a quoted string is compared with a number column as a
// double, which loses precision and skips the index), binary values from
// their \x hex text (as pages show them), everything else as a string the
// server converts to the column's type.
func literal(typeName, v string) (string, error) {
	switch {
	case numericType(typeName):
		if !isNumber(v) {
			return "", fmt.Errorf("%w: %q is not a number", data.ErrInvalid, v)
		}
		return v, nil
	case binaryType(typeName):
		b, ok := strings.CutPrefix(v, `\x`)
		if _, err := hex.DecodeString(b); !ok || err != nil {
			return "", fmt.Errorf("%w: binary values are hex with a \\x prefix, e.g. \\x0aff", data.ErrInvalid)
		}
		return "X'" + b + "'", nil
	}
	return quoteString(v), nil
}

// numericType reports whether values of a DATA_TYPE are numbers.
func numericType(t string) bool {
	switch t {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "year":
		return true
	}
	return false
}

// binaryType reports whether values of a DATA_TYPE are bytes, shown as
// \x hex.
func binaryType(t string) bool {
	switch t {
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob", "bit",
		"geometry", "point", "linestring", "polygon", "multipoint", "multilinestring", "multipolygon", "geometrycollection":
		return true
	}
	return false
}

// isNumber reports whether v is a plain decimal number: an optional sign,
// digits with an optional fraction, an optional exponent.
func isNumber(v string) bool {
	i := 0
	if i < len(v) && (v[i] == '+' || v[i] == '-') {
		i++
	}
	digits := 0
	for i < len(v) && v[i] >= '0' && v[i] <= '9' {
		i, digits = i+1, digits+1
	}
	if i < len(v) && v[i] == '.' {
		i++
		for i < len(v) && v[i] >= '0' && v[i] <= '9' {
			i, digits = i+1, digits+1
		}
	}
	if digits == 0 {
		return false
	}
	if i < len(v) && (v[i] == 'e' || v[i] == 'E') {
		i++
		if i < len(v) && (v[i] == '+' || v[i] == '-') {
			i++
		}
		start := i
		for i < len(v) && v[i] >= '0' && v[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(v)
}

// tokenKind is a lexical class of SQL text.
type tokenKind uint8

const (
	tokWord tokenKind = iota
	tokString
	tokNumber
	tokComment
	// tokExecComment is /*! ... */ or /*M! ... */, which MySQL and MariaDB
	// run as SQL.
	tokExecComment
	tokOther
)

// scanSQL splits sql into tokens the way MySQL's lexer does, as far as
// redaction and the read check need: quoted strings (with backslash
// escapes and doubled quotes), backtick names (words), comments (#, "-- "
// and /* */), numbers (including hex and bit literals) and words. fn sees
// each token's kind and text in order; whitespace and punctuation are
// tokOther, so the texts joined give sql back. An unterminated string or
// comment is an error.
func scanSQL(sql string, fn func(kind tokenKind, text string)) error {
	for i := 0; i < len(sql); {
		c := sql[i]
		start := i
		switch {
		case isSpace(c):
			for i < len(sql) && isSpace(sql[i]) {
				i++
			}
			fn(tokOther, sql[start:i])
		case c == '\'' || c == '"':
			end, ok := skipQuoted(sql, i, c)
			if !ok {
				return fmt.Errorf("%w: unterminated string", data.ErrInvalid)
			}
			i = end
			fn(tokString, sql[start:i])
		case c == '`':
			end, ok := skipQuoted(sql, i, '`')
			if !ok {
				return fmt.Errorf("%w: unterminated name", data.ErrInvalid)
			}
			i = end
			fn(tokWord, sql[start:i])
		case c == '#' || c == '-' && strings.HasPrefix(sql[i:], "--") && (i+2 == len(sql) || sql[i+2] <= ' '):
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			fn(tokComment, sql[start:i])
		case c == '/' && strings.HasPrefix(sql[i:], "/*"):
			end := strings.Index(sql[i+2:], "*/")
			if end < 0 {
				return fmt.Errorf("%w: unterminated comment", data.ErrInvalid)
			}
			i += 2 + end + 2
			kind := tokComment
			if body := sql[start+2:]; strings.HasPrefix(body, "!") || strings.HasPrefix(body, "M!") {
				kind = tokExecComment
			}
			fn(kind, sql[start:i])
		case (c == 'x' || c == 'X' || c == 'b' || c == 'B') && i+1 < len(sql) && sql[i+1] == '\'':
			end, ok := skipQuoted(sql, i+1, '\'')
			if !ok {
				return fmt.Errorf("%w: unterminated literal", data.ErrInvalid)
			}
			i = end
			fn(tokNumber, sql[start:i])
		case c >= '0' && c <= '9' || c == '.' && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9':
			for i < len(sql) && (isIdentChar(sql[i]) || sql[i] == '.' ||
				(sql[i] == '+' || sql[i] == '-') && (sql[i-1] == 'e' || sql[i-1] == 'E')) {
				i++
			}
			fn(tokNumber, sql[start:i])
		case isIdentChar(c) || c >= utf8.RuneSelf:
			for i < len(sql) && (isIdentChar(sql[i]) || sql[i] >= utf8.RuneSelf || sql[i] == '$') {
				i++
			}
			fn(tokWord, sql[start:i])
		default:
			i++
			fn(tokOther, sql[start:i])
		}
	}
	return nil
}

// skipQuoted returns the index after the quoted text starting at s[i]
// (quote q). Doubled quotes stay inside; so do backslash-escaped
// characters (MySQL's default; with NO_BACKSLASH_ESCAPES this can only
// overrun, which makes redaction hide more, never less).
func skipQuoted(s string, i int, q byte) (int, bool) {
	for j := i + 1; j < len(s); j++ {
		switch {
		case s[j] == '\\' && q != '`':
			j++
		case s[j] == q && j+1 < len(s) && s[j+1] == q:
			j++
		case s[j] == q:
			return j + 1, true
		}
	}
	return 0, false
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' }

func isIdentChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// redact replaces the literal values in sql (strings, numbers, hex and
// bit literals) with ? and drops comments; executable comments (/*! */)
// become /*!?*/, since their content runs.
func redact(sql string) string {
	var b strings.Builder
	b.Grow(len(sql))
	err := scanSQL(sql, func(kind tokenKind, text string) {
		switch kind {
		case tokString, tokNumber:
			b.WriteByte('?')
		case tokComment:
			b.WriteByte(' ')
		case tokExecComment:
			b.WriteString("/*!?*/")
		default:
			b.WriteString(text)
		}
	})
	if err != nil {
		b.WriteString("?")
	}
	return b.String()
}

// readKeywords are the statements Query runs: they read and, as the
// reader in a read-only transaction, can't write.
var readKeywords = map[string]bool{
	"select": true, "with": true, "show": true, "describe": true, "desc": true,
	"explain": true, "table": true, "values": true,
}

// checkReadStatement refuses what isn't one read: an empty statement, a
// first keyword that isn't a read, executable comments (/*! */, whose
// content the server runs) and locking reads (FOR UPDATE, FOR SHARE,
// LOCK IN SHARE MODE), which would hold row locks the app waits on. The
// reader's privileges are the real limit; this is for clear messages and
// for what privileges allow.
func checkReadStatement(sql string) error {
	var words []string
	exec := false
	err := scanSQL(sql, func(kind tokenKind, text string) {
		switch kind {
		case tokExecComment:
			exec = true
		case tokWord:
			words = append(words, strings.ToLower(text))
		case tokOther:
			if text == "(" && len(words) == 0 {
				words = append(words, "(")
			}
		}
	})
	if err != nil {
		return err
	}
	if exec {
		return fmt.Errorf("%w: executable comments (/*! ... */) aren't allowed; write the SQL itself", data.ErrInvalid)
	}
	if len(words) == 0 {
		return fmt.Errorf("%w: the statement is empty", data.ErrInvalid)
	}
	if !readKeywords[words[0]] && words[0] != "(" {
		return fmt.Errorf("%w: only reads run here (SELECT, WITH, SHOW, DESCRIBE, EXPLAIN, TABLE, VALUES); changes go through a changeset", data.ErrInvalid)
	}
	for i := range words {
		next := func(n int) string {
			if i+n < len(words) {
				return words[i+n]
			}
			return ""
		}
		if words[i] == "for" && (next(1) == "update" || next(1) == "share") ||
			words[i] == "lock" && next(1) == "in" && next(2) == "share" && next(3) == "mode" {
			return fmt.Errorf("%w: locking reads (FOR UPDATE, FOR SHARE) hold rows the app may be waiting on; read without them", data.ErrInvalid)
		}
	}
	return nil
}

// actorComment is a comment naming the caller, put before engine and
// caller SQL so the server's process list shows who runs what (MySQL has
// no per-statement application name). Only safe characters survive.
func actorComment(app, actor string) string {
	var b strings.Builder
	b.WriteString("/* ")
	b.WriteString(app)
	b.WriteByte(':')
	for _, r := range actor {
		if r < utf8.RuneSelf && (isIdentChar(byte(r)) || strings.ContainsRune("@.-+ ", r)) {
			b.WriteRune(r)
		}
	}
	b.WriteString(" */ ")
	return b.String()
}
