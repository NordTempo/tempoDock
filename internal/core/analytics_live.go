package core

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Live visitors (ADR 0037): only while someone watches a site. The store
// is the one source of a live view: the first watcher loads the site's
// visitors of the last liveWindow and its latest events, and the view is
// loaded again whenever the forwarders pass the site's events (at most
// every liveRefreshEvery) and every liveRecount without them, so visitors
// who left drop out. The forwarders only note which watched sites got
// events, decoding batches only while anyone watches any site. The last
// watcher leaving drops the site's state and its timer.

// liveWindow is how far back a visitor counts as live.
const liveWindow = 5 * time.Minute

// liveRecount is how often a watched site's view is loaded again without
// new events.
const liveRecount = 30 * time.Second

// liveRefreshEvery is the least time between two loads of a watched
// site's view while its events arrive. A variable so tests can wait less.
var liveRefreshEvery = 2 * time.Second

// liveSites holds the watched sites.
type liveSites struct {
	mu    sync.Mutex
	sites map[int64]*liveSite
	// watched counts the watched sites, read on every forwarded batch.
	watched atomic.Int32
}

// liveSite is one watched site.
type liveSite struct {
	watchers int
	view     apitypes.SiteLive
	// changed is closed and replaced when the view changes.
	changed chan struct{}
	// timer loads the view again at due: liveRecount after the last load,
	// or liveRefreshEvery after it once events arrived.
	timer *time.Timer
	due   time.Time
	last  time.Time
	// loading is set while a load runs; dirty when events arrived during
	// it, so the next one comes soon.
	loading, dirty bool
}

