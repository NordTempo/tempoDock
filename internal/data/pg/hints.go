package pg

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Hint thresholds. They flag what usually needs a person's look; nothing
// acts on them. Notes are worth knowing, warnings worth doing something
// about soon.
const (
	connectionsNote   = 0.80
	connectionsWarn   = 0.95
	idleInTxWarn      = time.Minute
	blockedWarn       = 5 * time.Second
	longQueryNote     = time.Minute
	longQueryWarn     = 10 * time.Minute
	unusedIndexBytes  = 1 << 20
	unusedIndexAge    = 7 * 24 * time.Hour
	fullScansMin      = 100
	fullScanRows      = 10_000
	deadRowsShare     = 0.2
	deadRowsMin       = 10_000
	deadRowsVacuumAge = 24 * time.Hour
	notAnalyzedRows   = 1_000
	cacheLow          = 0.99
	cacheLowReads     = 10_000
	tempSpillBytes    = 64 << 20
	xidAgeWarn        = 1_000_000_000
)

// hints derives what needs a look from the views read, warnings first,
// then in view order. Their words name things and give numbers, never
// values from the data.
func (e *Engine) hints(in *apitypes.DataInsights, ev evidence, now time.Time) []apitypes.DataHint {
	h := []apitypes.DataHint{}
	add := func(level string, view apitypes.DataInsightView, kind apitypes.DataHintKind, subject, title, detail, copy string) {
		h = append(h, apitypes.DataHint{Kind: kind, Level: level, View: view, Subject: subject, Title: title, Detail: detail, Copy: copy})
	}
	countedFor := time.Duration(0)
	if in.Since != nil {
		countedFor = now.Sub(*in.Since)
	}

	if ov := in.Overview; ov != nil {
		p := ov.Connections
		usable := p.Max - p.Reserved
		switch {
		case p.Full:
			add("warn", apitypes.InsightOverview, apitypes.HintConnectionsHigh, "", "Every connection is taken",
				"the database refused a new connection; restarting it frees them", "")
		case usable > 0 && float64(p.Used) >= connectionsWarn*float64(usable):
			add("warn", apitypes.InsightOverview, apitypes.HintConnectionsHigh, "", "Almost out of connections",
				fmt.Sprintf("%d of %d usable connections in use, %d idle", p.Used, usable, p.Idle), "")
		case usable > 0 && float64(p.Used) >= connectionsNote*float64(usable):
			add("note", apitypes.InsightOverview, apitypes.HintConnectionsHigh, "", "Connections running high",
				fmt.Sprintf("%d of %d usable connections in use, %d idle", p.Used, usable, p.Idle), "")
		}
		if ov.TransactionIDAge != nil && *ov.TransactionIDAge > xidAgeWarn {
			add("warn", apitypes.InsightOverview, apitypes.HintTransactionIDAge, "", "Transaction IDs running out",
				fmt.Sprintf("the oldest unfrozen transaction is %s IDs old; Postgres stops writes at about 2,100M", millions(*ov.TransactionIDAge)), "VACUUM (FREEZE);")
		}
		if ov.CacheHit != nil && *ov.CacheHit < cacheLow && ev.blksRead >= cacheLowReads {
			add("note", apitypes.InsightOverview, apitypes.HintCacheLow, "", "Cache hit ratio is low",
				fmt.Sprintf("%.1f%% of block reads came from memory; the data read often is bigger than shared_buffers", *ov.CacheHit*100), "")
		}
		if ov.QueryStats != apitypes.QueryStatsOn {
			detail, copy := "the server doesn't load pg_stat_statements, so slow and frequent queries can't be shown", ""
			if ov.QueryStats == apitypes.QueryStatsNoExtension {
				detail, copy = "pg_stat_statements is loaded but its extension isn't created in this database", "CREATE EXTENSION pg_stat_statements;"
			}
			add("note", apitypes.InsightQueries, apitypes.HintQueryStatsOff, "", "Query stats are off", detail, copy)
		}
	}

	if in.Queries != nil {
		if ev.dealloc > 0 {
			add("note", apitypes.InsightQueries, apitypes.HintQueryStatsDropping, "", "Query stats are dropping statements",
				fmt.Sprintf("%d statements were dropped for lack of room; raise pg_stat_statements.max", ev.dealloc), "")
		}
		for _, s := range in.Queries {
			if s.TempBytes >= tempSpillBytes {
				add("note", apitypes.InsightQueries, apitypes.HintTempSpill, s.ID, "Statement spills to disk",
					fmt.Sprintf("wrote %s to temporary files over %d calls; it may need an index or more work_mem", bytesText(s.TempBytes), s.Calls), "")
			}
		}
	}

	if in.Sessions != nil {
		e.sessionHints(in.Sessions, now, add)
	}

	for _, t := range in.Tables {
		name := t.Schema + "." + t.Name
		ident := pgx.Identifier{t.Schema, t.Name}.Sanitize()
		if t.DeadRows > deadRowsMin && float64(t.DeadRows) > deadRowsShare*float64(max(t.EstimatedRows, 1)) &&
			(t.VacuumedAt == nil || now.Sub(*t.VacuumedAt) > deadRowsVacuumAge) {
			add("warn", apitypes.InsightTables, apitypes.HintDeadRows, name, "Dead rows",
				fmt.Sprintf("%s dead rows next to %s live; last vacuumed %s", count(t.DeadRows), count(t.EstimatedRows), agoText(t.VacuumedAt, now)),
				"VACUUM (ANALYZE) "+ident+";")
		}
		if t.AnalyzedAt == nil && t.EstimatedRows >= notAnalyzedRows {
			add("note", apitypes.InsightTables, apitypes.HintNotAnalyzed, name, "Never analysed",
				fmt.Sprintf("about %s rows and no statistics, so plans guess", count(t.EstimatedRows)), "ANALYZE "+ident+";")
		}
		if t.SeqScans >= fullScansMin && t.SeqRowsRead/t.SeqScans >= fullScanRows && t.EstimatedRows >= fullScanRows && t.SeqScans > t.IndexScans {
			add("note", apitypes.InsightTables, apitypes.HintFullScans, name, "Likely missing index",
				fmt.Sprintf("%s full scans reading %s rows each, %s index scans", count(t.SeqScans), count(t.SeqRowsRead/t.SeqScans), count(t.IndexScans)), "")
		}
	}

	kept := map[string]bool{}
	for _, x := range sortedForDuplicates(in.Indexes) {
		name := x.Schema + "." + x.Name
		drop := "DROP INDEX CONCURRENTLY " + pgx.Identifier{x.Schema, x.Name}.Sanitize() + ";"
		switch {
		case !x.Valid:
			add("warn", apitypes.InsightIndexes, apitypes.HintInvalidIndex, name, "Invalid index",
				"a failed concurrent build left it unusable; writes still maintain it", drop)
			continue
		case x.Duplicate:
			if g := ev.dupGroups[name]; !kept[g] {
				kept[g] = true // the first of a group (constraints first) stays
			} else if !x.Constraint {
				add("warn", apitypes.InsightIndexes, apitypes.HintDuplicateIndex, name, "Duplicate index",
					fmt.Sprintf("same definition as another index on %s; %s", x.Table, sizeText(x.Bytes)), drop)
				continue
			}
		}
		if x.Scans == 0 && !x.Constraint && !x.Unique && x.Bytes != nil && *x.Bytes >= unusedIndexBytes && countedFor >= unusedIndexAge {
			add("note", apitypes.InsightIndexes, apitypes.HintUnusedIndex, name, "Unused index",
				fmt.Sprintf("no scans in %s; %s", durText(countedFor), sizeText(x.Bytes)), drop)
		}
	}

	order := map[apitypes.DataInsightView]int{}
	for i, v := range apitypes.DataInsightViews {
		order[v] = i
	}
	slices.SortStableFunc(h, func(a, b apitypes.DataHint) int {
		return cmp.Or(cmp.Compare(levelRank(a.Level), levelRank(b.Level)), cmp.Compare(order[a.View], order[b.View]))
	})
	return h
}

