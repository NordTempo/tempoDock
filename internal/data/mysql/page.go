package mysql

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

const (
	// maxReferenceFKs caps how many incoming foreign keys References reads
	// (one query each).
	maxReferenceFKs = 50
	// maxOffset is how deep a table without a key pages (by offset, which
	// costs the server every skipped row); past it, use SQL.
	maxOffset = 10_000
)

// outCol is how one output column is encoded: its index in the row read
// (-1 for a masked column), its cell kind, and whether its bytes go out
// as \x hex.
type outCol struct {
	src  int
	kind data.CellKind
	hex  bool
}

// appendRow appends one row as a JSON array (see apitypes.DataRow).
// scratch is reused for hex text.
func appendRow(dst []byte, vals [][]byte, cols []outCol, full bool, scratch *[]byte) []byte {
	dst = append(dst, '[')
	for i, c := range cols {
		if i > 0 {
			dst = append(dst, ',')
		}
		if c.src < 0 {
			dst = append(dst, data.MaskedCell...)
			continue
		}
		v := vals[c.src]
		if c.hex && v != nil {
			*scratch = hex.AppendEncode(append((*scratch)[:0], `\x`...), v)
			v = *scratch
		}
		dst = data.AppendCell(dst, v, c.kind, full)
	}
	return append(dst, ']')
}

// cellOf is the encoding of a column of DATA_TYPE t.
func cellOf(t string) (data.CellKind, bool) {
	switch t {
	case "tinyint", "smallint", "mediumint", "int", "integer", "year", "float", "double", "real":
		return data.CellNumber, false
	}
	return data.CellString, binaryType(t)
}

// resultColumn names a result column's type and picks its encoding from
// the protocol: integers that fit a double and floats are numbers,
// BIGINT and DECIMAL exact strings, binary strings, BIT and geometry \x
// hex.
func resultColumn(c wire.Column) (apitypes.DataResultColumn, outCol) {
	binary := c.Charset == wire.CharsetBinary
	name, kind, hexed := "text", data.CellString, false
	switch c.Type {
	case wire.TypeTiny:
		name, kind = "tinyint", data.CellNumber
	case wire.TypeShort:
		name, kind = "smallint", data.CellNumber
	case wire.TypeInt24:
		name, kind = "mediumint", data.CellNumber
	case wire.TypeLong:
		name, kind = "int", data.CellNumber
	case wire.TypeYear:
		name, kind = "year", data.CellNumber
	case wire.TypeFloat:
		name, kind = "float", data.CellNumber
	case wire.TypeDouble:
		name, kind = "double", data.CellNumber
	case wire.TypeLongLong:
		name = "bigint"
	case wire.TypeDecimal, wire.TypeNewDecimal:
		name = "decimal"
	case wire.TypeDate, wire.TypeNewDate:
		name = "date"
	case wire.TypeTime:
		name = "time"
	case wire.TypeDatetime:
		name = "datetime"
	case wire.TypeTimestamp:
		name = "timestamp"
	case wire.TypeJSON:
		// MySQL reports JSON with the binary charset; it is text.
		name = "json"
	case wire.TypeBit:
		name, hexed = "bit", true
	case wire.TypeGeometry:
		name, hexed = "geometry", true
	case wire.TypeNull:
		name = "null"
	case wire.TypeEnum:
		name = "enum"
	case wire.TypeSet:
		name = "set"
	case wire.TypeVarchar, wire.TypeVarString:
		name = "varchar"
		if binary {
			name, hexed = "varbinary", true
		}
	case wire.TypeString:
		name = "char"
		if binary {
			name, hexed = "binary", true
		}
	case wire.TypeTinyBlob, wire.TypeBlob, wire.TypeMediumBlob, wire.TypeLongBlob:
		if binary {
			name, hexed = "blob", true
		}
	}
	return apitypes.DataResultColumn{Name: c.Name, TypeName: name}, outCol{kind: kind, hex: hexed}
}

// orderCol is one column of a page's ORDER BY.
type orderCol struct {
	name     string
	nullable bool
	// at is the column's index in the row read (cursor values).
	at int
}

