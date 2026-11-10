package mcpkit

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/datapanel"
)

// Caps of data_insights, tighter than the API's: results go into a
// model's context.
const (
	insightQueriesDefault = 10
	insightQueriesMax     = 20
	insightSessionsMax    = 50
	insightTablesMax      = 20
	insightTextBytes      = 1024
)

// addInsightsTool registers data_insights over a host's panel operations.
func addInsightsTool[A, R, L any](s *Server[A], ops datapanel.Ops[A, R], locate func(*Call[A], L) (R, error), name func(R) string) {
	addLocatedTool(s, "data_insights", "How the database is doing",
		"Read a database's health from its own statistics, live: connections against the limit, cache hit ratio and size (overview); the slowest or most frequent statements (queries); "+
			"sessions that are running, idle in a transaction or blocked, with who blocks them (sessions); the largest tables and their dead rows and full scans (tables); index sizes and use (indexes). "+
			"Hints come first: what needs a look, with a statement to copy where one helps (Dock never runs it). Use it when an app is slow, a deploy failed on database errors or connections run out. "+
			"Overview, tables and indexes need data access level schema; queries and sessions read. Statement values are redacted unless you see sensitive columns. Nothing here changes the database.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			Views []apitypes.DataInsightView  `json:"views,omitempty" enum:"overview,queries,sessions,tables,indexes" doc:"Any of overview, queries, sessions, tables, indexes. Default: all you may see."`
			Order apitypes.DataStatementOrder `json:"order,omitempty" enum:"total,mean,calls,rows" doc:"Queries by total (default), mean, calls or rows."`
			Limit int                         `json:"limit,omitempty" doc:"Statements to return. Default 10, at most 20."`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			switch {
			case in.Limit <= 0:
				in.Limit = insightQueriesDefault
			case in.Limit > insightQueriesMax:
				in.Limit = insightQueriesMax
			}
			ins, err := ops.DataInsights(ctx, c.Actor, ref, apitypes.DataInsightsRequest{Views: in.Views, Order: in.Order, Limit: in.Limit})
			if err != nil {
				return Reply{}, err
			}
			return Reply{Summary: insightsSummary(name(ref), ins), Data: trimInsights(ins)}, nil
		})
}

// trimInsights cuts the lists to what a model needs: sessions that aren't
// idle, the largest tables, the indexes a hint names, texts at 1 KB.
func trimInsights(ins apitypes.DataInsights) apitypes.DataInsights {
	var sessions []apitypes.DataSession
	for _, s := range ins.Sessions {
		if s.State != "idle" && len(sessions) < insightSessionsMax {
			s.Text = cutText(s.Text)
			sessions = append(sessions, s)
		}
	}
	ins.Sessions = sessions
	queries := make([]apitypes.DataStatement, len(ins.Queries))
	for i, q := range ins.Queries {
		q.Text = cutText(q.Text)
		queries[i] = q
	}
	ins.Queries = queries
	if len(ins.Tables) > insightTablesMax {
		ins.Tables = ins.Tables[:insightTablesMax]
	}
	named := map[string]bool{}
	for _, h := range ins.Hints {
		if h.View == apitypes.InsightIndexes {
			named[h.Subject] = true
		}
	}
	var indexes []apitypes.DataIndexStats
	for _, x := range ins.Indexes {
		if named[x.Schema+"."+x.Name] {
			indexes = append(indexes, x)
		}
	}
	ins.Indexes = indexes
	return ins
}

// cutText cuts a statement text at insightTextBytes on a character
// boundary.
func cutText(s string) string {
	if len(s) <= insightTextBytes {
		return s
	}
	cut := insightTextBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// insightsSummary says the vital numbers and the warnings in one line.
func insightsSummary(db string, ins apitypes.DataInsights) string {
	var parts []string
	if ov := ins.Overview; ov != nil {
		p := ov.Connections
		if p.Full {
			parts = append(parts, "every connection is taken")
		} else {
			parts = append(parts, fmt.Sprintf("%d of %d connections in use (%d idle in a transaction, %d waiting on locks)", p.Used, p.Max, p.IdleInTransaction, p.Waiting))
		}
		if ov.CacheHit != nil {
			parts = append(parts, fmt.Sprintf("cache hit %.1f%%", *ov.CacheHit*100))
		}
	}
	warn, note := 0, 0
	var titles []string
	for _, h := range ins.Hints {
		if h.Level == "warn" {
			warn++
			titles = append(titles, h.Title+" ("+h.Subject+")")
		} else {
			note++
		}
	}
	parts = append(parts, fmt.Sprintf("%d warnings, %d notes", warn, note))
	s := db + ": " + strings.Join(parts, "; ")
	if len(titles) > 0 {
		s += ". Warnings: " + strings.Join(titles, "; ")
	}
	for _, u := range ins.Unavailable {
		s += fmt.Sprintf(". %s: %s", u.View, u.Reason)
	}
	return s + "."
}