// sessionHints flags idle transactions, the roots of lock chains and long
// queries. Dock's own and background sessions are left out.
func (e *Engine) sessionHints(sessions []apitypes.DataSession, now time.Time,
	add func(level string, view apitypes.DataInsightView, kind apitypes.DataHintKind, subject, title, detail, copy string)) {
	byID := map[string]apitypes.DataSession{}
	blocks := map[string]int{}
	for _, s := range sessions {
		byID[s.ID] = s
		if s.QueryAt != nil && now.Sub(*s.QueryAt) >= blockedWarn {
			for _, b := range s.BlockedBy {
				blocks[b]++
			}
		}
	}
	who := func(s apitypes.DataSession) string {
		return fmt.Sprintf("%s (%s), pid %d", cmp.Or(s.Application, "no application name"), s.User, s.PID)
	}
	for _, s := range sessions {
		if s.Kind != "client" {
			continue
		}
		n := blocks[s.ID]
		blocking := ""
		if n > 0 {
			blocking = fmt.Sprintf("; blocks %d %s", n, plural(n, "session", "sessions"))
		}
		switch {
		case s.State == "idle_in_transaction" && s.StateAt != nil && now.Sub(*s.StateAt) >= idleInTxWarn:
			add("warn", apitypes.InsightSessions, apitypes.HintIdleInTransaction, s.ID, "Idle in transaction "+durText(now.Sub(*s.StateAt)),
				who(s)+"; holds its locks and holds back vacuum"+blocking, "")
		case n > 0 && len(s.BlockedBy) == 0:
			add("warn", apitypes.InsightSessions, apitypes.HintBlocked, s.ID, fmt.Sprintf("Blocking %d %s", n, plural(n, "session", "sessions")),
				who(s)+" holds a lock others wait for", "")
		case s.State == "active" && len(s.BlockedBy) == 0 && s.QueryAt != nil && now.Sub(*s.QueryAt) >= longQueryNote:
			level := "note"
			if now.Sub(*s.QueryAt) >= longQueryWarn {
				level = "warn"
			}
			add(level, apitypes.InsightSessions, apitypes.HintLongQuery, s.ID, "Running for "+durText(now.Sub(*s.QueryAt)), who(s)+blocking, "")
		}
	}
}