// pagePlan is a compiled page request.
type pagePlan struct {
	sql      string
	countSQL string
	columns  []apitypes.DataResultColumn
	cols     []outCol
	// selTypes is each selected column's DATA_TYPE, for cursor values.
	selTypes []string
	order    []orderCol
	limit    int
	full     bool
	// offset is the row offset of a table without a key; -1 for keyset
	// pages.
	offset int64
	check  string
}

// cursor is the decoded form of DataPage.Next: a check of the request it
// belongs to, then the last row's order values (keyset) or the offset.
// The panel seals cursors before they leave the daemon.
type cursor struct {
	C string    `json:"c"`
	K []*string `json:"k,omitempty"`
	O int64     `json:"o,omitempty"`
}

var errBadCursor = fmt.Errorf("%w: the cursor doesn't belong to this table, order and filter; start from the first page", data.ErrInvalid)

// Page reads one page as the reader: keyset on the primary key (or a
// unique NOT NULL index), else by offset up to maxOffset. Masked calls see
// their sensitive columns as masked cells and can't filter or sort by
// them.
func (e *Engine) Page(ctx context.Context, t data.Target, c data.Call, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	defer e.release(cn)
	cat, err := e.catalog(ctx, t, cn.Conn)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	tm, err := cat.table(req.Table)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	masked := masks(c, tm)
	if err := checkMaskedUse(req, masked); err != nil {
		return apitypes.DataPage{}, err
	}
	plan, err := planPage(tm, req, masked)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	comment := actorComment(e.app, c.Actor)
	page, err := runPage(ctx, cn.Conn, comment, plan)
	if err != nil {
		return page, err
	}
	page.EstimatedRows = tm.EstimatedRows
	if req.Count {
		var n int64
		err := cn.Conn.query(ctx, comment+plan.countSQL, 1, func(r [][]byte) error {
			n, _ = strconv.ParseInt(string(r[0]), 10, 64)
			return nil
		})
		switch code := wire.ErrorCode(err); {
		case code == errMySQLTimeout || code == errMariaDBTimeout:
			// Counting took too long: no exact count.
		case err != nil:
			return page, dbError(err)
		default:
			page.ExactRows = &n
		}
	}
	return page, nil
}

// checkMaskedUse rejects filtering or sorting by a masked column, which
// would reveal its values.
func checkMaskedUse(req apitypes.DataPageRequest, masked map[string]bool) error {
	if col := strings.TrimPrefix(req.Order, "-"); masked[col] {
		return fmt.Errorf("%w: can't sort by sensitive column %s", data.ErrInvalid, col)
	}
	for _, f := range req.Filters {
		if masked[f.Column] {
			return fmt.Errorf("%w: can't filter by sensitive column %s", data.ErrInvalid, f.Column)
		}
	}
	return nil
}

