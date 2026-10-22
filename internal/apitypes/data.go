package apitypes

import (
	"encoding/json"
	"time"
)

// Database panel (ADR 0016). Endpoints live under
// /projects/{p}/environments/{e}/services/{s}/data; policy endpoints at
// /data-policy, /projects/{p}/data-policy and
// /projects/{p}/environments/{e}/data-policy.

// Limits of the database panel. Hitting a row or byte cap ends a result
// with Truncated set; it is never an error.
const (
	// DataPageLimit is the default rows per table page; DataPageMaxLimit
	// the most a page request may ask for.
	DataPageLimit    = 100
	DataPageMaxLimit = 1000
	// DataQueryLimit is the default row cap of a SQL query (the UI's).
	// The CLI sends its own default of 1 000; DataQueryMaxLimit is the
	// most anyone may ask for.
	DataQueryLimit    = 10_000
	DataQueryMaxLimit = 100_000
	// DataQueryMaxBytes caps one query response.
	DataQueryMaxBytes = 64 << 20
	// DataCellMaxBytes is the size above which a cell is truncated.
	DataCellMaxBytes = 8 << 10
	// DataFrameRows is the most rows one NDJSON rows frame carries.
	DataFrameRows = 500
	// DataSQLMaxBytes caps the SQL text of a query or saved query.
	DataSQLMaxBytes = 1 << 20
	// DataQueryTimeout is the default statement timeout of reads, and
	// DataQueryMaxTimeout the most a request may ask for (CLI --timeout).
	DataQueryTimeout    = 10 * time.Second
	DataQueryMaxTimeout = 60 * time.Second
	// DataChangesetMaxRows is the most changes (rows) in one changeset.
	DataChangesetMaxRows = 500
	// DataChangesetExpiry is how long a pending changeset waits for a
	// decision before it expires.
	DataChangesetExpiry = 7 * 24 * time.Hour
	// DataReferencesLimit is the default referencing rows returned per
	// foreign key; DataReferencesMaxLimit the most a request may ask for.
	DataReferencesLimit    = 10
	DataReferencesMaxLimit = 100
	// AuditSQLMaxBytes is how much SQL an audit detail keeps.
	AuditSQLMaxBytes = 16 << 10
)

// Audit actions of the database panel. Query history is the caller's own
// AuditDataQuery events.
const (
	AuditDataQuery   = "data.query"   // each SQL statement; detail: SQL, rows, duration, outcome
	AuditDataBrowse  = "data.browse"  // first table page per actor and table, then at most every 10 min
	AuditDataCommit  = "data.commit"  // each committed changeset; detail: generated SQL, rows
	AuditDataPropose = "data.propose" // changeset created as a proposal
	AuditDataApprove = "data.approve" // an admin committed someone else's changeset
	AuditDataReject  = "data.reject"
	AuditDataReveal  = "data.reveal" // sensitive values revealed in a page; throttled like data.browse
	AuditDataSetup   = "data.setup"  // Dock roles created or rotated
	AuditDataPolicy  = "data.policy" // a data access policy or token override changed
)

// DataEngine is a database engine the panel supports.
type DataEngine string

// Supported engines. MySQL/MariaDB and Redis come later.
const EnginePostgres DataEngine = "postgres"

// DatabaseSetting is a service's `database` setting: whether it is a
// database, overriding detection. It applies immediately and never creates
// a release.
type DatabaseSetting string

// Database settings.
const (
	// DatabaseDetect detects the engine from the image (the default).
	DatabaseDetect DatabaseSetting = ""
	// DatabasePostgres marks the service as Postgres (templates set it).
	DatabasePostgres DatabaseSetting = "postgres"
	// DatabaseNone marks the service as not a database.
	DatabaseNone DatabaseSetting = "none"
)

