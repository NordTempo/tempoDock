package secrets

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// Column is a sealed column of a host's SQLite store: where its values
// are and how a row's AAD is made. Hosts declare one per column, make
// their AADs with Column.AAD and pass the list to ResealColumn.
type Column struct {
	Table, Name string
	// Slot is the AAD's column name, Table.Name when empty. Two columns
	// share one when a value moves between them (a staged variable value
	// becoming the live one).
	Slot string
	// Key lists the SQL expressions naming a row, in the AAD's order:
	// its primary key or another unique key. Empty for a one-row table.
	Key []string
}

// AAD is the AAD of the column's value in the row named by key, the
// values of Column.Key in order.
func (c Column) AAD(key ...string) AAD { return Bind(c.slot(), key...) }

func (c Column) slot() string {
	if c.Slot != "" {
		return c.Slot
	}
	return c.Table + "." + c.Name
}

// resealBatch is how many rows one write transaction reseals.
const resealBatch = 100

// ResealColumns binds the unbound values of each column to their rows
// (Reseal), logging what it did. Values that don't open (the wrong key,
// or corrupted) stay as they are and fail when read. It is idempotent and
// safe to interrupt, or to run in two processes at once: bound values are
// never selected, resealing again gives the same bytes, and each update
// matches the row's key and its old value, so a value changed meanwhile
// is left alone.
func (b *Box) ResealColumns(ctx context.Context, db *sql.DB, cols []Column, log *slog.Logger) error {
	for _, col := range cols {
		n, skipped, err := b.resealColumn(ctx, db, col)
		if err != nil {
			return err
		}
		if n > 0 {
			log.Info("secrets: bound values to where they are stored", "column", col.Table+"."+col.Name, "count", n)
		}
		if skipped > 0 {
			log.Warn("secrets: values that don't open were left unbound; set them again", "column", col.Table+"."+col.Name, "count", skipped)
		}
	}
	return nil
}

// resealColumn reseals col in write transactions of resealBatch rows and
// returns how many values it resealed and how many it skipped.
func (b *Box) resealColumn(ctx context.Context, db *sql.DB, col Column) (resealed, skipped int, err error) {
	sel := "SELECT " + strings.Join(append(append([]string{}, col.Key...), col.Name), ", ") +
		" FROM " + col.Table + " WHERE substr(" + col.Name + ", 1, 1) = x'01'"
	where := make([]string, 0, len(col.Key)+1)
	for _, k := range col.Key {
		where = append(where, k+" = +?")
	}
	upd := "UPDATE " + col.Table + " SET " + col.Name + " = ? WHERE " +
		strings.Join(append(where, col.Name+" = +?"), " AND ")

	rows, err := db.QueryContext(ctx, sel)
	if err != nil {
		return 0, 0, fmt.Errorf("reseal %s: %w", col.slot(), err)
	}
	defer func() { _ = rows.Close() }()
	// args per row: the new value, the key's values, the old value.
	var batch [][]any
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		for _, args := range batch {
			if _, err := tx.ExecContext(ctx, upd, args...); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		resealed += len(batch)
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		key := make([]any, len(col.Key))
		var old []byte
		dest := make([]any, 0, len(key)+1)
		for i := range key {
			dest = append(dest, &key[i])
		}
		if err := rows.Scan(append(dest, &old)...); err != nil {
			return resealed, skipped, fmt.Errorf("reseal %s: %w", col.slot(), err)
		}
		parts := make([]string, len(key))
		for i, k := range key {
			if parts[i], err = keyPart(k); err != nil {
				return resealed, skipped, fmt.Errorf("reseal %s: %w", col.slot(), err)
			}
		}
		sealed, err := b.Reseal(old, col.AAD(parts...))
		if err != nil {
			skipped++
			continue
		}
		batch = append(batch, append(append([]any{sealed}, key...), old))
		if len(batch) == resealBatch {
			if err := flush(); err != nil {
				return resealed, skipped, fmt.Errorf("reseal %s: %w", col.slot(), err)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return resealed, skipped, fmt.Errorf("reseal %s: %w", col.slot(), err)
	}
	if err := flush(); err != nil {
		return resealed, skipped, fmt.Errorf("reseal %s: %w", col.slot(), err)
	}
	return resealed, skipped, nil
}

// keyPart is a key value as Column.AAD takes it: an integer in decimal,
// text as it is. The value itself goes back into the update, so it still
// compares equal to an expression without affinity.
func keyPart(v any) (string, error) {
	switch v := v.(type) {
	case int64:
		return strconv.FormatInt(v, 10), nil
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	}
	return "", fmt.Errorf("key value %v (%T) is neither an integer nor text", v, v)
}
