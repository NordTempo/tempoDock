package pg

import "github.com/NordTempo/tempoDock/internal/data"

// kindOfType picks the cell kind for a base type name (pg_type.typname).
func kindOfType(typeName string) data.CellKind {
	switch typeName {
	case "bool":
		return data.CellBool
	case "int2", "int4", "oid", "float4", "float8":
		return data.CellNumber
	}
	return data.CellString
}

// kindOfOID picks the cell kind for a result column's type OID (Postgres
// reports a domain's base type in row descriptions).
func kindOfOID(oid uint32) data.CellKind {
	switch oid {
	case 16:
		return data.CellBool
	case 21, 23, 26, 700, 701:
		return data.CellNumber
	}
	return data.CellString
}

// appendRow appends one row as a JSON array. src maps each output column
// to its index in vals, or -1 for a masked column; kinds is per output
// column. Values are Postgres text output; nil is NULL. Cells longer than
// apitypes.DataCellMaxBytes are truncated unless full is set.
func appendRow(dst []byte, vals [][]byte, src []int, kinds []data.CellKind, full bool) []byte {
	dst = append(dst, '[')
	for i, k := range kinds {
		if i > 0 {
			dst = append(dst, ',')
		}
		if k == data.CellMasked || src[i] < 0 {
			dst = append(dst, data.MaskedCell...)
			continue
		}
		dst = data.AppendCell(dst, vals[src[i]], k, full)
	}
	return append(dst, ']')
}
