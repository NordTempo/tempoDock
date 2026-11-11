package redis

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/redis/resp"
)

// Insights (ADR 0069, 0072): the monitor reads INFO, the slow log and
// CLIENT LIST, which see the server's statistics and never a key; the
// keys view samples key names and sizes as the reader, with bounded SCAN
// steps and MEMORY USAGE with 5 samples. Commands are always counted
// (INFO commandstats), so query stats are always on. Redis keeps no time
// for its last CONFIG RESETSTAT, so Since stays unset.

const (
	// insightsDeadline bounds one Insights call.
	insightsDeadline = 10 * time.Second
	// slowlogEntries is how much of the slow log the queries view reads
	// for each command's slowest call.
	slowlogEntries = "128"
	// sampleKeys caps the keys the keys view sizes, over all databases;
	// sampleSteps the SCAN steps per database and sampleTime the time all
	// of them take.
	sampleKeys  = 1000
	sampleSteps = 20
	sampleTime  = 200 * time.Millisecond
	// prefixesLimit caps the keys view, biggest estimates first.
	prefixesLimit = 100
)

// Hint thresholds (data.Hints has the shared ones).
const (
	memoryNote = 0.80
	memoryWarn = 0.90
	bigKeyNote = 16 << 20
)

// info is one INFO reply: its fields, the command stats and keyspace.
type info struct {
	now    time.Time
	fields map[string]string
	cmds   map[string]cmdStat
	dbs    []apitypes.DataKeyspaceDB
}

// cmdStat is one INFO commandstats line.
type cmdStat struct {
	calls, failed int64
	usec          float64
}

func (in info) int(k string) int64 {
	n, _ := strconv.ParseInt(in.fields[k], 10, 64)
	return n
}

// started is the server's start, from its uptime.
func (in info) started() time.Time {
	return in.now.Add(-time.Duration(in.int("uptime_in_seconds")) * time.Second)
}

