package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// catalogSQL reads every user table, view, materialized view and foreign
// table (partitions are covered by their parent) with columns, keys,
// foreign keys, indexes and constraints, as one JSON document whose shape
// matches apitypes.DataTable plus index columns. One round trip, whatever
// the number of tables.
const catalogSQL = `
SELECT coalesce(json_agg(r ORDER BY r.schema, r.name), '[]')::text FROM (
  SELECT n.nspname AS schema, c.relname AS name,
    CASE c.relkind WHEN 'r' THEN 'table' WHEN 'p' THEN 'partitioned_table' WHEN 'v' THEN 'view'
      WHEN 'm' THEN 'materialized_view' ELSE 'foreign_table' END AS kind,
    CASE WHEN c.relkind IN ('r', 'm') THEN c.reltuples::int8 ELSE -1 END AS estimated_rows,
    coalesce(pg_total_relation_size(c.oid), 0) AS size_bytes,
    coalesce(obj_description(c.oid, 'pg_class'), '') AS comment,
    (SELECT coalesce(json_agg(json_build_object(
        'name', a.attname,
        'type', format_type(a.atttypid, a.atttypmod),
        'type_name', coalesce(bt.typname, t.typname),
        'nullable', NOT a.attnotnull,
        'default', CASE WHEN a.attgenerated = '' THEN pg_get_expr(ad.adbin, ad.adrelid) END,
        'generated', a.attgenerated <> '' OR a.attidentity = 'a',
        'comment', col_description(c.oid, a.attnum)) ORDER BY a.attnum), '[]')
      FROM pg_attribute a
      JOIN pg_type t ON t.oid = a.atttypid
      LEFT JOIN pg_type bt ON t.typtype = 'd' AND bt.oid = t.typbasetype
      LEFT JOIN pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
      WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped) AS columns,
    (SELECT coalesce(json_agg(a.attname ORDER BY k.ord), '[]')
      FROM pg_constraint p
      CROSS JOIN unnest(p.conkey) WITH ORDINALITY k(num, ord)
      JOIN pg_attribute a ON a.attrelid = p.conrelid AND a.attnum = k.num
      WHERE p.conrelid = c.oid AND p.contype = 'p') AS primary_key,
    (SELECT coalesce(json_agg(json_build_object(
        'name', f.conname,
        'columns', (SELECT json_agg(a.attname ORDER BY k.ord) FROM unnest(f.conkey) WITH ORDINALITY k(num, ord)
          JOIN pg_attribute a ON a.attrelid = f.conrelid AND a.attnum = k.num),
        'ref_schema', rn.nspname,
        'ref_table', rc.relname,
        'ref_columns', (SELECT json_agg(a.attname ORDER BY k.ord) FROM unnest(f.confkey) WITH ORDINALITY k(num, ord)
          JOIN pg_attribute a ON a.attrelid = f.confrelid AND a.attnum = k.num)) ORDER BY f.conname), '[]')
      FROM pg_constraint f
      JOIN pg_class rc ON rc.oid = f.confrelid
      JOIN pg_namespace rn ON rn.oid = rc.relnamespace
      WHERE f.conrelid = c.oid AND f.contype = 'f' AND f.conparentid = 0) AS foreign_keys,
    (SELECT coalesce(json_agg(json_build_object(
        'name', ic.relname,
        'unique', i.indisunique,
        'primary', i.indisprimary,
        'definition', pg_get_indexdef(i.indexrelid),
        'cols', CASE WHEN i.indexprs IS NULL AND i.indpred IS NULL THEN
          (SELECT json_agg(a.attname ORDER BY k.ord) FROM unnest(i.indkey) WITH ORDINALITY k(num, ord)
            JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.num
            WHERE k.ord <= i.indnkeyatts) END) ORDER BY ic.relname), '[]')
      FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
      WHERE i.indrelid = c.oid) AS idx,
    (SELECT coalesce(json_agg(json_build_object(
        'name', x.conname,
        'kind', CASE x.contype WHEN 'c' THEN 'check' WHEN 'u' THEN 'unique' ELSE 'exclusion' END,
        'definition', pg_get_constraintdef(x.oid)) ORDER BY x.conname), '[]')
      FROM pg_constraint x
      WHERE x.conrelid = c.oid AND x.contype IN ('c', 'u', 'x')) AS constraints
  FROM pg_class c
  JOIN pg_namespace n ON n.oid = c.relnamespace
  WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f') AND NOT c.relispartition
    AND n.nspname NOT IN ('pg_catalog', 'information_schema')
    AND n.nspname !~ '^pg_(toast|temp_)'
) r`

