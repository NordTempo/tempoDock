package core

import (
	"bytes"
	"compress/gzip"
	"encoding/json/v2"
	"io"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

func TestSiteReplays(t *testing.T) {
	h := newHarness(t)
	site, svc, rel := countingSite(h)
	on, rate := true, 50
	if _, err := h.c.UpdateSite(h.ctx, testSystem, "shop", "shop.example.com", apitypes.UpdateSiteRequest{
		Settings: apitypes.SiteSettings{Replay: &on, ReplaySampleRate: &rate}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the ingest to record half the page loads", func() bool { return h.rt.AnalyticsSiteRules()[site].ReplayPercent == 50 })

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`[{"type":4,"timestamp":1}]`))
	_ = zw.Close()
	t0 := now().UnixMilli()
	line, _ := json.Marshal(analytics.ReplayChunk{Time: t0, Site: site, Service: svc, Release: rel, Path: "/cart", Visitor: 1,
		Segment: 0xabc, First: t0, Last: t0, Clock: t0, Events: 1, Browser: "Firefox", Data: gz.Bytes()})
	h.rt.AppendReplay(line)
	eventually(t, "the chunk forwarded from the replay spool", func() bool { return h.rt.Acked(node.SpoolReplay) == 1 })

	list, err := h.c.SiteReplays(h.ctx, testSystem, "shop", "shop.example.com", StatsQuery{Range: "24h"}, false, 100)
	if err != nil || !list.Recording || list.SampleRate != 50 || len(list.Replays) != 1 {
		t.Fatalf("replays = %+v, %v", list, err)
	}
	r := list.Replays[0]
	if r.ID != "0000000000000abc" || r.EntryPath != "/cart" || r.Release != "web r1" {
		t.Errorf("replay = %+v", r)
	}
	if _, err := h.c.SiteReplays(h.ctx, testSystem, "shop", "shop.example.com",
		StatsQuery{Filters: []analytics.Filter{{Dimension: analytics.DimReferrer, Op: analytics.OpIs, Value: "x"}}}, false, 10); CodeOf(err) != apitypes.CodeValidation {
		t.Errorf("replays by referrer: %v", err)
	}
	d, err := h.c.SiteReplay(h.ctx, testSystem, "shop", "shop.example.com", r.ID)
	if err != nil || len(d.SegmentList) != 1 {
		t.Fatalf("replay = %+v, %v", d, err)
	}
	body, _, err := h.c.SiteReplayEvents(h.ctx, testSystem, "shop", "shop.example.com", r.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	_ = body.Close()
	if string(got) != `{"segment":"0000000000000abc"}`+"\n"+`[{"type":4,"timestamp":1}]`+"\n" {
		t.Errorf("events = %q", got)
	}
	// Watching is audited; listing isn't.
	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Project: "shop", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	watched := 0
	for _, e := range events {
		if e.Action == "analytics.replay_view" && strings.Contains(e.Summary, r.ID) {
			watched++
		}
	}
	if watched != 1 {
		t.Errorf("%d audit events for watching the replay, want 1", watched)
	}

	outsider := Actor{Name: "other", Kind: "token", Scope: apitypes.ScopeAdmin, OrgID: 2}
	if _, _, err := h.c.SiteReplayEvents(h.ctx, outsider, "shop", "shop.example.com", r.ID, false); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("another org watched the replay: %v", err)
	}
}
