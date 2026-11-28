package mongo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

const (
	// maxCollections caps the collections the catalog reads (two
	// commands each); past it the rest are listed without counts or
	// fields.
	maxCollections = 500
	// sampleDocs is how many documents of each collection name its
	// columns.
	sampleDocs = 20
	// maxOffset is how deep a page goes (skip costs the server every
	// skipped document); past it, use the console.
	maxOffset = 10_000
	// catalogTimeout bounds each catalog command.
	catalogTimeout = 2 * time.Second
)

// catalog is a database server's collections as last read.
type catalog struct {
	tables []apitypes.DataTable
	readAt time.Time
}

// Schema lists every collection of every database but the server's own,
// with its estimated count and the fields of a sample of its documents.
// Cached for data.SchemaTTL.
func (e *Engine) Schema(ctx context.Context, t data.Target, c data.Call, sensitive []apitypes.DataColumnRef) (apitypes.DataSchema, error) {
	e.mu.Lock()
	cat := e.schemas[t.ServiceID]
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return apitypes.DataSchema{}, fmt.Errorf("%w: the engine is closed", data.ErrUnreachable)
	}
	if cat != nil && time.Since(cat.readAt) < data.SchemaTTL {
		return markSensitive(cat, sensitive, c.Masked), nil
	}
	e.mu.Lock()
	gen := e.gen
	e.mu.Unlock()
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	defer e.release(cn)
	if cat, err = readCatalog(ctx, cn); err != nil {
		return apitypes.DataSchema{}, err
	}
	// A Forget (or Close) while it was read may mean another server: the
	// result is returned but not kept.
	e.mu.Lock()
	if e.gen == gen && !e.closed {
		e.schemas[t.ServiceID] = cat
	}
	e.mu.Unlock()
	return markSensitive(cat, sensitive, c.Masked), nil
}

// readCatalog reads the databases, their collections, a count and a
// sample of field names and types for each.
func readCatalog(ctx context.Context, cn *cached) (*catalog, error) {
	reply, err := run(ctx, cn, authDB, wire.D{{Key: "listDatabases", Value: int32(1)}, {Key: "nameOnly", Value: true}})
	if err != nil {
		return nil, fmt.Errorf("%w: list databases: %w", data.ErrUnreachable, err)
	}
	var dbs []string
	list, _ := reply.Get("databases")
	for _, d := range asArray(list) {
		name, ok := must(asDoc(d).Get("name")).(string)
		if !ok {
			return nil, fmt.Errorf("%w: listDatabases sent a database without a name", data.ErrUnreachable)
		}
		if !systemDBs[name] {
			dbs = append(dbs, name)
		}
	}
	slices.Sort(dbs)
	cat := &catalog{readAt: time.Now(), tables: []apitypes.DataTable{}}
	read := 0
	for _, db := range dbs {
		colls, err := listCollections(ctx, cn, db, false)
		if err != nil {
			return nil, fmt.Errorf("%w: list collections of %s: %w", data.ErrUnreachable, db, err)
		}
		slices.SortFunc(colls, func(a, b any) int { return strings.Compare(docStr(a, "name"), docStr(b, "name")) })
		for _, cv := range colls {
			name, kind := docStr(cv, "name"), docStr(cv, "type")
			if strings.HasPrefix(name, "system.") {
				continue
			}
			tb := apitypes.DataTable{
				Schema: db, Name: name, Kind: apitypes.TableKindTable, EstimatedRows: -1,
				Columns: []apitypes.DataColumn{{Name: "_id", Type: "objectId", TypeName: typePrefix + "objectId"}}, PrimaryKey: []string{"_id"},
				ForeignKeys: []apitypes.DataForeignKey{}, Indexes: []apitypes.DataIndex{}, Constraints: []apitypes.DataConstraint{},
				Editable: kind == "collection",
			}
			if kind == "view" {
				tb.Kind = apitypes.TableKindView
			}
			if read < maxCollections {
				read++
				describe(ctx, cn, &tb)
			}
			cat.tables = append(cat.tables, tb)
		}
	}
	return cat, nil
}

