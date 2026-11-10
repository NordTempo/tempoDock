// Package data is the engine-neutral side of the database panel (ADR 0016).
//
// The panel (internal/datapanel) decides who may do what and resolves a
// database to a Target; an Engine (internal/data/pg for Postgres) talks to
// the database. Engines know nothing about actors, permissions or the
// store, and the panel knows nothing about drivers. What every engine
// does is Engine; tabular engines also implement Tables, key-value ones
// Keys. Connections are cached by internal/data/conncache.
//
// Idle means idle: an engine holds no connections, goroutines or timers
// while nobody uses the panel. Connections are opened on first use and
// closed IdleTimeout after their last use.
package data

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Engine limits that clients don't see. Client-visible caps (rows, bytes,
// timeouts, changeset size) are the apitypes.Data* constants.
const (
	// IdleTimeout closes a connection this long after its last use.
	IdleTimeout = 60 * time.Second
	// MaxConnsPerKey and MaxConns bound open connections per (service,
	// role) and per daemon. Callers past the bound wait for a free one
	// (until ctx ends).
	MaxConnsPerKey = 2
	MaxConns       = 8
	// SchemaTTL is how long an introspection result is reused.
	SchemaTTL = 60 * time.Second
	// ReadLockTimeout is lock_timeout for reads; reads also get the
	// request's statement timeout (apitypes.DataQueryTimeout by default).
	ReadLockTimeout = time.Second
	// WriteLockTimeout, WriteStatementTimeout and WriteIdleTimeout
	// (idle_in_transaction_session_timeout) apply to changeset commits.
	WriteLockTimeout      = 2 * time.Second
	WriteStatementTimeout = 30 * time.Second
	WriteIdleTimeout      = 30 * time.Second
	// MinPostgresVersion is the oldest supported server (predefined roles
	// such as pg_read_all_data arrived in 14), as server_version_num.
	MinPostgresVersion = 140000
)

// Engine errors, wrapped with details in plain words. Core maps them to API
// errors and statuses; anything else is unexpected (or, in Query, an error
// frame).
var (
	// ErrNotSetUp: the Dock roles don't exist or their login fails. Run
	// setup (again).
	ErrNotSetUp = errors.New("data access is not set up")
	// ErrUnsupported: the server is older than the engine supports.
	ErrUnsupported = errors.New("database version not supported")
	// ErrLoginFailed: the owner credentials were rejected during setup,
	// e.g. POSTGRES_PASSWORD changed after the volume was initialised.
	ErrLoginFailed = errors.New("owner login failed")
	// ErrUnreachable: dialing or the protocol handshake failed.
	ErrUnreachable = errors.New("database unreachable")
	// ErrNoTable: the table doesn't exist (or isn't visible to the role).
	ErrNoTable = errors.New("no such table")
	// ErrInvalid: the request doesn't fit the schema or the rules: unknown
	// column, bad cursor, filter value for is_null, several statements,
	// a change to a table that isn't editable or to a generated column.
	ErrInvalid = errors.New("invalid request")
	// ErrNoPrivilege: a Dock role lacks a privilege on a relation it
	// should read or write, typically one created by another role since
	// the last grants sync. Sync the grants (Engine.SyncGrants) and retry.
	ErrNoPrivilege = errors.New("missing privilege")
	// ErrCannotSetUp: the owner can log in but can't create roles
	// (neither superuser nor CREATEROLE), so setup would fail (Check).
	ErrCannotSetUp = errors.New("the owner can't create roles")
	// ErrFull: the server refused the login because every connection is
	// taken (SQLSTATE 53300).
	ErrFull = errors.New("every connection is taken")
)

// Role is which database role a call runs as.
type Role string

