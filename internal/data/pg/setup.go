package pg

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// scramIterations is Postgres' default for SCRAM-SHA-256 verifiers.
const scramIterations = 4096

// existingRole is a Dock role found in pg_roles during setup.
type existingRole struct {
	// Privileged: superuser, createdb, createrole, replication or
	// bypassrls; setup strips them.
	Privileged bool
	// MemberOf lists the roles it is a member of.
	MemberOf []string
}

// roleSpec is what setup makes a Dock role.
type roleSpec struct {
	name     data.Role
	verifier string
	// settings are per-role defaults (ALTER ROLE ... SET).
	settings [][2]string
}

// Setup creates or rotates Dock's roles as the owner and grants them their
// privileges:
//
//   - dock_reader: LOGIN, default_transaction_read_only on,
//     statement_timeout capped at the longest read; SELECT on the
//     relations and sequences of every user schema.
//
//   - dock_writer: LOGIN, statement and idle-in-transaction timeouts of a
//     commit; SELECT, INSERT, UPDATE and DELETE on the relations of every
//     user schema and USAGE on their sequences (DML, no DDL).
//
//   - dock_reader_masked (with t.ReaderMasked set): LOGIN, read-only like
//     dock_reader; SyncGrants grants its columns.
//
//   - dock_monitor (with t.Monitor set), after the transaction and best
//     effort: LOGIN, read-only, a member of pg_monitor only, no table
//     privileges (ensureMonitor). It reads statistics for insights.
//
// The first three are members of no role: predefined roles such as
// pg_read_all_data would also open the system catalogs (password hashes in
// pg_authid, user mapping options, large objects), which Dock's roles must
// never read. All are stripped of superuser, createdb, createrole,
// replication and bypassrls, so row-level security applies to them.
// Passwords are sent as SCRAM-SHA-256 verifiers, so the plain text never
// reaches the server or its logs. Everything runs in one transaction;
// re-running it is safe. Setup then drops the service's cached
// connections and schema.
func (e *Engine) Setup(ctx context.Context, t data.Target, owner, reader, writer data.Credentials) (data.ServerInfo, error) {
	specs, err := roleSpecs(e.roles, reader, writer, t.ReaderMasked)
	if err != nil {
		return data.ServerInfo{}, err
	}
	pg, info, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = pg.Close(context.Background()) }()

	existing, err := existingRoles(ctx, pg, e.roles)
	if err != nil {
		return info, fmt.Errorf("read roles: %w", err)
	}
	schemas, err := userSchemas(ctx, pg)
	if err != nil {
		return info, fmt.Errorf("read schemas: %w", err)
	}
	stmts := append(setupSQL(specs, existing, t.Database), grantsSQL(e.roles, schemas)...)
	err = pgx.BeginFunc(ctx, pg, func(tx pgx.Tx) error {
		for _, s := range stmts {
			if _, err := tx.Exec(ctx, s); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return info, fmt.Errorf("set up roles as %s (needs a superuser or CREATEROLE owner): %w", owner.User, err)
	}
	if t.Monitor.User != "" {
		// Best effort: insights create the monitor on first use when this
		// fails, and say why if that fails too.
		_ = ensureMonitor(ctx, pg, e.roles.Monitor, t.Monitor.Password, t.Database)
	}
	e.Forget(t.ServiceID)
	return info, nil
}

// ownerConn logs in as the owner (setup only, never cached) and checks the
// server version.
func (e *Engine) ownerConn(ctx context.Context, t data.Target, owner data.Credentials) (*pgx.Conn, data.ServerInfo, error) {
	if owner.User == "" || t.Dial == nil {
		return nil, data.ServerInfo{}, fmt.Errorf("%w: no owner credentials", data.ErrLoginFailed)
	}
	pg, err := e.connect(ctx, t, owner)
	if err != nil {
		return nil, data.ServerInfo{}, loginError(ctx, err, data.ErrLoginFailed)
	}
	var num int
	if err := pg.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		_ = pg.Close(context.Background())
		return nil, data.ServerInfo{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	info := serverInfo(pg, num)
	if num < data.MinPostgresVersion {
		_ = pg.Close(context.Background())
		return nil, info, unsupported(info)
	}
	return pg, info, nil
}

// SyncGrants brings the Dock roles' privileges in line with the schema and
// the sensitive columns, as the owner, in one transaction: it revokes any
// role membership they hold (setups before explicit grants made them
// members of pg_read_all_data and pg_write_all_data), grants dock_reader
// and dock_writer their privileges on every user schema (grantsSQL), and
// in every user schema revokes all of dock_reader_masked's table
// privileges (which also drops column grants), grants it USAGE, then
// SELECT on each relation's columns that aren't sensitive. Relations
// another role creates later stay invisible to the roles until the next
// sync (the owner's get dock_reader's and dock_writer's grants through
// default privileges). The owner's fresh catalog replaces the cached one,
// so the returned hash matches what Schema reports.
func (e *Engine) SyncGrants(ctx context.Context, t data.Target, owner data.Credentials, sensitive []apitypes.DataColumnRef) (string, error) {
	pg, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return "", err
	}
	defer func() { _ = pg.Close(context.Background()) }()
	existing, err := existingRoles(ctx, pg, e.roles)
	if err != nil {
		return "", fmt.Errorf("read roles: %w", err)
	}
	schemas, err := userSchemas(ctx, pg)
	if err != nil {
		return "", fmt.Errorf("read schemas: %w", err)
	}
	var doc string
	if err := pg.QueryRow(ctx, catalogSQL).Scan(&doc); err != nil {
		return "", fmt.Errorf("read schema: %w", err)
	}
	cat, err := parseCatalog([]byte(doc), time.Now())
	if err != nil {
		return "", err
	}
	marks := markSet(sensitive)
	schema := markSensitive(cat, marks)
	var leaks map[relRef]bool
	if len(marks) > 0 {
		deps, err := viewDeps(ctx, pg)
		if err != nil {
			return "", fmt.Errorf("read views: %w", err)
		}
		leaks = leakingViews(deps, marks)
	}
	stmts := membershipSQL(e.roles, existing)
	stmts = append(stmts, grantsSQL(e.roles, schemas)...)
	stmts = append(stmts, maskedGrantsSQL(e.roles, schema.Tables, leaks)...)
	if err := simpleExec(ctx, pg, "BEGIN;\n"+strings.Join(stmts, ";\n")+";\nCOMMIT"); err != nil {
		return "", fmt.Errorf("grant %s, %s and %s their privileges as %s: %w", e.roles.Reader, e.roles.Writer, e.roles.ReaderMasked, owner.User, err)
	}
	e.mu.Lock()
	if !e.closed {
		e.schemas[t.ServiceID] = cat
	}
	e.mu.Unlock()
	return data.GrantsHash(schema.Tables), nil
}

// userSchemasSQL lists the schemas Dock's roles may use: all but the
// system catalogs, information_schema and TOAST and temporary schemas.
const userSchemasSQL = `SELECT nspname FROM pg_namespace
WHERE nspname NOT IN ('pg_catalog', 'information_schema') AND nspname !~ '^pg_(toast|temp_)'
ORDER BY 1`

// userSchemas reads userSchemasSQL.
func userSchemas(ctx context.Context, pg *pgx.Conn) ([]string, error) {
	rows, err := pg.Query(ctx, userSchemasSQL)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// grantsSQL renders dock_reader's and dock_writer's privileges on the
// given schemas: USAGE on each, SELECT (reader) or SELECT and DML (writer)
// on all their relations, SELECT (reader) or USAGE and SELECT (writer, for
// nextval defaults) on their sequences. Default privileges give the same
// on schemas, relations and sequences the owner creates later, so an app
// migrating as the owner needs no sync. dock_reader_masked gets nothing
// here (maskedGrantsSQL).
func grantsSQL(roles data.Roles, schemas []string) []string {
	r := pgx.Identifier{string(roles.Reader)}.Sanitize()
	w := pgx.Identifier{string(roles.Writer)}.Sanitize()
	out := make([]string, 0, 5*len(schemas)+5)
	for _, name := range schemas {
		s := pgx.Identifier{name}.Sanitize()
		out = append(out,
			"GRANT USAGE ON SCHEMA "+s+" TO "+r+", "+w,
			"GRANT SELECT ON ALL TABLES IN SCHEMA "+s+" TO "+r,
			"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA "+s+" TO "+w,
			"GRANT SELECT ON ALL SEQUENCES IN SCHEMA "+s+" TO "+r,
			"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA "+s+" TO "+w)
	}
	return append(out,
		"ALTER DEFAULT PRIVILEGES GRANT USAGE ON SCHEMAS TO "+r+", "+w,
		"ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO "+r,
		"ALTER DEFAULT PRIVILEGES GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "+w,
		"ALTER DEFAULT PRIVILEGES GRANT SELECT ON SEQUENCES TO "+r,
		"ALTER DEFAULT PRIVILEGES GRANT USAGE, SELECT ON SEQUENCES TO "+w)
}

// membershipSQL revokes every role membership the existing roles hold.
func membershipSQL(roles data.Roles, existing map[string]existingRole) []string {
	var out []string
	for _, name := range []data.Role{roles.Reader, roles.Writer, roles.ReaderMasked} {
		for _, m := range existing[string(name)].MemberOf {
			out = append(out, "REVOKE "+pgx.Identifier{m}.Sanitize()+" FROM "+pgx.Identifier{string(name)}.Sanitize())
		}
	}
	return out
}

// relRef names a relation: a table, view or materialized view.
type relRef struct{ schema, name string }

// viewDep is one thing a view or materialized view reads: a column of
// another relation, or the whole of it (column "", a whole-row reference).
type viewDep struct {
	view, rel relRef
	column    string
}

// viewDepsSQL lists what every view and materialized view reads, from the
// dependencies of its rewrite rule: one row per column it uses.
const viewDepsSQL = `SELECT DISTINCT vn.nspname, vc.relname, rn.nspname, rc.relname, coalesce(a.attname, '')
FROM pg_rewrite r
JOIN pg_class vc ON vc.oid = r.ev_class AND vc.relkind IN ('v', 'm')
JOIN pg_namespace vn ON vn.oid = vc.relnamespace
JOIN pg_depend d ON d.classid = 'pg_rewrite'::regclass AND d.objid = r.oid
  AND d.refclassid = 'pg_class'::regclass AND d.refobjid <> r.ev_class
JOIN pg_class rc ON rc.oid = d.refobjid
JOIN pg_namespace rn ON rn.oid = rc.relnamespace
LEFT JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid AND d.refobjsubid > 0`

// viewDeps reads viewDepsSQL.
func viewDeps(ctx context.Context, pg *pgx.Conn) ([]viewDep, error) {
	rows, err := pg.Query(ctx, viewDepsSQL)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (viewDep, error) {
		var d viewDep
		err := r.Scan(&d.view.schema, &d.view.name, &d.rel.schema, &d.rel.name, &d.column)
		return d, err
	})
}

// leakingViews are the views and materialized views that read a sensitive
// column, directly or through another such view, or a whole row of a
// relation with one. They run with their owner's privileges, so the
// masked reader's column grants don't hold inside them: it gets nothing
// on them.
func leakingViews(deps []viewDep, marks map[colRef]bool) map[relRef]bool {
	hasSensitive := map[relRef]bool{}
	for m := range marks {
		hasSensitive[relRef{m.schema, m.table}] = true
	}
	leaks := map[relRef]bool{}
	for changed := true; changed; {
		changed = false
		for _, d := range deps {
			if leaks[d.view] {
				continue
			}
			whole := d.column == "" && hasSensitive[d.rel]
			if whole || leaks[d.rel] || marks[colRef{d.rel.schema, d.rel.name, d.column}] {
				leaks[d.view], hasSensitive[d.view], changed = true, true, true
			}
		}
	}
	return leaks
}

// maskedGrantsSQL renders the masked reader's statements for tables with
// their sensitive columns marked; leaks (leakingViews) get none.
func maskedGrantsSQL(roles data.Roles, tables []apitypes.DataTable, leaks map[relRef]bool) []string {
	role := pgx.Identifier{string(roles.ReaderMasked)}.Sanitize()
	var out []string
	var schema string
	for _, t := range tables {
		if t.Schema != schema {
			schema = t.Schema
			s := pgx.Identifier{schema}.Sanitize()
			out = append(out, "REVOKE ALL ON ALL TABLES IN SCHEMA "+s+" FROM "+role, "GRANT USAGE ON SCHEMA "+s+" TO "+role)
		}
		if leaks[relRef{t.Schema, t.Name}] {
			continue
		}
		var cols []string
		for _, c := range t.Columns {
			if !c.Sensitive {
				cols = append(cols, c.Name)
			}
		}
		if len(cols) > 0 {
			out = append(out, "GRANT SELECT ("+quoteAll(cols)+") ON "+pgx.Identifier{t.Schema, t.Name}.Sanitize()+" TO "+role)
		}
	}
	return out
}

// roleSpecs describes the roles setup creates, with SCRAM verifiers of
// the new passwords; the masked reader only when masked has a user.
func roleSpecs(roles data.Roles, reader, writer, masked data.Credentials) ([]roleSpec, error) {
	rv, err := scramVerifier(reader.Password)
	if err != nil {
		return nil, fmt.Errorf("reader password: %w", err)
	}
	wv, err := scramVerifier(writer.Password)
	if err != nil {
		return nil, fmt.Errorf("writer password: %w", err)
	}
	readSettings := [][2]string{
		{"default_transaction_read_only", "on"},
		{"statement_timeout", fmt.Sprint(maxReadTimeout.Milliseconds())},
	}
	specs := []roleSpec{
		{
			name:     roles.Reader,
			verifier: rv,
			settings: readSettings,
		},
		{
			name:     roles.Writer,
			verifier: wv,
			settings: [][2]string{
				{"statement_timeout", fmt.Sprint(data.WriteStatementTimeout.Milliseconds())},
				{"idle_in_transaction_session_timeout", fmt.Sprint(data.WriteIdleTimeout.Milliseconds())},
			},
		},
	}
	if masked.User != "" {
		mv, err := scramVerifier(masked.Password)
		if err != nil {
			return nil, fmt.Errorf("masked reader password: %w", err)
		}
		specs = append(specs, roleSpec{name: roles.ReaderMasked, verifier: mv, settings: readSettings})
	}
	return specs, nil
}

// existingRoles reads which of the roles already exist.
func existingRoles(ctx context.Context, pg *pgx.Conn, roles data.Roles) (map[string]existingRole, error) {
	rows, err := pg.Query(ctx, `
SELECT r.rolname,
       r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls,
       coalesce(array_agg(g.rolname) FILTER (WHERE g.rolname IS NOT NULL), '{}')
FROM pg_roles r
LEFT JOIN pg_auth_members m ON m.member = r.oid
LEFT JOIN pg_roles g ON g.oid = m.roleid
WHERE r.rolname = ANY($1)
GROUP BY 1, 2`, []string{string(roles.Reader), string(roles.Writer), string(roles.ReaderMasked)})
	if err != nil {
		return nil, err
	}
	out := map[string]existingRole{}
	for rows.Next() {
		var name string
		var r existingRole
		if err := rows.Scan(&name, &r.Privileged, &r.MemberOf); err != nil {
			return nil, err
		}
		out[name] = r
	}
	return out, rows.Err()
}

// setupSQL renders the statements that bring the roles to spec, members
// of no role (any membership is revoked). Role and
// database names are quoted identifiers; verifiers are base64 and are
// quoted literals.
func setupSQL(specs []roleSpec, existing map[string]existingRole, database string) []string {
	var out []string
	db := pgx.Identifier{database}.Sanitize()
	for _, s := range specs {
		role := pgx.Identifier{string(s.name)}.Sanitize()
		cur, exists := existing[string(s.name)]
		switch {
		case !exists:
			out = append(out, "CREATE ROLE "+role+" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD "+quoteLiteral(s.verifier))
		case cur.Privileged:
			out = append(out, "ALTER ROLE "+role+" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS INHERIT PASSWORD "+quoteLiteral(s.verifier))
		default:
			out = append(out, "ALTER ROLE "+role+" WITH LOGIN INHERIT PASSWORD "+quoteLiteral(s.verifier))
		}
		for _, m := range cur.MemberOf {
			out = append(out, "REVOKE "+pgx.Identifier{m}.Sanitize()+" FROM "+role)
		}
		for _, kv := range s.settings {
			out = append(out, "ALTER ROLE "+role+" SET "+kv[0]+" = "+quoteLiteral(kv[1]))
		}
		out = append(out, "GRANT CONNECT ON DATABASE "+db+" TO "+role)
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// scramVerifier hashes password into a SCRAM-SHA-256 verifier as Postgres
// stores it (RFC 5802/7677), so CREATE/ALTER ROLE never carries the plain
// password. Passwords must be printable ASCII (Dock generates them), which
// SASLprep leaves unchanged.
func scramVerifier(password string) (string, error) {
	if password == "" {
		return "", fmt.Errorf("%w: empty password", data.ErrInvalid)
	}
	for i := 0; i < len(password); i++ {
		if password[i] < 0x21 || password[i] > 0x7e {
			return "", fmt.Errorf("%w: passwords must be printable ASCII", data.ErrInvalid)
		}
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return scramVerifierSalt(password, salt, scramIterations)
}

// scramVerifierSalt is scramVerifier with a given salt and iteration count.
func scramVerifierSalt(password string, salt []byte, iterations int) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", err
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	b64 := base64.StdEncoding.EncodeToString
	var b strings.Builder
	fmt.Fprintf(&b, "SCRAM-SHA-256$%d:%s$%s:%s", iterations, b64(salt), b64(storedKey[:]), b64(serverKey))
	return b.String(), nil
}

func hmacSHA256(key []byte, msg string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(msg))
	return h.Sum(nil)
}