// UpdateDatabaseSettingsRequest changes a service's Dock-side database
// settings (PUT /services/{s}/database-settings). Only fields that are set
// change. They apply immediately: never staged, never a release.
type UpdateDatabaseSettingsRequest struct {
	Database *DatabaseSetting `json:"database,omitempty" enum:",postgres,none" doc:"Override database detection; empty detects from the image"`
	// DatabaseURLVariable names the variable with the connection URL:
	// DATABASE_URL, or another service's as api.DATABASE_URL. Empty uses
	// the service's own DATABASE_URL, then the POSTGRES_* variables.
	DatabaseURLVariable *string `json:"database_url_variable,omitempty"`
}

// DataLevel is how much of a database's data a subject may use. Each level
// includes the ones before it: none < schema < read < propose < write.
type DataLevel string

// Data access levels.
const (
	LevelNone DataLevel = "none"
	// LevelSchema allows tables, columns and relations; no rows.
	LevelSchema DataLevel = "schema"
	// LevelRead allows browsing and read-only SQL.
	LevelRead DataLevel = "read"
	// LevelPropose adds drafting changesets that an admin approves.
	LevelPropose DataLevel = "propose"
	// LevelWrite adds committing one's own changesets. Tokens never write
	// production directly: there, write acts as propose.
	LevelWrite DataLevel = "write"
)

func (l DataLevel) rank() int {
	switch l {
	case LevelSchema:
		return 1
	case LevelRead:
		return 2
	case LevelPropose:
		return 3
	case LevelWrite:
		return 4
	}
	return 0
}

// Includes reports whether l allows everything other allows.
func (l DataLevel) Includes(other DataLevel) bool { return l.rank() >= other.rank() }

// Min returns the lower of l and other.
func (l DataLevel) Min(other DataLevel) DataLevel {
	if l.rank() <= other.rank() {
		return l
	}
	return other
}

// DataSensitivity is how a subject sees columns marked sensitive.
type DataSensitivity string

// Sensitive-column visibility.
const (
	// SensitiveMasked shows {"masked": true} and never the value. Masked
	// callers' SQL runs as the panel's masked read-only role, which
	// Postgres denies the sensitive columns, so selecting one is an error frame.
	SensitiveMasked DataSensitivity = "masked"
	// SensitiveVisible shows values after an explicit, audited reveal:
	// table pages and references mask them like for masked callers unless
	// the page request sets Reveal, which is audited as data.reveal. SQL
	// and changesets aren't masked: each statement is audited with its
	// full text.
	SensitiveVisible DataSensitivity = "visible"
)

// DataSubject is who a data policy rule applies to. Admins are not a
// subject: they can always do everything (level write, sensitive visible).
type DataSubject string

// Data policy subjects.
const (
	SubjectReadUsers   DataSubject = "role:read"
	SubjectDeployUsers DataSubject = "role:deploy"
	// SubjectTokens is every API token without its own override.
	SubjectTokens DataSubject = "tokens"
)

// DataPolicyScope is where a data policy is set. The most specific one
// wins per subject and environment kind: environment (tempoBase:
// connection), then project, then default, then the built-in defaults.
type DataPolicyScope string

// Data policy scopes.
const (
	PolicyBuiltIn     DataPolicyScope = "built_in"
	PolicyDefault     DataPolicyScope = "default"
	PolicyProject     DataPolicyScope = "project"
	PolicyEnvironment DataPolicyScope = "environment"
	// PolicyConnection is tempoBase's per-connection scope (ADR 0024);
	// Dock never uses it.
	PolicyConnection DataPolicyScope = "connection"
)

// DataPolicyRule sets one subject's access to databases in environments of
// one kind. Built-in defaults: role:read none everywhere; role:deploy read
// on non-production and none on production; tokens none everywhere; all of
// them masked.
type DataPolicyRule struct {
	Subject DataSubject `json:"subject" enum:"role:read,role:deploy,tokens"`
	// Kind is the environment kind the rule covers. In an environment's
	// policy it must be that environment's kind.
	Kind      EnvironmentKind `json:"kind" enum:"production,non_production"`
	Level     DataLevel       `json:"level" enum:"none,schema,read,propose,write"`
	Sensitive DataSensitivity `json:"sensitive" enum:"masked,visible"`
	// From says which policy the rule comes from. Only set in
	// DataPolicy.Effective.
	From DataPolicyScope `json:"from,omitempty" enum:"built_in,default,project,environment,connection" readOnly:"true"`
}