// describe fills a collection's count and sampled fields; a command that
// fails (a view whose pipeline errs) leaves them unknown.
func describe(ctx context.Context, cn *cached, tb *apitypes.DataTable) {
	// The server ends a slow command at maxTimeMS with an error; the
	// socket's deadline is later, so the connection stays usable.
	cctx, cancel := context.WithTimeout(ctx, catalogTimeout+5*time.Second)
	defer cancel()
	if tb.Kind == apitypes.TableKindTable {
		if reply, err := run(cctx, cn, tb.Schema, wire.D{{Key: "count", Value: tb.Name}, {Key: "maxTimeMS", Value: int32(catalogTimeout / time.Millisecond)}}); err == nil {
			tb.EstimatedRows = asInt(reply.Get("n"))
		}
	}
	// Each document's field names and types, never its values.
	reply, err := run(cctx, cn, tb.Schema, wire.D{
		{Key: "aggregate", Value: tb.Name},
		{Key: "pipeline", Value: wire.A{
			wire.D{{Key: "$limit", Value: int32(sampleDocs)}},
			wire.D{{Key: "$project", Value: wire.D{{Key: "_id", Value: int32(0)}, {Key: "f", Value: wire.D{{Key: "$map", Value: wire.D{
				{Key: "input", Value: wire.D{{Key: "$objectToArray", Value: "$$ROOT"}}},
				{Key: "in", Value: wire.A{"$$this.k", wire.D{{Key: "$type", Value: "$$this.v"}}}},
			}}}}}}},
		}},
		{Key: "cursor", Value: wire.D{}}, {Key: "maxTimeMS", Value: int32(catalogTimeout / time.Millisecond)},
	})
	if err != nil {
		return
	}
	seen := map[string]int{"_id": 0}
	idSeen := false // the _id column's type is a default until sampled
	sample, _, err := drain(cctx, cn, tb.Schema, reply, sampleDocs, maxListBytes)
	if err != nil {
		return
	}
	for _, d := range sample {
		f, _ := asDoc(d).Get("f")
		for _, kv := range asArray(f) {
			pair := asArray(kv)
			if len(pair) != 2 {
				continue
			}
			name, _ := pair[0].(string)
			typ, _ := pair[1].(string)
			i, ok := seen[name]
			if !ok {
				seen[name] = len(tb.Columns)
				tb.Columns = append(tb.Columns, apitypes.DataColumn{Name: name, Type: typ, TypeName: typePrefix + typ, Nullable: true})
				continue
			}
			if i == 0 && !idSeen {
				idSeen = true
				tb.Columns[0].Type, tb.Columns[0].TypeName = typ, typePrefix+typ
			} else if tb.Columns[i].Type == "null" && typ != "null" {
				tb.Columns[i].Type, tb.Columns[i].TypeName = typ, typePrefix+typ
			} else if tb.Columns[i].Type != typ && typ != "null" && tb.Columns[i].Type != "null" {
				tb.Columns[i].Type, tb.Columns[i].TypeName = "mixed", typePrefix+"mixed"
			}
		}
	}
}

// maxNames bounds how many collections of one database are listed.
const maxNames = 10_000

// listCollections lists a database's collections, every batch of the
// cursor, up to maxNames.
func listCollections(ctx context.Context, cn *cached, db string, nameOnly bool) (wire.A, error) {
	reply, err := run(ctx, cn, db, wire.D{{Key: "listCollections", Value: int32(1)}, {Key: "nameOnly", Value: nameOnly}, {Key: "authorizedCollections", Value: true}})
	if err != nil {
		return nil, err
	}
	docs, _, err := drain(ctx, cn, db, reply, maxNames, maxListBytes)
	return docs, err
}

// maxListBytes bounds a collection list's replies; pageBytes a page's.
const (
	maxListBytes = 64 << 20
	pageBytes    = 32 << 20
)

