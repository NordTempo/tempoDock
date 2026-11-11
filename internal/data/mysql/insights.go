package mysql

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/conncache"
	"github.com/NordTempo/tempoDock/internal/data/mysql/wire"
)

// Insights (ADR 0069, 0072): fixed, read-only queries on the server's own
// statistics. The monitor (PROCESS, SELECT on performance_schema) reads
// sessions, statement digests and I/O counters; the reader reads sizes
// from information_schema, which shows a user only the tables it has
// privileges on. Every query runs under insightTimeout with its rows
// capped in SQL, and lock waits are read only when a transaction waits.
// performance_schema off (MariaDB's default) leaves the queries view and
// index use unavailable and the rest working.

const (
	// insightTimeout is the statement timeout of insight queries.
	insightTimeout = 2 * time.Second
	// insightsDeadline bounds a whole Insights call.
	insightsDeadline = 15 * time.Second
)

// Hint thresholds (data.Hints has the shared ones).
const (
	cacheLow          = 0.99
	cacheLowReads     = 10_000
	historyLong       = 1_000_000
	noIndexCalls      = 100
	tmpDiskTables     = 100
	fullScanRows      = 1_000_000
	fullScanTableRows = 10_000
	unusedIndexBytes  = 1 << 20
	unusedIndexAge    = 7 * 24 * time.Hour
)

// sessionWords are MySQL's words in session hints.
var sessionWords = data.SessionWords{ID: "connection", Cleanup: "purge"}

// status is what every view may need, read first.
type status struct {
	now        time.Time
	started    time.Time
	maxConns   int
	perfSchema bool
	pageSize   int64
	vars       map[string]int64
	// newProcesslist: performance_schema.processlist exists and is on
	// (MySQL 8.0.22+); unlike SHOW PROCESSLIST it takes no global mutex.
	newProcesslist bool
}

// readStatus reads status variables, a few settings and the InnoDB
// metrics MySQL keeps instead of status variables.
func readStatus(ctx context.Context, c *conn) (status, error) {
	st := status{now: time.Now().UTC(), vars: map[string]int64{}}
	err := c.query(ctx, `SHOW GLOBAL STATUS WHERE Variable_name IN ('Threads_connected', 'Threads_running', 'Uptime',
  'Innodb_buffer_pool_read_requests', 'Innodb_buffer_pool_reads', 'Innodb_deadlocks',
  'Performance_schema_digest_lost', 'Innodb_history_list_length')`, func(r [][]byte) error {
		st.vars[strings.ToLower(string(r[0]))] = atoi64(r[1])
		return nil
	})
	if err != nil {
		return st, err
	}
	err = c.query(ctx, "SELECT @@max_connections, @@performance_schema, @@innodb_page_size", func(r [][]byte) error {
		st.maxConns, st.perfSchema, st.pageSize = int(atoi64(r[0])), string(r[1]) == "1", atoi64(r[2])
		return nil
	})
	if err != nil {
		return st, err
	}
	if !c.mariadb {
		err = c.query(ctx, "SELECT NAME, COUNT FROM information_schema.INNODB_METRICS WHERE NAME IN ('lock_deadlocks', 'trx_rseg_history_len')",
			func(r [][]byte) error {
				name := map[string]string{"lock_deadlocks": "innodb_deadlocks", "trx_rseg_history_len": "innodb_history_list_length"}[string(r[0])]
				st.vars[name] = atoi64(r[1])
				return nil
			})
		if err != nil {
			return st, err
		}
		st.newProcesslist = st.perfSchema && serverInfo(c.Version).VersionNum >= 80022
	}
	st.started = st.now.Add(-time.Duration(st.vars["uptime"]) * time.Second).Truncate(time.Second)
	return st, nil
}

func atoi64(b []byte) int64 {
	n, _ := strconv.ParseInt(string(b), 10, 64)
	return n
}