// rawTable is one table as catalogSQL returns it.
type rawTable struct {
	apitypes.DataTable
	Idx []rawIndex `json:"idx"`
}

// rawIndex is an index plus its key columns (nil for expression or partial
// indexes).
type rawIndex struct {
	apitypes.DataIndex
	Cols []string `json:"cols"`
}

// colRef names a column, for sensitive marks.
type colRef struct{ schema, table, column string }

// catalog is one introspection of a database, shared by every caller until
// it expires. Nothing in it is modified after load.
type catalog struct {
	readAt time.Time
	tables []apitypes.DataTable
	// byName finds a table by tableKey(schema, name).
	byName map[string]*tableMeta
	// incoming lists the foreign keys referencing each table, by
	// tableKey, in schema/table/constraint order.
	incoming map[string][]incomingFK
}

// tableMeta is a table plus what the engine needs to read and write it.
type tableMeta struct {
	*apitypes.DataTable
	// col finds a column's index in Columns.
	col map[string]int
	// key orders pages: the primary key, else a unique index on NOT NULL
	// columns. Empty: sysKey.
	key []string
	// sysKey is the system columns that order pages of a table without
	// a key (ctid; tableoid too for partitioned tables). Empty with an
	// empty key (views, foreign tables): pages go by offset.
	sysKey []string
}

// incomingFK is a foreign key of from that references some table.
type incomingFK struct {
	from *tableMeta
	fk   apitypes.DataForeignKey
}

func tableKey(schema, name string) string { return schema + "\x00" + name }