// drain reads a cursor reply's batches until the cursor ends, max
// documents are in or the replies passed maxBytes (cut), and closes a
// cursor it leaves open.
func drain(ctx context.Context, cn *cached, db string, reply wire.D, max, maxBytes int) (docs wire.A, cut bool, err error) {
	docs = firstBatch(reply)
	id, ns := cursorID(reply), cursorNS(reply)
	bytes := cn.Conn.LastReply
	for id != 0 && len(docs) < max {
		if bytes > maxBytes {
			cut = true
			break
		}
		more, err := run(ctx, cn, db, wire.D{{Key: "getMore", Value: id}, {Key: "collection", Value: ns}})
		if err != nil {
			closeCursor(ctx, cn, db, ns, id)
			return nil, false, err
		}
		bytes += cn.Conn.LastReply
		c, _ := more.Get("cursor")
		nb, _ := asDoc(c).Get("nextBatch")
		docs, id = append(docs, asArray(nb)...), cursorID(more)
	}
	if id != 0 {
		closeCursor(ctx, cn, db, ns, id)
	}
	if len(docs) > max {
		docs = docs[:max]
	}
	return docs, cut, nil
}

// closeCursor closes a cursor while the connection still can, under a
// short context of its own: a caller that gave up must not leave it on
// the server.
func closeCursor(ctx context.Context, cn *cached, db, ns string, id int64) {
	if cn.Broken || cn.Conn.Broken() {
		return
	}
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, _ = run(kctx, cn, db, wire.D{{Key: "killCursors", Value: ns}, {Key: "cursors", Value: wire.A{id}}})
}

// cursorNS is the collection part of a cursor reply's namespace ("$cmd.listCollections" for listCollections).
func cursorNS(reply wire.D) string {
	c, _ := reply.Get("cursor")
	ns, _ := asDoc(c).Get("ns")
	s, _ := ns.(string)
	_, coll, _ := strings.Cut(s, ".")
	return coll
}

// markSensitive copies the catalog with the sensitive fields marked: a
// top-level field, or one that holds a marked path (customer.email marks
// customer). For a masked caller, a view in a database with marks keeps
// only _id: its sampled field names can come from values of a marked
// field, and Dock doesn't follow its pipeline.
func markSensitive(cat *catalog, sensitive []apitypes.DataColumnRef, masked bool) apitypes.DataSchema {
	tables := make([]apitypes.DataTable, len(cat.tables))
	for i, tb := range cat.tables {
		tables[i] = tb
		if masked && tb.Kind == apitypes.TableKindView && slices.ContainsFunc(sensitive, func(r apitypes.DataColumnRef) bool { return r.Schema == tb.Schema }) {
			tables[i].Columns = tb.Columns[:1]
			continue
		}
		paths := pathsOf(sensitive, tb.Schema, tb.Name)
		if len(paths) == 0 {
			continue
		}
		cols := make([]apitypes.DataColumn, len(tb.Columns))
		for j, col := range tb.Columns {
			col.Sensitive = slices.ContainsFunc(paths, func(p string) bool { return p == col.Name || strings.HasPrefix(p, col.Name+".") })
			cols[j] = col
		}
		tables[i].Columns = cols
	}
	return apitypes.DataSchema{Tables: tables, ReadAt: cat.readAt}
}

// pathsOf are the sensitive field paths of one collection.
func pathsOf(sensitive []apitypes.DataColumnRef, db, coll string) []string {
	var out []string
	for _, s := range sensitive {
		if s.Schema == db && s.Table == coll {
			out = append(out, s.Column)
		}
	}
	return out
}

// splitTable splits "database.collection" (a collection's name may hold
// dots, a database's can't).
func splitTable(table string) (db, coll string, err error) {
	db, coll, ok := strings.Cut(table, ".")
	if !ok || db == "" || coll == "" || systemDBs[db] {
		return "", "", fmt.Errorf("%w: %q isn't a collection; name it as database.collection", data.ErrInvalid, table)
	}
	return db, coll, nil
}