// DataPolicy is the data access policy at one scope.
type DataPolicy struct {
	Scope       DataPolicyScope `json:"scope" enum:"default,project,environment,connection"`
	Project     string          `json:"project,omitempty"`
	Environment string          `json:"environment,omitempty"`
	// Connection names tempoBase's connection for scope connection; Dock
	// never sets it.
	Connection string `json:"connection,omitempty"`
	// Rules are the rules set at this scope; anything not covered is
	// inherited.
	Rules []DataPolicyRule `json:"rules"`
	// Effective is the resolved rule for every subject and kind (for an
	// environment: its own kind only), with From saying where each comes
	// from.
	Effective []DataPolicyRule `json:"effective"`
}

// UpdateDataPolicyRequest replaces the rules set at one scope. At most one
// rule per subject and kind; no rules means inherit everything. Admin only.
type UpdateDataPolicyRequest struct {
	Rules []DataPolicyRule `json:"rules"`
}

// DataTokenAccess is a token's own data access, overriding the policy's
// tokens rule. It is capped by what the policy allows users with the
// token's scope and by its creator's current role, so a token never gets
// more than a person in its place would.
type DataTokenAccess struct {
	// Nullable in the schema, so UpdateTokenRequest can send data: null.
	_             struct{}        `nullable:"true"`
	Production    DataLevel       `json:"production" enum:"none,schema,read,propose,write"`
	NonProduction DataLevel       `json:"non_production" enum:"none,schema,read,propose,write"`
	Sensitive     DataSensitivity `json:"sensitive" enum:"masked,visible"`
}

// UpdateTokenRequest changes a token. Data replaces its data access
// override; null removes it, so the token follows the policy again.
type UpdateTokenRequest struct {
	Data *DataTokenAccess `json:"data"`
}

// DataPermissionSource is what decided one step of a DataPermission.
type DataPermissionSource string

// Data permission sources.
const (
	// PermissionAdmin: admins can do everything.
	PermissionAdmin DataPermissionSource = "admin"
	// PermissionPolicy: a policy rule (Step.Policy says which).
	PermissionPolicy DataPermissionSource = "policy"
	// PermissionToken: the token's own override.
	PermissionToken DataPermissionSource = "token"
	// PermissionCap: lowered to what the token's scope or creator allows.
	PermissionCap DataPermissionSource = "cap"
	// PermissionProduction: tokens only propose on production.
	PermissionProduction DataPermissionSource = "production"
)

// DataPermission is what the caller may do with one database, and why.
type DataPermission struct {
	Level     DataLevel       `json:"level" enum:"none,schema,read,propose,write"`
	Sensitive DataSensitivity `json:"sensitive" enum:"masked,visible"`
	// Approve: may commit or reject other people's changesets (admins).
	Approve bool `json:"approve"`
	// Manage: may set up data access, mark sensitive columns and change
	// the policy (admins).
	Manage bool `json:"manage"`
	// ConfirmCommit: the environment or connection is production, so
	// commits must send its name as confirm.
	ConfirmCommit bool `json:"confirm_commit"`
	// Why lists the steps that decided Level and Sensitive, in order.
	Why []DataPermissionStep `json:"why"`
}

// DataPermissionStep is one step in deciding a DataPermission.
type DataPermissionStep struct {
	Source DataPermissionSource `json:"source" enum:"admin,policy,token,cap,production"`
	// Policy and Subject name the rule, for policy steps.
	Policy    DataPolicyScope `json:"policy,omitempty" enum:"built_in,default,project,environment,connection"`
	Subject   DataSubject     `json:"subject,omitempty" enum:"role:read,role:deploy,tokens"`
	Level     DataLevel       `json:"level" enum:"none,schema,read,propose,write"`
	Sensitive DataSensitivity `json:"sensitive" enum:"masked,visible"`
	// Detail says it in words, e.g. "project shop: role:deploy may read
	// non-production".
	Detail string `json:"detail"`
}

