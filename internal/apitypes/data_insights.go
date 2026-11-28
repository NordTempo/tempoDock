package apitypes

import "time"

// Database insights (ADR 0069): how a database is doing, read
// on demand from its own statistics. One shape for every engine: an
// engine fills the views it has and says why the others are empty in
// Unavailable. Nothing is stored; every call reads the database.

// DataInsightView is one part of DataInsights.
type DataInsightView string

// Insight views. Overview, tables and indexes need data access level
// schema; queries and sessions carry statement texts, and keys (Redis'
// key prefixes) key names, so they need read.
const (
	InsightOverview DataInsightView = "overview"
	InsightQueries  DataInsightView = "queries"
	InsightSessions DataInsightView = "sessions"
	InsightTables   DataInsightView = "tables"
	InsightIndexes  DataInsightView = "indexes"
	InsightKeys     DataInsightView = "keys"
)

// DataInsightViews lists every view, in the order the panel shows them.
var DataInsightViews = []DataInsightView{InsightOverview, InsightQueries, InsightSessions, InsightTables, InsightIndexes, InsightKeys}

// InsightViewsOf lists the views an engine has: tables and indexes for
// the SQL engines, keys for Redis.
func InsightViewsOf(e DataEngine) []DataInsightView {
	switch e {
	case EngineRedis:
		return []DataInsightView{InsightOverview, InsightQueries, InsightSessions, InsightKeys}
	case EngineMongoDB:
		return []DataInsightView{InsightOverview, InsightSessions}
	}
	return DataInsightViews[:5]
}

// Level is the data access level a view needs.
func (v DataInsightView) Level() DataLevel {
	if v == InsightQueries || v == InsightSessions || v == InsightKeys {
		return LevelRead
	}
	return LevelSchema
}

// DataStatementOrder sorts the queries view.
type DataStatementOrder string

// Statement orders: total time (the default), mean time, calls, rows.
const (
	OrderTotal DataStatementOrder = "total"
	OrderMean  DataStatementOrder = "mean"
	OrderCalls DataStatementOrder = "calls"
	OrderRows  DataStatementOrder = "rows"
)

// Row caps of the insight views, and how much of a statement's text they
// keep.
const (
	DataInsightQueriesLimit    = 50
	DataInsightQueriesMaxLimit = 100
	DataInsightSessionsLimit   = 200
	DataInsightTablesLimit     = 200
	DataInsightIndexesLimit    = 500
	DataInsightTextMaxBytes    = 4096
)

// AuditDataInsights is written when a caller looks at a database's
// sessions or queries (statement texts), the first time and then at most
// every 10 minutes, like data.browse.
const AuditDataInsights = "data.insights"

// DataInsightsRequest picks the views to read. No views means every view
// the caller may see.
type DataInsightsRequest struct {
	Views []DataInsightView
	Order DataStatementOrder
	// Limit caps the queries view: 0 means DataInsightQueriesLimit.
	Limit int
}

// DataInsights is how a database is doing right now (GET /data/insights).
// Only the requested views are set.
type DataInsights struct {
	Engine DataEngine `json:"engine" enum:"postgres,mysql,redis,mongodb"`
	ReadAt time.Time  `json:"read_at"`
	// Since is when the counters (calls, scans, cache hits) started
	// counting, at the latest: their last reset, else the server's start.
	Since       *time.Time      `json:"since,omitempty"`
	SinceReason string          `json:"since_reason,omitempty" enum:"reset,start"`
	Overview    *DataOverview   `json:"overview,omitempty"`
	Queries     []DataStatement `json:"queries,omitempty"`
	// QueriesTracked is how many statements the server tracks (the
	// queries view shows the top Limit of them).
	QueriesTracked int64            `json:"queries_tracked,omitempty"`
	Sessions       []DataSession    `json:"sessions,omitempty"`
	Tables         []DataTableStats `json:"tables,omitempty"`
	Indexes        []DataIndexStats `json:"indexes,omitempty"`
	// Keys are Redis' key prefixes, from a sample of each database.
	Keys []DataKeyPrefix `json:"keys,omitempty"`
	// Hints are what needs a look, from the views read, warnings first.
	Hints []DataHint `json:"hints"`
	// Unavailable says why a requested view is empty or partial.
	Unavailable []DataUnavailable `json:"unavailable"`
}

