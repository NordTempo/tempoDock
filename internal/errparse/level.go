// Package errparse finds errors in containers' log output and groups them
// (ADR 0062, "One place for errors"). It is pure Go with no Dock
// dependencies: core feeds it each container's lines as they are written
// and gets back one Event per error, however many lines its stack trace
// took, with a fingerprint that stays the same for the same error across
// occurrences and deploys.
//
// A plain line costs a byte scan and no allocation (BenchmarkAssembler);
// only an error's lines are copied.
package errparse

import "strings"

// Level is how a log line reads: an error, a warning, or neither.
type Level uint8

// Levels.
const (
	LevelNone Level = iota
	LevelWarn
	LevelError
)

// LineLevel judges a line by the level it states, as the logs pane does
// (web/src/features/service/logLevel.ts), so a server that logs everything
// to stderr (Caddy) or says "error" in an info message isn't read as
// failing. A stated level is JSON ("level":"error", "lvl", "severity",
// pino's numeric "level":50), logfmt (level=error) or a tab-separated
// column (Caddy's and zap's console format); the first one wins. A line
// without a known level falls back to the words error, fatal, panic and
// exception. Unlike the logs pane, an HTTP 5xx status in an access line
// is not an error here: traffic counts those (core's traffic errors).
func LineLevel(s string) Level {
	words := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\t' {
			if lv, ok := tabLevel(s[i+1:]); ok {
				return lv
			}
			continue
		}
		switch c | 0x20 {
		case 'l', 's':
			lv, known, ok := statedLevel(s, i)
			if !ok {
				continue
			}
			if !known {
				return wordsLevel(words || hasErrorWord(s[i:]))
			}
			return lv
		case 'e', 'f', 'p':
			if !words && errorWordAt(s, i) {
				words = true
			}
		}
	}
	return wordsLevel(words)
}

// wordsLevel is the fallback's verdict.
func wordsLevel(words bool) Level {
	if words {
		return LevelError
	}
	return LevelNone
}

// levelKeys are the keys a stated level goes by.
var levelKeys = [...]string{"level", "lvl", "severity"}

// statedLevel reads a JSON or logfmt level whose key starts at s[i]. ok
// says one is there; known whether its value is a level LineLevel knows
// (an unknown one falls back to error words).
func statedLevel(s string, i int) (lv Level, known, ok bool) {
	for _, key := range levelKeys {
		end := i + len(key)
		if end >= len(s) || !strings.EqualFold(s[i:end], key) {
			continue
		}
		switch {
		case i > 0 && s[i-1] == '"' && s[end] == '"':
			// "level" \s* : \s* "? (word | digits)
			j := skipSpace(s, end+1)
			if j >= len(s) || s[j] != ':' {
				return 0, false, false
			}
			j = skipSpace(s, j+1)
			if j < len(s) && s[j] == '"' {
				j++
			}
			if n := digits(s[j:]); n > 0 {
				return numericLevel(s[j : j+n]), true, true
			}
			n := letters(s[j:])
			if n == 0 {
				return 0, false, false
			}
			lv, known = levelWord(s[j : j+n])
			return lv, known, true
		case (i == 0 || isSpace(s[i-1])) && s[end] == '=':
			// level= "? word
			j := end + 1
			if j < len(s) && s[j] == '"' {
				j++
			}
			n := letters(s[j:])
			if n == 0 {
				return 0, false, false
			}
			lv, known = levelWord(s[j : j+n])
			return lv, known, true
		}
		return 0, false, false
	}
	return 0, false, false
}

// tabLevel reads a console format's level column: rest follows a tab and
// must be one of the column's words and another tab.
func tabLevel(rest string) (Level, bool) {
	n := letters(rest)
	if n < 4 || n > 5 || n >= len(rest) || rest[n] != '\t' {
		return 0, false
	}
	w := rest[:n]
	for _, k := range [...]string{"debug", "info", "warn", "error", "fatal", "panic"} {
		if strings.EqualFold(w, k) {
			lv, _ := levelWord(k)
			return lv, true
		}
	}
	return 0, false
}

// levelWord maps a level's name to a Level; known is false for a name
// that isn't one.
func levelWord(w string) (lv Level, known bool) {
	for _, k := range [...]string{"error", "err", "fatal", "panic", "crit", "critical", "alert", "emerg", "emergency"} {
		if strings.EqualFold(w, k) {
			return LevelError, true
		}
	}
	for _, k := range [...]string{"warn", "warning"} {
		if strings.EqualFold(w, k) {
			return LevelWarn, true
		}
	}
	for _, k := range [...]string{"info", "notice", "debug", "trace"} {
		if strings.EqualFold(w, k) {
			return LevelNone, true
		}
	}
	return LevelNone, false
}

// numericLevel is pino's and bunyan's: 40 warn, 50 error, 60 fatal.
func numericLevel(d string) Level {
	n := 0
	for i := 0; i < len(d) && i < 4; i++ {
		n = n*10 + int(d[i]-'0')
	}
	switch {
	case len(d) > 4 || n >= 50:
		return LevelError
	case n >= 40:
		return LevelWarn
	}
	return LevelNone
}

// errorWords are the fallback's words, matched whole and in any case.
var errorWords = [...]string{"error", "fatal", "panic", "exception"}

// errorWordAt reports whether one of errorWords starts at s[i] as a
// whole word.
func errorWordAt(s string, i int) bool {
	if i > 0 && isWord(s[i-1]) {
		return false
	}
	for _, w := range errorWords {
		end := i + len(w)
		if end <= len(s) && strings.EqualFold(s[i:end], w) && (end == len(s) || !isWord(s[end])) {
			return true
		}
	}
	return false
}

// hasErrorWord reports whether s holds one of errorWords.
func hasErrorWord(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] | 0x20 {
		case 'e', 'f', 'p':
			if errorWordAt(s, i) {
				return true
			}
		}
	}
	return false
}

// isWord is a regular expression's \w.
func isWord(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}

// isSpace is a regular expression's \s, in ASCII.
func isSpace(c byte) bool { return c == ' ' || c >= '\t' && c <= '\r' }

// skipSpace returns the index of the first non-space byte at or after i.
func skipSpace(s string, i int) int {
	for i < len(s) && isSpace(s[i]) {
		i++
	}
	return i
}

// letters is the length of s's leading run of ASCII letters.
func letters(s string) int {
	n := 0
	for n < len(s) && s[n]|0x20 >= 'a' && s[n]|0x20 <= 'z' {
		n++
	}
	return n
}

// digits is the length of s's leading run of ASCII digits.
func digits(s string) int {
	n := 0
	for n < len(s) && s[n] >= '0' && s[n] <= '9' {
		n++
	}
	return n
}
