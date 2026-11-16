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

// maxUserConnections is MAX_USER_CONNECTIONS of Dock's users: the cached
// connections per user plus one that kills a query.
const maxUserConnections = data.MaxConnsPerKey + 1

// user is a Dock user at any host: Dock dials from the network's gateway,
// whose address the server sees, so the host part can't be narrower.
func user(name data.Role) string { return quoteString(string(name)) + "@'%'" }

// Setup creates Dock's users as the owner, or rotates their passwords
// when they exist, and grants the reader and writer their privileges on
// the user databases (the target's database alone when it names one).
// With t.ReaderMasked set it also creates or rotates the masked reader,
// which reads nothing until SyncGrants. Users are created with the
// server's default auth plugin, the password sent over Dock's private
// connection (MySQL has no verifier form both servers accept).
func (e *Engine) Setup(ctx context.Context, t data.Target, owner, reader, writer data.Credentials) (data.ServerInfo, error) {
	c, info, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = c.Close() }()
	users := []data.Credentials{reader, writer}
	if t.ReaderMasked.User != "" {
		users = append(users, t.ReaderMasked)
	}
	for _, u := range users {
		if err := ensureUser(ctx, c, data.Role(u.User), u.Password); err != nil {
			return info, fmt.Errorf("create %s as %s: %w", u.User, owner.User, err)
		}
	}
	dbs, err := userDatabases(ctx, c, t.Database)
	if err != nil {
		return info, err
	}
	for _, sql := range grantsSQL(e.roles, dbs) {
		if err := c.exec(ctx, sql); err != nil {
			return info, fmt.Errorf("grant %s and %s their privileges as %s: %w", e.roles.Reader, e.roles.Writer, owner.User, err)
		}
	}
	e.Forget(t.ServiceID)
	return info, nil
}

// ensureUser creates a user or sets its password, caps its connections,
// and takes every privilege it has away (grants follow).
func ensureUser(ctx context.Context, c *conn, name data.Role, password string) error {
	pw := quoteString(password)
	for _, sql := range []string{
		"CREATE USER IF NOT EXISTS " + user(name) + " IDENTIFIED BY " + pw,
		"ALTER USER " + user(name) + " IDENTIFIED BY " + pw + " WITH MAX_USER_CONNECTIONS " + strconv.Itoa(maxUserConnections),
		"REVOKE ALL PRIVILEGES, GRANT OPTION FROM " + user(name),
	} {
		if err := c.exec(ctx, sql); err != nil {
			return err
		}
	}
	return nil
}

// userDatabases lists the databases Dock's users get: database when set,
// else every user database the owner sees.
func userDatabases(ctx context.Context, c *conn, database string) ([]string, error) {
	if database != "" {
		return []string{database}, nil
	}
	var dbs []string
	err := c.query(ctx, "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE "+databasesFilter("SCHEMA_NAME", "")+" ORDER BY 1", 1,
		func(r [][]byte) error { dbs = append(dbs, string(r[0])); return nil })
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	return dbs, nil
}

// grantsSQL gives the reader SELECT and SHOW VIEW and the writer SELECT
// and DML on each database. Database-level grants cover tables created
// later, so an app migrating as the owner needs no sync. Neither gets
// anything on mysql.* (password hashes), nor EXECUTE (stored functions
// that could change session state), nor FILE (INTO OUTFILE, LOAD_FILE).
func grantsSQL(roles data.Roles, dbs []string) []string {
	var out []string
	for _, db := range dbs {
		q := quoteIdent(db) + ".*"
		out = append(out,
			"GRANT SELECT, SHOW VIEW ON "+q+" TO "+user(roles.Reader),
			"GRANT SELECT, INSERT, UPDATE, DELETE ON "+q+" TO "+user(roles.Writer))
	}
	return out
}

// SyncGrants gives the masked reader SELECT on every table and view of
// the user databases, column by column on tables with sensitive columns
// (which it can't select), as the owner, and returns GrantsHash of the
// schema it synced for. The reader's and writer's database-level grants
// are re-applied too, which covers databases created since setup.
func (e *Engine) SyncGrants(ctx context.Context, t data.Target, owner data.Credentials, sensitive []apitypes.DataColumnRef) (string, error) {
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	dbs, err := userDatabases(ctx, c, t.Database)
	if err != nil {
		return "", err
	}
	cat, err := readCatalog(ctx, c, t.Database)
	if err != nil {
		return "", fmt.Errorf("read schema: %w", err)
	}
	schema := markSensitive(cat, sensitive)
	stmts := grantsSQL(e.roles, dbs)
	if t.ReaderMasked.User != "" {
		stmts = append(stmts, "REVOKE ALL PRIVILEGES, GRANT OPTION FROM "+user(e.roles.ReaderMasked))
		stmts = append(stmts, maskedGrantsSQL(e.roles, schema.Tables)...)
	}
	for _, sql := range stmts {
		if err := c.exec(ctx, sql); err != nil {
			return "", fmt.Errorf("grant %s, %s and %s their privileges as %s: %w", e.roles.Reader, e.roles.Writer, e.roles.ReaderMasked, owner.User, err)
		}
	}
	e.mu.Lock()
	if !e.closed {
		cat.readAt = time.Now()
		e.schemas[t.ServiceID] = cat
	}
	e.mu.Unlock()
	return data.GrantsHash(schema.Tables), nil
}

// maskedGrantsSQL gives the masked reader SELECT on each table: the whole
// table when nothing in it is sensitive, else its other columns (none
// left: nothing). Revoked first, so marks taken away take effect. While
// any column is sensitive it gets no views (maskedViews).
func maskedGrantsSQL(roles data.Roles, tables []apitypes.DataTable) []string {
	views := maskedViews(tables)
	var out []string
	for _, tb := range tables {
		if tb.Kind == apitypes.TableKindView && !views {
			continue
		}
		target := quoteTable(tb.Schema, tb.Name)
		var cols []string
		masked := false
		for _, col := range tb.Columns {
			if col.Sensitive {
				masked = true
				continue
			}
			cols = append(cols, quoteIdent(col.Name))
		}
		switch {
		case !masked:
			out = append(out, "GRANT SELECT ON "+target+" TO "+user(roles.ReaderMasked))
		case len(cols) > 0:
			out = append(out, "GRANT SELECT ("+strings.Join(cols, ", ")+") ON "+target+" TO "+user(roles.ReaderMasked))
		}
	}
	return out
}

// maskedViews reports whether masked readers may read views: only while no
// column is sensitive. A view reads its tables with its definer's
// privileges (or, as the panel's pages do, as the reader), and MySQL
// can't say which columns MariaDB's views use, so any view could carry a
// sensitive column past the masked reader's column grants.
func maskedViews(tables []apitypes.DataTable) bool {
	for _, tb := range tables {
		for _, col := range tb.Columns {
			if col.Sensitive {
				return false
			}
		}
	}
	return true
}
