// Package datapanel is the database panel's logic without a host (ADR
// 0024): the data access policy, setup of the panel's roles, grants
// syncs, sealed page cursors, reveals, changesets and the audit of data
// actions. Two hosts embed it: Tempo Dock (internal/core, where a
// database is a service) and tempoBase (internal/tempobase, where it is a
// saved connection).
//
// A host resolves a request to a Database (it checks the caller may see
// it and decides their permission with Decide), then calls the Panel.
// The Panel talks to the database through a data.Engine and keeps its
// state through the Host: setup records, sensitive columns, saved
// queries, changesets and audit events live in the host's store.
//
// The package must not import a host's packages (core, api, store, node,
// tempobase): tempoBase's binary links it without Dock's daemon.
package datapanel

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/secrets"
)

// Actor is the caller as the panel sees it: a name for the audit log and
// application_name, and IDs that key cursors, throttles and authorship.
// Hosts convert their own actor (core.Actor, tempobase.Actor) to it.
type Actor struct {
	// Kind is "user", "token" or "system".
	Kind string
	Name string
	// UserID and TokenID identify the caller; zero when not applicable. A
	// token acting for a user (Dock's `dock login` tokens) has its own
	// TokenID; SavedQueryOwner maps it to the user.
	UserID  int64
	TokenID int64
}

// Database is one database resolved for one call: the host has checked
// the caller may see it and decided their permission. D is the host's
// own handle (Dock: the service with its project and environment;
// tempoBase: the connection row), passed back to the Host untouched.
type Database[D any] struct {
	// ID is the host's stable ID for the database (service ID, connection
	// ID). It keys the engine's connections and schema cache, sealed
	// cursors, the browse throttle and every Host store call.
	ID int64
	// Name is how messages name it: "shop/staging/postgres", "orders".
	Name       string
	Engine     apitypes.DataEngine
	DetectedBy apitypes.DataDetection
	// ConfirmName is what a production commit must confirm with: the
	// environment's name in Dock, the connection's name in tempoBase.
	ConfirmName string
	// Perm is the caller's permission here, from Decide.
	Perm apitypes.DataPermission
	Host D
}

// Conn is how to reach a database right now, from Host.Connect.
type Conn struct {
	// Dial opens a raw connection: Dock dials the service's container
	// through its node runtime, tempoBase dials the connection's host and
	// port.
	Dial func(ctx context.Context) (net.Conn, error)
	// TLS, when set, is negotiated over Dial's connection. Dock leaves it
	// nil (a private bridge network); tempoBase sets it from the
	// connection's TLS mode.
	TLS *tls.Config
	// TLSOptional falls back to plaintext when the server refuses TLS
	// (libpq's sslmode=prefer).
	TLSOptional bool
	// Database is the database name to connect to.
	Database string
	// Owner logs in for setup and grants syncs only; it never reaches a
	// cached connection.
	Owner data.Credentials
}

// Words are the host's wording in the panel's messages.
type Words struct {
	// Product owns the roles in messages: "Dock" gives "Dock's roles".
	Product string
	// SetupHint says how to set up data access, after "data access is not
	// set up for X; ": "an admin can run `dock db setup`".
	SetupHint string
	// LoginFailedHint follows an owner login failure: Dock points at
	// database_url_variable, tempoBase at the connection's URL.
	LoginFailedHint string
	// SetupAction is how an admin sets up, in "Not set up. <SetupAction>
	// to create Dock's roles in the database.": "Set up data access
	// (`dock db setup`)".
	SetupAction string
	// ConfirmWhat is what a production commit confirms with: "the
	// environment's name", "the connection's name".
	ConfirmWhat string
	// Database is what a database is to the host, in API docs ("per user
	// and service"): "service", "connection".
	Database string
	// QueryStatsHint says how to make the server load pg_stat_statements,
	// after "the server doesn't load pg_stat_statements; ".
	QueryStatsHint string
	// EnableQueryStats ends the description of turning on query stats
	// with what else the host does and who may: "Admin."
	EnableQueryStats string
}

// Options configure a Panel.
type Options[D any] struct {
	Host Host[D]
	// Engines are the engines the host supports, one per Kind. A database
	// of another engine is refused with a validation error.
	Engines []data.Engine
	// Box seals role passwords and page cursors.
	Box *secrets.Box
	// Roles are the role names the panel creates and logs in as; they
	// must match the engine's.
	Roles  data.Roles
	Words  Words
	Logger *slog.Logger
}

// Stream delivers items until it ends, ctx is done or send fails. ready is
// called once, before the first send; an error before ready means nothing
// was sent. core.Stream is an alias of it, so Dock's core and the Panel
// return the same type.
type Stream[T any] func(ctx context.Context, ready func(), send func(...T) error) error

