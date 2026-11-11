package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// catalog is one introspection of a server's user databases, shared by
// every caller until it expires. Nothing in it is modified after load.
type catalog struct {
	readAt time.Time
	tables []apitypes.DataTable
	// byName finds a table by tableKey(schema, name).
	byName map[string]*tableMeta
	// incoming lists the foreign keys referencing each table, by tableKey.
	incoming map[string][]incomingFK
}

// tableMeta is a table plus what the engine needs to read and write it.
type tableMeta struct {
	*apitypes.DataTable
	// col finds a column's index in Columns.
	col map[string]int
	// key orders pages: the primary key, else a unique index on NOT NULL
	// columns. Empty: pages go by offset (views, keyless tables).
	key []string
}

// incomingFK is a foreign key of from that references some table.
type incomingFK struct {
	from *tableMeta
	fk   apitypes.DataForeignKey
}

func tableKey(schema, name string) string { return schema + "\x00" + name }

// table resolves "database.name". Names may contain dots, so every split
// is tried.
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

// typeName is a column's DATA_TYPE.
func (tm *tableMeta) typeName(col string) string { return tm.Columns[tm.col[col]].TypeName }

// Schema returns the cached introspection (reading it again after
// SchemaTTL) with sensitive columns marked.
func (e *Engine) Schema(ctx context.Context, t data.Target, c data.Call, sensitive []apitypes.DataColumnRef) (apitypes.DataSchema, error) {
	e.mu.Lock()
	cat := e.schemas[t.ServiceID]
	e.mu.Unlock()
	if cat != nil && time.Since(cat.readAt) < e.schemaTTL {
		return markSensitive(cat, sensitive), nil
	}
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return apitypes.DataSchema{}, err
	}
	defer e.release(cn)
	if cat, err = e.catalog(ctx, t, cn.Conn); err != nil {
		return apitypes.DataSchema{}, err
	}
	return markSensitive(cat, sensitive), nil
}