// DataOverview is the database's vital numbers.
type DataOverview struct {
	Connections DataPressure `json:"connections"`
	// CacheHit is the share of block reads served from memory, 0 to 1.
	CacheHit  *float64 `json:"cache_hit,omitempty"`
	Bytes     int64    `json:"bytes"`
	Deadlocks int64    `json:"deadlocks"`
	// TempBytes is what queries spilled to temporary files.
	TempBytes int64 `json:"temp_bytes"`
	// TransactionIDAge is age(datfrozenxid): wraparound is at 2.1 billion.
	TransactionIDAge *int64              `json:"transaction_id_age,omitempty"`
	QueryStats       DataQueryStatsState `json:"query_stats" enum:"on,not_loaded,no_extension"`
	StartedAt        *time.Time          `json:"started_at,omitempty"`
	// Redis: the memory limit (maxmemory, 0 for none) and what happens at
	// it, keys evicted since the counters' start, commands per second
	// now, and the keys of every database.
	MaxBytes       int64  `json:"max_bytes,omitempty"`
	EvictionPolicy string `json:"eviction_policy,omitempty"`
	EvictedKeys    int64  `json:"evicted_keys,omitempty"`
	OpsPerSec      int64  `json:"ops_per_sec,omitempty"`
	Keys           int64  `json:"keys,omitempty"`
}

// DataQueryStatsState is whether the queries view can work: Postgres
// needs pg_stat_statements loaded by the server and created as an
// extension.
type DataQueryStatsState string

// Query stats states.
const (
	QueryStatsOn          DataQueryStatsState = "on"
	QueryStatsNotLoaded   DataQueryStatsState = "not_loaded"
	QueryStatsNoExtension DataQueryStatsState = "no_extension"
)

// DataPressure is a database's connections against its limit. Deploy
// diagnosis shows it for the databases a failing service uses: counts
// only, no statement texts.
type DataPressure struct {
	Used int `json:"used"`
	Max  int `json:"max"`
	// Reserved are slots only superusers (and holders of
	// pg_use_reserved_connections) get; others can use Max - Reserved.
	Reserved          int `json:"reserved"`
	Active            int `json:"active"`
	Idle              int `json:"idle"`
	IdleInTransaction int `json:"idle_in_transaction"`
	// Waiting are sessions waiting on a lock.
	Waiting int `json:"waiting"`
	// OldestTransactionAt is when the oldest open transaction began.
	OldestTransactionAt *time.Time `json:"oldest_transaction_at,omitempty"`
	// Full: the database refused Dock's login because every connection
	// is taken; the counts are unknown.
	Full bool `json:"full"`
}

// DataSession is one connection to the database.
type DataSession struct {
	// ID names the session for cancel and terminate: opaque, and bound
	// to this connection (Postgres reuses process IDs).
	ID          string `json:"id"`
	PID         int64  `json:"pid,omitempty"`
	User        string `json:"user"`
	Application string `json:"application"`
	Client      string `json:"client,omitempty"`
	// Kind: client (an app), dock (the panel itself), autovacuum or
	// background.
	Kind  string `json:"kind" enum:"client,dock,autovacuum,background"`
	State string `json:"state" enum:"active,idle,idle_in_transaction,other"`
	// TransactionAt, QueryAt and StateAt are when its transaction, its
	// current (or last) statement and its state began.
	TransactionAt *time.Time `json:"transaction_at,omitempty"`
	QueryAt       *time.Time `json:"query_at,omitempty"`
	StateAt       *time.Time `json:"state_at,omitempty"`
	// Wait is what it waits for, e.g. "Lock: transactionid".
	Wait string `json:"wait,omitempty"`
	// BlockedBy are the IDs of the sessions holding the lock it waits for.
	BlockedBy []string `json:"blocked_by,omitempty"`
	// Text is the statement, cut at DataInsightTextMaxBytes; its values
	// are redacted for callers who see sensitive columns masked.
	Text string `json:"text,omitempty"`
}

