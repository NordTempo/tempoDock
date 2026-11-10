package data

import (
	"strconv"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Cell encoding shared by the engines: how a value in a database's text
// format becomes a JSON cell of apitypes.DataRow. Engines decide each
// column's CellKind from their own type system and append rows with
// AppendCell.

// CellKind is how a column's text value becomes a JSON cell.
type CellKind uint8

// Cell kinds.
const (
	// CellString is a JSON string (truncated past
	// apitypes.DataCellMaxBytes unless full).
	CellString CellKind = iota
	// CellBool is "t" or "f" (Postgres' text output) as a JSON bool.
	CellBool
	// CellNumber is a JSON number when the text is one, else a string
	// (NaN, ±Infinity).
	CellNumber
	// CellMasked never reads a value: the cell is {"masked":true}.
	CellMasked
)

// MaskedCell is the JSON of a masked cell.
const MaskedCell = `{"masked":true}`

// AppendCell appends one value (the database's text output, nil for
// NULL) as a JSON cell.
func AppendCell(dst, v []byte, k CellKind, full bool) []byte {
	if v == nil {
		return append(dst, "null"...)
	}
	switch k {
	case CellBool:
		switch string(v) {
		case "t":
			return append(dst, "true"...)
		case "f":
			return append(dst, "false"...)
		}
	case CellNumber:
		// Databases print numbers JSON-compatibly; NaN and ±Infinity (and
		// anything unexpected) go out as strings.
		if IsJSONNumber(v) {
			return append(dst, v...)
		}
	case CellMasked:
		return append(dst, MaskedCell...)
	}
	if full || len(v) <= apitypes.DataCellMaxBytes {
		return AppendJSONString(dst, v)
	}
	cut := apitypes.DataCellMaxBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	dst = append(dst, `{"truncated":`...)
	dst = AppendJSONString(dst, v[:cut])
	dst = append(dst, `,"bytes":`...)
	dst = strconv.AppendInt(dst, int64(len(v)), 10)
	return append(dst, '}')
}

// IsJSONNumber reports whether b matches JSON's number grammar.
func IsJSONNumber(b []byte) bool {
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	default:
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		start := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		start := i
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(b)
}

const hexDigits = "0123456789abcdef"

// AppendJSONString appends s as a JSON string. Invalid UTF-8 becomes
// U+FFFD, like encoding/json.
func AppendJSONString(dst, s []byte) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRune(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, `�`...)
			i++
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}