// planPage compiles a page request against tm. Masked columns are left
// out of the select list. Every identifier is quoted and every value
// rendered by literal.
func planPage(tm *tableMeta, req apitypes.DataPageRequest, masked map[string]bool) (*pagePlan, error) {
	limit := req.Limit
	if limit == 0 {
		limit = apitypes.DataPageLimit
	}
	if limit < 0 || limit > apitypes.DataPageMaxLimit {
		return nil, fmt.Errorf("%w: limit must be 1 to %d", data.ErrInvalid, apitypes.DataPageMaxLimit)
	}
	if req.Full && limit != 1 {
		return nil, fmt.Errorf("%w: full values need limit 1", data.ErrInvalid)
	}
	plan := &pagePlan{limit: limit, full: req.Full, offset: -1}
	table := quoteTable(tm.Schema, tm.Name)

	selects := make([]string, 0, len(tm.Columns)+2)
	plan.columns = make([]apitypes.DataResultColumn, len(tm.Columns))
	plan.cols = make([]outCol, len(tm.Columns))
	at := map[string]int{}
	for i, col := range tm.Columns {
		plan.columns[i] = apitypes.DataResultColumn{Name: col.Name, TypeName: col.TypeName}
		if masked[col.Name] {
			plan.cols[i] = outCol{src: -1, kind: data.CellMasked}
			continue
		}
		kind, hexed := cellOf(col.TypeName)
		at[col.Name] = len(selects)
		plan.cols[i] = outCol{src: len(selects), kind: kind, hex: hexed}
		selects = append(selects, quoteIdent(col.Name))
		plan.selTypes = append(plan.selTypes, col.TypeName)
	}

	// Order: the requested column, then the key.
	sort, desc := strings.CutPrefix(req.Order, "-")
	var names []string
	if sort != "" {
		if _, ok := tm.col[sort]; !ok {
			return nil, fmt.Errorf("%w: no column %s to sort by", data.ErrInvalid, sort)
		}
		names = append(names, sort)
	}
	for _, k := range tm.key {
		if k != sort {
			names = append(names, k)
		}
	}
	for _, name := range names {
		oc := orderCol{name: name, nullable: tm.Columns[tm.col[name]].Nullable}
		if j, ok := at[name]; ok {
			oc.at = j
		} else {
			oc.at = len(selects)
			at[name] = oc.at
			selects = append(selects, quoteIdent(name))
			plan.selTypes = append(plan.selTypes, tm.typeName(name))
		}
		plan.order = append(plan.order, oc)
	}

	where, err := compileFilters(tm, req.Filters)
	if err != nil {
		return nil, err
	}
	plan.check = requestCheck(tm, req)
	plan.countSQL = "SELECT COUNT(*) FROM " + table + whereClause(where)

	keyset := len(tm.key) > 0
	if !keyset {
		plan.offset = 0
	}
	if req.After != "" {
		cur, err := decodeCursor(req.After, plan.check)
		if err != nil {
			return nil, err
		}
		if keyset {
			pred, err := keysetPredicate(tm, plan.order, cur.K, desc)
			if err != nil {
				return nil, err
			}
			where = append(where, pred)
		} else {
			if cur.O < 0 || cur.O > maxOffset || len(cur.K) > 0 {
				return nil, errBadCursor
			}
			plan.offset = cur.O
		}
	}

	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(strings.Join(selects, ", "))
	if len(selects) == 0 {
		b.WriteString("NULL")
	}
	b.WriteString(" FROM ")
	b.WriteString(table)
	b.WriteString(whereClause(where))
	if len(plan.order) > 0 {
		b.WriteString(" ORDER BY ")
		for i, oc := range plan.order {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(quoteIdent(oc.name))
			if desc {
				b.WriteString(" DESC")
			}
		}
	}
	b.WriteString(" LIMIT ")
	b.WriteString(strconv.Itoa(limit + 1))
	if plan.offset > 0 {
		b.WriteString(" OFFSET ")
		b.WriteString(strconv.FormatInt(plan.offset, 10))
	}
	plan.sql = b.String()
	return plan, nil
}

func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// compileFilters turns filters into SQL conditions with literal values.
// like and ilike match the column's text, so they work on any type; JSON
// compares as text too.
func compileFilters(tm *tableMeta, filters []apitypes.DataFilter) ([]string, error) {
	out := make([]string, 0, len(filters))
	for _, f := range filters {
		j, ok := tm.col[f.Column]
		if !ok {
			return nil, fmt.Errorf("%w: no column %s to filter by", data.ErrInvalid, f.Column)
		}
		col := tm.Columns[j]
		expr := quoteIdent(col.Name)
		text := "CAST(" + expr + " AS CHAR)"
		cmpExpr, typ := expr, col.TypeName
		if typ == "json" {
			cmpExpr, typ = text, "text"
		}
		lit := func(v string) (string, error) { return literal(typ, v) }
		if f.Op != apitypes.FilterIn && len(f.Values) > 0 {
			return nil, fmt.Errorf("%w: only in takes values", data.ErrInvalid)
		}
		var cond string
		switch f.Op {
		case apitypes.FilterEq, apitypes.FilterNe, apitypes.FilterLt, apitypes.FilterLte, apitypes.FilterGt, apitypes.FilterGte:
			v, err := lit(f.Value)
			if err != nil {
				return nil, err
			}
			op := map[apitypes.DataFilterOp]string{
				apitypes.FilterEq: " = ", apitypes.FilterNe: " <> ", apitypes.FilterLt: " < ",
				apitypes.FilterLte: " <= ", apitypes.FilterGt: " > ", apitypes.FilterGte: " >= ",
			}[f.Op]
			cond = cmpExpr + op + v
		case apitypes.FilterLike:
			cond = text + " LIKE " + quoteString(f.Value)
		case apitypes.FilterILike:
			cond = "LOWER(" + text + ") LIKE LOWER(" + quoteString(f.Value) + ")"
		case apitypes.FilterIn:
			if len(f.Values) == 0 || len(f.Values) > apitypes.DataPageMaxLimit || f.Value != "" {
				return nil, fmt.Errorf("%w: in takes 1 to %d values", data.ErrInvalid, apitypes.DataPageMaxLimit)
			}
			vals := make([]string, len(f.Values))
			for i, v := range f.Values {
				var err error
				if vals[i], err = lit(v); err != nil {
					return nil, err
				}
			}
			cond = cmpExpr + " IN (" + strings.Join(vals, ", ") + ")"
		case apitypes.FilterIsNull, apitypes.FilterNotNull:
			if f.Value != "" || len(f.Values) > 0 {
				return nil, fmt.Errorf("%w: %s takes no value", data.ErrInvalid, f.Op)
			}
			cond = expr + " IS NULL"
			if f.Op == apitypes.FilterNotNull {
				cond = expr + " IS NOT NULL"
			}
		default:
			return nil, fmt.Errorf("%w: unknown filter operator %q", data.ErrInvalid, f.Op)
		}
		out = append(out, cond)
	}
	return out, nil
}