// acquireMonitor hands out a monitor connection with the insight timeout.
func (e *Engine) acquireMonitor(ctx context.Context, t data.Target) (*cached, error) {
	cn, err := e.acquire(ctx, t, data.RoleMonitor)
	if err != nil {
		return nil, err
	}
	if err := cn.Conn.setTimeout(ctx, insightTimeout); err != nil {
		cn.Broken = true
		e.release(cn)
		return nil, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	return cn, nil
}

// insightCall is one Insights call: its connections and what it read.
type insightCall struct {
	e   *Engine
	ctx context.Context
	t   data.Target
	mon *conn
	st  status
	out *apitypes.DataInsights
	// rd is the reader, acquired when a view first needs it.
	rd   *cached
	evid evidence
}

// evidence is what the hints need beyond the API's fields.
type evidence struct {
	noIndex map[string]int64 // statements that ran without an index, by digest
	tmpDisk map[string]int64 // their temporary tables on disk
	dupKeep map[string]bool  // duplicate indexes to keep, by schema.name
}

// reader returns the call's reader connection.
func (ic *insightCall) reader() (*conn, error) {
	if ic.rd == nil {
		cn, err := ic.e.acquire(ic.ctx, ic.t, data.RoleReader)
		if err != nil {
			return nil, err
		}
		ic.rd = cn
	}
	return ic.rd.Conn, nil
}

// unavailable reports a view (or with partial, part of one) as not read.
func (ic *insightCall) unavailable(v apitypes.DataInsightView, partial bool, reason string) {
	ic.out.Unavailable = append(ic.out.Unavailable, apitypes.DataUnavailable{View: v, Partial: partial, Reason: reason})
}

// Insights reads the requested views, then derives the hints. A view
// that fails on its own is reported in Unavailable and the others still
// return.
func (e *Engine) Insights(ctx context.Context, t data.Target, c data.Call, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	ctx, cancel := context.WithTimeout(ctx, insightsDeadline)
	defer cancel()
	out := apitypes.DataInsights{Engine: apitypes.EngineMySQL, Hints: []apitypes.DataHint{}, Unavailable: []apitypes.DataUnavailable{}}
	mon, err := e.acquireMonitor(ctx, t)
	if err != nil {
		return out, err
	}
	defer e.release(mon)
	st, err := readStatus(ctx, mon.Conn)
	if err != nil {
		return out, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	out.ReadAt, out.Since, out.SinceReason = st.now, &st.started, "start"
	ic := &insightCall{e: e, ctx: ctx, t: t, mon: mon.Conn, st: st, out: &out}
	defer func() {
		if ic.rd != nil {
			e.release(ic.rd)
		}
	}()
	readers := map[apitypes.DataInsightView]func() error{
		apitypes.InsightOverview: ic.overview,
		apitypes.InsightQueries:  func() error { return ic.statements(req) },
		apitypes.InsightSessions: ic.sessions,
		apitypes.InsightTables:   ic.tables,
		apitypes.InsightIndexes:  ic.indexes,
	}
	for _, v := range apitypes.InsightViewsOf(apitypes.EngineMySQL) {
		if !slices.Contains(req.Views, v) {
			continue
		}
		if err := readers[v](); err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			ic.unavailable(v, false, "couldn't read it: "+dbError(err).Error())
		}
	}
	out.Hints = ic.hints()
	return out, nil
}

// pressureSQL counts open transactions, those waiting on a lock, those
// whose connection sleeps (idle in a transaction), and when the oldest
// began.
const pressureSQL = `SELECT COUNT(*), COALESCE(SUM(t.trx_state = 'LOCK WAIT'), 0), COALESCE(SUM(p.COMMAND = 'Sleep'), 0), MIN(t.trx_started)
FROM information_schema.INNODB_TRX t LEFT JOIN information_schema.PROCESSLIST p ON p.ID = t.trx_mysql_thread_id`

// pressure reads the connection counts on c.
func pressure(ctx context.Context, c *conn, st status) (apitypes.DataPressure, error) {
	p := apitypes.DataPressure{Used: int(st.vars["threads_connected"]), Max: st.maxConns}
	// The monitor's own connection runs this.
	p.Active = max(int(st.vars["threads_running"])-1, 0)
	err := c.query(ctx, pressureSQL, func(r [][]byte) error {
		p.Waiting, p.IdleInTransaction = int(atoi64(r[1])), int(atoi64(r[2]))
		if r[3] != nil {
			if at, err := time.ParseInLocation(time.DateTime, string(r[3]), time.UTC); err == nil {
				p.OldestTransactionAt = &at
			}
		}
		return nil
	})
	p.Idle = max(p.Used-p.Active-p.IdleInTransaction-1, 0)
	return p, err
}

// Pressure reads the connection counts only, as the monitor. A login
// refused for too many connections is Full, not an error.
func (e *Engine) Pressure(ctx context.Context, t data.Target) (apitypes.DataPressure, error) {
	mon, err := e.acquireMonitor(ctx, t)
	if errors.Is(err, data.ErrFull) {
		return apitypes.DataPressure{Full: true}, nil
	}
	if err != nil {
		return apitypes.DataPressure{}, err
	}
	defer e.release(mon)
	st, err := readStatus(ctx, mon.Conn)
	if err != nil {
		return apitypes.DataPressure{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	return pressure(ctx, mon.Conn, st)
}

// overview reads the connections, buffer pool, size and counters.
func (ic *insightCall) overview() error {
	p, err := pressure(ic.ctx, ic.mon, ic.st)
	if err != nil {
		return err
	}
	ov := apitypes.DataOverview{Connections: p, Deadlocks: ic.st.vars["innodb_deadlocks"], StartedAt: &ic.st.started, QueryStats: apitypes.QueryStatsOn}
	if !ic.st.perfSchema {
		ov.QueryStats = apitypes.QueryStatsNotLoaded
	}
	if req := ic.st.vars["innodb_buffer_pool_read_requests"]; req > 0 {
		hit := 1 - float64(ic.st.vars["innodb_buffer_pool_reads"])/float64(req)
		ov.CacheHit = &hit
	}
	rd, err := ic.reader()
	if err == nil {
		err = rd.query(ic.ctx, "SELECT COALESCE(SUM(DATA_LENGTH + INDEX_LENGTH), 0) FROM information_schema.TABLES WHERE "+databasesFilter("TABLE_SCHEMA", ic.t.Database),
			func(r [][]byte) error { ov.Bytes = atoi64(r[0]); return nil })
	}
	if err != nil {
		ic.unavailable(apitypes.InsightOverview, true, "the database's size couldn't be read: "+dbError(err).Error())
	}
	ic.out.Overview = &ov
	return nil
}

// Why views need performance_schema, and how to turn it on.
const (
	perfSchemaHow = "start the server with --performance-schema=ON (a restart)"
	noPerfSchema  = "performance_schema is off on this server; " + perfSchemaHow
)

// digestFilter limits statement digests to the app's databases, and to
// statements run without a default database.
func digestFilter(database string) string {
	return "(SCHEMA_NAME IS NULL OR " + databasesFilter("SCHEMA_NAME", database) + ")"
}

// statements reads the top statement digests. Their text has no literals
// (the server normalises it); QUERY_SAMPLE_TEXT, which has, is never
// read.
func (ic *insightCall) statements(req apitypes.DataInsightsRequest) error {
	if !ic.st.perfSchema {
		ic.unavailable(apitypes.InsightQueries, false, noPerfSchema)
		return nil
	}
	order := map[apitypes.DataStatementOrder]string{
		apitypes.OrderTotal: "SUM_TIMER_WAIT", apitypes.OrderMean: "AVG_TIMER_WAIT",
		apitypes.OrderCalls: "COUNT_STAR", apitypes.OrderRows: "SUM_ROWS_SENT",
	}[req.Order]
	limit := req.Limit
	if limit <= 0 {
		limit = apitypes.DataInsightQueriesLimit
	}
	limit = min(limit, apitypes.DataInsightQueriesMaxLimit)
	ic.out.Queries = []apitypes.DataStatement{}
	ic.evid.noIndex, ic.evid.tmpDisk = map[string]int64{}, map[string]int64{}
	// Timers are in picoseconds.
	err := ic.mon.query(ic.ctx, `SELECT DIGEST, COALESCE(DIGEST_TEXT, ''), COUNT_STAR, SUM_TIMER_WAIT / 1e9, AVG_TIMER_WAIT / 1e9, MAX_TIMER_WAIT / 1e9,
  SUM_ROWS_SENT, SUM_NO_INDEX_USED, SUM_CREATED_TMP_DISK_TABLES
FROM performance_schema.events_statements_summary_by_digest
WHERE DIGEST IS NOT NULL AND `+digestFilter(ic.t.Database)+`
ORDER BY `+cmp.Or(order, "SUM_TIMER_WAIT")+` DESC LIMIT `+strconv.Itoa(limit), func(r [][]byte) error {
		s := apitypes.DataStatement{ID: string(r[0]), Text: cutText(string(r[1])), Calls: atoi64(r[2]), Rows: atoi64(r[6])}
		s.TotalMs, _ = strconv.ParseFloat(string(r[3]), 64)
		s.MeanMs, _ = strconv.ParseFloat(string(r[4]), 64)
		s.MaxMs, _ = strconv.ParseFloat(string(r[5]), 64)
		ic.evid.noIndex[s.ID], ic.evid.tmpDisk[s.ID] = atoi64(r[7]), atoi64(r[8])
		ic.out.Queries = append(ic.out.Queries, s)
		return nil
	})
	if err != nil {
		return err
	}
	return ic.mon.query(ic.ctx, "SELECT COUNT(*) FROM performance_schema.events_statements_summary_by_digest",
		func(r [][]byte) error { ic.out.QueriesTracked = atoi64(r[0]); return nil })
}

// cutText cuts a statement at DataInsightTextMaxBytes, on a rune.
func cutText(s string) string {
	if len(s) <= apitypes.DataInsightTextMaxBytes {
		return s
	}
	cut := apitypes.DataInsightTextMaxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sessionID names a connection for Cancel: its ID and the server's start
// (IDs start over after a restart).
func sessionID(id int64, started time.Time) string {
	return strconv.FormatInt(id, 10) + "." + strconv.FormatInt(started.Unix(), 10)
}

// sessions reads the process list (at most DataInsightSessionsLimit
// connections, longest running first), the open transactions and, when
// one waits for a lock, who holds it.
func (ic *insightCall) sessions() error {
	type trx struct {
		started string
		state   string
		id      string
	}
	trxs := map[int64]trx{}
	err := ic.mon.query(ic.ctx, "SELECT trx_mysql_thread_id, trx_started, trx_state, trx_id FROM information_schema.INNODB_TRX",
		func(r [][]byte) error {
			trxs[atoi64(r[0])] = trx{started: string(r[1]), state: string(r[2]), id: string(r[3])}
			return nil
		})
	if err != nil {
		return err
	}
	blockedBy := map[int64][]int64{}
	waiting := slices.ContainsFunc(slicesOf(trxs), func(t trx) bool { return t.state == "LOCK WAIT" })
	if waiting {
		byTrx := map[string]int64{}
		for th, t := range trxs {
			byTrx[t.id] = th
		}
		sql := "SELECT REQUESTING_ENGINE_TRANSACTION_ID, BLOCKING_ENGINE_TRANSACTION_ID FROM performance_schema.data_lock_waits"
		if ic.mon.mariadb {
			sql = "SELECT requesting_trx_id, blocking_trx_id FROM information_schema.INNODB_LOCK_WAITS"
		}
		if ic.mon.mariadb || ic.st.perfSchema {
			err := ic.mon.query(ic.ctx, sql, func(r [][]byte) error {
				w, b := byTrx[string(r[0])], byTrx[string(r[1])]
				if w != 0 && b != 0 && !slices.Contains(blockedBy[w], b) {
					blockedBy[w] = append(blockedBy[w], b)
				}
				return nil
			})
			if err != nil {
				ic.unavailable(apitypes.InsightSessions, true, "who holds the locks couldn't be read: "+dbError(err).Error())
			}
		}
	}
	apps := map[int64]string{}
	if ic.st.perfSchema {
		err := ic.mon.query(ic.ctx, "SELECT PROCESSLIST_ID, ATTR_VALUE FROM performance_schema.session_connect_attrs WHERE ATTR_NAME = 'program_name'",
			func(r [][]byte) error { apps[atoi64(r[0])] = string(r[1]); return nil })
		if err != nil {
			return err
		}
	}

	src, timeCol := "information_schema.PROCESSLIST", "TIME * 1000"
	switch {
	case ic.st.newProcesslist:
		src = "performance_schema.processlist"
	case ic.mon.mariadb:
		timeCol = "TIME_MS"
	}
	dock := map[string]bool{string(ic.e.roles.Reader): true, string(ic.e.roles.Writer): true,
		string(ic.e.roles.ReaderMasked): true, string(ic.e.roles.Monitor): true}
	ic.out.Sessions = []apitypes.DataSession{}
	now := ic.st.now
	err = ic.mon.query(ic.ctx, "SELECT ID, USER, HOST, DB, COMMAND, "+timeCol+", STATE, INFO FROM "+src+
		" WHERE ID <> CONNECTION_ID() ORDER BY TIME DESC LIMIT "+strconv.Itoa(apitypes.DataInsightSessionsLimit),
		func(r [][]byte) error {
			id, user, command := atoi64(r[0]), string(r[1]), string(r[4])
			ms, _ := strconv.ParseFloat(string(r[5]), 64)
			since := now.Add(-time.Duration(ms) * time.Millisecond)
			s := apitypes.DataSession{
				ID: sessionID(id, ic.st.started), PID: id, User: user, Application: apps[id],
				Client: hostOnly(string(r[2])), Kind: "client", State: "other", StateAt: &since, Text: cutText(string(r[7])),
			}
			switch {
			case dock[user]:
				s.Kind = "dock"
			case user == "system user" || user == "event_scheduler" || command == "Daemon" || strings.HasPrefix(command, "Binlog"):
				s.Kind = "background"
			}
			tx, inTx := trxs[id]
			switch command {
			case "Sleep":
				s.State = "idle"
				if inTx {
					s.State = "idle_in_transaction"
				}
			case "Query", "Execute":
				s.State, s.QueryAt = "active", &since
			}
			if inTx {
				if at, err := time.ParseInLocation(time.DateTime, tx.started, time.UTC); err == nil {
					s.TransactionAt = &at
				}
				if tx.state == "LOCK WAIT" {
					s.Wait = "Lock: row"
				}
			}
			if st := string(r[6]); s.Wait == "" && strings.HasPrefix(st, "Waiting for") {
				s.Wait = st
			}
			for _, b := range blockedBy[id] {
				s.BlockedBy = append(s.BlockedBy, sessionID(b, ic.st.started))
			}
			ic.out.Sessions = append(ic.out.Sessions, s)
			return nil
		})
	return err
}

func slicesOf[K comparable, V any](m map[K]V) []V {
	out := make([]V, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// hostOnly is a process list HOST without its port ("10.0.0.5:51234").
func hostOnly(h string) string {
	if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.Contains(h[i:], "]") {
		return h[:i]
	}
	return h
}

// indexUse reads rows read per table through each index ("" for full
// scans) since the server started, from performance_schema.
func (ic *insightCall) indexUse() (map[[3]string]int64, error) {
	use := map[[3]string]int64{}
	err := ic.mon.query(ic.ctx, `SELECT OBJECT_SCHEMA, OBJECT_NAME, COALESCE(INDEX_NAME, ''), COUNT_READ
FROM performance_schema.table_io_waits_summary_by_index_usage WHERE OBJECT_TYPE = 'TABLE' AND `+databasesFilter("OBJECT_SCHEMA", ic.t.Database),
		func(r [][]byte) error {
			use[[3]string{string(r[0]), string(r[1]), string(r[2])}] = atoi64(r[3])
			return nil
		})
	return use, err
}

// tables reads the largest tables' sizes (as the reader) and, with
// performance_schema, the rows read by full scans and through indexes.
func (ic *insightCall) tables() error {
	rd, err := ic.reader()
	if err != nil {
		return err
	}
	ic.out.Tables = []apitypes.DataTableStats{}
	err = rd.query(ic.ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, COALESCE(TABLE_ROWS, 0), COALESCE(DATA_LENGTH, 0), COALESCE(INDEX_LENGTH, 0)
FROM information_schema.TABLES WHERE TABLE_TYPE = 'BASE TABLE' AND `+databasesFilter("TABLE_SCHEMA", ic.t.Database)+`
ORDER BY COALESCE(DATA_LENGTH, 0) + COALESCE(INDEX_LENGTH, 0) DESC LIMIT `+strconv.Itoa(apitypes.DataInsightTablesLimit),
		func(r [][]byte) error {
			total, index := atoi64(r[3])+atoi64(r[4]), atoi64(r[4])
			ic.out.Tables = append(ic.out.Tables, apitypes.DataTableStats{
				Schema: string(r[0]), Name: string(r[1]), EstimatedRows: atoi64(r[2]),
				TotalBytes: &total, IndexBytes: &index, EstimatedBytes: total,
			})
			return nil
		})
	if err != nil {
		return err
	}
	if !ic.st.perfSchema {
		ic.unavailable(apitypes.InsightTables, true, "rows read by full scans and through indexes need performance_schema; "+perfSchemaHow)
		return nil
	}
	use, err := ic.indexUse()
	if err != nil {
		ic.unavailable(apitypes.InsightTables, true, "table use couldn't be read: "+dbError(err).Error())
		return nil
	}
	for i := range ic.out.Tables {
		t := &ic.out.Tables[i]
		for k, n := range use {
			if k[0] == t.Schema && k[1] == t.Name {
				if k[2] == "" {
					t.SeqRowsRead += n
				} else {
					t.IndexScans += n
				}
			}
		}
	}
	return nil
}

// indexes reads every index (as the reader), with its size (the
// monitor's mysql.innodb_index_stats) and, with performance_schema, the
// rows read through it.
func (ic *insightCall) indexes() error {
	rd, err := ic.reader()
	if err != nil {
		return err
	}
	type key = [3]string
	var order []key
	byKey := map[key]*apitypes.DataIndexStats{}
	cols := map[key][]string{}
	err = rd.query(ic.ctx, `SELECT TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, NON_UNIQUE, COALESCE(COLUMN_NAME, '(expression)'), SUB_PART
FROM information_schema.STATISTICS WHERE `+databasesFilter("TABLE_SCHEMA", ic.t.Database)+`
ORDER BY TABLE_SCHEMA, TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX`, func(r [][]byte) error {
		k := key{string(r[0]), string(r[1]), string(r[2])}
		if _, ok := byKey[k]; !ok {
			if len(order) == apitypes.DataInsightIndexesLimit {
				return nil
			}
			primary := k[2] == "PRIMARY"
			unique := string(r[3]) == "0"
			byKey[k] = &apitypes.DataIndexStats{Schema: k[0], Table: k[1], Name: k[2], Unique: unique, Primary: primary, Constraint: primary || unique, Valid: true}
			order = append(order, k)
		}
		col := string(r[4])
		if r[5] != nil {
			col = quoteIdent(col) + "(" + string(r[5]) + ")"
		}
		cols[k] = append(cols[k], col)
		return nil
	})
	if err != nil {
		return err
	}
	sizes := map[key]int64{}
	err = ic.mon.query(ic.ctx, `SELECT database_name, table_name, index_name, stat_value FROM mysql.innodb_index_stats
WHERE stat_name = 'size' AND `+databasesFilter("database_name", ic.t.Database), func(r [][]byte) error {
		sizes[key{string(r[0]), string(r[1]), string(r[2])}] = atoi64(r[3]) * ic.st.pageSize
		return nil
	})
	if err != nil {
		ic.unavailable(apitypes.InsightIndexes, true, "index sizes couldn't be read: "+dbError(err).Error())
	}
	var use map[[3]string]int64
	if ic.st.perfSchema {
		if use, err = ic.indexUse(); err != nil {
			ic.unavailable(apitypes.InsightIndexes, true, "index use couldn't be read: "+dbError(err).Error())
		}
	} else {
		ic.unavailable(apitypes.InsightIndexes, true, "index use needs performance_schema; "+perfSchemaHow)
	}
	// Duplicates: the same columns in the same order on the same table.
	defs := map[string][]key{}
	for _, k := range order {
		d := k[0] + "\x00" + k[1] + "\x00" + strings.Join(cols[k], "\x00")
		defs[d] = append(defs[d], k)
	}
	ic.evid.dupKeep = map[string]bool{}
	ic.out.Indexes = make([]apitypes.DataIndexStats, 0, len(order))
	for _, k := range order {
		x := byKey[k]
		if n, ok := sizes[k]; ok {
			x.Bytes = &n
		}
		x.Scans = use[k]
		x.Definition = indexDefinition(k[2], k[1], x.Unique, cols[k])
		d := k[0] + "\x00" + k[1] + "\x00" + strings.Join(cols[k], "\x00")
		if group := defs[d]; len(group) > 1 {
			x.Duplicate = true
			// Keep one: a primary key, else a unique one, else the first.
			keep := slices.MinFunc(group, func(a, b key) int {
				return cmp.Or(rank(byKey[a])-rank(byKey[b]), strings.Compare(a[2], b[2]))
			})
			ic.evid.dupKeep[k[0]+"."+k[2]] = keep == k
		}
		ic.out.Indexes = append(ic.out.Indexes, *x)
	}
	return nil
}

// rank orders indexes for keeping one of duplicates: primary, unique,
// other.
func rank(x *apitypes.DataIndexStats) int {
	switch {
	case x.Primary:
		return 0
	case x.Unique:
		return 1
	}
	return 2
}

// hints derives what needs a look from the views read.
func (ic *insightCall) hints() []apitypes.DataHint {
	var h data.Hints
	in, st := ic.out, ic.st
	counted := st.now.Sub(st.started)
	if ov := in.Overview; ov != nil {
		h.Connections(ov.Connections)
		if ov.CacheHit != nil && *ov.CacheHit < cacheLow && st.vars["innodb_buffer_pool_reads"] >= cacheLowReads {
			h.Add("note", apitypes.InsightOverview, apitypes.HintCacheLow, "", "Buffer pool hit ratio is low",
				fmt.Sprintf("%.1f%% of page reads came from memory; the data read often is bigger than innodb_buffer_pool_size", *ov.CacheHit*100), "")
		}
		if n := st.vars["innodb_history_list_length"]; n >= historyLong {
			h.Add("warn", apitypes.InsightOverview, apitypes.HintHistoryLong, "", "Purge is falling behind",
				fmt.Sprintf("%s undo records wait to be purged; a long-open transaction usually holds them", data.Count(n)), "")
		}
		if ov.QueryStats != apitypes.QueryStatsOn {
			h.Add("note", apitypes.InsightQueries, apitypes.HintQueryStatsOff, "", "Query stats are off", noPerfSchema, "")
		}
	}
	if in.Queries != nil {
		if n := st.vars["performance_schema_digest_lost"]; n > 0 {
			h.Add("note", apitypes.InsightQueries, apitypes.HintQueryStatsDropping, "", "Query stats are dropping statements",
				fmt.Sprintf("%s statements weren't counted for lack of room; raise performance_schema_digests_size", data.Count(n)), "")
		}
		for _, s := range in.Queries {
			if n := ic.evid.noIndex[s.ID]; n >= noIndexCalls && n*2 >= s.Calls {
				h.Add("note", apitypes.InsightQueries, apitypes.HintFullScans, s.ID, "Runs without an index",
					fmt.Sprintf("%s of its %s runs used no index", data.Count(n), data.Count(s.Calls)), "")
			}
			if n := ic.evid.tmpDisk[s.ID]; n >= tmpDiskTables {
				h.Add("note", apitypes.InsightQueries, apitypes.HintTempSpill, s.ID, "Statement spills to disk",
					fmt.Sprintf("made %s temporary tables on disk over %s calls; it may need an index", data.Count(n), data.Count(s.Calls)), "")
			}
		}
	}
	if in.Sessions != nil {
		h.Sessions(in.Sessions, st.now, sessionWords)
	}
	for _, t := range in.Tables {
		if t.SeqRowsRead >= fullScanRows && t.EstimatedRows >= fullScanTableRows && t.SeqRowsRead > 10*t.IndexScans {
			h.Add("note", apitypes.InsightTables, apitypes.HintFullScans, t.Schema+"."+t.Name, "Likely missing index",
				fmt.Sprintf("%s rows read by full scans, %s through indexes", data.Count(t.SeqRowsRead), data.Count(t.IndexScans)), "")
		}
	}
	for _, x := range in.Indexes {
		name := x.Schema + "." + x.Name
		drop := "ALTER TABLE " + quoteTable(x.Schema, x.Table) + " DROP INDEX " + quoteIdent(x.Name) + ";"
		if x.Duplicate && !ic.evid.dupKeep[name] && !x.Primary {
			h.Add("warn", apitypes.InsightIndexes, apitypes.HintDuplicateIndex, name, "Duplicate index",
				fmt.Sprintf("same columns as another index on %s; %s", x.Table, sizeOf(x.Bytes)), drop)
			continue
		}
		if st.perfSchema && x.Scans == 0 && !x.Constraint && x.Bytes != nil && *x.Bytes >= unusedIndexBytes && counted >= unusedIndexAge {
			h.Add("note", apitypes.InsightIndexes, apitypes.HintUnusedIndex, name, "Unused index",
				fmt.Sprintf("no reads in %s; %s", data.DurText(counted), sizeOf(x.Bytes)), drop)
		}
	}
	return h.Sorted()
}

// sizeOf is a size in words, or "size unknown".
func sizeOf(n *int64) string {
	if n == nil {
		return "size unknown"
	}
	return data.BytesText(*n)
}

// EnsureMonitor creates or updates the monitor user as the owner: PROCESS
// (every connection and its statement, InnoDB's transactions), SELECT on
// performance_schema and on mysql.innodb_index_stats (index sizes), no
// table data. Its cached connections are dropped, so the next call logs
// in with the password set here.
func (e *Engine) EnsureMonitor(ctx context.Context, t data.Target, owner data.Credentials) error {
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := ensureUser(ctx, c, data.Role(t.Monitor.User), t.Monitor.Password); err != nil {
		return err
	}
	u := user(data.Role(t.Monitor.User))
	for _, sql := range []string{
		"GRANT PROCESS ON *.* TO " + u,
		"GRANT SELECT ON performance_schema.* TO " + u,
		"GRANT SELECT ON mysql.innodb_index_stats TO " + u,
	} {
		if err := c.exec(ctx, sql); err != nil {
			return fmt.Errorf("%s: %w", sql, err)
		}
	}
	e.conns.Drop(func(k conncache.Key) bool { return k.ID == t.ServiceID && k.Role == data.RoleMonitor })
	return nil
}

// EnableQueryStats reports whether statement digests are collected:
// performance_schema is a startup setting, so a server without it needs
// a restart, which Dock doesn't do on its own.
func (e *Engine) EnableQueryStats(ctx context.Context, t data.Target, owner data.Credentials) (apitypes.DataQueryStatsState, bool, error) {
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = c.Close() }()
	on := false
	if err := c.query(ctx, "SELECT @@performance_schema", func(r [][]byte) error { on = string(r[0]) == "1"; return nil }); err != nil {
		return "", false, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if !on {
		return "", false, fmt.Errorf("%w: %s; add it to the service's start command and deploy", data.ErrInvalid, noPerfSchema)
	}
	return apitypes.QueryStatsOn, false, nil
}

// ResetQueryStats empties the statement digests, as the owner.
func (e *Engine) ResetQueryStats(ctx context.Context, t data.Target, owner data.Credentials) error {
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.exec(ctx, "TRUNCATE TABLE performance_schema.events_statements_summary_by_digest"); err != nil {
		if wire.ErrorCode(err) != 0 {
			return fmt.Errorf("%w: %w", data.ErrInvalid, err)
		}
		return err
	}
	return nil
}

// Cancel kills a connection's statement (KILL QUERY) or the connection
// (KILL CONNECTION), as the owner, who may kill any. The session ID binds
// the connection to the server's run, and queryStartedAt, when set, must
// still be its statement's start (to the second: the process list counts
// seconds), so a reused ID or a later statement is never hit.
func (e *Engine) Cancel(ctx context.Context, t data.Target, owner data.Credentials, session string, queryStartedAt *time.Time, terminate bool) (data.CancelResult, error) {
	idText, startText, ok := strings.Cut(session, ".")
	id, err1 := strconv.ParseInt(idText, 10, 64)
	start, err2 := strconv.ParseInt(startText, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return data.CancelResult{}, fmt.Errorf("%w: %q is not a session ID from the sessions view", data.ErrInvalid, session)
	}
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return data.CancelResult{}, err
	}
	defer func() { _ = c.Close() }()
	st, err := readStatus(ctx, c)
	if err != nil {
		return data.CancelResult{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if d := st.started.Unix() - start; d < -2 || d > 2 {
		return data.CancelResult{}, nil // the server restarted since
	}
	var res data.CancelResult
	var running time.Duration
	err = c.query(ctx, "SELECT USER, COMMAND, TIME, COALESCE(INFO, '') FROM information_schema.PROCESSLIST WHERE ID = "+strconv.FormatInt(id, 10),
		func(r [][]byte) error {
			res.Found, res.User, res.Text = true, string(r[0]), cutText(string(r[3]))
			running = time.Duration(atoi64(r[2])) * time.Second
			if !terminate && string(r[1]) != "Query" && string(r[1]) != "Execute" {
				res.Found = false // nothing running to cancel
			}
			return nil
		})
	if err != nil {
		return data.CancelResult{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if !res.Found {
		return data.CancelResult{}, nil
	}
	if queryStartedAt != nil {
		if d := st.now.Add(-running).Sub(*queryStartedAt); d < -2*time.Second || d > 2*time.Second {
			return data.CancelResult{}, nil // another statement since
		}
	}
	if st.perfSchema {
		_ = c.query(ctx, "SELECT ATTR_VALUE FROM performance_schema.session_connect_attrs WHERE ATTR_NAME = 'program_name' AND PROCESSLIST_ID = "+strconv.FormatInt(id, 10),
			func(r [][]byte) error { res.Application = string(r[0]); return nil })
	}
	kill := "KILL QUERY "
	if terminate {
		kill = "KILL CONNECTION "
	}
	if err := c.exec(ctx, kill+strconv.FormatInt(id, 10)); err != nil {
		if wire.ErrorCode(err) == errUnknownThread {
			return data.CancelResult{}, nil // it ended meanwhile
		}
		return data.CancelResult{}, err
	}
	res.Running = running
	return res, nil
}
