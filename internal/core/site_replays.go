package core

import (
	"context"
	"io"
	"slices"
	"strconv"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Session replays (ADR 0037, phase A3): read with project read
// permission, like the rest of a site's analytics, but every replay
// opened is audited, since a recording can show what a visitor typed or
// read on the page.

// SiteReplays returns a site's replays that began in a range, newest
// first; errorsOnly keeps those with an error. Filters take page,
// country, browser, os, device and release. An unreachable store gives
// none and StoreOnline false.
func (c *Core) SiteReplays(ctx context.Context, a Actor, project, name string, q StatsQuery, errorsOnly bool, limit int) (apitypes.SiteReplays, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's replays")
	if err != nil {
		return apitypes.SiteReplays{}, err
	}
	for _, f := range q.Filters {
		if !slices.Contains(analytics.ReplayDimensions, f.Dimension) {
			return apitypes.SiteReplays{}, invalid("replays filter by page, country, browser, os, device and release; %q isn't one of them", f.Dimension)
		}
	}
	if err := checkFilters(q.Filters); err != nil {
		return apitypes.SiteReplays{}, err
	}
	from, to, err := q.span(now())
	if err != nil {
		return apitypes.SiteReplays{}, err
	}
	defaults, err := c.siteDefaults(ctx)
	if err != nil {
		return apitypes.SiteReplays{}, err
	}
	set := mergeSettings(defaults, siteOverrides(s.site))
	out := apitypes.SiteReplays{Site: s.site.Name, From: from, To: to, Replays: []apitypes.Replay{}, Recording: *set.Replay, SampleRate: *set.ReplaySampleRate}
	cl, _ := c.analyticsClient()
	if cl == nil {
		out.StoreError = analyticsOff
		return out, nil
	}
	list, err := cl.Replays(ctx, analytics.ReplaysQuery{Query: analytics.Query{Site: s.site.ID, From: from, To: to, Filters: q.Filters},
		ErrorsOnly: errorsOnly, Limit: min(max(limit, 1), analytics.MaxReplays)})
	if out.StoreOnline, out.StoreError, err = storeStatus(err); err != nil || !out.StoreOnline {
		return out, err
	}
	ids := make([]int64, len(list))
	for i, r := range list {
		ids[i] = r.Release
	}
	labels := c.releaseLabels(ctx, ids)
	for _, r := range list {
		out.Replays = append(out.Replays, toReplay(r, labels))
	}
	return out, nil
}

// toReplay converts the store's replay.
func toReplay(r analytics.Replay, labels map[int64]string) apitypes.Replay {
	out := apitypes.Replay{ID: r.ID, Start: time.UnixMilli(r.Start).UTC(), End: time.UnixMilli(r.End).UTC(), Segments: r.Segments,
		EntryPath: r.EntryPath, ExitPath: r.ExitPath, Country: r.Country, Browser: r.Browser, OS: r.OS, Device: r.Device,
		Release: labels[r.Release], Errors: r.Errors, Events: r.Events, Bytes: r.Bytes}
	for _, m := range r.Marks {
		out.Marks = append(out.Marks, apitypes.ReplayMark{T: m.T, Kind: kindNames[m.Kind], Name: m.Name})
	}
	return out
}

// kindNames name event kinds on a timeline.
var kindNames = map[analytics.Kind]string{analytics.KindPageview: "pageview", analytics.KindEvent: "event", analytics.KindError: "error"}

// SiteReplay returns one replay, found by any of its page loads: its
// segments and timeline. Its recording is SiteReplayEvents.
func (c *Core) SiteReplay(ctx context.Context, a Actor, project, name, id string) (apitypes.ReplayDetail, error) {
	_, d, err := c.replay(ctx, a, project, name, id)
	return d, err
}

// replay loads a site and one of its replays.
func (c *Core) replay(ctx context.Context, a Actor, project, name, id string) (siteScope, apitypes.ReplayDetail, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "watching a site's replays")
	if err != nil {
		return s, apitypes.ReplayDetail{}, err
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		return s, apitypes.ReplayDetail{}, errf(apitypes.CodeServerOffline, analyticsOff)
	}
	d, err := cl.Replay(ctx, analytics.ReplayQuery{Site: s.site.ID, ID: id})
	online, msg, err := storeStatus(err)
	if err != nil {
		return s, apitypes.ReplayDetail{}, err
	}
	if !online {
		return s, apitypes.ReplayDetail{}, errf(apitypes.CodeServerOffline, "%s", msg)
	}
	out := apitypes.ReplayDetail{Site: s.site.Name, Replay: toReplay(d.Replay, c.releaseLabels(ctx, []int64{d.Release})),
		SegmentList: []apitypes.ReplaySegment{}, Timeline: []apitypes.TimelineItem{}}
	for _, sg := range d.SegmentList {
		out.SegmentList = append(out.SegmentList, apitypes.ReplaySegment{ID: sg.ID, Start: time.UnixMilli(sg.Start).UTC(),
			End: time.UnixMilli(sg.End).UTC(), Path: sg.Path, SkewMS: sg.Skew, Bytes: sg.Bytes})
	}
	for _, it := range d.Timeline {
		out.Timeline = append(out.Timeline, apitypes.TimelineItem{At: time.UnixMilli(it.T).UTC(), Kind: kindNames[it.Kind], Path: it.Path,
			Name: it.Name, Group: it.Group})
	}
	return s, out, nil
}

// SiteReplayEvents opens a replay's recording, every page load's rrweb
// events in order, for the player to stream and close: JSON lines, a
// {"segment": id} line before each page load's chunks, each chunk a JSON
// array of rrweb events. With gzipped it may come gzip-compressed (zipped
// says so), as the store sent it. Every call is audited: watching a
// replay can show what a visitor typed or read.
func (c *Core) SiteReplayEvents(ctx context.Context, a Actor, project, name, id string, gzipped bool) (body io.ReadCloser, zipped bool, err error) {
	s, d, err := c.replay(ctx, a, project, name, id)
	if err != nil {
		return nil, false, err
	}
	cl, _ := c.analyticsClient()
	segments := make([]string, len(d.SegmentList))
	for i, sg := range d.SegmentList {
		segments[i] = sg.ID
	}
	body, zipped, err = cl.ReplayData(ctx, s.site.ID, segments, gzipped)
	online, msg, err := storeStatus(err)
	if err != nil {
		return nil, false, err
	}
	if !online {
		return nil, false, errf(apitypes.CodeServerOffline, "%s", msg)
	}
	c.audit(ctx, a, "analytics.replay_view", &s.project, &s.env, s.site.Name,
		"watched replay "+d.ID+" ("+strconv.Itoa(d.Segments)+" page loads from "+d.Start.Format(time.DateTime)+" UTC)")
	return body, zipped, nil
}