// Ops is the data panel as a host exposes it to its API and MCP endpoint:
// actor A asks about database R. Dock's *core.Core is
// Ops[core.Actor, core.ServiceRef]; tempoBase's host is
// Ops[tempobase.Actor, string] (the connection's name). The HTTP handlers
// and the MCP data tools are written against it.
type Ops[A, R any] interface {
	DataInfo(ctx context.Context, a A, ref R) (apitypes.DataInfo, error)
	SetupData(ctx context.Context, a A, ref R) (apitypes.DataInfo, error)
	DataSchema(ctx context.Context, a A, ref R) (apitypes.DataSchema, error)
	DataRows(ctx context.Context, a A, ref R, req apitypes.DataPageRequest) (apitypes.DataPage, error)
	DataReferences(ctx context.Context, a A, ref R, req apitypes.DataReferencesRequest) ([]apitypes.DataReferences, error)
	DataQuery(ctx context.Context, a A, ref R, req apitypes.DataQueryRequest) (Stream[apitypes.DataQueryFrame], error)
	DataHistory(ctx context.Context, a A, ref R, before int64, limit int) ([]apitypes.DataHistoryEntry, error)
	ListSavedQueries(ctx context.Context, a A, ref R) ([]apitypes.DataSavedQuery, error)
	SaveDataQuery(ctx context.Context, a A, ref R, id int64, req apitypes.SaveDataQueryRequest) (apitypes.DataSavedQuery, error)
	DeleteSavedQuery(ctx context.Context, a A, ref R, id int64) error
	ListDataChangesets(ctx context.Context, a A, ref R, status apitypes.DataChangesetStatus) ([]apitypes.DataChangeset, error)
	GetDataChangeset(ctx context.Context, a A, ref R, id int64) (apitypes.DataChangeset, error)
	CreateDataChangeset(ctx context.Context, a A, ref R, req apitypes.DataChangesetRequest) (apitypes.DataChangeset, error)
	CommitDataChangeset(ctx context.Context, a A, ref R, id int64, req apitypes.DataCommitRequest) (apitypes.DataChangeset, error)
	RejectDataChangeset(ctx context.Context, a A, ref R, id int64, req apitypes.DataRejectRequest) (apitypes.DataChangeset, error)
	SetSensitiveColumns(ctx context.Context, a A, ref R, req apitypes.SetSensitiveColumnsRequest) ([]apitypes.DataColumnRef, error)
	SetSensitiveKeys(ctx context.Context, a A, ref R, req apitypes.SetSensitiveKeysRequest) ([]string, error)
	DataInsights(ctx context.Context, a A, ref R, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error)
	DataKeys(ctx context.Context, a A, ref R, req apitypes.DataKeysRequest) (apitypes.DataKeyPage, error)
	DataKey(ctx context.Context, a A, ref R, req apitypes.DataKeyRequest) (apitypes.DataKeyValue, error)
	CancelDataSession(ctx context.Context, a A, ref R, session string, req apitypes.DataCancelRequest) (apitypes.DataCancelResult, error)
	ResetDataQueryStats(ctx context.Context, a A, ref R) error
	EnableDataQueryStats(ctx context.Context, a A, ref R) (apitypes.DataQueryStatsResult, error)
}

// Changeset is a stored changeset, decoded. Hosts store Changes,
// RowsAffected and Conflicts as JSON.
type Changeset struct {
	ID int64
	// Database is the Database.ID it belongs to.
	Database   int64
	Author     string
	AuthorKind string
	// UserID and TokenID are the author's (Actor's); zero when not set.
	UserID  int64
	TokenID int64
	Status  apitypes.DataChangesetStatus
	Message string
	Changes []apitypes.DataChange
	// KeyChanges are a key-value changeset's changes (Changes is empty).
	KeyChanges   []apitypes.DataKeyChange
	SQL          string
	RowsAffected []int64
	Conflicts    []apitypes.DataConflict
	Reason       string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	DecidedAt    *time.Time
	DecidedBy    string
}

// Decision moves a pending changeset to committed, conflict or rejected.
type Decision struct {
	Database     int64
	ID           int64
	Status       apitypes.DataChangesetStatus
	DecidedAt    time.Time
	DecidedBy    string
	RowsAffected []int64
	Conflicts    []apitypes.DataConflict
	Reason       string
}

// AccessColumns are the role passwords' columns in a host's data_access
// table, whose key column (named key) is the Database's ID, for the host's
// reseal (secrets.ResealColumn, ADR 0041). The Panel seals with their AADs.
func AccessColumns(key string) (reader, writer secrets.Column) {
	return secrets.Column{Table: "data_access", Name: "reader_secret", Key: []string{key}},
		secrets.Column{Table: "data_access", Name: "writer_secret", Key: []string{key}}
}

// accessAADs are the AADs of a database's role passwords.
func accessAADs(id int64) (reader, writer secrets.AAD) {
	r, w := AccessColumns("")
	key := strconv.FormatInt(id, 10)
	return r.AAD(key), w.AAD(key)
}