// sortedForDuplicates orders indexes so that, within a duplicate group,
// one backing a constraint comes first and is the one kept.
func sortedForDuplicates(list []apitypes.DataIndexStats) []apitypes.DataIndexStats {
	out := slices.Clone(list)
	slices.SortStableFunc(out, func(a, b apitypes.DataIndexStats) int {
		return cmp.Or(cmp.Compare(a.Schema+"."+a.Table, b.Schema+"."+b.Table), boolRank(b.Constraint)-boolRank(a.Constraint), cmp.Compare(a.Name, b.Name))
	})
	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

func levelRank(l string) int {
	if l == "warn" {
		return 0
	}
	return 1
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// durText is a duration as "6m 12s", "3d 4h".
func durText(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
}

// agoText is "3d 4h ago", or "never".
func agoText(t *time.Time, now time.Time) string {
	if t == nil {
		return "never"
	}
	return durText(now.Sub(*t)) + " ago"
}

// count is a number with thousands separators: 12,431.
func count(n int64) string {
	s := fmt.Sprint(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// millions is n in millions: "1,240M".
func millions(n int64) string { return count(n/1_000_000) + "M" }

// bytesText is a size in KB, MB or GB.
func bytesText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
}

// sizeText is bytesText, or "size unknown (table locked)".
func sizeText(n *int64) string {
	if n == nil {
		return "size unknown, its table is locked"
	}
	return bytesText(*n)
}
