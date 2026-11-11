package mcpkit

import (
	"context"
	"fmt"
	"strings"
	"time"
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
		"A database's health: overview, slowest queries, sessions and blocking, tables, indexes (Redis: keys). Hints first.",
		ReadOnly, func(ctx context.Context, c *Call[A], loc L, in struct {
			Views []apitypes.DataInsightView  `json:"views,omitempty" enum:"overview,queries,sessions,tables,indexes,keys" doc:"overview, queries, sessions, tables, indexes or keys (Redis). Default: all."`
			Order apitypes.DataStatementOrder `json:"order,omitempty" enum:"total,mean,calls,rows"`
			Limit int                         `json:"limit,omitempty" doc:"Default 10."`
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

// addCancelTool registers data_cancel over a host's panel operations.
func addCancelTool[A, R, L any](s *Server[A], ops datapanel.Ops[A, R], locate func(*Call[A], L) (R, error), name func(R) string) {
	addLocatedTool(s, "data_cancel", "Cancel a query or end a session",
		"Cancel a session's query, or terminate the session (from data_insights' sessions).",
		Destructive, func(ctx context.Context, c *Call[A], loc L, in struct {
			Session        string     `json:"session"`
			QueryStartedAt *time.Time `json:"query_started_at,omitempty" doc:"The session's query_at."`
			Terminate      bool       `json:"terminate,omitempty"`
		}) (Reply, error) {
			ref, err := locate(c, loc)
			if err != nil {
				return Reply{}, err
			}
			res, err := ops.CancelDataSession(ctx, c.Actor, ref, in.Session, apitypes.DataCancelRequest{QueryStartedAt: in.QueryStartedAt, Terminate: in.Terminate})
			if err != nil {
				return Reply{}, err
			}
			verb := "Cancelled the query of"
			if res.Terminated {
				verb = "Ended the session of"
			}
			return Reply{Summary: fmt.Sprintf("%s %s (%s) in %s, running %s.", verb, res.Application, res.User, name(ref),
				(time.Duration(res.RunningMs) * time.Millisecond).Round(time.Second)), Data: res}, nil
		})
}

// trimInsights cuts the lists to what a model needs: sessions that aren't
// idle, the largest tables and key prefixes, the indexes a hint names, texts at 1 KB.
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
	if len(ins.Keys) > insightTablesMax {
		ins.Keys = ins.Keys[:insightTablesMax]
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
		switch {
		case p.Full:
			parts = append(parts, "every connection is taken")
		case ins.Engine == apitypes.EngineRedis:
			mem := fmt.Sprintf("%d of %d connections in use; %d MB of memory used", p.Used, p.Max, ov.Bytes>>20)
			if ov.MaxBytes > 0 {
				mem += fmt.Sprintf(" of %d MB (%s)", ov.MaxBytes>>20, ov.EvictionPolicy)
			}
			parts = append(parts, mem)
		default:
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