// Page reads one page of a collection's documents, by offset (sorted by
// _id unless req.Order says otherwise, _id after it), at most maxOffset
// deep.
func (e *Engine) Page(ctx context.Context, t data.Target, c data.Call, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	db, coll, err := splitTable(req.Table)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	masked := maskedPaths(c, db, coll)
	filter, err := pageFilter(req.Filters, masked)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	if filter, err = withWhere(filter, req.Where, masked); err != nil {
		return apitypes.DataPage{}, err
	}
	sort := wire.D{}
	if req.Order != "" {
		field, dir := strings.TrimPrefix(req.Order, "-"), int32(1)
		if strings.HasPrefix(req.Order, "-") {
			dir = -1
		}
		if touches(masked, field) {
			return apitypes.DataPage{}, fmt.Errorf("%w: %s is sensitive; it can't be sorted by while masked", data.ErrInvalid, field)
		}
		if field != "_id" {
			sort = append(sort, wire.E{Key: field, Value: dir})
		}
	}
	if touches(masked, "_id") {
		// The order of masked keys would say something about them:
		// storage order instead, unless the caller sorted by a field.
		if len(sort) == 0 {
			sort = wire.D{{Key: "$natural", Value: int32(1)}}
		}
	} else {
		sort = append(sort, wire.E{Key: "_id", Value: int32(1)})
	}
	offset := 0
	if req.After != "" {
		if offset, err = strconv.Atoi(strings.TrimPrefix(req.After, "o:")); err != nil || !strings.HasPrefix(req.After, "o:") || offset < 0 {
			return apitypes.DataPage{}, fmt.Errorf("%w: bad cursor", data.ErrInvalid)
		}
	}
	if offset > maxOffset {
		return apitypes.DataPage{}, fmt.Errorf("%w: pages go %d documents deep; filter, or use the console with skip", data.ErrInvalid, maxOffset)
	}
	limit := req.Limit
	if limit <= 0 {
		limit = apitypes.DataPageLimit
	}
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return apitypes.DataPage{}, err
	}
	defer e.release(cn)
	if c.Masked && slices.ContainsFunc(c.Sensitive, func(r apitypes.DataColumnRef) bool { return r.Schema == db }) {
		// A view can show a marked field of another collection under any
		// name; Dock can't follow its pipeline, so it isn't read masked.
		// The kind is read now, not from the cached catalog.
		view, err := isView(ctx, cn, db, coll)
		if err != nil {
			return apitypes.DataPage{}, queryError(err)
		}
		if view {
			return apitypes.DataPage{}, fmt.Errorf("%w: %s is a view, and %s has sensitive fields masked for you; a view can show them under other names, so it can't be read masked", data.ErrInvalid, coll, db)
		}
	}
	timeout := int32(apitypes.DataQueryTimeout / time.Millisecond)
	reply, err := run(ctx, cn, db, wire.D{
		{Key: "find", Value: coll}, {Key: "filter", Value: filter}, {Key: "sort", Value: sort},
		{Key: "skip", Value: int64(offset)}, {Key: "limit", Value: int64(limit + 1)}, {Key: "batchSize", Value: int32(limit + 1)},
		{Key: "maxTimeMS", Value: timeout},
	})
	if err != nil {
		return apitypes.DataPage{}, queryError(err)
	}
	// Big documents fill a batch (16 MB) before limit+1 of them: read on
	// until the one after the page is in, or there is none.
	docs, cut, err := drain(ctx, cn, db, reply, limit+1, pageBytes)
	if err != nil {
		return apitypes.DataPage{}, queryError(err)
	}
	page := apitypes.DataPage{EstimatedRows: -1, Rows: []apitypes.DataRow{}}
	switch {
	case len(docs) > limit:
		docs = docs[:limit]
		page.Next = "o:" + strconv.Itoa(offset+limit)
	case cut:
		// Big documents: a shorter page, and the next one after it.
		page.Next = "o:" + strconv.Itoa(offset+len(docs))
	}
	e.mu.Lock()
	if cat := e.schemas[t.ServiceID]; cat != nil {
		if i := slices.IndexFunc(cat.tables, func(tb apitypes.DataTable) bool { return tb.Schema == db && tb.Name == coll }); i >= 0 {
			page.EstimatedRows = cat.tables[i].EstimatedRows
		}
	}
	e.mu.Unlock()
	var cols []string
	page.Columns, cols = columnsOf(docs)
	var scratch []byte
	for _, d := range docs {
		page.Rows = append(page.Rows, appendRow(nil, asDoc(d), cols, masked, req.Full, &scratch))
	}
	if req.Count {
		if reply, err := run(ctx, cn, db, wire.D{{Key: "count", Value: coll}, {Key: "query", Value: filter}, {Key: "maxTimeMS", Value: timeout}}); err == nil {
			n := asInt(reply.Get("n"))
			page.ExactRows = &n
		}
	}
	return page, nil
}

