// Package data is the engine-neutral side of the database panel (ADR 0016).
//
// Core decides who may do what and resolves a service to a Target; an
// Engine (internal/data/pg for Postgres) talks to the database. Engines
// know nothing about actors, permissions or the store, and core knows
// nothing about SQL drivers.
//
// Idle means idle: an engine holds no connections, goroutines or timers
// while nobody uses the panel. Connections are opened on first use and
// closed IdleTimeout after their last use.
package data

import (
	"context"
	"crypto/sha256"
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
)

// Role is which database role a call runs as.
type Role string

// Dock's roles inside a user's database. The owner role is only used by
// Setup and never reaches a cached connection.
const (
	RoleReader Role = "dock_reader"
	RoleWriter Role = "dock_writer"
	// RoleReaderMasked can select every column but the sensitive ones
	// (column grants, synced by Engine.SyncMasked).
	RoleReaderMasked Role = "dock_reader_masked"
)

// Credentials log in to a database. They never leave the daemon: String
// leaves out the password and JSON leaves it out entirely.
type Credentials struct {
	User     string
	Password string `json:"-"`
}

func (c Credentials) String() string { return c.User }

// Target is one service's database, as core resolved it.
type Target struct {
	// ServiceID keys the connection cache and the schema cache.
	ServiceID int64
	// Dial opens a raw connection to the database: node.Runtime.Dial bound
	// to the service's running container and port. Engines use it for
	// every connection, including Postgres cancel requests.
	Dial func(ctx context.Context) (net.Conn, error)
	// Database is the database name to connect to.
	Database string
	// Reader and Writer are the Dock roles' credentials (stored encrypted
	// by core). Zero before setup.
	Reader Credentials
	Writer Credentials
	// ReaderMasked is RoleReaderMasked's login, for masked calls' SQL.
	// Zero: masked calls can't run SQL (pages and references still work,
	// masked by the engine).
	ReaderMasked Credentials
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

// Engine talks to databases of one kind. Implementations are safe for
// concurrent use. Reads run as Reader in a READ ONLY transaction with a
// statement timeout; Commit runs as Writer in one transaction.
type Engine interface {
	// Kind is the engine this implements.
	Kind() apitypes.DataEngine

	// Status logs in as Reader and reports the server. ErrNotSetUp when
	// the role is missing or its password doesn't match, ErrUnsupported
	// when the server is too old.
	Status(ctx context.Context, t Target) (ServerInfo, error)
	// Setup creates Dock's roles with the owner's credentials, or rotates
	// their passwords when they exist, and drops cached connections and
	// schema for the target. reader and writer carry the new passwords,
	// generated by core. With t.ReaderMasked set it also creates or
	// rotates RoleReaderMasked, which can select nothing until SyncMasked.
	Setup(ctx context.Context, t Target, owner, reader, writer Credentials) (ServerInfo, error)
	// SyncMasked grants RoleReaderMasked SELECT on every column but the
	// sensitive ones (revoking everything else) as the owner, and returns
	// MaskedHash of the schema it synced for. Core runs it after Setup and
	// whenever the marks or the schema change.
	SyncMasked(ctx context.Context, t Target, owner Credentials, sensitive []apitypes.DataColumnRef) (string, error)

	// Schema introspects the database with one catalog query, cached for
	// SchemaTTL. sensitive marks columns in the result.
	Schema(ctx context.Context, t Target, c Call, sensitive []apitypes.DataColumnRef) (apitypes.DataSchema, error)
	// Page reads one keyset page. Core has applied defaults and limits to
	// req.Limit.
	Page(ctx context.Context, t Target, c Call, req apitypes.DataPageRequest) (apitypes.DataPage, error)
	// References reads the rows referencing one row, per incoming foreign
	// key.
	References(ctx context.Context, t Target, c Call, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error)
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

	// Forget drops cached connections and schema of a service (archived,
	// container replaced, roles rotated).
	Forget(serviceID int64)
	// Close closes every connection; the daemon calls it on shutdown.
	Close()
}

// MaskedHash identifies what RoleReaderMasked's grants were synced for:
// every table's columns and which of them are sensitive, as Schema marks
// them. Core compares it with the hash SyncMasked returned to tell when
// the grants are out of date.
func MaskedHash(tables []apitypes.DataTable) string {
	h := sha256.New()
	for _, t := range tables {
		for _, c := range t.Columns {
			_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%t\n", t.Schema, t.Name, c.Name, c.Sensitive)
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}
