package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// goalSQL is the condition an event meets for a goal, binding its value
// to p: a page view whose path matches the pattern (* is any run of
// characters), or a custom event by name.
func goalSQL(p *params, g analytics.GoalSpec) (string, error) {
	switch g.Kind {
	case analytics.GoalPage:
		if g.Match == "" {
			return "", badQuery("a page goal needs a path")
		}
		if !strings.Contains(g.Match, "*") {
			return fmt.Sprintf("(kind = %d AND path = %s)", analytics.KindPageview, p.add(g.Match)), nil
		}
		like := strings.ReplaceAll(escapeLike(g.Match), "*", "%")
		return fmt.Sprintf("(kind = %d AND path LIKE %s ESCAPE '\\')", analytics.KindPageview, p.add(like)), nil
	case analytics.GoalEvent:
		if g.Match == "" {
			return "", badQuery("an event goal needs the event's name")
		}
		return fmt.Sprintf("(kind = %d AND name = %s)", analytics.KindEvent, p.add(g.Match)), nil
	}
	return "", badQuery("a goal is a page or an event, not %q", g.Kind)
}

// Goals counts each of q's goals over q's events: one scan for all of
// them. Visitors and sessions are HyperLogLog counts (about 2% off), as
// for every filtered query.
func (s *Store) Goals(ctx context.Context, q analytics.GoalsQuery) ([]analytics.GoalResult, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if err := check(q.Query); err != nil {
		return nil, err
	}
	if len(q.Goals) == 0 || len(q.Goals) > analytics.MaxGoals {
		return nil, badQuery("count 1 to %d goals", analytics.MaxGoals)
	}
	var p params
	w := where(&p, q.Query, q.From, q.To)
	var cols, either []string
	for _, g := range q.Goals {
		cond, err := goalSQL(&p, g)
		if err != nil {
			return nil, err
		}
		either = append(either, cond)
		cols = append(cols, "approx_count_distinct(visitor_id) FILTER (WHERE "+cond+")",
			"approx_count_distinct(session_id) FILTER (WHERE "+cond+")", "count(*) FILTER (WHERE "+cond+")")
	}
	row := s.db.QueryRowContext(ctx, "SELECT "+strings.Join(cols, ", ")+" FROM events WHERE "+w+" AND ("+strings.Join(either, " OR ")+")", p...)
	out := make([]analytics.GoalResult, len(q.Goals))
	dest := make([]any, 0, 3*len(out))
	for i := range out {
		dest = append(dest, &out[i].Visitors, &out[i].Sessions, &out[i].Count)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	return out, nil
}

// Funnel counts the sessions of q's events that reach its steps in order:
// a session reaches a step when it has a matching event at or after the
// time it reached the step before. Step by step, each a join on the last.
func (s *Store) Funnel(ctx context.Context, q analytics.FunnelQuery) ([]analytics.FunnelStep, error) {
	s.gate.RLock()
	defer s.gate.RUnlock()
	if err := check(q.Query); err != nil {
		return nil, err
	}
	if len(q.Steps) < 2 || len(q.Steps) > analytics.MaxFunnelSteps {
		return nil, badQuery("a funnel has 2 to %d steps", analytics.MaxFunnelSteps)
	}
	var p params
	w := where(&p, q.Query, q.From, q.To)
	conds := make([]string, len(q.Steps))
	for i, st := range q.Steps {
		c, err := goalSQL(&p, st)
		if err != nil {
			return nil, err
		}
		conds[i] = c
	}
	var b strings.Builder
	b.WriteString("WITH ev AS (SELECT session_id, visitor_id, ts, kind, path, name FROM events WHERE " + w + " AND (" + strings.Join(conds, " OR ") + "))")
	for i, c := range conds {
		n := strconv.Itoa(i)
		if i == 0 {
			b.WriteString(",\ns0 AS (SELECT session_id, any_value(visitor_id) AS v, min(ts) AS t FROM ev WHERE " + c + " GROUP BY session_id)")
			continue
		}
		prev := "s" + strconv.Itoa(i-1)
		b.WriteString(",\ns" + n + " AS (SELECT ev.session_id, any_value(" + prev + ".v) AS v, min(ev.ts) AS t FROM ev JOIN " + prev +
			" USING (session_id) WHERE " + c + " AND ev.ts >= " + prev + ".t GROUP BY ev.session_id)")
	}
	var sel []string
	for i := range conds {
		n := "s" + strconv.Itoa(i)
		sel = append(sel, "(SELECT count(*) FROM "+n+")", "(SELECT approx_count_distinct(v) FROM "+n+")")
	}
	b.WriteString("\nSELECT " + strings.Join(sel, ", "))
	out := make([]analytics.FunnelStep, len(q.Steps))
	dest := make([]any, 0, 2*len(out))
	for i := range out {
		dest = append(dest, &out[i].Sessions, &out[i].Visitors)
	}
	if err := s.db.QueryRowContext(ctx, b.String(), p...).Scan(dest...); err != nil {
		return nil, err
	}
	return out, nil
}