// Dock's roles inside a user's database. The owner role is only used by
// Setup and never reaches a cached connection.
const (
	RoleReader Role = "dock_reader"
	RoleWriter Role = "dock_writer"
	// RoleReaderMasked can select every column but the sensitive ones
	// (column grants, synced by Engine.SyncGrants).
	RoleReaderMasked Role = "dock_reader_masked"
	// RoleMonitor reads the database's statistics for insights: a member
	// of pg_monitor, with no table privileges, and only ever running SQL
	// compiled into the engine, never SQL a caller wrote.
	RoleMonitor Role = "dock_monitor"
)

// Roles names the roles a host creates in a user's database. Dock and
// tempoBase use different names (ADR 0024), so both can manage one
// database without rotating each other's passwords. Engines are built
// with one set (pg.Options.Roles) and keep calling them by Dock's names
// (RoleReader, ...) inside.
type Roles struct {
	Reader, Writer, ReaderMasked, Monitor Role
}

// DockRoles are Tempo Dock's roles; TempoBaseRoles are tempoBase's.
var (
	DockRoles      = Roles{Reader: RoleReader, Writer: RoleWriter, ReaderMasked: RoleReaderMasked, Monitor: RoleMonitor}
	TempoBaseRoles = Roles{Reader: "tempobase_reader", Writer: "tempobase_writer", ReaderMasked: "tempobase_reader_masked", Monitor: "tempobase_monitor"}
)

// Credentials log in to a database. They never leave the daemon: String
// leaves out the password and JSON leaves it out entirely.
type Credentials struct {
	User     string
	Password string `json:"-"`
}

func (c Credentials) String() string { return c.User }

// Target is one database, as the panel resolved it.
type Target struct {
	// ServiceID is the database's ID (Dock: the service's, tempoBase: the
	// connection's); it keys the connection cache and the schema cache.
	ServiceID int64
	// Dial opens a raw connection to the database: in Dock
	// node.Runtime.Dial bound to the service's running container and port,
	// in tempoBase a TCP dial of the connection's host. Engines use it for
	// every connection, including Postgres cancel requests.
	Dial func(ctx context.Context) (net.Conn, error)
	// TLS, when set, is negotiated over Dial's connection; nil is
	// plaintext (Dock's private networks). TLSOptional falls back to
	// plaintext when the server refuses TLS (sslmode=prefer).
	TLS         *tls.Config
	TLSOptional bool
	// Database is the database name to connect to.
	Database string
	// Reader and Writer are the Dock roles' credentials (stored encrypted
	// by core). Zero before setup.
	Reader Credentials
	Writer Credentials
	// ReaderMasked is RoleReaderMasked's login, for masked calls' SQL and
	// references. Zero: masked calls can't run SQL (pages and references
	// still work, masked by the engine).
	ReaderMasked Credentials
	// Monitor is RoleMonitor's login, for insights. Zero: no insights.
	Monitor Credentials
}

// Call is per-call context the engine needs besides the target.
type Call struct {
	// Actor names the caller for the database (application_name =
	// "dock:<actor>"), so pg_stat_activity shows who runs what.
	Actor string
	// Masked hides Sensitive columns: pages and references show them as
	// {"masked": true} and refuse to filter or sort by them, and SQL runs
	// as RoleReaderMasked, which can't select them.
	Masked bool
	// Sensitive is the database's sensitive columns, as an admin marked
	// them. Only used with Masked.
	Sensitive []apitypes.DataColumnRef
}

// ServerInfo describes a reachable database server.
type ServerInfo struct {
	// Version is the display version, e.g. "16.4"; VersionNum is
	// server_version_num, e.g. 160004.
	Version    string
	VersionNum int
}

// QueryStats is what a query did, for its audit detail.
type QueryStats struct {
	Rows     int64
	Duration time.Duration
	Outcome  apitypes.DataOutcome
}

// CommitResult is the outcome of applying a changeset. With conflicts,
// nothing was written.
type CommitResult struct {
	// SQL is the transaction as it ran (or would have), values inlined.
	SQL          string
	RowsAffected []int64
	Conflicts    []apitypes.DataConflict
	Duration     time.Duration
}

