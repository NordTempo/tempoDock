package mysql

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

// commitFailed is DataConflict.Change when the COMMIT itself failed
// rather than one change.
const commitFailed = -1

// changePlan is one compiled change: the statement that runs (and that
// the review shows: values are literals either way), and how to read the
// row's current values when it matches nothing.
type changePlan struct {
	op  apitypes.DataChangeOp
	sql string
	// checkSQL reads checkCols of the row by key (update and delete).
	checkSQL  string
	checkCols []string
	tm        *tableMeta
}

// Render validates changes against the schema and returns the transaction
// a commit would run.
func (e *Engine) Render(ctx context.Context, t data.Target, changes []apitypes.DataChange) (string, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return "", err
	}
	defer e.release(cn)
	cat, err := e.catalog(ctx, t, cn.Conn)
	if err != nil {
		return "", err
	}
	plans, err := planChanges(cat, changes)
	if err != nil {
		return "", err
	}
	return renderTx(plans), nil
}

// Commit applies changes as the writer in one transaction. Updates and
// deletes match the primary key and the Old values (null-safe, <=>), and
// the connection counts matched rows (CLIENT_FOUND_ROWS), so an update to
// the value a row already has still counts. A change that matches no row
// is a conflict (missing, or changed with the row's current values), and
// a failing statement is a "failed" conflict that ends the run. Any
// conflict rolls everything back.
func (e *Engine) Commit(ctx context.Context, t data.Target, c data.Call, changes []apitypes.DataChange) (data.CommitResult, error) {
	start := time.Now()
	var res data.CommitResult
	ctx, cancel := context.WithTimeout(ctx, data.WriteStatementTimeout)
	defer cancel()
	cn, err := e.acquire(ctx, t, data.RoleWriter)
	if err != nil {
		return res, err
	}
	defer e.release(cn)
	w := cn.Conn
	cat, err := e.catalog(ctx, t, w)
	if err != nil {
		return res, err
	}
	plans, err := planChanges(cat, changes)
	if err != nil {
		return res, err
	}
	res.SQL = renderTx(plans)

	fail := func(err error) (data.CommitResult, error) {
		cn.Broken = true
		res.Duration = time.Since(start)
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		return res, err
	}
	comment := actorComment(e.app, c.Actor)
	if err := w.exec(ctx, "START TRANSACTION"); err != nil {
		return fail(fmt.Errorf("%w: %w", data.ErrUnreachable, err))
	}
	affected := make([]int64, 0, len(plans))
	for i, p := range plans {
		stop := w.watch(ctx)
		r, err := w.Exec(comment + p.sql)
		stop()
		if err != nil {
			se, ok := errors.AsType[*wire.Error](err)
			if !ok || ctx.Err() != nil {
				return fail(err)
			}
			res.Conflicts = append(res.Conflicts, apitypes.DataConflict{Change: i, Reason: apitypes.ConflictFailed, Message: se.Message})
			break
		}
		affected = append(affected, int64(r.AffectedRows))
		if r.AffectedRows == 0 && p.op != apitypes.DataChangeInsert {
			conflict, err := diagnose(ctx, w, i, p)
			if err != nil {
				return fail(err)
			}
			res.Conflicts = append(res.Conflicts, conflict)
		}
	}
	if len(res.Conflicts) > 0 {
		if w.exec(ctx, "ROLLBACK") != nil {
			cn.Broken = true
		}
		res.Duration = time.Since(start)
		return res, nil
	}
	if err := w.exec(ctx, "COMMIT"); err != nil {
		se, ok := errors.AsType[*wire.Error](err)
		if !ok {
			return fail(err)
		}
		res.Conflicts = []apitypes.DataConflict{{Change: commitFailed, Reason: apitypes.ConflictFailed, Message: se.Message}}
		res.Duration = time.Since(start)
		return res, nil
	}
	res.RowsAffected = affected
	res.Duration = time.Since(start)
	return res, nil
}