// DataStatus is whether the panel can use a database right now.
type DataStatus string

// Data statuses.
const (
	DataReady DataStatus = "ready"
	// DataSetupNeeded: Dock's roles don't exist (or their login fails);
	// an admin runs "Set up data access" (`dock db setup`).
	DataSetupNeeded DataStatus = "setup_needed"
	// DataUnsupported: the server version is too old (Postgres < 14).
	DataUnsupported DataStatus = "unsupported"
	// DataUnreachable: the container runs but the database can't be
	// reached or logged in to; Reason says what to do.
	DataUnreachable DataStatus = "unreachable"
	// DataNotRunning: the service has no running container.
	DataNotRunning DataStatus = "not_running"
)

// DataDetection is how a service was found to be a database.
type DataDetection string

// Detection sources: the `database` setting, or the image name.
const (
	DetectedBySetting DataDetection = "setting"
	DetectedByImage   DataDetection = "image"
)

// DataInfo is a database service's panel state (GET /data). Services that
// aren't databases are not_found.
type DataInfo struct {
	Engine     DataEngine    `json:"engine" enum:"postgres"`
	DetectedBy DataDetection `json:"detected_by" enum:"setting,image"`
	Status     DataStatus    `json:"status" enum:"ready,setup_needed,unsupported,unreachable,not_running"`
	// Reason explains any status but ready and says what to do.
	Reason string `json:"reason,omitempty"`
	// ServerVersion is e.g. "16.4", once the database was reached.
	ServerVersion string     `json:"server_version,omitempty"`
	SetUpAt       *time.Time `json:"set_up_at,omitempty"`
	SetUpBy       string     `json:"set_up_by,omitempty"`
	// UsedBy names services whose variables point at this database.
	UsedBy []string       `json:"used_by"`
	Access DataPermission `json:"access"`
}

// DataTableKind is what kind of relation a table is.
type DataTableKind string

// Table kinds. Only plain and partitioned tables with a primary key are
// editable.
const (
	TableKindTable       DataTableKind = "table"
	TableKindPartitioned DataTableKind = "partitioned_table"
	TableKindView        DataTableKind = "view"
	TableKindMatView     DataTableKind = "materialized_view"
	TableKindForeign     DataTableKind = "foreign_table"
)

// DataSchema is a database's structure (GET /data/schema), from one catalog
// query cached for 60 s. Tables are sorted by schema, then name; system
// schemas are left out.
type DataSchema struct {
	Tables []DataTable `json:"tables"`
	// ReadAt is when the catalog was read.
	ReadAt time.Time `json:"read_at"`
}

// DataTable is one table, view or similar.
type DataTable struct {
	Schema string        `json:"schema" example:"public"`
	Name   string        `json:"name" example:"orders"`
	Kind   DataTableKind `json:"kind" enum:"table,partitioned_table,view,materialized_view,foreign_table"`
	// EstimatedRows is Postgres' estimate (pg_class.reltuples); -1 when
	// the table was never analysed.
	EstimatedRows int64 `json:"estimated_rows"`
	// ExactRows is set for a few never-analysed tables (the smallest),
	// counted with COUNT(*) when the catalog is read if that finished
	// within 200 ms. Unset while EstimatedRows is -1: unknown.
	ExactRows *int64       `json:"exact_rows,omitempty"`
	SizeBytes int64        `json:"size_bytes"`
	Comment   string       `json:"comment,omitempty"`
	Columns   []DataColumn `json:"columns"`
	// PrimaryKey lists the key's columns in order. Empty means pages go by
	// ctid and the table is read-only.
	PrimaryKey  []string         `json:"primary_key"`
	ForeignKeys []DataForeignKey `json:"foreign_keys"`
	Indexes     []DataIndex      `json:"indexes"`
	// Constraints are check, unique and exclusion constraints.
	Constraints []DataConstraint `json:"constraints"`
	// Editable: a table (not a view) with a primary key.
	Editable bool `json:"editable"`
}