// readInfo reads the INFO sections the views need in one command.
func readInfo(c *conn) (info, error) {
	v, err := c.Do("INFO", "server", "clients", "memory", "persistence", "stats", "keyspace", "commandstats")
	if err != nil {
		return info{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	if err := v.Err(); err != nil {
		return info{}, accessError(err)
	}
	return parseInfo(v.Str, time.Now().UTC()), nil
}

// parseInfo reads INFO's "name:value" lines; commandstats lines are
// "cmdstat_get:calls=3,usec=12,...".
func parseInfo(text string, now time.Time) info {
	in := info{now: now, fields: map[string]string{}, cmds: map[string]cmdStat{}, dbs: parseKeyspace(text)}
	for line := range strings.SplitSeq(text, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		name, isCmd := strings.CutPrefix(k, "cmdstat_")
		if !isCmd {
			in.fields[k] = v
			continue
		}
		var st cmdStat
		for f := range strings.SplitSeq(v, ",") {
			fk, fv, _ := strings.Cut(f, "=")
			switch fk {
			case "calls":
				st.calls, _ = strconv.ParseInt(fv, 10, 64)
			case "failed_calls":
				st.failed, _ = strconv.ParseInt(fv, 10, 64)
			case "usec":
				st.usec, _ = strconv.ParseFloat(fv, 64)
			}
		}
		in.cmds[name] = st
	}
	return in
}

// acquireMonitor hands out a monitor connection. A refused login is
// ErrNotSetUp, so the panel creates the monitor (again: a restart drops
// ACL users) and retries.
func (e *Engine) acquireMonitor(ctx context.Context, t data.Target) (*cached, error) {
	cn, err := e.acquire(ctx, t, data.RoleMonitor)
	if errors.Is(err, data.ErrNoPrivilege) {
		return nil, fmt.Errorf("%w: the monitor user can't log in", data.ErrNotSetUp)
	}
	return cn, err
}

// pressure is the connection counts from INFO clients. active_clients
// (Redis 8) counts the monitor itself; servers without it leave the
// active and idle counts at zero.
func pressure(in info) apitypes.DataPressure {
	p := apitypes.DataPressure{Used: int(in.int("connected_clients")), Max: int(in.int("maxclients"))}
	if _, ok := in.fields["active_clients"]; ok {
		p.Active = max(int(in.int("active_clients"))-1, 0)
		p.Idle = max(p.Used-p.Active-1, 0)
	}
	return p
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
	in, err := call(ctx, mon, readInfo)
	if err != nil {
		return apitypes.DataPressure{}, err
	}
	return pressure(in), nil
}

// Insights reads the requested views from one INFO and derives the
// hints. A view that fails on its own is reported in Unavailable and the
// others still return.
func (e *Engine) Insights(ctx context.Context, t data.Target, c data.Call, req apitypes.DataInsightsRequest) (apitypes.DataInsights, error) {
	ctx, cancel := context.WithTimeout(ctx, insightsDeadline)
	defer cancel()
	out := apitypes.DataInsights{Engine: apitypes.EngineRedis, Hints: []apitypes.DataHint{}, Unavailable: []apitypes.DataUnavailable{}}
	mon, err := e.acquireMonitor(ctx, t)
	if err != nil {
		return out, err
	}
	defer e.release(mon)
	in, err := call(ctx, mon, readInfo)
	if err != nil {
		return out, err
	}
	out.ReadAt = in.now
	var h data.Hints
	for _, v := range req.Views {
		var err error
		switch v {
		case apitypes.InsightOverview:
			out.Overview = overview(in, &h)
		case apitypes.InsightQueries:
			err = runOn(ctx, mon, func(c *conn) (err error) {
				out.Queries, out.QueriesTracked, err = statements(c, in, req)
				return err
			})
		case apitypes.InsightSessions:
			var partial bool
			err = runOn(ctx, mon, func(c *conn) (err error) {
				out.Sessions, partial, err = e.sessions(c, in)
				return err
			})
			if partial {
				out.Unavailable = append(out.Unavailable, apitypes.DataUnavailable{View: v, Partial: true,
					Reason: fmt.Sprintf("%s clients; the %d most recently active are shown", data.Count(in.int("connected_clients")), apitypes.DataInsightSessionsLimit)})
			}
		case apitypes.InsightKeys:
			out.Keys, err = e.prefixes(ctx, t, c, in, &h)
		}
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			out.Unavailable = append(out.Unavailable, apitypes.DataUnavailable{View: v, Reason: "couldn't read it: " + err.Error()})
		}
	}
	out.Hints = h.Sorted()
	return out, nil
}

// runOn is call for fn that set their results themselves.
func runOn(ctx context.Context, cn *cached, fn func(c *conn) error) error {
	_, err := call(ctx, cn, func(c *conn) (struct{}, error) { return struct{}{}, fn(c) })
	return err
}

// overview is the server's vital numbers, with the memory, eviction,
// persistence and connection hints.
func overview(in info, h *data.Hints) *apitypes.DataOverview {
	started := in.started()
	ov := &apitypes.DataOverview{
		Connections:    pressure(in),
		Bytes:          in.int("used_memory"),
		QueryStats:     apitypes.QueryStatsOn,
		StartedAt:      &started,
		MaxBytes:       in.int("maxmemory"),
		EvictionPolicy: in.fields["maxmemory_policy"],
		EvictedKeys:    in.int("evicted_keys"),
		OpsPerSec:      in.int("instantaneous_ops_per_sec"),
	}
	if hits, misses := in.int("keyspace_hits"), in.int("keyspace_misses"); hits+misses > 0 {
		r := float64(hits) / float64(hits+misses)
		ov.CacheHit = &r
	}
	for _, db := range in.dbs {
		ov.Keys += db.Keys
	}
	h.Connections(ov.Connections)
	if ov.MaxBytes > 0 {
		share := float64(ov.Bytes) / float64(ov.MaxBytes)
		at := "Redis evicts keys by " + ov.EvictionPolicy + " at the limit"
		if ov.EvictionPolicy == "noeviction" {
			at = "writes fail at the limit (noeviction)"
		}
		detail := fmt.Sprintf("%s of %s used; %s", data.BytesText(ov.Bytes), data.BytesText(ov.MaxBytes), at)
		switch {
		case share >= memoryWarn:
			h.Add("warn", apitypes.InsightOverview, apitypes.HintMemoryHigh, "", "Memory almost full", detail, "")
		case share >= memoryNote:
			h.Add("note", apitypes.InsightOverview, apitypes.HintMemoryHigh, "", "Memory running high", detail, "")
		}
	}
	if ov.EvictedKeys > 0 {
		h.Add("note", apitypes.InsightOverview, apitypes.HintEvictions, "", "Keys evicted",
			fmt.Sprintf("%s keys evicted by %s to stay under the memory limit; if they are data, not a cache, raise maxmemory", data.Count(ov.EvictedKeys), ov.EvictionPolicy), "")
	}
	switch {
	case in.fields["rdb_last_bgsave_status"] == "err":
		h.Add("warn", apitypes.InsightOverview, apitypes.HintPersistenceFailing, "", "Snapshots failing",
			"the last background save failed (often a full disk); by default Redis refuses writes until one succeeds", "")
	case in.fields["aof_enabled"] == "1" && in.fields["aof_last_write_status"] == "err":
		h.Add("warn", apitypes.InsightOverview, apitypes.HintPersistenceFailing, "", "Append-only file failing",
			"the last write to the append-only file failed (often a full disk); Redis refuses writes until it succeeds", "")
	}
	return ov
}

// statements are the command stats, the top req.Limit by req.Order,
// with each command's slowest call in the slow log. Their text is the
// command's name: INFO counts commands, not statements with values.
func statements(c *conn, in info, req apitypes.DataInsightsRequest) ([]apitypes.DataStatement, int64, error) {
	slowest := map[string]float64{}
	v, err := c.Do("SLOWLOG", "GET", slowlogEntries)
	if err != nil {
		return nil, 0, err
	}
	if err := v.Err(); err != nil {
		return nil, 0, accessError(err)
	}
	for _, entry := range v.Arr {
		// id, time, microseconds, arguments, ...
		if len(entry.Arr) < 4 || len(entry.Arr[3].Arr) == 0 {
			continue
		}
		args := entry.Arr[3].Arr
		name := strings.ToLower(args[0].Str)
		if len(args) > 1 {
			if sub := name + "|" + strings.ToLower(args[1].Str); in.cmds[sub] != (cmdStat{}) {
				name = sub
			}
		}
		slowest[name] = max(slowest[name], float64(entry.Arr[2].Int)/1000)
	}
	out := make([]apitypes.DataStatement, 0, len(in.cmds))
	for name, st := range in.cmds {
		if st.calls == 0 {
			continue
		}
		out = append(out, apitypes.DataStatement{
			ID:      name,
			Text:    strings.ToUpper(strings.ReplaceAll(name, "|", " ")),
			Calls:   st.calls,
			TotalMs: st.usec / 1000,
			MeanMs:  st.usec / 1000 / float64(st.calls),
			MaxMs:   slowest[name],
		})
	}
	by := map[apitypes.DataStatementOrder]func(apitypes.DataStatement) float64{
		apitypes.OrderTotal: func(s apitypes.DataStatement) float64 { return s.TotalMs },
		apitypes.OrderMean:  func(s apitypes.DataStatement) float64 { return s.MeanMs },
		apitypes.OrderCalls: func(s apitypes.DataStatement) float64 { return float64(s.Calls) },
		// Commands return no rows: rows sorts by calls.
		apitypes.OrderRows: func(s apitypes.DataStatement) float64 { return float64(s.Calls) },
	}[req.Order]
	if by == nil {
		by = func(s apitypes.DataStatement) float64 { return s.TotalMs }
	}
	slices.SortFunc(out, func(a, b apitypes.DataStatement) int {
		return cmp.Or(cmp.Compare(by(b), by(a)), cmp.Compare(a.ID, b.ID))
	})
	tracked := int64(len(out))
	if req.Limit > 0 && len(out) > req.Limit {
		out = out[:req.Limit]
	}
	return out, tracked, nil
}

// sessionID names a client for Cancel: its ID and the server's start
// (IDs start over after a restart).
func sessionID(id int64, started time.Time) string {
	return strconv.FormatInt(id, 10) + "." + strconv.FormatInt(started.Unix(), 10)
}

// sessions reads CLIENT LIST: clients in MULTI or blocked first, then the
// most recently active, at most DataInsightSessionsLimit. partial: there
// were more (or the list was cut at the reply limit).
func (e *Engine) sessions(c *conn, in info) ([]apitypes.DataSession, bool, error) {
	v, err := c.Do("CLIENT", "LIST")
	if err != nil {
		return nil, false, err
	}
	if err := v.Err(); err != nil {
		return nil, false, accessError(err)
	}
	text := v.Str
	partial := v.Cut()
	if partial {
		text = text[:max(strings.LastIndexByte(text, '\n'), 0)]
	}
	started := in.started()
	type row struct {
		s    apitypes.DataSession
		idle int64
	}
	var rows []row
	for line := range strings.SplitSeq(text, "\n") {
		f := clientFields(line)
		if f["id"] == "" {
			continue
		}
		id, _ := strconv.ParseInt(f["id"], 10, 64)
		idle, _ := strconv.ParseInt(f["idle"], 10, 64)
		at := in.now.Add(-time.Duration(idle) * time.Second)
		s := apitypes.DataSession{
			ID: sessionID(id, started), PID: id, User: f["user"],
			Application: cmp.Or(f["name"], f["lib-name"]),
			Client:      hostOnly(f["addr"]),
			Kind:        "client", State: "idle", QueryAt: &at, StateAt: &at,
			Text: strings.ToUpper(strings.ReplaceAll(f["cmd"], "|", " ")),
		}
		flags := f["flags"]
		switch {
		case f["name"] == e.app || slices.Contains([]string{string(e.roles.Reader), string(e.roles.Writer), string(e.roles.Monitor)}, f["user"]):
			s.Kind = "dock"
		case strings.ContainsAny(flags, "SMP"):
			s.Kind = "background" // replication links, pub/sub
		}
		switch {
		case strings.Contains(flags, "x"):
			s.State = "idle_in_transaction" // queued commands, waiting for EXEC
		case strings.Contains(flags, "b"):
			s.State, s.Wait = "other", "blocked on "+s.Text
		case idle == 0:
			s.State = "active"
		}
		if s.Text == "NULL" {
			s.Text = ""
		}
		rows = append(rows, row{s, idle})
	}
	rank := map[string]int{"idle_in_transaction": 0, "other": 1, "active": 2, "idle": 3}
	slices.SortStableFunc(rows, func(a, b row) int {
		return cmp.Or(cmp.Compare(rank[a.s.State], rank[b.s.State]), cmp.Compare(a.idle, b.idle))
	})
	if len(rows) > apitypes.DataInsightSessionsLimit {
		rows, partial = rows[:apitypes.DataInsightSessionsLimit], true
	}
	out := make([]apitypes.DataSession, len(rows))
	for i, r := range rows {
		out[i] = r.s
	}
	return out, partial, nil
}

// clientFields reads one CLIENT LIST line, "id=7 addr=... name=app".
func clientFields(line string) map[string]string {
	f := map[string]string{}
	for part := range strings.FieldsSeq(line) {
		if k, v, ok := strings.Cut(part, "="); ok {
			f[k] = v
		}
	}
	return f
}

// hostOnly is an address without its port ("10.0.0.5:51234").
func hostOnly(addr string) string {
	if i := strings.LastIndexByte(addr, ':'); i > 0 {
		return addr[:i]
	}
	return addr
}

// prefix is a key's part up to and with its first ':' ("" without one).
func prefix(key string) string {
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return key[:i+1]
	}
	return ""
}