// DataStatement is one normalized statement's totals since Since.
type DataStatement struct {
	// ID is the engine's statement ID (Postgres' queryid).
	ID      string  `json:"id"`
	Text    string  `json:"text"`
	User    string  `json:"user,omitempty"`
	Calls   int64   `json:"calls"`
	TotalMs float64 `json:"total_ms"`
	MeanMs  float64 `json:"mean_ms"`
	MaxMs   float64 `json:"max_ms"`
	Rows    int64   `json:"rows"`
	// CacheHit is the share of its block reads served from memory.
	CacheHit  *float64 `json:"cache_hit,omitempty"`
	TempBytes int64    `json:"temp_bytes"`
}

// DataTableStats is one table's size and use.
type DataTableStats struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	// TotalBytes (table, indexes and TOAST) and IndexBytes are exact,
	// unless Locked: then only EstimatedBytes, from the catalog.
	TotalBytes     *int64 `json:"total_bytes,omitempty"`
	IndexBytes     *int64 `json:"index_bytes,omitempty"`
	EstimatedBytes int64  `json:"estimated_bytes"`
	// Locked: another session holds or waits for an exclusive lock on it,
	// so its exact size wasn't read (that would wait in the lock queue).
	Locked        bool       `json:"locked,omitempty"`
	EstimatedRows int64      `json:"estimated_rows"`
	DeadRows      int64      `json:"dead_rows"`
	SeqScans      int64      `json:"seq_scans"`
	SeqRowsRead   int64      `json:"seq_rows_read"`
	IndexScans    int64      `json:"index_scans"`
	VacuumedAt    *time.Time `json:"vacuumed_at,omitempty"`
	AnalyzedAt    *time.Time `json:"analyzed_at,omitempty"`
	CacheHit      *float64   `json:"cache_hit,omitempty"`
}

// DataIndexStats is one index's size and use.
type DataIndexStats struct {
	Schema string `json:"schema"`
	Table  string `json:"table"`
	Name   string `json:"name"`
	// Bytes is unset when the table is locked (see DataTableStats).
	Bytes      *int64     `json:"bytes,omitempty"`
	Scans      int64      `json:"scans"`
	LastScanAt *time.Time `json:"last_scan_at,omitempty"`
	Unique     bool       `json:"unique"`
	Primary    bool       `json:"primary"`
	// Constraint: it backs a primary key, unique or exclusion constraint.
	Constraint bool `json:"constraint"`
	// Valid is false for a failed concurrent build.
	Valid bool `json:"valid"`
	// Duplicate: another index on the table has the same definition.
	Duplicate  bool   `json:"duplicate"`
	Definition string `json:"definition"`
}

// DataKeyPrefix is one key prefix of a Redis database ("user:", or ""
// for keys without one), from a sample of its keys: what the sample
// found, scaled up to the database's key count.
type DataKeyPrefix struct {
	DB     int    `json:"db"`
	Prefix string `json:"prefix"`
	// SampledKeys and SampledBytes are what the sample found
	// (MEMORY USAGE, 5 samples each).
	SampledKeys  int64 `json:"sampled_keys"`
	SampledBytes int64 `json:"sampled_bytes"`
	// EstimatedKeys and EstimatedBytes scale the sample to the database.
	EstimatedKeys  int64 `json:"estimated_keys"`
	EstimatedBytes int64 `json:"estimated_bytes"`
	// Biggest is the sample's biggest key with this prefix (its sensitive
	// pattern for callers who see those masked), and its size.
	Biggest      string `json:"biggest"`
	BiggestBytes int64  `json:"biggest_bytes"`
}

// DataHintKind is what a hint is about.
type DataHintKind string