// Engine talks to databases of one kind: what every engine does, whatever
// its data model. Implementations are safe for concurrent use. Reads run
// as Reader, read-only and time-limited; Commit runs as Writer, all or
// nothing. A host registers its engines with the panel by Kind
// (datapanel.Options.Engines).
type Engine interface {
	// Kind is the engine this implements.
	Kind() apitypes.DataEngine

	// Check logs in as owner and reports the server without changing
	// anything: ErrUnsupported when it is too old, ErrCannotSetUp when the
	// owner can't create roles, ErrLoginFailed when the login is refused,
	// ErrUnreachable when the server can't be reached. ServerInfo is set
	// whenever the login succeeded.
	Check(ctx context.Context, t Target, owner Credentials) (ServerInfo, error)
	// Status logs in as Reader and reports the server. ErrNotSetUp when
	// the role is missing, its password doesn't match or it still has
	// the grants of an older setup; ErrUnsupported when the server is too
	// old.
	Status(ctx context.Context, t Target) (ServerInfo, error)
	// Setup creates Dock's roles with the owner's credentials, or rotates
	// their passwords when they exist, and drops cached connections and
	// schema for the target. reader and writer carry the new passwords,
	// generated by core. With t.ReaderMasked set it also creates or
	// rotates RoleReaderMasked, which can select nothing until
	// SyncGrants. Reader and Writer get their grants in the same
	// transaction.
	Setup(ctx context.Context, t Target, owner, reader, writer Credentials) (ServerInfo, error)
	// SyncGrants brings the roles' privileges in line with the schema and
	// the sensitive columns, as the owner, in one transaction: Reader
	// SELECT and Writer DML on every relation of every user schema (never
	// the system catalogs), RoleReaderMasked SELECT on every column but the
	// sensitive ones, and no membership in predefined roles. It returns
	// GrantsHash of the schema it synced for. Core runs it after Setup,
	// whenever the marks or the schema change, and when a call fails with
	// ErrNoPrivilege.
	SyncGrants(ctx context.Context, t Target, owner Credentials, sensitive []apitypes.DataColumnRef) (string, error)
	// Redact replaces the literal values in sql (strings, numbers) with ?
	// and drops comments, for showing statements to callers who must not
	// see the values (audit summaries, masked callers).
	Redact(sql string) string

	// Query runs one read-only statement and streams frames to send:
	// header, rows (at most apitypes.DataFrameRows each, encoded as they
	// are read), then end or error. Core has applied defaults and limits
	// to req. A returned error means nothing was sent (dial, login,
	// setup); once connected, database errors go out as an error frame
	// and are reflected in the stats. ctx ending cancels the query on the
	// server.
	Query(ctx context.Context, t Target, c Call, req apitypes.DataQueryRequest, send func(apitypes.DataQueryFrame) error) (QueryStats, error)

	// Render validates changes against the schema and renders the SQL a
	// commit would run, without touching data.
	Render(ctx context.Context, t Target, changes []apitypes.DataChange) (string, error)
	// Commit applies changes in one transaction as Writer. Each update and
	// delete matches the primary key and the Old values; any mismatch
	// rolls everything back and is reported in Conflicts.
	Commit(ctx context.Context, t Target, c Call, changes []apitypes.DataChange) (CommitResult, error)

	// Insights reads how the database is doing, as Monitor, for the
	// requested views (core has filled in defaults and checked the
	// caller's level per view), with hints from what it read. Texts are
	// as the server has them: the caller redacts them. ErrNotSetUp when
	// the monitor can't log in (EnsureMonitor creates it), ErrFull when
	// every connection is taken.
	Insights(ctx context.Context, t Target, c Call, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error)
	// Pressure reads the connection counts only, as Monitor: one cheap
	// query. A login refused for too many connections is Full, not an
	// error.
	Pressure(ctx context.Context, t Target) (apitypes.DataPressure, error)
	// EnableQueryStats creates the query stats extension (Postgres'
	// pg_stat_statements) as the owner, when the server loads it. It
	// reports the state after (QueryStatsNotLoaded when the server needs
	// a configuration change and a restart first) and whether it created
	// the extension just now.
	EnableQueryStats(ctx context.Context, t Target, owner Credentials) (state apitypes.DataQueryStatsState, created bool, err error)
	// ResetQueryStats sets the query stats back to zero, as the owner.
	// ErrInvalid when they are off.
	ResetQueryStats(ctx context.Context, t Target, owner Credentials) error
	// Cancel cancels a session's query (or with terminate ends the
	// session) as the owner, who can signal the app's sessions even when
	// they run as a superuser. session is an ID from Insights;
	// queryStartedAt, when set, must still be the query's start. Found is
	// false when the session ended (or ran another query) since.
	Cancel(ctx context.Context, t Target, owner Credentials, session string, queryStartedAt *time.Time, terminate bool) (CancelResult, error)

	// EnsureMonitor creates or updates Monitor with the owner's
	// credentials and tries to make it a member of pg_monitor; a server
	// that refuses that leaves it without (insights then can't see other
	// roles' statement texts, and say so).
	EnsureMonitor(ctx context.Context, t Target, owner Credentials) error

	// Forget drops cached connections and schema of a service (archived,
	// container replaced, roles rotated).
	Forget(serviceID int64)
	// Close closes every connection; the daemon calls it on shutdown.
	Close()
}