// keysetPredicate is the condition for rows after the cursor values vals
// in the order cols (all ascending, or all descending). Only the first
// column may be nullable (a sort column); MySQL sorts nulls first
// ascending and last descending.
func keysetPredicate(tm *tableMeta, cols []orderCol, vals []*string, desc bool) (string, error) {
	if len(vals) != len(cols) || len(cols) == 0 {
		return "", errBadCursor
	}
	for _, v := range vals[1:] {
		if v == nil {
			return "", errBadCursor
		}
	}
	op := " > "
	if desc {
		op = " < "
	}
	row := func(cs []orderCol, vs []*string) (string, error) {
		exprs := make([]string, len(cs))
		lits := make([]string, len(cs))
		for i := range cs {
			exprs[i] = quoteIdent(cs[i].name)
			l, err := literal(tm.typeName(cs[i].name), *vs[i])
			if err != nil {
				return "", errBadCursor
			}
			lits[i] = l
		}
		return "(" + strings.Join(exprs, ", ") + ")" + op + "(" + strings.Join(lits, ", ") + ")", nil
	}
	first := cols[0]
	firstExpr := quoteIdent(first.name)
	switch {
	case vals[0] != nil && (!first.nullable || !desc):
		// No nulls, or (ascending) past them: a plain row compare, which
		// excludes nulls.
		r, err := row(cols, vals)
		return "(" + r + ")", err
	case vals[0] != nil:
		// Descending: the nulls come last, after every value.
		r, err := row(cols, vals)
		return "(" + r + " OR " + firstExpr + " IS NULL)", err
	case !first.nullable || len(cols) == 1:
		return "", errBadCursor
	case desc:
		r, err := row(cols[1:], vals[1:])
		return "(" + firstExpr + " IS NULL AND " + r + ")", err
	default:
		r, err := row(cols[1:], vals[1:])
		return "((" + firstExpr + " IS NULL AND " + r + ") OR " + firstExpr + " IS NOT NULL)", err
	}
}

// requestCheck fingerprints what a cursor is valid for, so one used with
// another table, order or filter is refused with a clear error. It isn't
// a security check (see cursor).
func requestCheck(tm *tableMeta, req apitypes.DataPageRequest) string {
	h := fnv.New64a()
	h.Write([]byte(tableKey(tm.Schema, tm.Name)))
	h.Write([]byte{0})
	h.Write([]byte(req.Order))
	h.Write([]byte{0})
	f, _ := json.Marshal(req.Filters)
	h.Write(f)
	return hex.EncodeToString(h.Sum(nil))
}

