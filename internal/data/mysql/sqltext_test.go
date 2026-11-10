package mysql

import (
	"errors"
	"testing"

	"github.com/NordTempo/tempoDock/internal/data"
)

func TestLiteral(t *testing.T) {
	for _, c := range []struct {
		typ, v, want string
		bad          bool
	}{
		{typ: "bigint", v: "9007199254740993", want: "9007199254740993"},
		{typ: "decimal", v: "-1.50", want: "-1.50"},
		{typ: "double", v: "1e100", want: "1e100"},
		{typ: "int", v: "1 OR 1=1", bad: true},
		{typ: "int", v: "", bad: true},
		{typ: "int", v: "0x10", bad: true},
		{typ: "varchar", v: "it's", want: "'it''s'"},
		// A backslash or control byte means the same in every sql_mode
		// only as a hex literal.
		{typ: "varchar", v: `a\'b`, want: "_utf8mb4 X'615c2762'"},
		{typ: "text", v: "line\nbreak", want: "_utf8mb4 X'6c696e650a627265616b'"},
		{typ: "varchar", v: "\xff", want: "X'ff'"},
		{typ: "varbinary", v: `\x00ff`, want: "X'00ff'"},
		{typ: "blob", v: "00ff", bad: true},
		{typ: "blob", v: `\xzz`, bad: true},
		{typ: "datetime", v: "2026-10-20 14:02:00", want: "'2026-10-20 14:02:00'"},
	} {
		got, err := literal(c.typ, c.v)
		if c.bad {
			if !errors.Is(err, data.ErrInvalid) {
				t.Errorf("%s %q: %q %v, want invalid", c.typ, c.v, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s %q: %q %v, want %q", c.typ, c.v, got, err, c.want)
		}
	}
	if got := quoteIdent("a`b"); got != "`a``b`" {
		t.Errorf("quoteIdent: %s", got)
	}
}

func TestCheckReadStatement(t *testing.T) {
	for sql, ok := range map[string]bool{
		"SELECT 1":                                 true,
		"  -- note\n select * from t":              true,
		"(SELECT 1) UNION (SELECT 2)":              true,
		"WITH x AS (SELECT 1) SELECT * FROM x":     true,
		"SHOW TABLES":                              true,
		"EXPLAIN SELECT 1":                         true,
		"SELECT 'for update' FROM t":               true,
		"SELECT /*+ MAX_EXECUTION_TIME(1) */ 1":    true,
		"SELECT `for` FROM t":                      true,
		"":                                         false,
		"-- only a comment":                        false,
		"UPDATE t SET a = 1":                       false,
		"SET @a = 1":                               false,
		"CALL p()":                                 false,
		"SELECT * FROM t FOR UPDATE":               false,
		"SELECT * FROM t for share":                false,
		"SELECT * FROM t LOCK IN SHARE MODE":       false,
		"SELECT /*!50000 1 */":                     false,
		"SELECT /*M!100000 1 */":                   false,
		"SELECT 'unterminated":                     false,
		"/* hidden */ DELETE FROM t":               false,
		"SELECT 1 /* x */ FOR /* y */ UPDATE":      false,
		"SELECT 'it\\'s' FROM t FOR UPDATE":        false,
		"SELECT \"quoted \"\" for update\" FROM t": true,
	} {
		if err := checkReadStatement(sql); (err == nil) != ok {
			t.Errorf("%q: %v", sql, err)
		}
	}
}

func TestRedact(t *testing.T) {
	for sql, want := range map[string]string{
		"SELECT * FROM t WHERE a = 'x' AND b = 12 -- secret": "SELECT * FROM t WHERE a = ? AND b = ?  ",
		"SELECT X'00ff', 0x1f, b'101', \"s\\\"q\"":           "SELECT ?, ?, ?, ?",
		"SELECT `a'b` FROM t # trailing":                     "SELECT `a'b` FROM t  ",
		"SELECT /*!50000 'x' */ 1.5e3":                       "SELECT /*!?*/ ?",
		"SELECT 'unterminated":                               "SELECT ?",
	} {
		if got := redact(sql); got != want {
			t.Errorf("%q: got %q, want %q", sql, got, want)
		}
	}
}

func TestActorComment(t *testing.T) {
	if got := actorComment("dock", "ada@x.io */ DROP"); got != "/* dock:ada@x.io  DROP */ " {
		t.Errorf("got %q", got)
	}
}
