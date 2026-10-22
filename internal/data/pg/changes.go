package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// commitFailed is DataConflict.Change when the COMMIT itself failed (a
// deferred constraint, a serialization failure) rather than one change.
const commitFailed = -1

// changePlan is one compiled change: the parameterised statement that
// runs, the same statement with literals for review, and how to read the
// row's current values when it matches nothing.
type changePlan struct {
	op     apitypes.DataChangeOp
	sql    string
	args   []any
	render string
	// checkSQL reads checkCols of the row by key as text (update and
	// delete).
	checkSQL  string
	checkArgs []any
	checkCols []string
}

// sqlPair builds a statement twice: with bound parameters to run, and with
// quoted literals to show.
type sqlPair struct {
	exec, show strings.Builder
	p          params
}

func (s *sqlPair) raw(v string) {
	s.exec.WriteString(v)
	s.show.WriteString(v)
}

// val writes a value: NULL, or a parameter (exec) and a literal (show).
func (s *sqlPair) val(v *string) {
	if v == nil {
		s.raw("NULL")
		return
	}
	s.exec.WriteString(s.p.add(*v))
	s.show.WriteString(quoteLiteral(*v))
}

// Render validates changes against the schema and returns the transaction
// a commit would run, values inlined.
func (e *Engine) Render(ctx context.Context, t data.Target, changes []apitypes.DataChange) (string, error) {
	var sql string
	err := e.read(ctx, t, data.Call{}, apitypes.DataQueryTimeout, func(cn *conn) error {
		cat, err := e.catalog(ctx, t, cn, true)
		if err != nil {
			return err
		}
		plans, err := planChanges(cat, changes)
		if err != nil {
			return err
		}
		sql = renderTx(plans)
		return nil
	})
	return sql, err
}