// isView reports whether db.coll is a view, as the server says now.
func isView(ctx context.Context, cn *cached, db, coll string) (bool, error) {
	reply, err := run(ctx, cn, db, wire.D{{Key: "listCollections", Value: int32(1)}, {Key: "filter", Value: wire.D{{Key: "name", Value: coll}}}, {Key: "authorizedCollections", Value: true}})
	if err != nil {
		return false, err
	}
	for _, d := range firstBatch(reply) {
		if docStr(d, "type") == "view" {
			return true, nil
		}
	}
	return false, nil
}

// References: documents have no foreign keys.
func (e *Engine) References(context.Context, data.Target, data.Call, apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error) {
	return []apitypes.DataReferences{}, nil
}

// maskedPaths are the sensitive paths a call must not see in one
// collection.
func maskedPaths(c data.Call, db, coll string) []string {
	if !c.Masked {
		return nil
	}
	return pathsOf(c.Sensitive, db, coll)
}

// touches reports whether path and a masked path are one, or one holds
// the other.
func touches(masked []string, path string) bool {
	path = withoutIndexes(path)
	return slices.ContainsFunc(masked, func(m string) bool {
		m = withoutIndexes(m)
		return m == path || strings.HasPrefix(m, path+".") || strings.HasPrefix(path, m+".")
	})
}

