package mongo

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mongo/wire"
)

// A changeset changes one document: a single server has no
// transactions, and one updateOne, insert or deleteOne is atomic on its
// own. An update and a delete match _id and every old value the editor
// saw, whole (a field it saw missing must still be missing), so a
// document whose seen fields changed since makes the changeset a
// conflict. Fields the editor didn't see aren't guarded: an update
// doesn't write them, and a delete removes them with the document.
// Values are Extended JSON text; nil in New removes the field ($unset).

// op is one change as the command that runs it.
type op struct {
	db, coll string
	kind     apitypes.DataChangeOp
	filter   wire.D
	update   wire.D
	doc      wire.D
	// compared are the fields the filter compares, for the conflict.
	compared []string
}

// plan checks the changes and builds their command.
func plan(changes []apitypes.DataChange, masked func(db, coll string) []string) (op, error) {
	if len(changes) != 1 {
		return op{}, fmt.Errorf("%w: a MongoDB changeset changes one document (a single server has no transactions); send each document's change on its own", data.ErrInvalid)
	}
	ch := changes[0]
	db, coll, err := splitTable(ch.Table)
	if err != nil {
		return op{}, err
	}
	o := op{db: db, coll: coll, kind: ch.Op}
	for _, k := range slices.Concat(slices.Collect(maps.Keys(ch.New)), slices.Collect(maps.Keys(ch.Old))) {
		if k == "" || strings.HasPrefix(k, "$") {
			return o, fmt.Errorf("%w: %q isn't a field", data.ErrInvalid, k)
		}
		// A position (tags.0) or a placeholder (tags.$[]) reaches into an
		// array, which the old-value guard can't compare: change the
		// whole field instead.
		// A change names a top-level field, as the grid's columns are.
		// A dot would be read as a path: the field "a.b" and a.b inside
		// a are not the same, and positions (tags.0) or placeholders
		// (tags.$[]) reach into arrays the guard can't compare.
		if strings.Contains(k, ".") {
			return o, fmt.Errorf("%w: %q names a path; change the whole top-level field (%s)", data.ErrInvalid, k, strings.SplitN(k, ".", 2)[0])
		}
		if touches(masked(db, coll), k) {
			return o, fmt.Errorf("%w: %s is sensitive and masked for you; it can't be changed", data.ErrInvalid, k)
		}
	}
	value := func(s *string) (any, error) {
		v, err := wire.ParseJSON(*s)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", data.ErrInvalid, err)
		}
		return v, nil
	}
	if ch.Op != apitypes.DataChangeInsert {
		idText, ok := ch.Key["_id"]
		if !ok || len(ch.Key) != 1 {
			return o, fmt.Errorf("%w: a document's key is its _id", data.ErrInvalid)
		}
		id, err := value(&idText)
		if err != nil {
			return o, err
		}
		// $eq, so an _id that looks like an operator ({$ne: null}) is
		// still the one document's literal key.
		o.filter = wire.D{{Key: "_id", Value: wire.D{{Key: "$eq", Value: id}}}}
		var same wire.A
		for _, k := range slices.Sorted(maps.Keys(ch.Old)) {
			if k == "_id" {
				continue
			}
			o.compared = append(o.compared, k)
			if ch.Old[k] == nil {
				o.filter = append(o.filter, wire.E{Key: k, Value: wire.D{{Key: "$exists", Value: false}}})
				continue
			}
			v, err := value(ch.Old[k])
			if err != nil {
				return o, err
			}
			// The whole value, as $expr compares it: a query's {$eq: "B"}
			// would also match an array holding "B", and null a missing
			// field. $literal keeps a "$..." string from naming a field.
			same = append(same, wire.D{{Key: "$eq", Value: wire.A{"$" + k, wire.D{{Key: "$literal", Value: v}}}}})
			// $eq takes int32 7 for int64 7: the type must be the one seen
			// too (of the field itself; inside a document or an array,
			// values compare as $eq does).
			if t := typeName(v); t != "other" {
				same = append(same, wire.D{{Key: "$eq", Value: wire.A{wire.D{{Key: "$type", Value: "$" + k}}, t}}})
			}
		}
		if len(same) > 0 {
			o.filter = append(o.filter, wire.E{Key: "$expr", Value: wire.D{{Key: "$and", Value: same}}})
		}
	}
	switch ch.Op {
	case apitypes.DataChangeUpdate:
		set, unset := wire.D{}, wire.D{}
		for _, k := range slices.Sorted(maps.Keys(ch.New)) {
			if k == "_id" {
				return o, fmt.Errorf("%w: a document's _id can't change; insert a copy and delete it", data.ErrInvalid)
			}
			if _, ok := ch.Old[k]; !ok {
				return o, fmt.Errorf("%w: the update of %s needs the old value seen", data.ErrInvalid, k)
			}
			if ch.New[k] == nil {
				unset = append(unset, wire.E{Key: k, Value: ""})
				continue
			}
			v, err := value(ch.New[k])
			if err != nil {
				return o, err
			}
			set = append(set, wire.E{Key: k, Value: v})
		}
		if len(set)+len(unset) == 0 {
			return o, fmt.Errorf("%w: the update changes nothing", data.ErrInvalid)
		}
		if len(set) > 0 {
			o.update = append(o.update, wire.E{Key: "$set", Value: set})
		}
		if len(unset) > 0 {
			o.update = append(o.update, wire.E{Key: "$unset", Value: unset})
		}
	case apitypes.DataChangeInsert:
		o.doc = wire.D{}
		keys := slices.Sorted(maps.Keys(ch.New))
		if i := slices.Index(keys, "_id"); i > 0 {
			keys = append([]string{"_id"}, slices.Delete(keys, i, i+1)...)
		}
		for _, k := range keys {
			if ch.New[k] == nil {
				continue
			}
			v, err := value(ch.New[k])
			if err != nil {
				return o, err
			}
			o.doc = append(o.doc, wire.E{Key: k, Value: v})
		}
	case apitypes.DataChangeDelete:
	default:
		return o, fmt.Errorf("%w: unknown change %q", data.ErrInvalid, ch.Op)
	}
	return o, nil
}

