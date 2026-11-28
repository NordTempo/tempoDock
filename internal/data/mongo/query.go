package mongo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

// The console runs one read as the reader, in the shell's words:
//
//	db.orders.find({status: "open"}, {total: 1}).sort({createdAt: -1}).limit(20).skip(40)
//	db.orders.findOne({_id: ObjectId("...")})
//	db.orders.aggregate([{$group: {_id: "$status", n: {$sum: 1}}}])
//	db.orders.countDocuments({status: "open"})
//	db.orders.distinct("status")
//	db.getSiblingDB("analytics").daily.find()      db.getCollection("a.b").find()
//	show dbs      show collections
//
// The reader's role can't write; stages and operators that write or run
// code ($out, $merge, $where, $function, $accumulator) are refused before
// anything is sent.

// statement is one parsed console line.
type statement struct {
	db, coll, method string
	args             wire.A
	sort, project    wire.D
	limit, skip      int64
	show             string
}

// forbidden are the stages and operators the console refuses.
var forbidden = []string{"$out", "$merge", "$where", "$function", "$accumulator"}

// parseStatement reads one console line; defaultDB is the database it
// runs in unless it says.
func parseStatement(text, defaultDB string) (statement, error) {
	st := statement{db: defaultDB}
	s := strings.TrimSuffix(strings.TrimSpace(text), ";")
	if rest, ok := strings.CutPrefix(s, "show "); ok {
		switch rest = strings.TrimSpace(rest); rest {
		case "dbs", "databases", "collections", "tables":
			st.show = rest
			return st, nil
		}
		return st, fmt.Errorf("show dbs or show collections, not show %s", rest)
	}
	rest, ok := strings.CutPrefix(s, "db.")
	if !ok {
		return st, errors.New(`start with db.<collection>.find(...), aggregate([...]), countDocuments(...) or distinct("field"), or show collections`)
	}
	name := func() string {
		i := 0
		for i < len(rest) && (rest[i] == '_' || rest[i] == '-' || rest[i] == '$' || rest[i] >= '0' && rest[i] <= '9' || rest[i]|0x20 >= 'a' && rest[i]|0x20 <= 'z') {
			i++
		}
		n := rest[:i]
		rest = rest[i:]
		return n
	}
	strArg := func(what string) (string, error) {
		args, r, err := wire.ParseArgs(rest)
		if err != nil {
			return "", err
		}
		rest = r
		if len(args) != 1 {
			return "", fmt.Errorf("%s() takes one name", what)
		}
		v, ok := args[0].(string)
		if !ok {
			return "", fmt.Errorf("%s() takes a name in quotes", what)
		}
		return v, nil
	}
	for st.coll == "" {
		switch word := name(); word {
		case "getSiblingDB":
			db, err := strArg(word)
			if err != nil {
				return st, err
			}
			st.db = db
		case "getCollection":
			coll, err := strArg(word)
			if err != nil {
				return st, err
			}
			st.coll = coll
			continue
		case "":
			return st, errors.New("name a collection: db.orders.find()")
		default:
			st.coll = word
			continue
		}
		if rest, ok = strings.CutPrefix(rest, "."); !ok {
			return st, errors.New("expected . after getSiblingDB(...)")
		}
	}
	for rest != "" {
		if rest, ok = strings.CutPrefix(strings.TrimSpace(rest), "."); !ok {
			return st, fmt.Errorf("expected .method(...), found %q", rest)
		}
		method := name()
		args, r, err := wire.ParseArgs(rest)
		if err != nil {
			return st, fmt.Errorf("%s: %w", method, err)
		}
		rest = strings.TrimSpace(r)
		if st.method == "" {
			switch method {
			case "find", "findOne", "aggregate", "countDocuments", "estimatedDocumentCount", "distinct":
				st.method, st.args = method, args
				continue
			}
			return st, fmt.Errorf("%s isn't a read the console runs: use find, findOne, aggregate, countDocuments, estimatedDocumentCount or distinct", method)
		}
		if st.method != "find" {
			return st, fmt.Errorf(".%s() follows find() only", method)
		}
		switch method {
		case "sort", "project", "projection":
			d, ok := one[wire.D](args)
			if !ok {
				return st, fmt.Errorf(".%s() takes a document", method)
			}
			if method == "sort" {
				st.sort = d
			} else {
				st.project = d
			}
		case "limit", "skip":
			n, ok := number(args)
			if !ok || n < 0 {
				return st, fmt.Errorf(".%s() takes a number", method)
			}
			if method == "limit" {
				st.limit = n
			} else {
				st.skip = n
			}
		default:
			return st, fmt.Errorf(".%s() isn't supported after find(); use sort, project, limit or skip", method)
		}
	}
	if st.method == "" {
		return st, fmt.Errorf("say what to do with %s: .find(), .aggregate([...]), .countDocuments()", st.coll)
	}
	for _, v := range []any{st.args, st.project, st.sort} {
		if bad := firstForbidden(v); bad != "" {
			return st, fmt.Errorf("%s isn't allowed in the console: it writes or runs code", bad)
		}
	}
	return st, nil
}