// prefixes samples each database's keys as the reader (sampleSteps SCAN
// steps, sampleKeys in all, within sampleTime), sizes them with MEMORY
// USAGE and groups them by prefix, scaled to each database's key count.
// The sample's big keys are hints; sensitive keys show by pattern to
// masked callers.
func (e *Engine) prefixes(ctx context.Context, t data.Target, c data.Call, in info, h *data.Hints) ([]apitypes.DataKeyPrefix, error) {
	cn, err := e.acquire(ctx, t, data.RoleReader)
	if err != nil {
		return nil, err
	}
	defer e.release(cn)
	deadline := time.Now().Add(sampleTime)
	name := func(key string) string {
		if c.Masked {
			for _, p := range c.SensitiveKeys {
				if data.KeyMatch(p, key) {
					return p
				}
			}
		}
		if !utf8.ValidString(key) {
			return `\x` + hex.EncodeToString([]byte(key))
		}
		return key
	}
	left := sampleKeys
	var out []apitypes.DataKeyPrefix
	for _, db := range in.dbs {
		if db.Keys == 0 || left <= 0 || time.Now().After(deadline) {
			continue
		}
		var keys []string
		var sizes []int64
		err := runOn(ctx, cn, func(rc *conn) (err error) {
			keys, sizes, err = sample(rc, db.DB, min(left, sampleKeys/len(in.dbs)+1), deadline)
			return err
		})
		if err != nil {
			return out, err
		}
		left -= len(keys)
		if len(keys) == 0 {
			continue
		}
		groups := map[string]*apitypes.DataKeyPrefix{}
		for i, k := range keys {
			p := prefix(k)
			g := groups[p]
			if g == nil {
				g = &apitypes.DataKeyPrefix{DB: db.DB, Prefix: name(p)}
				groups[p] = g
			}
			g.SampledKeys++
			g.SampledBytes += sizes[i]
			if sizes[i] > g.BiggestBytes {
				g.Biggest, g.BiggestBytes = name(k), sizes[i]
			}
			if sizes[i] >= bigKeyNote {
				h.Add("note", apitypes.InsightKeys, apitypes.HintBigKey, name(k), "Big key",
					fmt.Sprintf("%s in db %d; commands that read or delete it whole (DEL, HGETALL, SMEMBERS) block the server while they run", data.BytesText(sizes[i]), db.DB), "")
			}
		}
		scale := float64(db.Keys) / float64(len(keys))
		for _, g := range groups {
			g.EstimatedKeys = int64(float64(g.SampledKeys) * scale)
			g.EstimatedBytes = int64(float64(g.SampledBytes) * scale)
			out = append(out, *g)
		}
	}
	slices.SortFunc(out, func(a, b apitypes.DataKeyPrefix) int {
		return cmp.Or(cmp.Compare(b.EstimatedBytes, a.EstimatedBytes), cmp.Compare(a.DB, b.DB), cmp.Compare(a.Prefix, b.Prefix))
	})
	if len(out) > prefixesLimit {
		out = out[:prefixesLimit]
	}
	return out, nil
}