// diagnose explains why change i matched no row.
func diagnose(ctx context.Context, c *conn, i int, p changePlan) (apitypes.DataConflict, error) {
	var current map[string]*string
	err := c.query(ctx, p.checkSQL, len(p.checkCols), func(r [][]byte) error {
		current = make(map[string]*string, len(p.checkCols))
		for j, col := range p.checkCols {
			if r[j] == nil {
				current[col] = nil
				continue
			}
			s := string(r[j])
			if _, hexed := cellOf(p.tm.typeName(col)); hexed {
				s = `\x` + hex.EncodeToString(r[j])
			}
			current[col] = &s
		}
		return nil
	})
	if err != nil {
		return apitypes.DataConflict{}, err
	}
	if current == nil {
		return apitypes.DataConflict{Change: i, Reason: apitypes.ConflictMissing, Message: "the row no longer exists"}, nil
	}
	return apitypes.DataConflict{Change: i, Reason: apitypes.ConflictChanged, Current: current, Message: "the row changed since it was read"}, nil
}

// renderTx is the transaction of plans as a reviewer reads it.
func renderTx(plans []changePlan) string {
	var b strings.Builder
	b.WriteString("START TRANSACTION;\n")
	for _, p := range plans {
		b.WriteString(p.sql)
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
	p.tm = tm
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
	val := func(col string, v *string) (string, error) {
		if v == nil {
			return "NULL", nil
		}
		return literal(tm.typeName(col), *v)
	}
	table := quoteTable(tm.Schema, tm.Name)
	var b strings.Builder
	switch ch.Op {
	case apitypes.DataChangeInsert:
		if len(ch.Key) > 0 || len(ch.Old) > 0 {
			return p, fmt.Errorf("%w: an insert has only new values", data.ErrInvalid)
		}
		vals := make([]string, len(newCols))
		for j, col := range newCols {
			if vals[j], err = val(col, ch.New[col]); err != nil {
				return p, fmt.Errorf("%s: %w", col, err)
			}
		}
		b.WriteString("INSERT INTO " + table + " (" + joinIdents(newCols) + ") VALUES (" + strings.Join(vals, ", ") + ")")
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
		sets := make([]string, len(newCols))
		for j, col := range newCols {
			v, err := val(col, ch.New[col])
			if err != nil {
				return p, fmt.Errorf("%s: %w", col, err)
			}
			sets[j] = quoteIdent(col) + " = " + v
		}
		match, err := matchClause(tm, ch.Key, ch.Old, oldCols, val)
		if err != nil {
			return p, err
		}
		b.WriteString("UPDATE " + table + " SET " + strings.Join(sets, ", ") + match)
	case apitypes.DataChangeDelete:
		if err := checkKey(tm, ch.Key); err != nil {
			return p, err
		}
		if len(ch.New) > 0 {
			return p, fmt.Errorf("%w: a delete has no new values", data.ErrInvalid)
		}
		match, err := matchClause(tm, ch.Key, ch.Old, oldCols, val)
		if err != nil {
			return p, err
		}
		b.WriteString("DELETE FROM " + table + match)
	default:
		return p, fmt.Errorf("%w: unknown change %q", data.ErrInvalid, ch.Op)
	}
	p.sql = b.String()

	if ch.Op != apitypes.DataChangeInsert {
		sel := "1"
		if len(oldCols) > 0 {
			sel = joinIdents(oldCols)
		}
		cond, err := keyCondition(tm, ch.Key)
		if err != nil {
			return p, err
		}
		p.checkSQL = "SELECT " + sel + " FROM " + table + " WHERE " + cond
		p.checkCols = oldCols
	}
	return p, nil
}

// matchClause is the WHERE clause of an update or delete: the primary
// key, then every old value, compared null-safely (<=>).
func matchClause(tm *tableMeta, key map[string]string, old map[string]*string, oldCols []string, val func(string, *string) (string, error)) (string, error) {
	cond, err := keyCondition(tm, key)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(" WHERE " + cond)
	for _, col := range oldCols {
		v, err := val(col, old[col])
		if err != nil {
			return "", fmt.Errorf("old %s: %w", col, err)
		}
		expr := quoteIdent(col)
		if tm.typeName(col) == "json" {
			// JSON compares by value: the old text normalised as JSON.
			if v != "NULL" {
				v = "CAST(" + v + " AS JSON)"
			}
		}
		b.WriteString(" AND " + expr + " <=> " + v)
	}
	return b.String(), nil
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