// Commit applies changes as the writer in one transaction. Updates and
// deletes match the primary key and the Old values; a change that matches
// no row is a conflict (missing, or changed with the row's current
// values), and a failing statement is a "failed" conflict that ends the
// run. Any conflict rolls everything back. Conflicts are results, not
// errors; an error means the database couldn't be used (or, when the
// connection broke during COMMIT, that the outcome is unknown).
func (e *Engine) Commit(ctx context.Context, t data.Target, c data.Call, changes []apitypes.DataChange) (data.CommitResult, error) {
	start := time.Now()
	var res data.CommitResult
	cn, err := e.acquire(ctx, t, data.RoleWriter)
	if err != nil {
		return res, err
	}
	defer e.release(cn)
	cat, err := e.catalog(ctx, t, cn, false)
	if err != nil {
		return res, err
	}
	plans, err := planChanges(cat, changes)
	if err != nil {
		return res, err
	}
	res.SQL = renderTx(plans)

	fail := func(err error) (data.CommitResult, error) {
		cn.broken = true
		res.Duration = time.Since(start)
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	begin := beginSQL(false, data.WriteStatementTimeout, data.WriteLockTimeout, data.WriteIdleTimeout, c.Actor)
	if err := simpleExec(ctx, cn.pg, begin); err != nil {
		return fail(fmt.Errorf("%w: %w", data.ErrUnreachable, err))
	}
	affected := make([]int64, 0, len(plans))
	for i, p := range plans {
		tag, err := cn.pg.Exec(ctx, p.sql, p.args...)
		if err != nil {
			pgErr, ok := errors.AsType[*pgconn.PgError](err)
			if !ok || ctx.Err() != nil {
				return fail(err)
			}
			res.Conflicts = append(res.Conflicts, apitypes.DataConflict{Change: i, Reason: apitypes.ConflictFailed, Message: pgErr.Message})
			break
		}
		affected = append(affected, tag.RowsAffected())
		if tag.RowsAffected() == 0 && p.op != apitypes.DataChangeInsert {
			conflict, err := diagnose(ctx, cn.pg, i, p)
			if err != nil {
				return fail(err)
			}
			res.Conflicts = append(res.Conflicts, conflict)
		}
	}
	if len(res.Conflicts) > 0 {
		if err := simpleExec(ctx, cn.pg, "ROLLBACK"); err != nil {
			cn.broken = true
		}
		res.Duration = time.Since(start)
		return res, nil
	}
	if err := simpleExec(ctx, cn.pg, "COMMIT"); err != nil {
		pgErr, ok := errors.AsType[*pgconn.PgError](err)
		if !ok {
			return fail(err)
		}
		res.Conflicts = []apitypes.DataConflict{{Change: commitFailed, Reason: apitypes.ConflictFailed, Message: pgErr.Message}}
		res.Duration = time.Since(start)
		return res, nil
	}
	res.RowsAffected = affected
	res.Duration = time.Since(start)
	return res, nil
}

// diagnose explains why change i matched no row.
func diagnose(ctx context.Context, pg *pgx.Conn, i int, p changePlan) (apitypes.DataConflict, error) {
	rows, err := pg.Query(ctx, p.checkSQL, append([]any{textResults}, p.checkArgs...)...)
	if err != nil {
		return apitypes.DataConflict{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return apitypes.DataConflict{}, err
		}
		return apitypes.DataConflict{Change: i, Reason: apitypes.ConflictMissing, Message: "the row no longer exists"}, nil
	}
	current := make(map[string]*string, len(p.checkCols))
	for j, v := range rows.RawValues()[:len(p.checkCols)] {
		if v != nil {
			s := string(v)
			current[p.checkCols[j]] = &s
		} else {
			current[p.checkCols[j]] = nil
		}
	}
	return apitypes.DataConflict{Change: i, Reason: apitypes.ConflictChanged, Current: current, Message: "the row changed since it was read"}, nil
}

// renderTx is the transaction of plans as a reviewer reads it.
func renderTx(plans []changePlan) string {
	var b strings.Builder
	b.WriteString("BEGIN;\n")
	for _, p := range plans {
		b.WriteString(p.render)
		b.WriteString(";\n")
	}
	b.WriteString("COMMIT;\n")
	return b.String()
}

// planChanges validates changes against the catalog and compiles them.
// Columns always come in table order, so the SQL is deterministic.
func planChanges(cat *catalog, changes []apitypes.DataChange) ([]changePlan, error) {
	if len(changes) == 0 || len(changes) > apitypes.DataChangesetMaxRows {
		return nil, fmt.Errorf("%w: a changeset has 1 to %d changes", data.ErrInvalid, apitypes.DataChangesetMaxRows)
	}
	plans := make([]changePlan, len(changes))
	for i, ch := range changes {
		p, err := planChange(cat, ch)
		if err != nil {
			return nil, fmt.Errorf("change %d: %w", i+1, err)
		}
		plans[i] = p
	}
	return plans, nil
}

func planChange(cat *catalog, ch apitypes.DataChange) (changePlan, error) {
	p := changePlan{op: ch.Op}
	tm, err := cat.table(ch.Table)
	if err != nil {
		return p, err
	}
	if !tm.Editable {
		return p, fmt.Errorf("%w: %s.%s isn't editable (only tables with a primary key are)", data.ErrInvalid, tm.Schema, tm.Name)
	}
	newCols, err := changeColumns(tm, ch.New, true)
	if err != nil {
		return p, err
	}
	oldCols, err := changeColumns(tm, ch.Old, false)
	if err != nil {
		return p, err
	}
	table := pgx.Identifier{tm.Schema, tm.Name}.Sanitize()
	var s sqlPair
	switch ch.Op {
	case apitypes.DataChangeInsert:
		if len(ch.Key) > 0 || len(ch.Old) > 0 {
			return p, fmt.Errorf("%w: an insert has only new values", data.ErrInvalid)
		}
		s.raw("INSERT INTO " + table)
		if len(newCols) == 0 {
			s.raw(" DEFAULT VALUES")
			break
		}
		s.raw(" (" + quoteAll(newCols) + ") VALUES (")
		for j, col := range newCols {
			if j > 0 {
				s.raw(", ")
			}
			s.val(ch.New[col])
		}
		s.raw(")")
	case apitypes.DataChangeUpdate:
		if err := checkKey(tm, ch.Key); err != nil {
			return p, err
		}
		if len(newCols) == 0 {
			return p, fmt.Errorf("%w: an update needs new values", data.ErrInvalid)
		}
		for _, col := range newCols {
			if _, ok := ch.Old[col]; !ok {
				return p, fmt.Errorf("%w: old value of %s missing; send the values you saw for every column you change", data.ErrInvalid, col)
			}
		}
		s.raw("UPDATE " + table + " SET ")
		for j, col := range newCols {
			if j > 0 {
				s.raw(", ")
			}
			s.raw(pgx.Identifier{col}.Sanitize() + " = ")
			s.val(ch.New[col])
		}
		writeMatch(&s, tm, ch.Key, ch.Old, oldCols)
	case apitypes.DataChangeDelete:
		if err := checkKey(tm, ch.Key); err != nil {
			return p, err
		}
		if len(ch.New) > 0 {
			return p, fmt.Errorf("%w: a delete has no new values", data.ErrInvalid)
		}
		s.raw("DELETE FROM " + table)
		writeMatch(&s, tm, ch.Key, ch.Old, oldCols)
	default:
		return p, fmt.Errorf("%w: unknown change %q", data.ErrInvalid, ch.Op)
	}
	p.sql, p.args, p.render = s.exec.String(), s.p.args, s.show.String()

	if ch.Op != apitypes.DataChangeInsert {
		var cp params
		sel := "1"
		if len(oldCols) > 0 {
			exprs := make([]string, len(oldCols))
			for j, col := range oldCols {
				exprs[j] = pgx.Identifier{col}.Sanitize() + "::text"
			}
			sel = strings.Join(exprs, ", ")
		}
		p.checkSQL = "SELECT " + sel + " FROM " + table + " WHERE " + keyCondition(tm, ch.Key, &cp)
		p.checkArgs, p.checkCols = cp.args, oldCols
	}
	return p, nil
}

// writeMatch writes the WHERE clause of an update or delete: the primary
// key, then every old value (IS NULL for nulls; through text for types
// without equality).
func writeMatch(s *sqlPair, tm *tableMeta, key map[string]string, old map[string]*string, oldCols []string) {
	s.raw(" WHERE ")
	for j, k := range tm.PrimaryKey {
		if j > 0 {
			s.raw(" AND ")
		}
		v := key[k]
		s.raw(pgx.Identifier{k}.Sanitize() + " = ")
		s.val(&v)
	}
	for _, col := range oldCols {
		expr := pgx.Identifier{col}.Sanitize()
		if noEquality(tm.Columns[tm.col[col]].TypeName) {
			expr += "::text"
		}
		v := old[col]
		if v == nil {
			s.raw(" AND " + expr + " IS NULL")
			continue
		}
		s.raw(" AND " + expr + " = ")
		s.val(v)
	}
}

// changeColumns returns the columns of values in table order, rejecting
// unknown columns and, for values to write, generated ones.
func changeColumns(tm *tableMeta, values map[string]*string, writing bool) ([]string, error) {
	for name := range values {
		j, ok := tm.col[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s.%s has no column %s", data.ErrInvalid, tm.Schema, tm.Name, name)
		}
		if writing && tm.Columns[j].Generated {
			return nil, fmt.Errorf("%w: %s is generated and can't be written", data.ErrInvalid, name)
		}
	}
	out := make([]string, 0, len(values))
	for _, col := range tm.Columns {
		if _, ok := values[col.Name]; ok {
			out = append(out, col.Name)
		}
	}
	return out, nil
}

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = pgx.Identifier{n}.Sanitize()
	}
	return strings.Join(q, ", ")
}