// DataColumn is one column of a table.
type DataColumn struct {
	Name string `json:"name"`
	// Type is the declared type as \d shows it, e.g. "numeric(10,2)";
	// TypeName the base type's name, e.g. "numeric", "int8", "_text".
	Type     string `json:"type"`
	TypeName string `json:"type_name"`
	Nullable bool   `json:"nullable"`
	// Default is the default expression, if any.
	Default string `json:"default,omitempty"`
	// Generated columns (GENERATED ALWAYS) are never editable.
	Generated bool   `json:"generated"`
	Sensitive bool   `json:"sensitive"`
	Comment   string `json:"comment,omitempty"`
}

// DataForeignKey is an outgoing foreign key: Columns reference RefColumns
// of RefSchema.RefTable.
type DataForeignKey struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	RefSchema  string   `json:"ref_schema"`
	RefTable   string   `json:"ref_table"`
	RefColumns []string `json:"ref_columns"`
}

// DataIndex is one index.
type DataIndex struct {
	Name    string `json:"name"`
	Unique  bool   `json:"unique"`
	Primary bool   `json:"primary"`
	// Definition is the CREATE INDEX statement (pg_get_indexdef).
	Definition string `json:"definition"`
}

// DataConstraintKind is the kind of a constraint in DataTable.Constraints.
type DataConstraintKind string

// Constraint kinds.
const (
	ConstraintCheck     DataConstraintKind = "check"
	ConstraintUnique    DataConstraintKind = "unique"
	ConstraintExclusion DataConstraintKind = "exclusion"
)

// DataConstraint is one constraint besides keys.
type DataConstraint struct {
	Name string             `json:"name"`
	Kind DataConstraintKind `json:"kind" enum:"check,unique,exclusion"`
	// Definition is pg_get_constraintdef, e.g. "CHECK (total >= 0)".
	Definition string `json:"definition"`
}

// DataFilterOp is a filter operator.
type DataFilterOp string

// Filter operators. is_null and not_null take no value; in takes Values.
const (
	FilterEq      DataFilterOp = "eq"
	FilterNe      DataFilterOp = "ne"
	FilterLt      DataFilterOp = "lt"
	FilterLte     DataFilterOp = "lte"
	FilterGt      DataFilterOp = "gt"
	FilterGte     DataFilterOp = "gte"
	FilterLike    DataFilterOp = "like"
	FilterILike   DataFilterOp = "ilike"
	FilterIn      DataFilterOp = "in"
	FilterIsNull  DataFilterOp = "is_null"
	FilterNotNull DataFilterOp = "not_null"
)

// DataFilter is one structured condition, compiled to parameterised SQL.
// Filters in a request are ANDed. Free-form filtering is the SQL editor.
type DataFilter struct {
	Column string       `json:"column"`
	Op     DataFilterOp `json:"op" enum:"eq,ne,lt,lte,gt,gte,like,ilike,in,is_null,not_null"`
	// Value is in Postgres text input format (all ops but in, is_null,
	// not_null).
	Value string `json:"value,omitempty"`
	// Values are for in, at most DataPageMaxLimit of them.
	Values []string `json:"values,omitempty"`
}

// DataPageRequest asks for one keyset page of a table
// (GET /data/tables/{table}/rows, table as "schema.name"). Over HTTP,
// order, after, limit, count, full and reveal are query parameters and
// filter is a JSON array of DataFilter.
type DataPageRequest struct {
	// Table is "schema.name".
	Table string `json:"table"`
	// Order is a column to sort by, "-column" for descending. The primary
	// key (or ctid) is always appended, so order is total. Empty: primary
	// key ascending.
	Order   string       `json:"order,omitempty"`
	Filters []DataFilter `json:"filters,omitempty"`
	// After is the Next cursor of the previous page. Cursors are opaque
	// and only valid with the same table, order and filters.
	After string `json:"after,omitempty"`
	// Limit is rows per page: 0 means DataPageLimit, at most
	// DataPageMaxLimit.
	Limit int `json:"limit,omitempty"`
	// Count asks for an exact row count (under the statement timeout).
	Count bool `json:"count,omitempty"`
	// Full returns cells untruncated, for the row detail view. Only with
	// limit 1.
	Full bool `json:"full,omitempty"`
	// Reveal shows the table's sensitive columns, which pages mask even
	// for callers with sensitive visible. Only for those callers (others
	// get forbidden); audited as data.reveal, at most every 10 min per
	// caller and table.
	Reveal bool `json:"reveal,omitempty"`
}