// Hint kinds.
const (
	HintConnectionsHigh    DataHintKind = "connections_high"
	HintIdleInTransaction  DataHintKind = "idle_in_transaction"
	HintBlocked            DataHintKind = "blocked"
	HintLongQuery          DataHintKind = "long_query"
	HintDuplicateIndex     DataHintKind = "duplicate_index"
	HintInvalidIndex       DataHintKind = "invalid_index"
	HintUnusedIndex        DataHintKind = "unused_index"
	HintFullScans          DataHintKind = "full_scans"
	HintDeadRows           DataHintKind = "dead_rows"
	HintNotAnalyzed        DataHintKind = "not_analyzed"
	HintCacheLow           DataHintKind = "cache_low"
	HintTempSpill          DataHintKind = "temp_spill"
	HintTransactionIDAge   DataHintKind = "transaction_id_age"
	HintQueryStatsDropping DataHintKind = "query_stats_dropping"
	HintQueryStatsOff      DataHintKind = "query_stats_off"
	// HintHistoryLong: MySQL's purge falls behind (a long undo history).
	HintHistoryLong DataHintKind = "history_long"
	// Redis: memory near its limit, keys evicted, saves failing, a big
	// key in the sample.
	HintMemoryHigh         DataHintKind = "memory_high"
	HintEvictions          DataHintKind = "evictions"
	HintPersistenceFailing DataHintKind = "persistence_failing"
	HintBigKey             DataHintKind = "big_key"
)

// DataHint is one thing that needs a look. Its words carry no values
// from the data: only names and numbers.
type DataHint struct {
	Kind  DataHintKind    `json:"kind" enum:"connections_high,idle_in_transaction,blocked,long_query,duplicate_index,invalid_index,unused_index,full_scans,dead_rows,not_analyzed,cache_low,temp_spill,transaction_id_age,query_stats_dropping,query_stats_off,history_long,memory_high,evictions,persistence_failing,big_key"`
	Level string          `json:"level" enum:"warn,note"`
	View  DataInsightView `json:"view" enum:"overview,queries,sessions,tables,indexes,keys"`
	// Subject is what it is about: "public.orders_status2", a session or
	// statement ID, or empty for the database.
	Subject string `json:"subject,omitempty"`
	// Title is short ("Duplicate index"); Detail the evidence in words.
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// Copy is a statement to run yourself or put in a migration, e.g.
	// DROP INDEX CONCURRENTLY ...; Dock never runs it.
	Copy string `json:"copy,omitempty"`
}

// DataUnavailable says why a requested view is empty (or, with Partial,
// incomplete) and what to do.
type DataUnavailable struct {
	View    DataInsightView `json:"view" enum:"overview,queries,sessions,tables,indexes,keys"`
	Partial bool            `json:"partial,omitempty"`
	Reason  string          `json:"reason"`
}

// Audit actions of insight actions. Summaries name the session's
// application and user and how long it ran, never its statement; the
// detail holds the statement as the caller could see it.
const (
	AuditDataCancel     = "data.cancel"      // a query cancelled; detail: its statement
	AuditDataTerminate  = "data.terminate"   // a session ended; detail: its statement
	AuditDataStatsReset = "data.stats_reset" // query stats reset
)

// DataCancelRequest cancels a session's query, or with Terminate ends
// the session (POST /data/insights/sessions/{session}/cancel).
type DataCancelRequest struct {
	// Terminate ends the session (its connection and open transaction)
	// instead of cancelling its query.
	Terminate bool `json:"terminate,omitempty"`
	// QueryStartedAt is the query_at the caller saw: the cancel only hits
	// that query, never one the session started since.
	QueryStartedAt *time.Time `json:"query_started_at,omitempty"`
	// Confirm is the environment's (or connection's) name, needed to end
	// a session on production.
	Confirm string `json:"confirm,omitempty"`
}

// DataCancelResult says what was cancelled or ended.
type DataCancelResult struct {
	Session     string `json:"session"`
	Terminated  bool   `json:"terminated"`
	User        string `json:"user"`
	Application string `json:"application"`
	// RunningMs is how long the query (or, ended, the transaction) had
	// been running.
	RunningMs int64 `json:"running_ms"`
}

// DataQueryStatsResult says what turning on query stats did.
type DataQueryStatsResult struct {
	// State: on (the extension exists now), or staged (the start command
	// change waits to apply; it applies with the next deploy, which
	// restarts the database).
	State  string `json:"state" enum:"on,staged"`
	Detail string `json:"detail"`
}