// Access is a database's setup record: the roles' passwords, sealed with
// Options.Box, and what their grants were last synced for.
type Access struct {
	Engine           apitypes.DataEngine
	ServerVersion    string
	ReaderSecret     []byte
	WriterSecret     []byte
	GrantsSyncedHash string
	SetUpAt          time.Time
	SetUpBy          string
}

// AuditEntry is one data audit event (apitypes.AuditData* actions). Its
// summary never carries values: everyone who may read the host's audit
// log sees it.
type AuditEntry struct {
	Action  string
	Summary string
	// Detail is set for data.query and data.commit: the full SQL and what
	// it did, stored in the host's detail row.
	Detail *AuditDetail
}

// AuditDetail is the detail row of a data.query or data.commit event.
type AuditDetail struct {
	SQL      string
	Rows     int64
	Duration time.Duration
	Outcome  apitypes.DataOutcome
}

// Host is what the Panel needs from the product it runs in: how to reach
// a database, and a store for the panel's state and audit events. Store
// methods take the Database.ID; Connect, Audit and QueryHistory take the
// host's handle. Implementations are safe for concurrent use and run a
// fixed number of queries per call.
type Host[D any] interface {
	// Connect says how to reach the database now. It returns an error
	// wrapping ErrNotRunning when there is nothing to dial (a stopped
	// service), and a validation Error when the connection details can't
	// be derived (an unparsable URL); both messages say what to do.
	Connect(ctx context.Context, db D) (Conn, error)

	// Access returns the setup record, nil before setup.
	Access(ctx context.Context, id int64) (*Access, error)
	// PutAccess stores the setup record, replacing any.
	PutAccess(ctx context.Context, id int64, acc Access) error
	// SetGrantsHash records what the roles' grants were synced for.
	SetGrantsHash(ctx context.Context, id int64, hash string) error

	// SensitiveColumns lists the columns an admin marked sensitive.
	SensitiveColumns(ctx context.Context, id int64) ([]apitypes.DataColumnRef, error)
	// ReplaceSensitiveColumns replaces them in one transaction.
	ReplaceSensitiveColumns(ctx context.Context, id int64, cols []apitypes.DataColumnRef) error
	// SensitiveKeys lists a key-value database's sensitive key patterns;
	// ReplaceSensitiveKeys replaces them in one transaction. A host
	// without key-value databases returns none and refuses to store any.
	SensitiveKeys(ctx context.Context, id int64) ([]string, error)
	ReplaceSensitiveKeys(ctx context.Context, id int64, patterns []string) error

	// SavedQueryOwner is the user whose saved queries the actor uses: the
	// user, or the user a token acts for; 0 when there is none (agent
	// tokens), which the Panel refuses with a generic message. A host may
	// return its own error instead, saying how its callers get a user.
	SavedQueryOwner(ctx context.Context, a Actor) (int64, error)
	// SavedQueries lists a user's saved queries for the database, by name.
	SavedQueries(ctx context.Context, id, user int64) ([]apitypes.DataSavedQuery, error)
	// SaveQuery creates (queryID 0) or replaces a saved query. It returns
	// ErrDuplicate when the user has another one with that name and
	// ErrNotFound for an unknown queryID.
	SaveQuery(ctx context.Context, id, user, queryID int64, name, sql string, at time.Time) (apitypes.DataSavedQuery, error)
	// DeleteSavedQuery deletes one; false when it didn't exist.
	DeleteSavedQuery(ctx context.Context, id, user, queryID int64) (bool, error)

	// CreateChangeset stores a pending changeset and returns it with its ID.
	CreateChangeset(ctx context.Context, cs Changeset) (Changeset, error)
	// Changeset loads one of the database's changesets; ErrNotFound if it
	// has none with that ID.
	Changeset(ctx context.Context, id, changesetID int64) (Changeset, error)
	// Changesets lists the database's changesets, newest first: the
	// pending ones, or the latest limit in any status.
	Changesets(ctx context.Context, id int64, pendingOnly bool, limit int) ([]Changeset, error)
	// DecideChangeset records a decision on a pending changeset;
	// ErrNotFound when it is no longer pending.
	DecideChangeset(ctx context.Context, d Decision) (Changeset, error)
	// ExpireChangesets marks every pending changeset past its expiry as
	// expired. The Panel calls it before reading changesets: nothing
	// expires them on a timer.
	ExpireChangesets(ctx context.Context, now time.Time) error

	// Audit records a data event about db, with its detail row in the same
	// transaction. Failures are the host's to log; audit never fails a
	// call that already reached the database.
	Audit(ctx context.Context, a Actor, db D, e AuditEntry)
	// QueryHistory returns the actor's own data.query events on db, newest
	// first, with audit IDs below before (0: from the newest).
	QueryHistory(ctx context.Context, a Actor, db D, before int64, limit int) ([]apitypes.DataHistoryEntry, error)
}