// DataResultColumn is one column of a page or query result.
type DataResultColumn struct {
	Name string `json:"name"`
	// TypeName is the type's name, e.g. "int4", "timestamptz", "_text".
	TypeName string `json:"type_name"`
}

// DataRow is one result row: a JSON array with one cell per column, in
// column order. A cell is:
//   - null for SQL NULL;
//   - a JSON bool for bool;
//   - a JSON number for int2, int4, oid, float4 and float8 (NaN and
//     ±Infinity are strings);
//   - a string in Postgres text output format for everything else,
//     including int8 and numeric (exact), timestamps, uuid, json/jsonb (the
//     JSON text), arrays ({1,2}) and bytea (\x hex);
//   - {"truncated": "<first DataCellMaxBytes bytes>", "bytes": <full size>}
//     for a longer value (DataPageRequest.Full fetches all of it);
//   - {"masked": true} for a sensitive column the caller can't see or
//     hasn't revealed (DataPageRequest.Reveal).
//
// Values sent to the server (filters, keys, changesets) are strings in
// Postgres text input format, or null; Postgres casts them to the column
// type. The engine encodes rows as it reads them, so a DataRow is raw JSON.
type DataRow = json.RawMessage

// DataPage is one keyset page of a table.
type DataPage struct {
	Columns []DataResultColumn `json:"columns"`
	Rows    []DataRow          `json:"rows"`
	// Next is the cursor of the following page; empty on the last one.
	Next          string `json:"next,omitempty"`
	EstimatedRows int64  `json:"estimated_rows"`
	// ExactRows is set when the request asked for Count and counting
	// finished within the statement timeout.
	ExactRows *int64 `json:"exact_rows,omitempty"`
}

// DataReferencesRequest asks for the rows that reference one row through
// incoming foreign keys (GET /data/tables/{table}/references; key is a
// JSON object query parameter).
type DataReferencesRequest struct {
	Table string `json:"table"`
	// Key is the referenced row's primary key, column to value.
	Key map[string]string `json:"key"`
	// Limit is rows per foreign key: 0 means DataReferencesLimit, at most
	// DataReferencesMaxLimit.
	Limit int `json:"limit,omitempty"`
}

// DataReferences is one incoming foreign key and the rows that use it.
type DataReferences struct {
	Constraint string   `json:"constraint"`
	Schema     string   `json:"schema"`
	Table      string   `json:"table"`
	Columns    []string `json:"columns"`
	// Filters open the referencing table on exactly these rows.
	Filters []DataFilter `json:"filters"`
	// Page holds the first rows; Next is set when there are more.
	Page DataPage `json:"page"`
}

// DataQueryRequest runs one read-only SQL statement (POST /data/query) as
// the panel's read-only role in a READ ONLY transaction. Several statements in one string
// are rejected. The response is NDJSON (application/x-ndjson) of
// DataQueryFrame.
type DataQueryRequest struct {
	SQL string `json:"sql" minLength:"1" maxLength:"1048576"`
	// Limit caps rows: 0 means DataQueryLimit, at most DataQueryMaxLimit.
	Limit int `json:"limit,omitempty" minimum:"0" maximum:"100000"`
	// TimeoutSeconds is the statement timeout: 0 means DataQueryTimeout,
	// at most DataQueryMaxTimeout.
	TimeoutSeconds int `json:"timeout_seconds,omitempty" minimum:"0" maximum:"60"`
}

// DataFrameType is the kind of an NDJSON query frame.
type DataFrameType string