// Tables is an engine whose data are tables (Postgres): introspection,
// keyset pages and foreign key navigation. The panel's table operations
// refuse databases whose engine doesn't implement it.
type Tables interface {
	// Schema introspects the database with one catalog query, cached for
	// SchemaTTL. sensitive marks columns in the result.
	Schema(ctx context.Context, t Target, c Call, sensitive []apitypes.DataColumnRef) (apitypes.DataSchema, error)
	// Page reads one keyset page. Core has applied defaults and limits to
	// req.Limit. Next (and req.After) are the engine's own cursors, which
	// hold the last row's key values in the clear: core seals them before
	// they leave the daemon.
	Page(ctx context.Context, t Target, c Call, req apitypes.DataPageRequest) (apitypes.DataPage, error)
	// References reads the rows referencing one row, per incoming foreign
	// key. Masked calls run as RoleReaderMasked, can't look up a row by a
	// sensitive key and leave out foreign keys over sensitive columns.
	References(ctx context.Context, t Target, c Call, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error)
}

// Keys is an engine whose data are keys (Redis, Valkey): the keyspace,
// keys found with bounded SCAN steps, and one key's value a page at a
// time. The panel's key operations refuse engines without it.
type Keys interface {
	// Keyspace lists the logical databases and their key counts, in a
	// DataSchema without tables.
	Keyspace(ctx context.Context, t Target, c Call) (apitypes.DataSchema, error)
	// ScanKeys reads one page of keys. Next (and req.After) are the
	// engine's cursors; the panel seals them.
	ScanKeys(ctx context.Context, t Target, c Call, req apitypes.DataKeysRequest) (apitypes.DataKeyPage, error)
	// Key reads one key and a page of its value; ErrNoTable when the key
	// doesn't exist.
	Key(ctx context.Context, t Target, c Call, req apitypes.DataKeyRequest) (apitypes.DataKeyValue, error)
}

// CancelResult is the session a Cancel hit.
type CancelResult struct {
	Found       bool
	User        string
	Application string
	// Text is the session's statement as the server has it.
	Text    string
	Running time.Duration
}

// GrantsHash identifies what the roles' grants were synced for:
// every table's columns and which of them are sensitive, as Schema marks
// them. Core compares it with the hash SyncGrants returned to tell when
// the grants are out of date.
func GrantsHash(tables []apitypes.DataTable) string {
	h := sha256.New()
	for _, t := range tables {
		for _, c := range t.Columns {
			_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\n", t.Schema, t.Name, c.Name, c.Sensitive)
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
