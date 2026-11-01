package store

import (
	"context"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
)

// boom is visitor's error of group g at day0 plus offset.
func boom(visitor uint64, offset time.Duration, g uint64, msg string) analytics.Event {
	e := at(visitor, offset, "/cart")
	e.Kind, e.Name, e.Release = analytics.KindError, msg, 100+int64(offset/time.Hour)
	e.Error = &analytics.ErrorInfo{Type: "TypeError", Stack: "at render (/app.js:1:2)", Frame: "render (/app.js)", Group: g}
	return e
}

func TestErrorGroups(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	_, news, err := s.Write(ctx, "server-1", 1, []analytics.Event{
		at(1, 0, "/cart"), boom(1, time.Minute, 7, "x of 1"), boom(1, 2*time.Minute, 7, "x of 2"),
		boom(2, 3*time.Minute, 9, "other"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(news) != 2 || news[0].Group != groupHex(7) || news[0].Message != "x of 1" || news[1].Group != groupHex(9) {
		t.Fatalf("new groups %+v", news)
	}
	// A later batch with group 7 again: not new.
	_, news, err = s.Write(ctx, "server-1", 2, []analytics.Event{boom(3, 2*time.Hour, 7, "x of 3")}, nil)
	if err != nil || len(news) != 0 {
		t.Fatalf("second write: news %+v, err %v", news, err)
	}
	// Errors aren't events: the dashboard doesn't count them.
	if o, err := s.Overview(ctx, dayQuery); err != nil || o.Events != 0 || o.Pageviews != 1 || o.Visitors != 1 {
		t.Errorf("overview with errors: %+v, %v", o, err)
	}
	groups, err := s.Errors(ctx, analytics.ErrorsQuery{Query: dayQuery, Bucket: analytics.BucketHour})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups %+v", groups)
	}
	g := groups[0]
	if g.Group != groupHex(7) || g.Count != 3 || g.Sessions != 1 || g.Message != "x of 3" || g.FirstRelease != 100 ||
		g.FirstSeen != day0.Add(time.Minute).UnixMilli() || g.LastSeen != day0.Add(2*time.Hour).UnixMilli() || len(g.Series) != 2 {
		t.Errorf("group 7: %+v", g)
	}
	d, err := s.Error(ctx, analytics.ErrorQuery{Query: dayQuery, Group: groupHex(7)})
	if err != nil {
		t.Fatal(err)
	}
	if d.Count != 3 || d.Stack == "" || len(d.Recent) != 3 || d.Recent[2].Session == "" || d.Recent[0].Session != "" ||
		len(d.Pages) != 1 || d.Pages[0].Count != 3 || len(d.Releases) != 2 {
		t.Errorf("detail %+v", d)
	}
	if _, err := s.Errors(ctx, analytics.ErrorsQuery{Query: analytics.Query{Site: 1, From: dayQuery.From, To: dayQuery.To,
		Filters: []analytics.Filter{{Dimension: analytics.DimChannel, Op: analytics.OpIs, Value: "direct"}}}}); err == nil {
		t.Error("errors filtered by channel, which they don't record")
	}
	filtered, err := s.Errors(ctx, analytics.ErrorsQuery{Query: analytics.Query{Site: 1, From: dayQuery.From, To: dayQuery.To,
		Filters: []analytics.Filter{{Dimension: analytics.DimRelease, Op: analytics.OpIs, Value: "102"}}}})
	if err != nil || len(filtered) != 1 || filtered[0].Count != 1 {
		t.Errorf("filtered by release: %+v, %v", filtered, err)
	}

	// Retention: errors go, and the groups not seen since.
	res, err := s.Prune(ctx, analytics.PruneQuery{Site: 1, Before: day0.UnixMilli(), ErrorsBefore: day0.Add(time.Hour).UnixMilli()})
	if err != nil || res.ErrorsDeleted != 3 {
		t.Fatalf("prune: %+v, %v", res, err)
	}
	if _, err := s.Error(ctx, analytics.ErrorQuery{Query: dayQuery, Group: groupHex(9)}); err == nil {
		t.Error("group 9, last seen before the cut, is still there")
	}
	if d, err := s.Error(ctx, analytics.ErrorQuery{Query: dayQuery, Group: groupHex(7)}); err != nil || d.Count != 1 {
		t.Errorf("group 7 after the prune: %+v, %v", d, err)
	}
}

func TestVitals(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	var events []analytics.Event
	for i := range 20 {
		e := at(uint64(i), time.Duration(i)*time.Minute, "/")
		e.Kind, e.Device = analytics.KindVitals, "desktop"
		if i%2 == 1 {
			e.Path, e.Device = "/slow", "mobile"
		}
		lcp := 1000.0 + 300*float64(i)
		e.Vitals = &analytics.Vitals{LCP: lcp, INP: -1, CLS: 0.05, FCP: 800, TTFB: 200}
		events = append(events, at(uint64(i), time.Duration(i)*time.Minute, e.Path), e)
	}
	write(t, s, "server-1", 1, events...)
	v, err := s.Vitals(ctx, analytics.VitalsQuery{Query: dayQuery, Dimensions: []analytics.Dimension{analytics.DimDevice, analytics.DimPage}, Metric: analytics.VitalLCP, Bucket: analytics.BucketDay})
	if err != nil {
		t.Fatal(err)
	}
	a := v.All
	if a.Loads != 20 || a.LCP.Samples != 20 || a.INP.Samples != 0 || a.INP.P75 != -1 || a.CLS.Good != 20 {
		t.Errorf("all: %+v", a)
	}
	// LCPs 1000 to 6700 by 300: 6 of them at most 2500 ms, 9 over 4000.
	if a.LCP.Good != 6 || a.LCP.Poor != 9 || a.LCP.P75 < 5000 || a.LCP.P75 > 5600 {
		t.Errorf("LCP %+v", a.LCP)
	}
	if d := v.Breakdowns[analytics.DimDevice]; len(d) != 2 || d[0].Loads != 10 || d[0].LCP.Samples != 10 || len(v.Breakdowns[analytics.DimPage]) != 2 ||
		len(v.Series) != 1 || v.Series[0].Samples != 20 {
		t.Errorf("breakdowns %+v, series %+v", v.Breakdowns, v.Series)
	}
	// Page loads' vitals don't count as events.
	if o, err := s.Overview(ctx, dayQuery); err != nil || o.Events != 0 || o.Pageviews != 20 {
		t.Errorf("overview with vitals: %+v, %v", o, err)
	}
}