// watchSite registers a watcher of site, loading its view from the store
// for the first one. leave unregisters it.
func (c *Core) watchSite(ctx context.Context, site int64) (leave func(), err error) {
	l := &c.analytics.live
	l.mu.Lock()
	ls, ok := l.sites[site]
	if ok {
		ls.watchers++
		l.mu.Unlock()
		return func() { c.unwatchSite(site) }, nil
	}
	l.mu.Unlock()

	cl, _ := c.analyticsClient()
	if cl == nil {
		return nil, errf(apitypes.CodeServerOffline, "analytics is off; it starts when a hostname collects")
	}
	snap, err := cl.Live(ctx, analytics.LiveQuery{Site: site, Since: now().Add(-liveWindow).UnixMilli()})
	if err != nil {
		return nil, errf(apitypes.CodeServerOffline, "the analytics server is offline or busy; live visitors come back with it")
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sites == nil {
		l.sites = map[int64]*liveSite{}
	}
	if ls, ok := l.sites[site]; ok { // another watcher won the race
		ls.watchers++
		return func() { c.unwatchSite(site) }, nil
	}
	t := now()
	// Events forwarded between the load and now weren't noted: the first
	// reload comes soon.
	ls = &liveSite{watchers: 1, view: toLiveView(snap), changed: make(chan struct{}), last: t, due: t.Add(liveRefreshEvery)}
	ls.timer = time.AfterFunc(liveRefreshEvery, func() { c.reloadSite(site, ls) })
	l.sites[site] = ls
	l.watched.Add(1)
	return func() { c.unwatchSite(site) }, nil
}

// unwatchSite drops a watcher, and the site's state with the last.
func (c *Core) unwatchSite(site int64) {
	l := &c.analytics.live
	l.mu.Lock()
	defer l.mu.Unlock()
	ls, ok := l.sites[site]
	if !ok {
		return
	}
	if ls.watchers--; ls.watchers > 0 {
		return
	}
	ls.timer.Stop()
	delete(l.sites, site)
	l.watched.Add(-1)
}

// reloadSite loads a watched site's view from the store again, in the
// background, and arms the next load while it is still watched. ls is the
// state the timer belongs to: a site watched again after its last watcher
// left has new state and a timer of its own.
func (c *Core) reloadSite(site int64, ls *liveSite) {
	l := &c.analytics.live
	l.mu.Lock()
	defer l.mu.Unlock()
	// Close cancels c.ctx under this lock, so no goroutine starts after.
	if l.sites[site] != ls || ls.loading || c.ctx.Err() != nil {
		return
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		ls.armLocked(liveRecount)
		return
	}
	ls.loading, ls.dirty = true, false
	c.wg.Go(func() {
		ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
		snap, err := cl.Live(ctx, analytics.LiveQuery{Site: site, Since: now().Add(-liveWindow).UnixMilli()})
		cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		ls.loading = false
		if l.sites[site] != ls {
			return
		}
		// A failed load keeps the view; the next one tries again.
		if err == nil {
			ls.view = toLiveView(snap)
			ls.notify()
		}
		ls.last = now()
		if ls.dirty {
			ls.armLocked(liveRefreshEvery)
		} else {
			ls.armLocked(liveRecount)
		}
	})
}

// armLocked sets the next load d from now. Called with the live lock held.
func (ls *liveSite) armLocked(d time.Duration) {
	ls.due = now().Add(d)
	ls.timer.Reset(d)
}

// notify wakes the site's views. Called with the live lock held.
func (ls *liveSite) notify() {
	close(ls.changed)
	ls.changed = make(chan struct{})
}

// observeLive notes which watched sites a forwarded batch has events of,
// so their views load again soon. With none watched it costs one atomic
// load: batches aren't decoded. Lines are decoded (their site only)
// outside the lock.
func (c *Core) observeLive(lines []byte) {
	l := &c.analytics.live
	if l.watched.Load() == 0 {
		return
	}
	var sites []int64
	for line := range bytes.SplitSeq(lines, []byte{'\n'}) {
		var e struct {
			Site int64 `json:"s"`
		}
		if len(line) == 0 || json.Unmarshal(line, &e) != nil || slices.Contains(sites, e.Site) {
			continue
		}
		sites = append(sites, e.Site)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, site := range sites {
		ls, ok := l.sites[site]
		switch {
		case !ok:
		case ls.loading:
			ls.dirty = true
		default:
			// Soon, but no sooner than liveRefreshEvery after the last load.
			t := now()
			at := ls.last.Add(liveRefreshEvery)
			if at.Before(t) {
				at = t
			}
			if at.Before(ls.due) {
				ls.due = at
				ls.timer.Reset(at.Sub(t))
			}
		}
	}
}

// toLiveView converts the store's live snapshot.
func toLiveView(snap analytics.Live) apitypes.SiteLive {
	out := apitypes.SiteLive{Visitors: len(snap.Visitors), Recent: make([]apitypes.LiveVisit, 0, len(snap.Recent))}
	for _, e := range snap.Recent {
		out.Recent = append(out.Recent, apitypes.LiveVisit{
			At: time.UnixMilli(e.Time).UTC(), Kind: kindName(e.Kind), Path: e.Path, Name: e.Name, Hostname: e.Hostname,
			Referrer: e.Referrer, Country: e.Country, City: e.City, Browser: e.Browser, OS: e.OS, Device: e.Device,
		})
	}
	return out
}

// liveView is a site's current view, and the channel its next change
// closes; nil when the site isn't watched.
func (c *Core) liveView(site int64) (apitypes.SiteLive, <-chan struct{}) {
	l := &c.analytics.live
	l.mu.Lock()
	defer l.mu.Unlock()
	ls, ok := l.sites[site]
	if !ok {
		return apitypes.SiteLive{Recent: []apitypes.LiveVisit{}}, nil
	}
	return ls.view, ls.changed
}

// kindName names an event kind.
func kindName(k analytics.Kind) string {
	switch k {
	case analytics.KindEvent:
		return "event"
	case analytics.KindOutbound:
		return "outbound"
	}
	return "pageview"
}

// SiteLive streams a site's live view: now, and again whenever it
// changes (new events, or visitors dropping out of the last five
// minutes). It costs nothing while nobody watches.
func (c *Core) SiteLive(ctx context.Context, a Actor, project, name string) (Stream[apitypes.SiteLive], error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "watching a site live")
	if err != nil {
		return nil, err
	}
	leave, err := c.watchSite(ctx, s.site.ID)
	if err != nil {
		return nil, err
	}
	return held(c, a, counted(c, a, func(ctx context.Context, ready func(), send func(...apitypes.SiteLive) error) error {
		defer leave()
		ready()
		var last apitypes.SiteLive
		first := true
		for {
			view, changed := c.liveView(s.site.ID)
			if changed == nil {
				return nil
			}
			if first || view.Visitors != last.Visitors || len(view.Recent) != len(last.Recent) ||
				(len(view.Recent) > 0 && len(last.Recent) > 0 && view.Recent[0] != last.Recent[0]) {
				if err := send(view); err != nil {
					return err
				}
				first, last = false, view
			}
			select {
			case <-ctx.Done():
				return nil
			case <-changed:
			}
		}
	})), nil
}
