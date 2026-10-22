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
	// grants are the predefined roles it is a member of; any other
	// membership is revoked.
	grants []string
	// settings are per-role defaults (ALTER ROLE ... SET).
	settings [][2]string
}

// Setup creates or rotates dock_reader and dock_writer as the owner:
//
//   - dock_reader: LOGIN, member of pg_read_all_data only,
//     default_transaction_read_only on, statement_timeout capped at the
//     longest read.
//
//   - dock_writer: LOGIN, member of pg_read_all_data and pg_write_all_data
//     only (DML, no DDL), statement and idle-in-transaction timeouts of a
//     commit.
//
//   - dock_reader_masked (with t.ReaderMasked set): LOGIN, member of no
//     role, read-only like dock_reader; SyncMasked grants its columns.
//
// All are stripped of superuser, createdb, createrole, replication and
// bypassrls, so row-level security applies to them. Passwords are sent as
// SCRAM-SHA-256 verifiers, so the plain text never reaches the server or
// its logs. Everything runs in one transaction; re-running it is safe.
// Setup then drops the service's cached connections and schema.
func (e *Engine) Setup(ctx context.Context, t data.Target, owner, reader, writer data.Credentials) (data.ServerInfo, error) {
	specs, err := dockRoles(reader, writer, t.ReaderMasked)
	if err != nil {
		return data.ServerInfo{}, err
	}
	pg, info, err := ownerConn(ctx, t, owner)
	if err != nil {
		return info, err
	}
	defer func() { _ = pg.Close(context.Background()) }()

	existing, err := existingRoles(ctx, pg)
	if err != nil {
		return info, fmt.Errorf("read roles: %w", err)
	}
	stmts := setupSQL(specs, existing, t.Database)
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
	e.Forget(t.ServiceID)
	return info, nil
}

// ownerConn logs in as the owner (setup only, never cached) and checks the
// server version.
func ownerConn(ctx context.Context, t data.Target, owner data.Credentials) (*pgx.Conn, data.ServerInfo, error) {
	if owner.User == "" || t.Dial == nil {
		return nil, data.ServerInfo{}, fmt.Errorf("%w: no owner credentials", data.ErrLoginFailed)
	}
	pg, err := connect(ctx, t, owner)
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

// SyncMasked brings dock_reader_masked's grants in line with the schema
// and the sensitive columns, as the owner, in one transaction: in every
// user schema it revokes all table privileges (which also drops column
// grants), grants USAGE, then grants SELECT on each relation's columns
// that aren't sensitive. Relations created later stay invisible to it
// until the next sync. The owner's fresh catalog replaces the cached one,
// so the returned hash matches what Schema reports.
func (e *Engine) SyncMasked(ctx context.Context, t data.Target, owner data.Credentials, sensitive []apitypes.DataColumnRef) (string, error) {
	pg, _, err := ownerConn(ctx, t, owner)
	if err != nil {
		return "", err
	}
	defer func() { _ = pg.Close(context.Background()) }()
	var doc string
	if err := pg.QueryRow(ctx, catalogSQL).Scan(&doc); err != nil {
		return "", fmt.Errorf("read schema: %w", err)
	}
	cat, err := parseCatalog([]byte(doc), time.Now())
	if err != nil {
		return "", err
	}
	schema := markSensitive(cat, markSet(sensitive))
	if err := simpleExec(ctx, pg, "BEGIN;\n"+strings.Join(maskedGrantsSQL(schema.Tables), ";\n")+";\nCOMMIT"); err != nil {
		return "", fmt.Errorf("grant %s its columns as %s: %w", data.RoleReaderMasked, owner.User, err)
	}
	e.mu.Lock()
	if !e.closed {
		e.schemas[t.ServiceID] = cat
	}
	e.mu.Unlock()
	return data.MaskedHash(schema.Tables), nil
}

// maskedGrantsSQL renders SyncMasked's statements for tables with their
// sensitive columns marked.
func maskedGrantsSQL(tables []apitypes.DataTable) []string {
	role := pgx.Identifier{string(data.RoleReaderMasked)}.Sanitize()
	var out []string
	var schema string
	for _, t := range tables {
		if t.Schema != schema {
			schema = t.Schema
			s := pgx.Identifier{schema}.Sanitize()
			out = append(out, "REVOKE ALL ON ALL TABLES IN SCHEMA "+s+" FROM "+role, "GRANT USAGE ON SCHEMA "+s+" TO "+role)
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

// dockRoles describes the roles setup creates, with SCRAM verifiers of the
// new passwords; the masked reader only when masked has a user.
func dockRoles(reader, writer, masked data.Credentials) ([]roleSpec, error) {
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
			name:     data.RoleReader,
			verifier: rv,
			grants:   []string{"pg_read_all_data"},
			settings: readSettings,
		},
		{
			name:     data.RoleWriter,
			verifier: wv,
			grants:   []string{"pg_read_all_data", "pg_write_all_data"},
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
		specs = append(specs, roleSpec{name: data.RoleReaderMasked, verifier: mv, settings: readSettings})
	}
	return specs, nil
}

// existingRoles reads the Dock roles that already exist.
func existingRoles(ctx context.Context, pg *pgx.Conn) (map[string]existingRole, error) {
	rows, err := pg.Query(ctx, `
SELECT r.rolname,
       r.rolsuper OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication OR r.rolbypassrls,
       coalesce(array_agg(g.rolname) FILTER (WHERE g.rolname IS NOT NULL), '{}')
FROM pg_roles r
LEFT JOIN pg_auth_members m ON m.member = r.oid
LEFT JOIN pg_roles g ON g.oid = m.roleid
WHERE r.rolname = ANY($1)
GROUP BY 1, 2`, []string{string(data.RoleReader), string(data.RoleWriter), string(data.RoleReaderMasked)})
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

// setupSQL renders the statements that bring the roles to spec. Role and
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
			if !contains(s.grants, m) {
				out = append(out, "REVOKE "+pgx.Identifier{m}.Sanitize()+" FROM "+role)
			}
		}
		for _, g := range s.grants {
			if !contains(cur.MemberOf, g) {
				out = append(out, "GRANT "+pgx.Identifier{g}.Sanitize()+" TO "+role)
			}
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