// sample SCANs db for up to n keys in sampleSteps steps or until
// deadline, then sizes them in one pipeline.
func sample(c *conn, db, n int, deadline time.Time) ([]string, []int64, error) {
	if err := c.selectDB(db); err != nil {
		return nil, nil, err
	}
	var keys []string
	cursor := "0"
	for step := 0; step < sampleSteps && len(keys) < n && time.Now().Before(deadline); step++ {
		v, err := c.Do("SCAN", cursor, "COUNT", "100")
		if err != nil {
			return nil, nil, err
		}
		if err := v.Err(); err != nil {
			return nil, nil, accessError(err)
		}
		if len(v.Arr) != 2 {
			return nil, nil, fmt.Errorf("%w: unexpected SCAN reply", data.ErrUnreachable)
		}
		for _, k := range v.Arr[1].Arr {
			if len(keys) < n {
				keys = append(keys, k.Str)
			}
		}
		if cursor = v.Arr[0].Str; cursor == "0" {
			break
		}
	}
	if len(keys) == 0 {
		return nil, nil, nil
	}
	cmds := make([][]string, len(keys))
	for i, k := range keys {
		cmds[i] = []string{"MEMORY", "USAGE", k, "SAMPLES", "5"}
	}
	replies, err := c.Pipeline(cmds...)
	if err != nil {
		return nil, nil, err
	}
	sizes := make([]int64, len(keys))
	for i, r := range replies {
		if r.Kind == resp.Int {
			sizes[i] = r.Int // a key gone since the SCAN is Null: 0
		}
	}
	return keys, sizes, nil
}