// withoutIndexes drops a path's array positions and $ placeholders
// (items.0.email, items.$.email are items.email), as MongoDB reaches the
// same field through them.
func withoutIndexes(path string) string {
	parts := strings.Split(path, ".")
	out := parts[:0]
	for _, p := range parts {
		if p == "$" || p == "$[]" || strings.HasPrefix(p, "$[") || strings.Trim(p, "0123456789") == "" {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ".")
}

// pageFilter turns the page's filters into a find filter. Values are
// Extended JSON as typed in the shell; like and ilike take SQL's % and _.
func pageFilter(filters []apitypes.DataFilter, masked []string) (wire.D, error) {
	// One $and of every condition, so two on one field both hold.
	all := wire.A{}
	for _, f := range filters {
		if f.Column == "" || strings.HasPrefix(f.Column, "$") {
			return nil, fmt.Errorf("%w: a filter needs a field", data.ErrInvalid)
		}
		if touches(masked, f.Column) {
			return nil, fmt.Errorf("%w: %s is sensitive; it can't be filtered on while masked", data.ErrInvalid, f.Column)
		}
		var cond wire.D
		switch f.Op {
		case apitypes.FilterIsNull:
			cond = wire.D{{Key: "$eq", Value: nil}}
		case apitypes.FilterNotNull:
			cond = wire.D{{Key: "$ne", Value: nil}}
		case apitypes.FilterLike, apitypes.FilterILike:
			opts := "s"
			if f.Op == apitypes.FilterILike {
				opts = "si"
			}
			cond = wire.D{{Key: "$regex", Value: likePattern(f.Value)}, {Key: "$options", Value: opts}}
		case apitypes.FilterIn:
			vals := wire.A{}
			for _, s := range f.Values {
				v, err := filterValue(s)
				if err != nil {
					return nil, err
				}
				vals = append(vals, v)
			}
			cond = wire.D{{Key: "$in", Value: vals}}
		default:
			op, ok := map[apitypes.DataFilterOp]string{
				apitypes.FilterEq: "$eq", apitypes.FilterNe: "$ne", apitypes.FilterLt: "$lt",
				apitypes.FilterLte: "$lte", apitypes.FilterGt: "$gt", apitypes.FilterGte: "$gte",
			}[f.Op]
			if !ok {
				return nil, fmt.Errorf("%w: unknown filter %q", data.ErrInvalid, f.Op)
			}
			v, err := filterValue(f.Value)
			if err != nil {
				return nil, err
			}
			cond = wire.D{{Key: op, Value: v}}
		}
		all = append(all, wire.D{{Key: f.Column, Value: cond}})
	}
	if len(all) == 0 {
		return wire.D{}, nil
	}
	return wire.D{{Key: "$and", Value: all}}, nil
}

// withWhere ANDs a page's find filter (req.Where) into filter. What runs
// code is refused, as in the console, and so is any field that touches a
// masked path, wherever it is in the filter.
func withWhere(filter wire.D, where string, masked []string) (wire.D, error) {
	if strings.TrimSpace(where) == "" {
		return filter, nil
	}
	w, err := wire.ParseDoc(where)
	if err != nil {
		return nil, fmt.Errorf("%w: the filter: %w", data.ErrInvalid, err)
	}
	if bad := firstForbidden(w); bad != "" {
		return nil, fmt.Errorf("%w: %s isn't allowed in a filter: it runs code", data.ErrInvalid, bad)
	}
	if len(masked) > 0 && hasKey(w, "$text") {
		return nil, fmt.Errorf("%w: $text searches fields a text index covers, which can be sensitive; it isn't allowed while fields are masked for you", data.ErrInvalid)
	}
	if f := touchedField(w, masked); f != "" {
		return nil, fmt.Errorf("%w: %s is sensitive; it can't be filtered on while masked", data.ErrInvalid, f)
	}
	if len(filter) == 0 {
		return w, nil
	}
	return wire.D{{Key: "$and", Value: wire.A{filter, w}}}, nil
}

// hasKey reports whether key is a field name anywhere in v.
func hasKey(v any, key string) bool {
	switch x := v.(type) {
	case wire.D:
		for _, e := range x {
			if e.Key == key || hasKey(e.Value, key) {
				return true
			}
		}
	case wire.A:
		for _, e := range x {
			if hasKey(e, key) {
				return true
			}
		}
	}
	return false
}

// touchedField is a field name in a filter (at any depth, through $and,
// $or and the like) that touches a masked path, or "".
func touchedField(v any, masked []string) string {
	if len(masked) == 0 {
		return ""
	}
	switch x := v.(type) {
	case wire.D:
		for _, e := range x {
			if !strings.HasPrefix(e.Key, "$") && touches(masked, e.Key) {
				return e.Key
			}
			// These name fields inside strings ("$customer.email").
			if e.Key == "$expr" || e.Key == "$jsonSchema" {
				return e.Key
			}
			if f := touchedField(e.Value, masked); f != "" {
				return f
			}
		}
	case wire.A:
		for _, e := range x {
			if f := touchedField(e, masked); f != "" {
				return f
			}
		}
	}
	return ""
}

// filterValue reads a filter value: Extended JSON, or a bare word as a
// string (status = open).
func filterValue(s string) (any, error) {
	v, err := wire.ParseJSON(s)
	if err == nil {
		return v, nil
	}
	if t := strings.TrimSpace(s); t != "" && !strings.ContainsAny(t[:1], `{["'`) {
		return t, nil
	}
	return nil, fmt.Errorf("%w: %w", data.ErrInvalid, err)
}

// likePattern is SQL LIKE's pattern as a regular expression anchored to
// the whole string (\A, \z), run with dot-all so % and _ match newlines.
func likePattern(p string) string {
	var b strings.Builder
	b.WriteString(`\A`)
	for _, r := range p {
		switch r {
		case '%':
			b.WriteString(".*")
		case '_':
			b.WriteByte('.')
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString(`\z`)
	return b.String()
}

// columnsOf are a page's columns: _id, then every other top-level field
// in the order first seen, typed by its values ("mixed" when they
// differ).
func columnsOf(docs wire.A) ([]apitypes.DataResultColumn, []string) {
	names := []string{"_id"}
	types := map[string]string{}
	for _, d := range docs {
		for _, f := range asDoc(d) {
			typ := typeName(f.Value)
			prev, seen := types[f.Key]
			switch {
			case !seen:
				types[f.Key] = typ
				if f.Key != "_id" {
					names = append(names, f.Key)
				}
			case prev != typ && typ != "null" && prev != "null":
				types[f.Key] = "mixed"
			case prev == "null":
				types[f.Key] = typ
			}
		}
	}
	out := make([]apitypes.DataResultColumn, len(names))
	for i, n := range names {
		out[i] = apitypes.DataResultColumn{Name: n, TypeName: typePrefix + types[n]}
	}
	return out, names
}

// typePrefix starts every MongoDB column's type name ("bson:string"), so
// the UI reads its cells as Extended JSON whatever the type is called.
const typePrefix = "bson:"

// typeName is a value's BSON type as $type names it.
func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case string:
		return "string"
	case int32:
		return "int"
	case int64:
		return "long"
	case float64:
		return "double"
	case wire.Decimal128:
		return "decimal"
	case wire.ObjectID:
		return "objectId"
	case wire.DateTime:
		return "date"
	case wire.D:
		return "object"
	case wire.A:
		return "array"
	case wire.Binary:
		return "binData"
	case wire.Symbol:
		return "symbol"
	case wire.JavaScript:
		return "javascript"
	case wire.Regex:
		return "regex"
	case wire.Timestamp:
		return "timestamp"
	case wire.MinKey:
		return "minKey"
	case wire.MaxKey:
		return "maxKey"
	}
	return "other"
}

// maskedValue stands for a masked value inside a document's cell.
var maskedValue = wire.D{{Key: "$masked", Value: true}}

// appendRow appends one document as a row of cols: each cell its field's
// Extended JSON text, null when the field is missing. A masked field is
// {"masked": true}; one holding a masked path shows it as
// {"$masked": true} inside.
func appendRow(dst []byte, d wire.D, cols []string, masked []string, full bool, scratch *[]byte) []byte {
	dst = append(dst, '[')
	for i, name := range cols {
		if i > 0 {
			dst = append(dst, ',')
		}
		v, ok := d.Get(name)
		switch {
		case !ok:
			dst = append(dst, "null"...)
		case slices.Contains(masked, name):
			dst = append(dst, data.MaskedCell...)
		default:
			for _, m := range masked {
				if len(m) > len(name) && m[len(name)] == '.' && strings.HasPrefix(m, name) {
					v = maskPath(v, strings.Split(m[len(name)+1:], "."))
				}
			}
			*scratch = wire.AppendJSON((*scratch)[:0], v)
			dst = data.AppendCell(dst, *scratch, data.CellString, full)
		}
	}
	return append(dst, ']')
}

// maskPath is v with the value at path (through documents, and through
// every element of an array) replaced by maskedValue. It copies what it
// changes.
func maskPath(v any, path []string) any {
	switch x := v.(type) {
	case wire.D:
		out := slices.Clone(x)
		for i, e := range out {
			if e.Key == path[0] {
				if len(path) == 1 {
					out[i].Value = maskedValue
				} else {
					out[i].Value = maskPath(e.Value, path[1:])
				}
			}
		}
		return out
	case wire.A:
		// A position (items.0.email) or a placeholder ($, $[]) masks the
		// field in every element: MongoDB reaches it through any of them.
		// In a document, a numeric part is a key like any other.
		rest := path
		if p := path[0]; p == "$" || strings.HasPrefix(p, "$[") || strings.Trim(p, "0123456789") == "" {
			rest = path[1:]
		}
		out := make(wire.A, len(x))
		for i, e := range x {
			if len(rest) == 0 {
				out[i] = maskedValue
			} else {
				out[i] = maskPath(e, rest)
			}
		}
		return out
	}
	return v
}

// queryError maps a server error of a read: a time limit is the
// caller's to see; others are invalid requests (a bad filter).
func queryError(err error) error {
	if se, ok := errors.AsType[*wire.ServerError](err); ok {
		if se.Code == wire.CodeUnauthorized {
			return fmt.Errorf("%w: %s", data.ErrNoPrivilege, se.Msg)
		}
		return fmt.Errorf("%w: %s", data.ErrInvalid, se.Error())
	}
	return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
}

func asDoc(v any) wire.D {
	d, _ := v.(wire.D)
	return d
}

func asArray(v any) wire.A {
	a, _ := v.(wire.A)
	return a
}

func docStr(v any, key string) string {
	s, _ := asDoc(v).Get(key)
	str, _ := s.(string)
	return str
}

// asInt reads a number field (servers send int32, int64 or double).
func asInt(v any, _ bool) int64 {
	switch n := v.(type) {
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

// firstBatch is a cursor reply's first batch.
func firstBatch(reply wire.D) wire.A {
	c, _ := reply.Get("cursor")
	b, _ := asDoc(c).Get("firstBatch")
	return asArray(b)
}
