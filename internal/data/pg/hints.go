package pg

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
)

// Hint thresholds. They flag what usually needs a person's look; nothing
// acts on them. Notes are worth knowing, warnings worth doing something
// about soon.
const (
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
	var h data.Hints
	add := h.Add
	countedFor := time.Duration(0)
	if in.Since != nil {
		countedFor = now.Sub(*in.Since)
	}

	if ov := in.Overview; ov != nil {
		h.Connections(ov.Connections)
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
					fmt.Sprintf("wrote %s to temporary files over %d calls; it may need an index or more work_mem", data.BytesText(s.TempBytes), s.Calls), "")
			}
		}
	}

	if in.Sessions != nil {
		h.Sessions(in.Sessions, now, data.SessionWords{ID: "pid", Cleanup: "vacuum"})
	}

	for _, t := range in.Tables {
		name := t.Schema + "." + t.Name
		ident := pgx.Identifier{t.Schema, t.Name}.Sanitize()
		if t.DeadRows > deadRowsMin && float64(t.DeadRows) > deadRowsShare*float64(max(t.EstimatedRows, 1)) &&
			(t.VacuumedAt == nil || now.Sub(*t.VacuumedAt) > deadRowsVacuumAge) {
			add("warn", apitypes.InsightTables, apitypes.HintDeadRows, name, "Dead rows",
				fmt.Sprintf("%s dead rows next to %s live; last vacuumed %s", data.Count(t.DeadRows), data.Count(t.EstimatedRows), data.AgoText(t.VacuumedAt, now)),
				"VACUUM (ANALYZE) "+ident+";")
		}
		if t.AnalyzedAt == nil && t.EstimatedRows >= notAnalyzedRows {
			add("note", apitypes.InsightTables, apitypes.HintNotAnalyzed, name, "Never analysed",
				fmt.Sprintf("about %s rows and no statistics, so plans guess", data.Count(t.EstimatedRows)), "ANALYZE "+ident+";")
		}
		if t.SeqScans >= fullScansMin && t.SeqRowsRead/t.SeqScans >= fullScanRows && t.EstimatedRows >= fullScanRows && t.SeqScans > t.IndexScans {
			add("note", apitypes.InsightTables, apitypes.HintFullScans, name, "Likely missing index",
				fmt.Sprintf("%s full scans reading %s rows each, %s index scans", data.Count(t.SeqScans), data.Count(t.SeqRowsRead/t.SeqScans), data.Count(t.IndexScans)), "")
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
				fmt.Sprintf("no scans in %s; %s", data.DurText(countedFor), sizeText(x.Bytes)), drop)
		}
	}

	return h.Sorted()
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

// millions is n in millions: "1,240M".
func millions(n int64) string { return data.Count(n/1_000_000) + "M" }

// sizeText is data.BytesText, or "size unknown (table locked)".
func sizeText(n *int64) string {
	if n == nil {
		return "size unknown, its table is locked"
	}
	return data.BytesText(*n)
}