// EnableQueryStats: Redis always counts commands.
func (e *Engine) EnableQueryStats(context.Context, data.Target, data.Credentials) (apitypes.DataQueryStatsState, bool, error) {
	return apitypes.QueryStatsOn, false, nil
}

// ResetQueryStats sets INFO's counters (commands, hits, evictions) back
// to zero and empties the slow log, as the owner.
func (e *Engine) ResetQueryStats(ctx context.Context, t data.Target, owner data.Credentials) error {
	c, _, err := e.ownerConn(ctx, t, owner)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	replies, err := c.Pipeline([]string{"CONFIG", "RESETSTAT"}, []string{"SLOWLOG", "RESET"})
	if err != nil {
		return fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	for _, r := range replies {
		if err := r.Err(); err != nil {
			return accessError(err)
		}
	}
	return nil
}

// errNoCancel: Redis runs one command at a time and can't stop it.
var errNoCancel = fmt.Errorf("%w: Redis can't cancel a running command; end the client instead (terminate)", data.ErrInvalid)

// Cancel ends a client (CLIENT KILL ID) as the owner; cancelling a
// command is refused. The session ID binds the client to the server's
// run, so a reused ID after a restart is never hit.
func (e *Engine) Cancel(ctx context.Context, t data.Target, owner data.Credentials, session string, _ *time.Time, terminate bool) (data.CancelResult, error) {
	if !terminate {
		return data.CancelResult{}, errNoCancel
	}
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
	in, err := readInfo(c)
	if err != nil {
		return data.CancelResult{}, err
	}
	if d := in.started().Unix() - start; d < -2 || d > 2 {
		return data.CancelResult{}, nil // the server restarted since
	}
	replies, err := c.Pipeline([]string{"CLIENT", "LIST", "ID", idText}, []string{"CLIENT", "KILL", "ID", idText})
	if err != nil {
		return data.CancelResult{}, fmt.Errorf("%w: %w", data.ErrUnreachable, err)
	}
	for _, r := range replies {
		if err := r.Err(); err != nil {
			return data.CancelResult{}, accessError(err)
		}
	}
	f := clientFields(replies[0].Str)
	if replies[1].Int == 0 || f["id"] != strconv.FormatInt(id, 10) {
		return data.CancelResult{}, nil // gone
	}
	age, _ := strconv.ParseInt(f["age"], 10, 64)
	return data.CancelResult{
		Found: true, User: f["user"], Application: cmp.Or(f["name"], f["lib-name"]),
		Text: strings.ToUpper(strings.ReplaceAll(f["cmd"], "|", " ")), Running: time.Duration(age) * time.Second,
	}, nil
}