// Query frames come in this order: one header, rows frames, then exactly
// one end or error frame, which ends the stream. Permission and validation
// errors are normal problem responses before any frame; anything Postgres
// reports is an error frame, possibly after rows.
const (
	FrameHeader DataFrameType = "header"
	FrameRows   DataFrameType = "rows"
	FrameEnd    DataFrameType = "end"
	FrameError  DataFrameType = "error"
)

// DataQueryFrame is one line of a query's NDJSON response. The fields set
// depend on Type.
type DataQueryFrame struct {
	Type DataFrameType `json:"type" enum:"header,rows,end,error"`
	// Columns (header).
	Columns []DataResultColumn `json:"columns,omitempty"`
	// Rows (rows): up to DataFrameRows.
	Rows []DataRow `json:"rows,omitempty"`
	// RowCount, Truncated, Command and DurationMS (end). Truncated means
	// a row or byte cap ended the result early.
	RowCount   int64  `json:"row_count,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	Command    string `json:"command,omitempty" example:"SELECT 120"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Error (error).
	Error *DataQueryError `json:"error,omitempty"`
}

// DataQueryError is an error reported by the database.
type DataQueryError struct {
	Message string `json:"message"`
	// SQLState is the Postgres error code, e.g. "42P01"; "57014" is a
	// statement timeout or cancel.
	SQLState string `json:"sqlstate,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Hint     string `json:"hint,omitempty"`
	// Position is the 1-based character in the SQL the error points at,
	// 0 when none.
	Position int `json:"position,omitempty"`
}

// DataChangeOp is what one change does to a row.
type DataChangeOp string

// Change operations.
const (
	DataChangeInsert DataChangeOp = "insert"
	DataChangeUpdate DataChangeOp = "update"
	DataChangeDelete DataChangeOp = "delete"
)

// DataChange is one row change in a changeset. Values are strings in
// Postgres text input format, or null.
type DataChange struct {
	Op DataChangeOp `json:"op" enum:"insert,update,delete"`
	// Table is "schema.name"; it must be editable.
	Table string `json:"table"`
	// Key is the row's primary key (update, delete).
	Key map[string]string `json:"key,omitempty"`
	// Old holds the values the editor saw: for update, of every column in
	// New. The row must still match Key and Old, or the whole changeset
	// fails with a conflict.
	Old map[string]*string `json:"old,omitempty"`
	// New holds the values to set (update) or insert (insert; columns
	// left out get their default).
	New map[string]*string `json:"new,omitempty"`
}

// DataChangesetRequest previews or creates a changeset
// (POST /data/changesets). Creating stores it as pending; it is written
// only by a commit.
type DataChangesetRequest struct {
	Changes []DataChange `json:"changes" minItems:"1" maxItems:"500"`
	// Message says why, for reviewers (agents should always set it).
	Message string `json:"message,omitempty" maxLength:"1000"`
	// DryRun validates and renders the SQL without storing anything.
	DryRun bool `json:"dry_run,omitempty"`
}

// DataChangesetStatus is where a changeset is in its lifecycle:
// pending → committed | rejected | expired | conflict. All but pending are
// final.
type DataChangesetStatus string

// Changeset statuses. Preview is a dry run's result, never stored.
const (
	ChangesetPreview   DataChangesetStatus = "preview"
	ChangesetPending   DataChangesetStatus = "pending"
	ChangesetCommitted DataChangesetStatus = "committed"
	ChangesetRejected  DataChangesetStatus = "rejected"
	ChangesetExpired   DataChangesetStatus = "expired"
	ChangesetConflict  DataChangesetStatus = "conflict"
)

// DataChangeset is a set of row changes applied in one transaction.
type DataChangeset struct {
	// ID is 0 for a preview.
	ID         int64               `json:"id"`
	Status     DataChangesetStatus `json:"status" enum:"preview,pending,committed,rejected,expired,conflict"`
	Author     string              `json:"author"`
	AuthorKind string              `json:"author_kind" enum:"user,token"`
	Message    string              `json:"message,omitempty"`
	Changes    []DataChange        `json:"changes"`
	// SQL is the transaction exactly as it runs, values inlined as quoted
	// literals, one statement per change in order.
	SQL string `json:"sql"`
	// RowsAffected is per change, in order, once committed.
	RowsAffected []int64 `json:"rows_affected,omitempty"`
	// Conflicts list the rows that no longer matched (status conflict);
	// nothing was written.
	Conflicts []DataConflict `json:"conflicts,omitempty"`
	// ConfirmCommit: production, so a commit must confirm the
	// environment's or connection's name.
	ConfirmCommit bool       `json:"confirm_commit"`
	CreatedAt     time.Time  `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	DecidedAt     *time.Time `json:"decided_at,omitempty"`
	DecidedBy     string     `json:"decided_by,omitempty"`
	// Reason is the rejecter's note.
	Reason string `json:"reason,omitempty"`
}

