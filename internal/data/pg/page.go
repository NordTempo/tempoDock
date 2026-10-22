package pg

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// maxReferenceFKs caps how many incoming foreign keys References reads
// (one query each).
const maxReferenceFKs = 50

// textResults asks pgx for every result column in text format, which the
// row encoder turns into cells without decoding.
var textResults = pgx.QueryResultFormats{pgx.TextFormatCode}

// orderCol is one column of a page's ORDER BY.
type orderCol struct {
	expr     string // quoted identifier
	nullable bool
	// at is the column's index in the result (cursor values).
	at int
}

// pagePlan is a compiled page request.
type pagePlan struct {
	sql       string
	args      []any
	countSQL  string
	countArgs []any
	columns   []apitypes.DataResultColumn
	// src and kinds are per output column (see appendRow).
	src   []int
	kinds []cellKind
	order []orderCol
	limit int
	full  bool
	// offset is the row offset of an offset-paged relation (views and
	// foreign tables have no key); -1 for keyset pages.
	offset int64
	check  string
}

// cursor is the decoded form of DataPage.Next: a check of the request it
// belongs to, then the last row's order values (keyset) or the offset.
// The values are in the clear, sensitive ones included when the key is
// sensitive, and the check is no MAC: core seals cursors (encrypts and
// authenticates them) before they leave the daemon, so callers can
// neither read nor forge one.
type cursor struct {
	C string    `json:"c"`
	K []*string `json:"k,omitempty"`
	O int64     `json:"o,omitempty"`
}

// Page reads one keyset page as the reader. Relations without any key
// (views, foreign tables) page by offset, in the requested order or none.
// Masked calls see their sensitive columns as masked cells and can't
// filter or sort by them.
func (e *Engine) Page(ctx context.Context, t data.Target, c data.Call, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	var page apitypes.DataPage
	err := e.read(ctx, t, data.RoleReader, c, apitypes.DataQueryTimeout, func(cn *conn) error {
		cat, err := e.catalog(ctx, t, cn, true)
		if err != nil {
			return err
		}
		tm, err := cat.table(req.Table)
		if err != nil {
			return err
		}
		masked := masks(c, tm)
		if err := checkMaskedUse(req, masked); err != nil {
			return err
		}
		plan, err := planPage(tm, req, masked)
		if err != nil {
			return err
		}
		page, err = runPage(ctx, cn.pg, plan)
		if err != nil {
			return err
		}
		page.EstimatedRows = tm.EstimatedRows
		if req.Count {
			var n int64
			err := cn.pg.QueryRow(ctx, plan.countSQL, plan.countArgs...).Scan(&n)
			if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && pgErr.Code == "57014" && ctx.Err() == nil {
				return nil // counting took too long: no exact count
			}
			if err != nil {
				return dbError(err)
			}
			page.ExactRows = &n
		}
		return nil
	})
	return page, err
}

// checkMaskedUse rejects filtering or sorting by a masked column, which
// would reveal its values.
func checkMaskedUse(req apitypes.DataPageRequest, masked map[string]bool) error {
	if masked[strings.TrimPrefix(req.Order, "-")] {
		return fmt.Errorf("%w: can't sort by sensitive column %s", data.ErrInvalid, strings.TrimPrefix(req.Order, "-"))
	}
	for _, f := range req.Filters {
		if masked[f.Column] {
			return fmt.Errorf("%w: can't filter by sensitive column %s", data.ErrInvalid, f.Column)
		}
	}
	return nil
}

// params collects positional parameters while SQL is built.
type params struct{ args []any }

// add binds v and returns its placeholder.
func (p *params) add(v any) string {
	p.args = append(p.args, v)
	return "$" + strconv.Itoa(len(p.args))
}

