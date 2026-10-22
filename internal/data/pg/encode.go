package pg

import (
	"strconv"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// cellKind is how a column's text output becomes a JSON cell (see
// apitypes.DataRow).
type cellKind uint8

const (
	kindString cellKind = iota
	kindBool
	kindNumber
	// kindMasked never reads a value: the cell is {"masked":true}.
	kindMasked
)

// kindOfType picks the cell kind for a base type name (pg_type.typname).
func kindOfType(typeName string) cellKind {
	switch typeName {
	case "bool":
		return kindBool
	case "int2", "int4", "oid", "float4", "float8":
		return kindNumber
	}
	return kindString
}

// kindOfOID picks the cell kind for a result column's type OID (Postgres
// reports a domain's base type in row descriptions).
func kindOfOID(oid uint32) cellKind {
	switch oid {
	case 16:
		return kindBool
	case 21, 23, 26, 700, 701:
		return kindNumber
	}
	return kindString
}

const maskedCell = `{"masked":true}`

// appendRow appends one row as a JSON array. src maps each output column
// to its index in vals, or -1 for a masked column; kinds is per output
// column. Values are Postgres text output; nil is NULL. Cells longer than
// apitypes.DataCellMaxBytes are truncated unless full is set.
func appendRow(dst []byte, vals [][]byte, src []int, kinds []cellKind, full bool) []byte {
	dst = append(dst, '[')
	for i, k := range kinds {
		if i > 0 {
			dst = append(dst, ',')
		}
		if k == kindMasked || src[i] < 0 {
			dst = append(dst, maskedCell...)
			continue
		}
		dst = appendCell(dst, vals[src[i]], k, full)
	}
	return append(dst, ']')
}

// appendCell appends one value (text output, nil for NULL) as a JSON cell.
func appendCell(dst, v []byte, k cellKind, full bool) []byte {
	if v == nil {
		return append(dst, "null"...)
	}
	switch k {
	case kindBool:
		switch string(v) {
		case "t":
			return append(dst, "true"...)
		case "f":
			return append(dst, "false"...)
		}
	case kindNumber:
		// Postgres prints numbers JSON-compatibly; NaN and ±Infinity (and
		// anything unexpected) go out as strings.
		if isJSONNumber(v) {
			return append(dst, v...)
		}
	case kindMasked:
		return append(dst, maskedCell...)
	}
	if full || len(v) <= apitypes.DataCellMaxBytes {
		return appendJSONString(dst, v)
	}
	cut := apitypes.DataCellMaxBytes
	for cut > 0 && !utf8.RuneStart(v[cut]) {
		cut--
	}
	dst = append(dst, `{"truncated":`...)
	dst = appendJSONString(dst, v[:cut])
	dst = append(dst, `,"bytes":`...)
	dst = strconv.AppendInt(dst, int64(len(v)), 10)
	return append(dst, '}')
}

// isJSONNumber reports whether b matches JSON's number grammar.
func isJSONNumber(b []byte) bool {
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

// appendJSONString appends s as a JSON string. Invalid UTF-8 becomes
// U+FFFD, like encoding/json.
func appendJSONString(dst, s []byte) []byte {
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