// table resolves "schema.name". Names may contain dots, so every split is
// tried.
func (cat *catalog) table(name string) (*tableMeta, error) {
	for i := 0; i < len(name); i++ {
		if name[i] == '.' {
			if tm, ok := cat.byName[tableKey(name[:i], name[i+1:])]; ok {
				return tm, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: %s", data.ErrNoTable, name)
}

// Schema returns the cached introspection (reading it again after
// SchemaTTL) with sensitive columns marked. sensitive also becomes the
// service's marks for masked reads (Page, References).
func (e *Engine) Schema(ctx context.Context, t data.Target, c data.Call, sensitive []apitypes.DataColumnRef) (apitypes.DataSchema, error) {
	marks := make(map[colRef]bool, len(sensitive))
	for _, s := range sensitive {
		marks[colRef{s.Schema, s.Table, s.Column}] = true
	}
	e.mu.Lock()
	e.marks[t.ServiceID] = marks
	e.mu.Unlock()

	var cat *catalog
	err := e.read(ctx, t, c, apitypes.DataQueryTimeout, func(cn *conn) error {
		var err error
		cat, err = e.catalog(ctx, t, cn)
		return err
	})
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	return markSensitive(cat, marks), nil
}

// markSensitive returns cat's schema with marks applied. Tables without a
// sensitive column share the cached column slices; callers must not modify
// the result.
func markSensitive(cat *catalog, marks map[colRef]bool) apitypes.DataSchema {
	out := apitypes.DataSchema{ReadAt: cat.readAt, Tables: slices.Clone(cat.tables)}
	if len(marks) == 0 {
		return out
	}
	for i := range out.Tables {
		tb := &out.Tables[i]
		var cols []apitypes.DataColumn
		for j, col := range tb.Columns {
			if marks[colRef{tb.Schema, tb.Name, col.Name}] {
				if cols == nil {
					cols = slices.Clone(tb.Columns)
				}
				cols[j].Sensitive = true
			}
		}
		if cols != nil {
			tb.Columns = cols
		}
	}
	return out
}

// catalog returns the service's cached catalog, or reads it on cn.
func (e *Engine) catalog(ctx context.Context, t data.Target, cn *conn) (*catalog, error) {
	e.mu.Lock()
	cat := e.schemas[t.ServiceID]
	e.mu.Unlock()
	if cat != nil && time.Since(cat.readAt) < e.schemaTTL {
		return cat, nil
	}
	var doc string
	if err := cn.pg.QueryRow(ctx, catalogSQL).Scan(&doc); err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	cat, err := parseCatalog([]byte(doc), time.Now())
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if !e.closed {
		e.schemas[t.ServiceID] = cat
	}
	e.mu.Unlock()
	return cat, nil
}

// parseCatalog builds a catalog from catalogSQL's document.
func parseCatalog(doc []byte, readAt time.Time) (*catalog, error) {
	var raw []rawTable
	if err := json.Unmarshal(doc, &raw); err != nil {
		return nil, fmt.Errorf("read schema: %w", err)
	}
	cat := &catalog{
		readAt:   readAt,
		tables:   make([]apitypes.DataTable, len(raw)),
		byName:   make(map[string]*tableMeta, len(raw)),
		incoming: map[string][]incomingFK{},
	}
	metas := make([]*tableMeta, len(raw))
	for i, r := range raw {
		tb := r.DataTable
		tb.Columns = nonNil(tb.Columns)
		tb.PrimaryKey = nonNil(tb.PrimaryKey)
		tb.ForeignKeys = nonNil(tb.ForeignKeys)
		tb.Constraints = nonNil(tb.Constraints)
		tb.Indexes = make([]apitypes.DataIndex, len(r.Idx))
		for j, ix := range r.Idx {
			tb.Indexes[j] = ix.DataIndex
		}
		tb.Editable = (tb.Kind == apitypes.TableKindTable || tb.Kind == apitypes.TableKindPartitioned) && len(tb.PrimaryKey) > 0
		cat.tables[i] = tb

		tm := &tableMeta{DataTable: &cat.tables[i], col: make(map[string]int, len(tb.Columns))}
		for j, col := range tb.Columns {
			tm.col[col.Name] = j
		}
		tm.key = pageKey(tm, r.Idx)
		if len(tm.key) == 0 {
			switch tb.Kind {
			case apitypes.TableKindTable, apitypes.TableKindMatView:
				tm.sysKey = []string{"ctid"}
			case apitypes.TableKindPartitioned:
				tm.sysKey = []string{"tableoid", "ctid"}
			}
		}
		metas[i] = tm
		cat.byName[tableKey(tb.Schema, tb.Name)] = tm
	}
	for _, tm := range metas {
		for _, fk := range tm.ForeignKeys {
			k := tableKey(fk.RefSchema, fk.RefTable)
			cat.incoming[k] = append(cat.incoming[k], incomingFK{from: tm, fk: fk})
		}
	}
	return cat, nil
}

// pageKey picks the columns that make a page order total: the primary key,
// else the unique index on NOT NULL columns with the fewest columns.
func pageKey(tm *tableMeta, idx []rawIndex) []string {
	if len(tm.PrimaryKey) > 0 {
		return tm.PrimaryKey
	}
	var best []string
	for _, ix := range idx {
		if !ix.Unique || len(ix.Cols) == 0 || best != nil && len(ix.Cols) >= len(best) {
			continue
		}
		ok := true
		for _, c := range ix.Cols {
			if j, found := tm.col[c]; !found || tm.Columns[j].Nullable {
				ok = false
				break
			}
		}
		if ok {
			best = ix.Cols
		}
	}
	return best
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// masks returns which of tm's columns a call must not see: the service's
// sensitive marks for masked calls, none otherwise.
func (e *Engine) masks(t data.Target, c data.Call, tm *tableMeta) (map[string]bool, error) {
	if !c.Masked {
		return nil, nil
	}
	e.mu.Lock()
	marks, ok := e.marks[t.ServiceID]
	e.mu.Unlock()
	if !ok {
		// Fail closed: without marks every column could be sensitive.
		return nil, fmt.Errorf("%w: sensitive columns unknown; read the schema first", data.ErrInvalid)
	}
	var out map[string]bool
	for _, col := range tm.Columns {
		if marks[colRef{tm.Schema, tm.Name, col.Name}] {
			if out == nil {
				out = map[string]bool{}
			}
			out[col.Name] = true
		}
	}
	return out, nil
}

// noEquality reports whether a type has no = operator, so comparisons go
// through its text output.
func noEquality(typeName string) bool {
	switch strings.TrimPrefix(typeName, "_") {
	case "json", "xml", "point", "line", "lseg", "box", "path", "polygon", "circle":
		return true
	}
	return false
}