func one[T any](args wire.A) (T, bool) {
	var zero T
	if len(args) != 1 {
		return zero, false
	}
	v, ok := args[0].(T)
	return v, ok
}

func number(args wire.A) (int64, bool) {
	if len(args) != 1 {
		return 0, false
	}
	switch n := args[0].(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// firstForbidden finds a forbidden key anywhere in v.
func firstForbidden(v any) string {
	switch x := v.(type) {
	case wire.D:
		for _, e := range x {
			if slices.Contains(forbidden, e.Key) {
				return e.Key
			}
			if bad := firstForbidden(e.Value); bad != "" {
				return bad
			}
		}
	case wire.A:
		for _, e := range x {
			if bad := firstForbidden(e); bad != "" {
				return bad
			}
		}
	}
	return ""
}

// command is the statement as a server command, and whether its reply is
// a cursor.
func (st statement) command(limit int64, timeout time.Duration) (wire.D, bool, error) {
	maxTime := wire.E{Key: "maxTimeMS", Value: int64(timeout / time.Millisecond)}
	docArg := func(i int) (wire.D, error) {
		if len(st.args) <= i {
			return wire.D{}, nil
		}
		d, ok := st.args[i].(wire.D)
		if !ok {
			return nil, fmt.Errorf("%s's argument %d must be a document", st.method, i+1)
		}
		return d, nil
	}
	switch st.method {
	case "find", "findOne":
		filter, err := docArg(0)
		if err != nil {
			return nil, false, err
		}
		project := st.project
		if len(st.args) > 1 {
			if project, err = docArg(1); err != nil {
				return nil, false, err
			}
		}
		n := limit
		if st.method == "findOne" {
			n = 1
		} else if st.limit > 0 && st.limit < n {
			n = st.limit
		}
		cmd := wire.D{{Key: "find", Value: st.coll}, {Key: "filter", Value: filter}, {Key: "limit", Value: n}, maxTime}
		if len(project) > 0 {
			cmd = append(cmd, wire.E{Key: "projection", Value: project})
		}
		if len(st.sort) > 0 {
			cmd = append(cmd, wire.E{Key: "sort", Value: st.sort})
		}
		if st.skip > 0 {
			cmd = append(cmd, wire.E{Key: "skip", Value: st.skip})
		}
		return cmd, true, nil
	case "aggregate":
		pipeline, ok := one[wire.A](st.args)
		if !ok {
			return nil, false, errors.New("aggregate() takes a pipeline: [{$match: ...}, ...]")
		}
		pipeline = append(slices.Clone(pipeline), wire.D{{Key: "$limit", Value: limit}})
		return wire.D{{Key: "aggregate", Value: st.coll}, {Key: "pipeline", Value: pipeline}, {Key: "cursor", Value: wire.D{}}, maxTime}, true, nil
	case "countDocuments":
		filter, err := docArg(0)
		if err != nil {
			return nil, false, err
		}
		return wire.D{{Key: "count", Value: st.coll}, {Key: "query", Value: filter}, maxTime}, false, nil
	case "estimatedDocumentCount":
		return wire.D{{Key: "count", Value: st.coll}, maxTime}, false, nil
	case "distinct":
		if len(st.args) == 0 {
			return nil, false, errors.New(`distinct() takes a field: distinct("status")`)
		}
		key, ok := st.args[0].(string)
		if !ok {
			return nil, false, errors.New(`distinct() takes a field in quotes`)
		}
		filter, err := docArg(1)
		if err != nil {
			return nil, false, err
		}
		return wire.D{{Key: "distinct", Value: st.coll}, {Key: "key", Value: key}, {Key: "query", Value: filter}, maxTime}, false, nil
	}
	return nil, false, fmt.Errorf("unknown method %s", st.method)
}

// otherFields names the last column of a console result: the fields of a
// document that the first batch didn't have, as one document.
const otherFields = "(other fields)"

// Query runs one console statement as the reader and streams its
// documents: the columns are the first batch's top-level fields, plus
// otherFields for any later document's new ones.
func (e *Engine) Query(ctx context.Context, t data.Target, c data.Call, req apitypes.DataQueryRequest, send func(apitypes.DataQueryFrame) error) (data.QueryStats, error) {
	start := time.Now()
	stats := data.QueryStats{Outcome: apitypes.OutcomeOK}
	fail := func(err error) (data.QueryStats, error) {
		stats.Outcome, stats.Duration = apitypes.OutcomeError, time.Since(start)
		var se *wire.ServerError
		if errors.As(err, &se) && (se.Code == wire.CodeMaxTimeMS || se.Code == wire.CodeInterrupted) {
			stats.Outcome = apitypes.OutcomeTimeout
		}
		if ctx.Err() != nil {
			stats.Outcome = apitypes.OutcomeCanceled
		}
		return stats, send(apitypes.DataQueryFrame{Type: apitypes.FrameError, Error: &apitypes.DataQueryError{Message: err.Error()}})
	}
	limit := int64(req.Limit)
	if limit <= 0 {
		limit = apitypes.DataQueryLimit
	}
	timeout := time.Duration(req.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = apitypes.DataQueryTimeout
	}
	db := t.Database
	if db == "" || systemDBs[db] {
		db = "test"
	}
	st, err := parseStatement(req.SQL, db)
	if err != nil {
		return fail(err)
	}
	if st.coll != "" && len(maskedPaths(c, st.db, st.coll)) > 0 {
		return fail(errors.New("this collection has sensitive fields masked for you, so the console can't read it"))
	}
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return stats, err
	}
	defer e.release(cn)
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	if st.show != "" {
		return e.show(ctx, cn, st, limit, send, stats, start)
	}
	cmd, cursor, err := st.command(limit, timeout)
	if err != nil {
		return fail(err)
	}
	reply, err := run(ctx, cn, st.db, cmd)
	if err != nil {
		return fail(err)
	}
	if !cursor {
		return sendValues(reply, st, limit, send, stats, start)
	}
	batch := firstBatch(reply)
	cols, names := columnsOf(batch)
	cols = append(cols, apitypes.DataResultColumn{Name: otherFields, TypeName: typePrefix + "object"})
	if err := send(apitypes.DataQueryFrame{Type: apitypes.FrameHeader, Columns: cols}); err != nil {
		return stats, err
	}
	id := cursorID(reply)
	var rows []apitypes.DataRow
	var bytes int
	truncated := false
	for {
		for _, d := range batch {
			if stats.Rows >= limit {
				truncated = true
				break
			}
			row := appendQueryRow(nil, asDoc(d), names)
			if bytes+len(row) > apitypes.DataQueryMaxBytes {
				truncated = true
				break
			}
			bytes += len(row)
			rows = append(rows, row)
			stats.Rows++
			if len(rows) == apitypes.DataFrameRows {
				if err := send(apitypes.DataQueryFrame{Type: apitypes.FrameRows, Rows: rows}); err != nil {
					return stats, err
				}
				rows = nil
			}
		}
		if truncated || id == 0 {
			break
		}
		more, err := run(ctx, cn, st.db, wire.D{{Key: "getMore", Value: id}, {Key: "collection", Value: st.coll}})
		if err != nil {
			return fail(err)
		}
		c, _ := more.Get("cursor")
		nb, _ := asDoc(c).Get("nextBatch")
		batch, id = asArray(nb), cursorID(more)
	}
	if id != 0 {
		_, _ = run(ctx, cn, st.db, wire.D{{Key: "killCursors", Value: st.coll}, {Key: "cursors", Value: wire.A{id}}})
	}
	if len(rows) > 0 {
		if err := send(apitypes.DataQueryFrame{Type: apitypes.FrameRows, Rows: rows}); err != nil {
			return stats, err
		}
	}
	stats.Duration = time.Since(start)
	return stats, send(apitypes.DataQueryFrame{
		Type: apitypes.FrameEnd, RowCount: stats.Rows, Truncated: truncated,
		Command: st.method + " " + strconv.FormatInt(stats.Rows, 10), DurationMS: stats.Duration.Milliseconds(),
	})
}

// appendQueryRow is a console row: the named fields, then the others as
// one document (null when there are none).
func appendQueryRow(dst []byte, d wire.D, names []string) []byte {
	var scratch []byte
	dst = appendRow(dst, d, names, nil, true, &scratch)
	var extra wire.D
	for _, f := range d {
		if !slices.Contains(names, f.Key) {
			extra = append(extra, f)
		}
	}
	dst = dst[:len(dst)-1]
	if extra == nil {
		return append(dst, ",null]"...)
	}
	dst = append(dst, ',')
	return append(data.AppendJSONString(dst, wire.AppendJSON(nil, extra)), ']')
}

func cursorID(reply wire.D) int64 {
	c, _ := reply.Get("cursor")
	id, _ := asDoc(c).Get("id")
	n, _ := id.(int64)
	return n
}

// sendValues sends a non-cursor reply: a count, or distinct's values.
func sendValues(reply wire.D, st statement, limit int64, send func(apitypes.DataQueryFrame) error, stats data.QueryStats, start time.Time) (data.QueryStats, error) {
	if st.method != "distinct" {
		row := apitypes.DataRow("[" + strconv.FormatInt(asInt(reply.Get("n")), 10) + "]")
		return sendList(apitypes.DataResultColumn{Name: "count", TypeName: "int8"}, []apitypes.DataRow{row}, limit, st.method, send, stats, start)
	}
	vals, _ := reply.Get("values")
	var rows []apitypes.DataRow
	for _, v := range asArray(vals) {
		rows = append(rows, append(data.AppendJSONString([]byte{'['}, wire.AppendJSON(nil, v)), ']'))
	}
	return sendList(apitypes.DataResultColumn{Name: "value", TypeName: typePrefix + "mixed"}, rows, limit, st.method, send, stats, start)
}

// sendList sends rows of one column within the request's row limit and
// the byte budget, as cursor results are: frames of DataFrameRows, and
// Truncated when a cap cut the list.
func sendList(col apitypes.DataResultColumn, rows []apitypes.DataRow, limit int64, command string, send func(apitypes.DataQueryFrame) error, stats data.QueryStats, start time.Time) (data.QueryStats, error) {
	if err := send(apitypes.DataQueryFrame{Type: apitypes.FrameHeader, Columns: []apitypes.DataResultColumn{col}}); err != nil {
		return stats, err
	}
	truncated, bytes, n := false, 0, 0
	for _, r := range rows {
		if int64(n) >= limit || bytes+len(r) > apitypes.DataQueryMaxBytes {
			truncated = true
			break
		}
		bytes += len(r)
		n++
	}
	for chunk := range slices.Chunk(rows[:n], apitypes.DataFrameRows) {
		if err := send(apitypes.DataQueryFrame{Type: apitypes.FrameRows, Rows: chunk}); err != nil {
			return stats, err
		}
	}
	stats.Rows, stats.Duration = int64(n), time.Since(start)
	return stats, send(apitypes.DataQueryFrame{Type: apitypes.FrameEnd, RowCount: stats.Rows, Truncated: truncated, Command: command, DurationMS: stats.Duration.Milliseconds()})
}

// show answers show dbs and show collections.
func (e *Engine) show(ctx context.Context, cn *cached, st statement, limit int64, send func(apitypes.DataQueryFrame) error, stats data.QueryStats, start time.Time) (data.QueryStats, error) {
	var names []string
	var err error
	if st.show == "dbs" || st.show == "databases" {
		var reply wire.D
		if reply, err = run(ctx, cn, authDB, wire.D{{Key: "listDatabases", Value: int32(1)}, {Key: "nameOnly", Value: true}}); err == nil {
			list, _ := reply.Get("databases")
			for _, d := range asArray(list) {
				names = append(names, docStr(d, "name"))
			}
		}
	} else {
		var colls wire.A
		if colls, err = listCollections(ctx, cn, st.db, true); err == nil {
			for _, d := range colls {
				names = append(names, docStr(d, "name"))
			}
		}
	}
	if err != nil {
		stats.Outcome, stats.Duration = apitypes.OutcomeError, time.Since(start)
		return stats, send(apitypes.DataQueryFrame{Type: apitypes.FrameError, Error: &apitypes.DataQueryError{Message: err.Error()}})
	}
	rows := make([]apitypes.DataRow, len(names))
	for i, n := range names {
		rows[i] = append(data.AppendJSONString([]byte{'['}, []byte(n)), ']')
	}
	return sendList(apitypes.DataResultColumn{Name: "name", TypeName: "text"}, rows, limit, "show "+st.show, send, stats, start)
}

// Redact replaces the values in a console line or a command's Extended
// JSON with ?: quoted strings that aren't keys (a key is followed by :)
// and numbers outside names.
func (e *Engine) Redact(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(text) && text[j] != c {
				if text[j] == '\\' {
					j++
				}
				j++
			}
			end := min(j+1, len(text))
			k := end
			for k < len(text) && text[k] == ' ' {
				k++
			}
			if k < len(text) && text[k] == ':' {
				b.WriteString(text[i:end])
			} else {
				b.WriteByte('?')
			}
			i = end - 1
		case c >= '0' && c <= '9' && (i == 0 || !isNameByte(text[i-1])):
			j := i
			for j < len(text) && (text[j] >= '0' && text[j] <= '9' || text[j] == '.' || text[j] == 'e' || text[j] == 'E') {
				j++
			}
			b.WriteByte('?')
			i = j - 1
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isNameByte(c byte) bool {
	return c == '_' || c == '$' || c >= '0' && c <= '9' || c|0x20 >= 'a' && c|0x20 <= 'z'
}