func encodeCursor(c cursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(s, check string) (cursor, error) {
	var c cursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || json.Unmarshal(b, &c) != nil || c.C != check {
		return c, errBadCursor
	}
	return c, nil
}

// runPage runs a planned page and encodes its rows. It reads limit+1 rows
// to know whether there's a next page.
func runPage(ctx context.Context, c *conn, comment string, plan *pagePlan) (apitypes.DataPage, error) {
	page := apitypes.DataPage{Columns: plan.columns, Rows: []apitypes.DataRow{}}
	var buf, scratch []byte
	ends := make([]int, 0, plan.limit)
	var last []*string
	more := false
	stop := c.watch(ctx)
	// The query reads limit+1 rows: the last one only says there's more.
	err := c.Query(comment+plan.sql, func(got []wire.Column) error {
		if len(got) < len(plan.selTypes) {
			return fmt.Errorf("%w: the server sent %d columns, want %d", data.ErrUnreachable, len(got), len(plan.selTypes))
		}
		return nil
	}, func(vals [][]byte) error {
		if len(ends) == plan.limit {
			more = true
			return nil
		}
		buf = appendRow(buf, vals, plan.cols, plan.full, &scratch)
		ends = append(ends, len(buf))
		if len(ends) == plan.limit && plan.offset < 0 {
			last = make([]*string, len(plan.order))
			for i, oc := range plan.order {
				if v := vals[oc.at]; v != nil {
					s := string(v)
					if _, hexed := cellOf(plan.selTypes[oc.at]); hexed {
						s = `\x` + hex.EncodeToString(v)
					}
					last[i] = &s
				}
			}
		}
		return nil
	}, nil)
	stop()
	if err != nil {
		return page, dbError(err)
	}
	page.Rows = make([]apitypes.DataRow, len(ends))
	start := 0
	for i, end := range ends {
		page.Rows[i] = buf[start:end:end]
		start = end
	}
	if more {
		switch {
		case plan.offset >= 0 && plan.offset+int64(plan.limit) <= maxOffset:
			page.Next = encodeCursor(cursor{C: plan.check, O: plan.offset + int64(plan.limit)})
		case plan.offset < 0:
			page.Next = encodeCursor(cursor{C: plan.check, K: last})
		}
	}
	return page, nil
}

// References reads, per foreign key referencing the row (at most
// maxReferenceFKs), the first rows that reference it, with Postgres'
// rules for masked calls (pg.Engine.References): they run as the masked
// reader, can't name the row by a sensitive key, and foreign keys that
// need a sensitive column are left out.
func (e *Engine) References(ctx context.Context, t data.Target, c data.Call, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	limit := req.Limit
	if limit == 0 {
		limit = apitypes.DataReferencesLimit
	}
	if limit < 0 || limit > apitypes.DataReferencesMaxLimit {
		return nil, fmt.Errorf("%w: limit must be 1 to %d", data.ErrInvalid, apitypes.DataReferencesMaxLimit)
	}
	role := data.RoleReader
	if c.Masked && t.ReaderMasked.User != "" {
		role = data.RoleReaderMasked
	}
	cn, err := e.acquire(ctx, t, role)
	if err != nil {
		return nil, err
	}
	defer e.release(cn)
	cat, err := e.catalog(ctx, t, cn.Conn)
	if err != nil {
		return nil, err
	}
	tm, err := cat.table(req.Table)
	if err != nil {
		return nil, err
	}
	if err := checkKey(tm, req.Key); err != nil {
		return nil, err
	}
	masked := masks(c, tm)
	for _, k := range tm.PrimaryKey {
		if masked[k] {
			return nil, fmt.Errorf("%w: can't look up references by sensitive column %s", data.ErrInvalid, k)
		}
	}
	var fks []incomingFK
	for _, in := range cat.incoming[tableKey(tm.Schema, tm.Name)] {
		if len(fks) == maxReferenceFKs {
			break
		}
		if !usesMasked(in, masked, masks(c, in.from)) {
			fks = append(fks, in)
		}
	}
	comment := actorComment(e.app, c.Actor)
	values, err := referencedValues(ctx, cn.Conn, comment, tm, req.Key, fks)
	if err != nil {
		return nil, err
	}
	out := make([]apitypes.DataReferences, 0, len(fks))
fk:
	for _, in := range fks {
		filters := make([]apitypes.DataFilter, len(in.fk.Columns))
		for i, col := range in.fk.Columns {
			v := values[in.fk.RefColumns[i]]
			if v == nil {
				continue fk // a NULL is never referenced
			}
			filters[i] = apitypes.DataFilter{Column: col, Op: apitypes.FilterEq, Value: *v}
		}
		preq := apitypes.DataPageRequest{Table: in.from.Schema + "." + in.from.Name, Filters: filters, Limit: limit}
		plan, err := planPage(in.from, preq, masks(c, in.from))
		if err != nil {
			return nil, err
		}
		page, err := runPage(ctx, cn.Conn, comment, plan)
		if err != nil {
			return nil, err
		}
		page.EstimatedRows = in.from.EstimatedRows
		out = append(out, apitypes.DataReferences{
			Constraint: in.fk.Name, Schema: in.from.Schema, Table: in.from.Name,
			Columns: in.fk.Columns, Filters: filters, Page: page,
		})
	}
	return out, nil
}

// usesMasked reports whether reading the rows of foreign key in needs a
// masked column (see pg's usesMasked).
func usesMasked(in incomingFK, refMasked, fromMasked map[string]bool) bool {
	for _, col := range in.fk.RefColumns {
		if refMasked[col] {
			return true
		}
	}
	for _, col := range append(slices.Clone(in.fk.Columns), in.from.key...) {
		if fromMasked[col] {
			return true
		}
	}
	return false
}

// checkKey requires key to name exactly tm's primary key columns.
func checkKey(tm *tableMeta, key map[string]string) error {
	if len(tm.PrimaryKey) == 0 {
		return fmt.Errorf("%w: %s.%s has no primary key", data.ErrInvalid, tm.Schema, tm.Name)
	}
	if len(key) != len(tm.PrimaryKey) {
		return fmt.Errorf("%w: the key must be the primary key (%s)", data.ErrInvalid, strings.Join(tm.PrimaryKey, ", "))
	}
	for _, k := range tm.PrimaryKey {
		if _, ok := key[k]; !ok {
			return fmt.Errorf("%w: the key must be the primary key (%s)", data.ErrInvalid, strings.Join(tm.PrimaryKey, ", "))
		}
	}
	return nil
}

// keyCondition matches tm's primary key against key.
func keyCondition(tm *tableMeta, key map[string]string) (string, error) {
	conds := make([]string, len(tm.PrimaryKey))
	for i, k := range tm.PrimaryKey {
		l, err := literal(tm.typeName(k), key[k])
		if err != nil {
			return "", err
		}
		conds[i] = quoteIdent(k) + " = " + l
	}
	return strings.Join(conds, " AND "), nil
}

// referencedValues returns the referenced row's values of every column the
// foreign keys point at: from the key when they are key columns, else
// read from the row (one query).
func referencedValues(ctx context.Context, c *conn, comment string, tm *tableMeta, key map[string]string, fks []incomingFK) (map[string]*string, error) {
	values := make(map[string]*string, len(key))
	for k, v := range key {
		values[k] = &v
	}
	var need []string
	for _, in := range fks {
		for _, rc := range in.fk.RefColumns {
			if _, ok := values[rc]; !ok && !slices.Contains(need, rc) {
				need = append(need, rc)
			}
		}
	}
	if len(need) == 0 {
		return values, nil
	}
	cond, err := keyCondition(tm, key)
	if err != nil {
		return nil, err
	}
	exprs := make([]string, len(need))
	for i, n := range need {
		exprs[i] = quoteIdent(n)
	}
	found := false
	err = c.query(ctx, comment+"SELECT "+strings.Join(exprs, ", ")+" FROM "+quoteTable(tm.Schema, tm.Name)+" WHERE "+cond, len(need), func(r [][]byte) error {
		found = true
		for i, n := range need {
			if r[i] != nil {
				s := string(r[i])
				if _, hexed := cellOf(tm.typeName(n)); hexed {
					s = `\x` + hex.EncodeToString(r[i])
				}
				values[n] = &s
			} else {
				values[n] = nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, dbError(err)
	}
	if !found {
		return nil, fmt.Errorf("%w: no row with that key", data.ErrInvalid)
	}
	return values, nil
}