// planPage compiles a page request against tm. masked columns are left out
// of the select list. Every identifier is quoted and every value bound.
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
	table := pgx.Identifier{tm.Schema, tm.Name}.Sanitize()

	// Select list: visible columns, then order columns not among them.
	selects := make([]string, 0, len(tm.Columns)+2)
	plan.columns = make([]apitypes.DataResultColumn, len(tm.Columns))
	plan.src = make([]int, len(tm.Columns))
	plan.kinds = make([]cellKind, len(tm.Columns))
	at := map[string]int{}
	for i, col := range tm.Columns {
		plan.columns[i] = apitypes.DataResultColumn{Name: col.Name, TypeName: col.TypeName}
		if masked[col.Name] {
			plan.src[i], plan.kinds[i] = -1, kindMasked
			continue
		}
		at[col.Name] = len(selects)
		plan.src[i], plan.kinds[i] = len(selects), kindOfType(col.TypeName)
		selects = append(selects, pgx.Identifier{col.Name}.Sanitize())
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
	key := tm.key
	if len(key) == 0 {
		key = tm.sysKey
	}
	for _, k := range key {
		if k != sort {
			names = append(names, k)
		}
	}
	for _, name := range names {
		oc := orderCol{expr: pgx.Identifier{name}.Sanitize()}
		if j, ok := tm.col[name]; ok {
			oc.nullable = tm.Columns[j].Nullable
		}
		if j, ok := at[name]; ok {
			oc.at = j
		} else {
			oc.at = len(selects)
			at[name] = oc.at
			selects = append(selects, oc.expr)
		}
		plan.order = append(plan.order, oc)
	}

	var p params
	where, err := compileFilters(tm, req.Filters, &p)
	if err != nil {
		return nil, err
	}
	plan.check = requestCheck(tm, req)
	plan.countSQL = "SELECT count(*) FROM " + table + whereClause(where)
	plan.countArgs = append([]any(nil), p.args...)

	keyset := len(key) > 0
	if !keyset {
		plan.offset = 0
	}
	if req.After != "" {
		cur, err := decodeCursor(req.After, plan.check)
		if err != nil {
			return nil, err
		}
		if keyset {
			pred, err := keysetPredicate(plan.order, cur.K, desc, &p)
			if err != nil {
				return nil, err
			}
			where = append(where, pred)
		} else {
			if cur.O < 0 || len(cur.K) > 0 {
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
			b.WriteString(oc.expr)
			switch {
			case desc && oc.nullable:
				b.WriteString(" DESC NULLS FIRST")
			case desc:
				b.WriteString(" DESC")
			case oc.nullable:
				b.WriteString(" NULLS LAST")
			}
		}
	}
	b.WriteString(" LIMIT ")
	b.WriteString(p.add(int64(limit + 1)))
	if plan.offset > 0 {
		b.WriteString(" OFFSET ")
		b.WriteString(p.add(plan.offset))
	}
	plan.sql = b.String()
	plan.args = p.args
	return plan, nil
}

func whereClause(conds []string) string {
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// compileFilters turns filters into SQL conditions with bound values.
// Postgres infers each parameter's type from its column; like and ilike
// match the column's text output, so they work on any type.
func compileFilters(tm *tableMeta, filters []apitypes.DataFilter, p *params) ([]string, error) {
	out := make([]string, 0, len(filters))
	for _, f := range filters {
		j, ok := tm.col[f.Column]
		if !ok {
			return nil, fmt.Errorf("%w: no column %s to filter by", data.ErrInvalid, f.Column)
		}
		col := tm.Columns[j]
		expr := pgx.Identifier{col.Name}.Sanitize()
		cmp := expr
		if noEquality(col.TypeName) {
			cmp = expr + "::text"
		}
		hasValue := f.Value != "" || len(f.Values) > 0
		var cond string
		switch f.Op {
		case apitypes.FilterEq:
			cond = cmp + " = " + p.add(f.Value)
		case apitypes.FilterNe:
			cond = cmp + " <> " + p.add(f.Value)
		case apitypes.FilterLt:
			cond = expr + " < " + p.add(f.Value)
		case apitypes.FilterLte:
			cond = expr + " <= " + p.add(f.Value)
		case apitypes.FilterGt:
			cond = expr + " > " + p.add(f.Value)
		case apitypes.FilterGte:
			cond = expr + " >= " + p.add(f.Value)
		case apitypes.FilterLike:
			cond = expr + "::text LIKE " + p.add(f.Value)
		case apitypes.FilterILike:
			cond = expr + "::text ILIKE " + p.add(f.Value)
		case apitypes.FilterIn:
			if len(f.Values) == 0 || len(f.Values) > apitypes.DataPageMaxLimit || f.Value != "" {
				return nil, fmt.Errorf("%w: in takes 1 to %d values", data.ErrInvalid, apitypes.DataPageMaxLimit)
			}
			if strings.HasPrefix(col.TypeName, "_") {
				return nil, fmt.Errorf("%w: in doesn't work on array column %s", data.ErrInvalid, col.Name)
			}
			// One array parameter in text input format: the statement is
			// the same however many values there are.
			arr := p.add(arrayLiteral(f.Values))
			if cmp != expr {
				arr += "::text[]"
			}
			cond = cmp + " = ANY(" + arr + ")"
		case apitypes.FilterIsNull, apitypes.FilterNotNull:
			if hasValue {
				return nil, fmt.Errorf("%w: %s takes no value", data.ErrInvalid, f.Op)
			}
			cond = expr + " IS NULL"
			if f.Op == apitypes.FilterNotNull {
				cond = expr + " IS NOT NULL"
			}
		default:
			return nil, fmt.Errorf("%w: unknown filter operator %q", data.ErrInvalid, f.Op)
		}
		if f.Op != apitypes.FilterIn && len(f.Values) > 0 {
			return nil, fmt.Errorf("%w: only in takes values", data.ErrInvalid)
		}
		out = append(out, cond)
	}
	return out, nil
}

// keysetPredicate is the condition for rows after the cursor values vals
// in the order cols (all ascending, or all descending). Only the first
// column may be nullable (a sort column); nulls sort last ascending and
// first descending, like Postgres' default.
func keysetPredicate(cols []orderCol, vals []*string, desc bool, p *params) (string, error) {
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
	row := func(cs []orderCol, vs []*string) string {
		exprs := make([]string, len(cs))
		ph := make([]string, len(cs))
		for i := range cs {
			exprs[i] = cs[i].expr
			ph[i] = p.add(*vs[i])
		}
		return "(" + strings.Join(exprs, ", ") + ")" + op + "(" + strings.Join(ph, ", ") + ")"
	}
	first := cols[0]
	switch {
	case vals[0] != nil && (!first.nullable || desc):
		// Past the nulls (descending) or no nulls: a plain row compare,
		// which excludes nulls.
		return "(" + row(cols, vals) + ")", nil
	case vals[0] != nil:
		return "(" + row(cols, vals) + " OR " + first.expr + " IS NULL)", nil
	case !first.nullable || len(cols) == 1:
		return "", errBadCursor
	case desc:
		return "((" + first.expr + " IS NULL AND " + row(cols[1:], vals[1:]) + ") OR " + first.expr + " IS NOT NULL)", nil
	default:
		return "(" + first.expr + " IS NULL AND " + row(cols[1:], vals[1:]) + ")", nil
	}
}

var errBadCursor = fmt.Errorf("%w: the cursor doesn't belong to this table, order and filter; start from the first page", data.ErrInvalid)

// requestCheck fingerprints what a cursor is valid for, so a cursor used
// with another table, order or filter is refused with a clear error. It
// isn't a security check (see cursor).
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
// to know whether there's a next page; rows are encoded into one buffer.
func runPage(ctx context.Context, pg *pgx.Conn, plan *pagePlan) (apitypes.DataPage, error) {
	page := apitypes.DataPage{Columns: plan.columns, Rows: []apitypes.DataRow{}}
	rows, err := pg.Query(ctx, plan.sql, append([]any{textResults}, plan.args...)...)
	if err != nil {
		return page, dbError(err)
	}
	defer rows.Close()
	var buf []byte
	ends := make([]int, 0, plan.limit)
	var last []*string
	more := false
	for rows.Next() {
		if len(ends) == plan.limit {
			more = true
			break
		}
		vals := rows.RawValues()
		buf = appendRow(buf, vals, plan.src, plan.kinds, plan.full)
		ends = append(ends, len(buf))
		if len(ends) == plan.limit && plan.offset < 0 {
			last = make([]*string, len(plan.order))
			for i, oc := range plan.order {
				if v := vals[oc.at]; v != nil {
					s := string(v)
					last[i] = &s
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return page, dbError(err)
	}
	page.Rows = make([]apitypes.DataRow, len(ends))
	start := 0
	for i, end := range ends {
		page.Rows[i] = buf[start:end:end]
		start = end
	}
	if more {
		if plan.offset >= 0 {
			page.Next = encodeCursor(cursor{C: plan.check, O: plan.offset + int64(plan.limit)})
		} else {
			page.Next = encodeCursor(cursor{C: plan.check, K: last})
		}
	}
	return page, nil
}

// References reads, per foreign key referencing the row (at most
// maxReferenceFKs keys), the first rows that reference it. Masked calls
// run as the masked reader (when the target has it), so the database
// itself keeps sensitive columns out of reach; they can't name the row by
// a sensitive key column (whether the row exists would tell its value),
// and foreign keys that would need a sensitive column (as a referencing
// or referenced column, or to order the referencing table) are left out,
// since their filters would carry its value.
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
	var out []apitypes.DataReferences
	err := e.read(ctx, t, role, c, apitypes.DataQueryTimeout, func(cn *conn) error {
		cat, err := e.catalog(ctx, t, cn, true)
		if err != nil {
			return err
		}
		tm, err := cat.table(req.Table)
		if err != nil {
			return err
		}
		if err := checkKey(tm, req.Key); err != nil {
			return err
		}
		masked := masks(c, tm)
		for _, k := range tm.PrimaryKey {
			if masked[k] {
				return fmt.Errorf("%w: can't look up references by sensitive column %s", data.ErrInvalid, k)
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
		values, err := referencedValues(ctx, cn.pg, tm, req.Key, fks)
		if err != nil {
			return err
		}
		out = make([]apitypes.DataReferences, 0, len(fks))
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
				return err
			}
			page, err := runPage(ctx, cn.pg, plan)
			if err != nil {
				return err
			}
			page.EstimatedRows = in.from.EstimatedRows
			out = append(out, apitypes.DataReferences{
				Constraint: in.fk.Name,
				Schema:     in.from.Schema,
				Table:      in.from.Name,
				Columns:    in.fk.Columns,
				Filters:    filters,
				Page:       page,
			})
		}
		return nil
	})
	return out, err
}

// usesMasked reports whether reading the rows of foreign key in needs a
// masked column: one it references (refMasked, the referenced table's) or
// one of the referencing table's (fromMasked) that it filters or orders
// by.
func usesMasked(in incomingFK, refMasked, fromMasked map[string]bool) bool {
	for _, col := range in.fk.RefColumns {
		if refMasked[col] {
			return true
		}
	}
	key := in.from.key
	if len(key) == 0 {
		key = in.from.sysKey
	}
	for _, col := range append(slices.Clone(in.fk.Columns), key...) {
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

// referencedValues returns the referenced row's values of every column the
// foreign keys point at: from the key when they are key columns, else read
// from the row (one query).
func referencedValues(ctx context.Context, pg *pgx.Conn, tm *tableMeta, key map[string]string, fks []incomingFK) (map[string]*string, error) {
	values := make(map[string]*string, len(key))
	for k, v := range key {
		values[k] = &v
	}
	var need []string
	for _, in := range fks {
		for _, rc := range in.fk.RefColumns {
			if _, ok := values[rc]; !ok && !contains(need, rc) {
				need = append(need, rc)
			}
		}
	}
	if len(need) == 0 {
		return values, nil
	}
	var p params
	exprs := make([]string, len(need))
	for i, n := range need {
		exprs[i] = pgx.Identifier{n}.Sanitize() + "::text"
	}
	sql := "SELECT " + strings.Join(exprs, ", ") + " FROM " + pgx.Identifier{tm.Schema, tm.Name}.Sanitize() + " WHERE " + keyCondition(tm, key, &p)
	dest := make([]any, len(need))
	got := make([]*string, len(need))
	for i := range dest {
		dest[i] = &got[i]
	}
	err := pg.QueryRow(ctx, sql, p.args...).Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: no row with that key", data.ErrInvalid)
	}
	if err != nil {
		return nil, dbError(err)
	}
	for i, n := range need {
		values[n] = got[i]
	}
	return values, nil
}

// keyCondition matches tm's primary key against key.
func keyCondition(tm *tableMeta, key map[string]string, p *params) string {
	conds := make([]string, len(tm.PrimaryKey))
	for i, k := range tm.PrimaryKey {
		conds[i] = pgx.Identifier{k}.Sanitize() + " = " + p.add(key[k])
	}
	return strings.Join(conds, " AND ")
}