// catalog returns t's cached catalog, or reads and caches it on c.
func (e *Engine) catalog(ctx context.Context, t data.Target, c *conn) (*catalog, error) {
	e.mu.Lock()
	cat := e.schemas[t.ServiceID]
	e.mu.Unlock()
	if cat != nil && time.Since(cat.readAt) < e.schemaTTL {
		return cat, nil
	}
	cat, err := readCatalog(ctx, c, t.Database)
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

// databasesFilter is the WHERE condition on a TABLE_SCHEMA-like column
// for the databases Dock covers: database alone when set, else every user
// database.
func databasesFilter(column, database string) string {
	if database != "" {
		return column + " = " + quoteString(database)
	}
	return column + " NOT IN ('mysql', 'sys', 'performance_schema', 'information_schema')"
}

// readCatalog introspects the databases with five queries on
// information_schema, whatever the number of tables. Sizes and row counts
// are the server's cached statistics (never refreshed here: setting
// information_schema_stats_expiry to 0 would open every table).
func readCatalog(ctx context.Context, c *conn, database string) (*catalog, error) {
	cat := &catalog{readAt: time.Now(), byName: map[string]*tableMeta{}, incoming: map[string][]incomingFK{}}
	str := func(b []byte) string { return string(b) }
	num := func(b []byte) int64 {
		n, _ := strconv.ParseInt(string(b), 10, 64)
		return n
	}

	err := c.query(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, TABLE_ROWS, COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0), TABLE_COMMENT
FROM information_schema.TABLES WHERE `+databasesFilter("TABLE_SCHEMA", database)+` ORDER BY TABLE_SCHEMA, TABLE_NAME`,
		6, func(r [][]byte) error {
			tb := apitypes.DataTable{Schema: str(r[0]), Name: str(r[1]), Kind: apitypes.TableKindTable, EstimatedRows: -1,
				SizeBytes: num(r[4]), Comment: str(r[5]), Columns: []apitypes.DataColumn{}, PrimaryKey: []string{},
				ForeignKeys: []apitypes.DataForeignKey{}, Indexes: []apitypes.DataIndex{}, Constraints: []apitypes.DataConstraint{}}
			if str(r[2]) == "VIEW" {
				tb.Kind = apitypes.TableKindView
			} else if r[3] != nil {
				tb.EstimatedRows = num(r[3])
			}
			if strings.EqualFold(tb.Comment, "VIEW") {
				tb.Comment = ""
			}
			cat.tables = append(cat.tables, tb)
			return nil
		})
	if err != nil {
		return nil, dbError(err)
	}
	for i := range cat.tables {
		tb := &cat.tables[i]
		cat.byName[tableKey(tb.Schema, tb.Name)] = &tableMeta{DataTable: tb, col: map[string]int{}}
	}
	find := func(schema, name []byte) *tableMeta { return cat.byName[tableKey(string(schema), string(name))] }

	err = c.query(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, DATA_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA, COLUMN_COMMENT
FROM information_schema.COLUMNS WHERE `+databasesFilter("TABLE_SCHEMA", database)+` ORDER BY TABLE_SCHEMA, TABLE_NAME, ORDINAL_POSITION`,
		9, func(r [][]byte) error {
			tm := find(r[0], r[1])
			if tm == nil {
				return nil
			}
			extra := strings.ToUpper(str(r[7]))
			col := apitypes.DataColumn{
				Name: str(r[2]), Type: str(r[3]), TypeName: strings.ToLower(str(r[4])), Nullable: str(r[5]) == "YES",
				Generated: strings.Contains(extra, "VIRTUAL GENERATED") || strings.Contains(extra, "STORED GENERATED") || strings.Contains(extra, "PERSISTENT GENERATED"),
				Comment:   str(r[8]),
			}
			// MariaDB reports "no default" on a nullable column as NULL text.
			if d := r[6]; d != nil && !col.Generated && string(d) != "NULL" {
				col.Default = string(d)
			}
			tm.col[col.Name] = len(tm.Columns)
			tm.Columns = append(tm.Columns, col)
			return nil
		})
	if err != nil {
		return nil, dbError(err)
	}

	// Indexes, one row per column; COLUMN_NAME is NULL for expression
	// parts (MySQL 8.0.13+).
	type idx struct {
		schema, table, name string
		unique              bool
		cols                []string
		expr                bool
	}
	var indexes []*idx
	err = c.query(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, COLUMN_NAME, SUB_PART
FROM information_schema.STATISTICS WHERE `+databasesFilter("TABLE_SCHEMA", database)+` ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`,
		6, func(r [][]byte) error {
			schema, table, name := str(r[0]), str(r[1]), str(r[2])
			if n := len(indexes); n == 0 || indexes[n-1].schema != schema || indexes[n-1].table != table || indexes[n-1].name != name {
				indexes = append(indexes, &idx{schema: schema, table: table, name: name, unique: str(r[3]) == "0"})
			}
			ix := indexes[len(indexes)-1]
			switch {
			case r[4] == nil:
				ix.expr = true
				ix.cols = append(ix.cols, "(expression)")
			case r[5] != nil:
				ix.expr = true // a prefix doesn't identify a row
				ix.cols = append(ix.cols, quoteIdent(str(r[4]))+"("+str(r[5])+")")
			default:
				ix.cols = append(ix.cols, str(r[4]))
			}
			return nil
		})
	if err != nil {
		return nil, dbError(err)
	}
	keys := map[*tableMeta][][]string{}
	for _, ix := range indexes {
		tm := cat.byName[tableKey(ix.schema, ix.table)]
		if tm == nil {
			continue
		}
		primary := ix.name == "PRIMARY"
		if primary && !ix.expr {
			tm.PrimaryKey = ix.cols
		}
		if ix.unique && !ix.expr {
			keys[tm] = append(keys[tm], ix.cols)
		}
		tm.Indexes = append(tm.Indexes, apitypes.DataIndex{Name: ix.name, Unique: ix.unique, Primary: primary, Definition: indexDefinition(ix.name, ix.table, ix.unique, ix.cols)})
		if ix.unique && !primary {
			tm.Constraints = append(tm.Constraints, apitypes.DataConstraint{Name: ix.name, Kind: apitypes.ConstraintUnique, Definition: "UNIQUE (" + joinIdents(ix.cols) + ")"})
		}
	}

	err = c.query(ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, COLUMN_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, REFERENCED_COLUMN_NAME
FROM information_schema.KEY_COLUMN_USAGE WHERE REFERENCED_TABLE_NAME IS NOT NULL AND `+databasesFilter("TABLE_SCHEMA", database)+`
ORDER BY TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`,
		7, func(r [][]byte) error {
			tm := find(r[0], r[1])
			if tm == nil {
				return nil
			}
			n := len(tm.ForeignKeys)
			if n == 0 || tm.ForeignKeys[n-1].Name != str(r[2]) {
				tm.ForeignKeys = append(tm.ForeignKeys, apitypes.DataForeignKey{Name: str(r[2]), RefSchema: str(r[4]), RefTable: str(r[5])})
				n++
			}
			fk := &tm.ForeignKeys[n-1]
			fk.Columns = append(fk.Columns, str(r[3]))
			fk.RefColumns = append(fk.RefColumns, str(r[6]))
			return nil
		})
	if err != nil {
		return nil, dbError(err)
	}

	err = c.query(ctx, `SELECT tc.TABLE_SCHEMA, tc.TABLE_NAME, cc.CONSTRAINT_NAME, cc.CHECK_CLAUSE
FROM information_schema.TABLE_CONSTRAINTS tc
JOIN information_schema.CHECK_CONSTRAINTS cc ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
WHERE tc.CONSTRAINT_TYPE = 'CHECK' AND `+databasesFilter("tc.TABLE_SCHEMA", database)+`
ORDER BY tc.TABLE_SCHEMA, tc.TABLE_NAME, cc.CONSTRAINT_NAME`,
		4, func(r [][]byte) error {
			if tm := find(r[0], r[1]); tm != nil {
				tm.Constraints = append(tm.Constraints, apitypes.DataConstraint{Name: str(r[2]), Kind: apitypes.ConstraintCheck, Definition: "CHECK (" + str(r[3]) + ")"})
			}
			return nil
		})
	if err != nil {
		return nil, dbError(err)
	}

	for i := range cat.tables {
		tb := &cat.tables[i]
		tm := cat.byName[tableKey(tb.Schema, tb.Name)]
		tm.key = pageKey(tm, keys[tm])
		tb.Editable = tb.Kind == apitypes.TableKindTable && len(tb.PrimaryKey) > 0
		for _, fk := range tb.ForeignKeys {
			k := tableKey(fk.RefSchema, fk.RefTable)
			cat.incoming[k] = append(cat.incoming[k], incomingFK{from: tm, fk: fk})
		}
	}
	return cat, nil
}