// binary compares strings byte for byte, whatever collation the
// collection has: under a case-insensitive one, "A" would pass a guard
// on "a".
var binary = wire.E{Key: "collation", Value: wire.D{{Key: "locale", Value: "simple"}}}

// text is the command as the shell would read it, values inlined.
func (o op) text() string {
	target := "db.getSiblingDB(" + wire.Compact(o.db) + ").getCollection(" + wire.Compact(o.coll) + ")"
	switch o.kind {
	case apitypes.DataChangeUpdate:
		return target + ".updateOne(" + wire.Compact(o.filter) + ", " + wire.Compact(o.update) + `, {"collation":{"locale":"simple"}})`
	case apitypes.DataChangeInsert:
		return target + ".insertOne(" + wire.Compact(o.doc) + ")"
	}
	return target + ".deleteOne(" + wire.Compact(o.filter) + `, {"collation":{"locale":"simple"}})`
}

// Render checks the change and shows the command a commit would run.
func (e *Engine) Render(_ context.Context, _ data.Target, changes []apitypes.DataChange) (string, error) {
	o, err := plan(changes, func(string, string) []string { return nil })
	if err != nil {
		return "", err
	}
	return o.text(), nil
}

// Commit runs the change as the writer. A document that no longer
// matches its old values is a conflict, with its current values.
func (e *Engine) Commit(ctx context.Context, t data.Target, c data.Call, changes []apitypes.DataChange) (data.CommitResult, error) {
	o, err := plan(changes, func(db, coll string) []string { return maskedPaths(c, db, coll) })
	if err != nil {
		return data.CommitResult{}, err
	}
	res := data.CommitResult{SQL: o.text()}
	cn, err := e.acquire(ctx, t, data.RoleWriter)
	if err != nil {
		return res, err
	}
	defer e.release(cn)
	start := time.Now()
	var cmd wire.D
	switch o.kind {
	case apitypes.DataChangeUpdate:
		cmd = wire.D{{Key: "update", Value: o.coll}, {Key: "updates", Value: wire.A{wire.D{{Key: "q", Value: o.filter}, {Key: "u", Value: o.update}, binary}}}}
	case apitypes.DataChangeInsert:
		cmd = wire.D{{Key: "insert", Value: o.coll}, {Key: "documents", Value: wire.A{o.doc}}}
	default:
		cmd = wire.D{{Key: "delete", Value: o.coll}, {Key: "deletes", Value: wire.A{wire.D{{Key: "q", Value: o.filter}, {Key: "limit", Value: int32(1)}, binary}}}}
	}
	cmd = append(cmd, wire.E{Key: "writeConcern", Value: wire.D{{Key: "w", Value: int32(1)}}})
	reply, err := run(ctx, cn, o.db, cmd)
	res.Duration = time.Since(start)
	if err != nil {
		return res, queryError(err)
	}
	if werr, _ := reply.Get("writeErrors"); len(asArray(werr)) > 0 {
		// The server's message can quote values (a duplicate key's,
		// sensitive ones too): only its code goes to the caller.
		code := asInt(asDoc(asArray(werr)[0]).Get("code"))
		msg := fmt.Sprintf("MongoDB refused the write (error %d)", code)
		if code == 11000 {
			msg = "a unique index already has a document with these values"
		}
		res.Conflicts = []apitypes.DataConflict{{Change: 0, Reason: apitypes.ConflictFailed, Message: msg}}
		return res, nil
	}
	n := asInt(reply.Get("n"))
	res.RowsAffected = []int64{n}
	if n == 1 || o.kind == apitypes.DataChangeInsert {
		return res, nil
	}
	// Nothing matched: gone, or changed since it was read.
	res.RowsAffected = nil
	current, err := run(ctx, cn, o.db, wire.D{{Key: "find", Value: o.coll}, {Key: "filter", Value: o.filter[:1]}, {Key: "limit", Value: int64(1)}, {Key: "singleBatch", Value: true}, binary})
	if err != nil {
		return res, queryError(err)
	}
	docs := firstBatch(current)
	if len(docs) == 0 {
		res.Conflicts = []apitypes.DataConflict{{Change: 0, Reason: apitypes.ConflictMissing, Message: "the document is gone"}}
		return res, nil
	}
	cur := map[string]*string{}
	for _, k := range o.compared {
		if v, ok := lookup(asDoc(docs[0]), k); ok {
			s := wire.Compact(v)
			cur[k] = &s
		} else {
			cur[k] = nil
		}
	}
	res.Conflicts = []apitypes.DataConflict{{Change: 0, Reason: apitypes.ConflictChanged, Current: cur, Message: "the document changed since it was read"}}
	return res, nil
}

// lookup reads a dotted path through documents.
func lookup(d wire.D, path string) (any, bool) {
	var v any = d
	for part := range strings.SplitSeq(path, ".") {
		doc, ok := v.(wire.D)
		if !ok {
			return nil, false
		}
		if v, ok = doc.Get(part); !ok {
			return nil, false
		}
	}
	return v, true
}