// DataConflictReason says why a change didn't match the current data.
type DataConflictReason string

// Conflict reasons.
const (
	// ConflictChanged: the row's values differ from Old.
	ConflictChanged DataConflictReason = "changed"
	// ConflictMissing: no row has Key any more.
	ConflictMissing DataConflictReason = "missing"
	// ConflictFailed: the statement failed (e.g. a constraint); Message
	// has the database's error.
	ConflictFailed DataConflictReason = "failed"
)

// DataConflict is one change of a changeset that couldn't be applied.
type DataConflict struct {
	// Change is the index in DataChangeset.Changes.
	Change int                `json:"change"`
	Reason DataConflictReason `json:"reason" enum:"changed,missing,failed"`
	// Current holds the row's current values of the compared columns
	// (changed only).
	Current map[string]*string `json:"current,omitempty"`
	Message string             `json:"message"`
}

// DataCommitRequest commits a pending changeset
// (POST /data/changesets/{id}/commit). It is revalidated against current
// data first. Committing someone else's changeset is approving it (admins).
type DataCommitRequest struct {
	// Confirm is the environment's or connection's name; required on
	// production.
	Confirm string `json:"confirm,omitempty"`
}

// DataRejectRequest rejects a pending changeset
// (POST /data/changesets/{id}/reject). Admins, or the author withdrawing.
type DataRejectRequest struct {
	Reason string `json:"reason,omitempty" maxLength:"1000"`
}

// DataSavedQuery is a named query, per user and service.
type DataSavedQuery struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	SQL       string    `json:"sql"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SaveDataQueryRequest creates or replaces a saved query.
type SaveDataQueryRequest struct {
	Name string `json:"name" minLength:"1" maxLength:"100"`
	SQL  string `json:"sql" minLength:"1" maxLength:"1048576"`
}

// DataColumnRef names one column.
type DataColumnRef struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Column string `json:"column"`
}

// SetSensitiveColumnsRequest replaces a database's sensitive columns
// (PUT /data/sensitive-columns). Admin only.
type SetSensitiveColumnsRequest struct {
	Columns []DataColumnRef `json:"columns"`
}

// DataOutcome is how a data operation ended, in its audit detail.
type DataOutcome string

// Data outcomes.
const (
	OutcomeOK       DataOutcome = "ok"
	OutcomeError    DataOutcome = "error"
	OutcomeTimeout  DataOutcome = "timeout"
	OutcomeCanceled DataOutcome = "canceled"
	OutcomeConflict DataOutcome = "conflict"
)

// AuditDetail is the long part of an audit event (AuditEvent.DetailID),
// kept out of the main log so it stays scannable.
type AuditDetail struct {
	ID int64 `json:"id"`
	// SQL is the statement or generated SQL, cut at AuditSQLMaxBytes.
	SQL        string      `json:"sql"`
	Rows       int64       `json:"rows"`
	DurationMS int64       `json:"duration_ms"`
	Outcome    DataOutcome `json:"outcome" enum:"ok,error,timeout,canceled,conflict"`
}

// DataHistoryEntry is one of the caller's past queries on a database,
// read from their data.query audit events.
type DataHistoryEntry struct {
	// AuditID is the audit event's ID; pass it as before to page back.
	AuditID   int64     `json:"audit_id"`
	CreatedAt time.Time `json:"created_at"`
	AuditDetail
}