// pageKey picks the columns that make a page order total: the primary key,
// else the unique index over NOT NULL columns with the fewest columns
// (what InnoDB clusters a table without a primary key on).
func pageKey(tm *tableMeta, unique [][]string) []string {
	if len(tm.PrimaryKey) > 0 {
		return tm.PrimaryKey
	}
	var best []string
	for _, cols := range unique {
		if best != nil && len(cols) >= len(best) {
			continue
		}
		ok := true
		for _, c := range cols {
			if j, found := tm.col[c]; !found || tm.Columns[j].Nullable {
				ok = false
				break
			}
		}
		if ok {
			best = cols
		}
	}
	return best
}

// indexDefinition renders an index the way SHOW CREATE TABLE would
// declare it.
func indexDefinition(name, table string, unique bool, cols []string) string {
	switch {
	case name == "PRIMARY":
		return "PRIMARY KEY (" + joinIdents(cols) + ")"
	case unique:
		return "CREATE UNIQUE INDEX " + quoteIdent(name) + " ON " + quoteIdent(table) + " (" + joinIdents(cols) + ")"
	}
	return "CREATE INDEX " + quoteIdent(name) + " ON " + quoteIdent(table) + " (" + joinIdents(cols) + ")"
}

// joinIdents quotes plain column names (prefix and expression parts come
// already rendered) and joins them.
func joinIdents(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		if strings.HasPrefix(c, "`") || strings.HasPrefix(c, "(") {
			q[i] = c
		} else {
			q[i] = quoteIdent(c)
		}
	}
	return strings.Join(q, ", ")
}

// markSensitive is the catalog as a DataSchema with sensitive columns
// marked. The tables are copied; the catalog stays shared.
func markSensitive(cat *catalog, sensitive []apitypes.DataColumnRef) apitypes.DataSchema {
	marks := make(map[[3]string]bool, len(sensitive))
	for _, s := range sensitive {
		marks[[3]string{s.Schema, s.Table, s.Column}] = true
	}
	tables := make([]apitypes.DataTable, len(cat.tables))
	for i, tb := range cat.tables {
		tables[i] = tb
		if len(marks) == 0 {
			continue
		}
		cols := make([]apitypes.DataColumn, len(tb.Columns))
		for j, col := range tb.Columns {
			col.Sensitive = marks[[3]string{tb.Schema, tb.Name, col.Name}]
			cols[j] = col
		}
		tables[i].Columns = cols
	}
	return apitypes.DataSchema{Tables: tables, ReadAt: cat.readAt}
}

// masks returns which of tm's columns a call must not see: its sensitive
// columns for masked calls, none otherwise.
func masks(c data.Call, tm *tableMeta) map[string]bool {
	if !c.Masked {
		return nil
	}
	var out map[string]bool
	for _, s := range c.Sensitive {
		if s.Schema == tm.Schema && s.Table == tm.Name {
			if out == nil {
				out = map[string]bool{}
			}
			out[s.Column] = true
		}
	}
	return out
}
