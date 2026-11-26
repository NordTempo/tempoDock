package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/version"
)

// Is a site's tracking script working? The state comes from the last
// event (the store's newest one in 24 hours, and the daily totals of 30
// days) and, when the site isn't active, from a check of its pages run on
// demand: Dock fetches each hostname's page, looks for the script tag and
// the Content-Security-Policy, and fetches /_dock/a/s.js to see that it
// is Dock's (ingest.ScriptHeader). Nothing runs at idle: the page asks
// when it opens. Checks are kept in memory for a few sites, so a page
// that opens again shows the last one without fetching anything.

const (
	// trackingActive is how recent the last event of an active site is.
	trackingActive = 24 * time.Hour
	// stoppedPerDay is the daily average of the week before the last 24
	// hours above which a silent day means the script stopped.
	stoppedPerDay = 10
	// siteCheckEvery is how often a site's pages may be checked.
	siteCheckEvery = time.Minute
	// siteCheckFresh is how long a check stands before the page asks for
	// a new one.
	siteCheckFresh = 10 * time.Minute
	// siteChecksKept caps the checks kept in memory, the oldest going.
	siteChecksKept = 64
	// siteCheckHosts is the most hostnames one check fetches.
	siteCheckHosts = 5
	// siteCheckTimeout bounds each request of a check.
	siteCheckTimeout = 5 * time.Second
	// siteCheckBody is the most of a page a check reads.
	siteCheckBody = 512 << 10
)

// siteChecks are the newest checks, by site ID, and the guarded client
// that runs them: public addresses only, as for an org's hooks, no
// redirects followed by itself, no idle connections.
type siteChecks struct {
	// scheme is https; tests' sites listen on plain http.
	scheme string
	mu     sync.Mutex
	byID   map[int64]apitypes.SiteCheck
	guard  egressGuard
	client *http.Client
}

// newSiteChecks makes the checks' state; allow is the private CIDRs the
// operator lets apps reach.
func newSiteChecks(allow []netip.Prefix) *siteChecks {
	s := &siteChecks{scheme: "https", byID: map[int64]apitypes.SiteCheck{}}
	s.guard.what, s.guard.private, s.guard.allow = "tracking checks", true, allow
	s.client = &http.Client{
		Timeout:       siteCheckTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: siteCheckTimeout, Control: s.guard.control}).DialContext,
			TLSHandshakeTimeout: siteCheckTimeout,
			DisableKeepAlives:   true,
		},
	}
	return s
}

// AllowLoopbackSiteChecks lets checks fetch this server's own addresses,
// which they never may in production. For tests whose sites listen on
// 127.0.0.1.
func (c *Core) AllowLoopbackSiteChecks(allow bool) { c.tracking.guard.loopback.Store(allow) }

// get returns a site's newest check.
func (s *siteChecks) get(site int64) (apitypes.SiteCheck, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch, ok := s.byID[site]
	return ch, ok
}

// put keeps a site's newest check, dropping the oldest beyond the cap.
func (s *siteChecks) put(site int64, ch apitypes.SiteCheck) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[site] = ch
	for len(s.byID) > siteChecksKept {
		var oldest int64
		var at time.Time
		for id, c := range s.byID {
			if oldest == 0 || c.At.Before(at) {
				oldest, at = id, c.At
			}
		}
		delete(s.byID, oldest)
	}
}

// SiteTracking says whether a site's tracking script works. Two store
// queries; no request leaves the server. Scope: read.
func (c *Core) SiteTracking(ctx context.Context, a Actor, project, name string) (apitypes.SiteTracking, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "reading a site's tracking")
	if err != nil {
		return apitypes.SiteTracking{}, err
	}
	return c.siteTracking(ctx, s.site.ID)
}

// siteTracking is SiteTracking for a loaded site.
func (c *Core) siteTracking(ctx context.Context, siteID int64) (apitypes.SiteTracking, error) {
	out := apitypes.SiteTracking{State: apitypes.TrackingUnknown}
	if ch, ok := c.tracking.get(siteID); ok {
		out.Check = &ch
	}
	cl, _ := c.analyticsClient()
	if cl == nil {
		out.StoreError = "analytics is off; it starts when a hostname collects"
		return out, nil
	}
	t := now()
	live, err := cl.Live(ctx, analytics.LiveQuery{Site: siteID, Since: t.Add(-trackingActive).UnixMilli()})
	if err != nil {
		return trackingStoreError(out, err)
	}
	if len(live.Recent) > 0 {
		last := time.UnixMilli(live.Recent[0].Time)
		out.State, out.LastEventAt = apitypes.TrackingActive, &last
		return out, nil
	}
	days, err := cl.Series(ctx, analytics.SeriesQuery{Query: analytics.Query{Site: siteID, From: t.AddDate(0, 0, -30).UnixMilli(), To: t.UnixMilli()}, Bucket: analytics.Bucket("day")})
	if err != nil {
		return trackingStoreError(out, err)
	}
	var week, month int64
	weekFrom := t.Add(-trackingActive).AddDate(0, 0, -7).UnixMilli()
	for _, d := range days {
		month += d.Pageviews
		if d.T >= weekFrom {
			week += d.Pageviews
		}
		if d.Pageviews > 0 {
			last := time.UnixMilli(d.T)
			out.LastEventAt = &last
		}
	}
	switch {
	case week >= 7*stoppedPerDay:
		out.State = apitypes.TrackingStopped
	case month > 0:
		out.State = apitypes.TrackingQuiet
	default:
		out.State = apitypes.TrackingWaiting
	}
	if out.Check != nil {
		if problem := checkProblem(*out.Check); problem != "" {
			out.State = problem
		}
	}
	out.CheckDue = out.Check == nil || now().Sub(out.Check.At) > siteCheckFresh
	return out, nil
}

// trackingStoreError answers with the store's state when it can't be
// read, as the dashboard does (storeAnswer); other errors are errors.
func trackingStoreError(out apitypes.SiteTracking, err error) (apitypes.SiteTracking, error) {
	st, err := storeAnswer(apitypes.SiteStats{}, err)
	if err != nil {
		return out, err
	}
	out.StoreError = st.StoreError
	return out, nil
}

// checkProblem is what a check found wrong: the first failing step's
// kind, unless some hostname passed every step (then the script is fine
// there, and the site waits for visitors).
func checkProblem(ch apitypes.SiteCheck) apitypes.TrackingState {
	var first apitypes.TrackingState
	for _, h := range ch.Hosts {
		passed := len(h.Steps) > 0
		for _, st := range h.Steps {
			if st.Result != apitypes.CheckOK {
				passed = false
			}
			if st.Result == apitypes.CheckFail && first == "" {
				first = st.Problem
			}
		}
		if passed {
			return ""
		}
	}
	return first
}

// CheckSiteTracking checks a site's pages now (at most once a minute per
// site; a newer check is returned as is) and returns the site's tracking
// with it. Scope: read: it reads public pages, as any visitor could.
func (c *Core) CheckSiteTracking(ctx context.Context, a Actor, project, name string) (apitypes.SiteTracking, error) {
	s, err := c.loadSite(ctx, a, project, name, apitypes.ScopeRead, "checking a site's tracking")
	if err != nil {
		return apitypes.SiteTracking{}, err
	}
	if ch, ok := c.tracking.get(s.site.ID); !ok || now().Sub(ch.At) >= siteCheckEvery {
		hosts, err := c.store.ListSiteHostnames(ctx, []int64{s.site.ID})
		if err != nil {
			return apitypes.SiteTracking{}, err
		}
		names := make([]string, 0, len(hosts))
		for _, h := range hosts {
			// Only the environment the caller was allowed to read: a
			// site moved since counts another's hostnames.
			if h.EnvironmentID == s.site.EnvironmentID {
				names = append(names, h.Hostname)
			}
		}
		c.tracking.put(s.site.ID, c.checkSite(ctx, names))
	}
	return c.siteTracking(ctx, s.site.ID)
}

// checkSite checks up to siteCheckHosts of a site's hostnames at once.
func (c *Core) checkSite(ctx context.Context, hosts []string) apitypes.SiteCheck {
	out := apitypes.SiteCheck{At: now(), Hosts: make([]apitypes.SiteCheckHost, min(len(hosts), siteCheckHosts))}
	var wg sync.WaitGroup
	for i := range out.Hosts {
		// What a check that panics leaves: the hostname, and that it failed.
		out.Hosts[i] = apitypes.SiteCheckHost{Hostname: hosts[i], Steps: []apitypes.SiteCheckStep{
			{Result: apitypes.CheckFail, Text: "the check failed on a bug in Dock; try again, and report it if it stays"},
		}}
		goIn(&wg, c.log, "checking a site's hostname", func() { out.Hosts[i] = c.checkSiteHost(ctx, hosts[i], hosts) })
	}
	wg.Wait()
	return out
}

// scriptTagRe finds script tags' attributes in a page; srcRe and nonceRe
// read two of them. The one Dock's Caddy adds is marked data-dock-auto.
var (
	scriptTagRe = regexp.MustCompile(`(?is)<script\b([^>]*)>`)
	srcRe       = regexp.MustCompile(`(?is)(?:^|\s)src\s*=\s*["']?([^"'\s>]+)`)
	nonceRe     = regexp.MustCompile(`(?is)(?:^|\s)nonce\s*=\s*["']?([^"'\s>]+)`)
)

// checkSiteHost checks one hostname of a site: its page (a redirect to
// another of the site's hostnames is followed once), the script tag, the
// page's Content-Security-Policy and the script itself.
func (c *Core) checkSiteHost(ctx context.Context, host string, siteHosts []string) apitypes.SiteCheckHost {
	out := apitypes.SiteCheckHost{Hostname: host}
	step := func(r apitypes.CheckResult, problem apitypes.TrackingState, fix, format string, args ...any) {
		out.Steps = append(out.Steps, apitypes.SiteCheckStep{Result: r, Problem: problem, Text: fmt.Sprintf(format, args...), Fix: fix})
	}
	page := c.tracking.scheme + "://" + host + "/"
	resp, err := c.siteGet(ctx, page, true)
	if err == nil && resp.status/100 == 3 {
		to, _ := url.Parse(resp.header.Get("Location"))
		// next is where it goes with any port; a port alone (:443) doesn't
		// make it another host.
		next, bare := "", ""
		if to != nil {
			u := resolveRef(page, to)
			next, bare = strings.ToLower(u.Host), strings.ToLower(u.Hostname())
		}
		switch {
		case next == "" || next == host || bare == host:
			// A redirect within the host (to /en/, to https): follow it.
			if to != nil {
				resp, err = c.siteGet(ctx, resolveRef(page, to).String(), true)
			}
		case slices.Contains(siteHosts, next) || slices.Contains(siteHosts, bare):
			step(apitypes.CheckOK, "", "", "redirects to %s, checked on its own", next)
			return out
		default:
			step(apitypes.CheckFail, apitypes.TrackingWrongHost, "",
				"redirects to %s, which isn't a domain of this site's service: visitors land there and count for its service's site, if it has one", next)
			return out
		}
	}
	if err != nil {
		step(apitypes.CheckSkip, "", "", "can't reach %s from this server: %s", host, err)
		return out
	}
	if resp.status/100 != 2 {
		step(apitypes.CheckSkip, "", "", "the page answers %d, so it can't be read", resp.status)
		return out
	}
	step(apitypes.CheckOK, "", "", "the page answers %d", resp.status)

	found, nonce := false, ""
	for _, m := range scriptTagRe.FindAllSubmatch(resp.body, -1) {
		s := srcRe.FindSubmatch(m[1])
		if s == nil || !strings.Contains(string(s[1]), ingest.ScriptPath) {
			continue
		}
		src := string(s[1])
		if n := nonceRe.FindSubmatch(m[1]); n != nil {
			nonce = string(n[1])
		}
		found = true
		if u, err := url.Parse(src); err == nil && u.Host != "" && !strings.EqualFold(u.Hostname(), host) {
			step(apitypes.CheckFail, apitypes.TrackingWrongHost, `<script src="`+ingest.ScriptPath+`" defer></script>`,
				"the script tag loads it from %s; use the relative address so each hostname serves its own", u.Hostname())
		} else if strings.Contains(string(m[1]), "data-dock-auto") {
			step(apitypes.CheckOK, "", "", "Dock adds the script tag to the page")
		} else {
			step(apitypes.CheckOK, "", "", "the page has the script tag")
		}
		break
	}
	if !found {
		step(apitypes.CheckFail, apitypes.TrackingNoScript, `<script src="`+ingest.ScriptPath+`" defer></script>`,
			"no Dock script tag in the page's HTML. Dock adds it to a page with a <head> unless the site's auto_script is off, the page is marked no-transform or its Content-Security-Policy blocks the script, or the server's Caddy isn't Dock's (an update installs it); otherwise add it to the page's <head> (an app that adds it from JavaScript shows active once events arrive)")
	}
	if csp := resp.header.Get("Content-Security-Policy"); csp != "" {
		if d := cspBlocks(csp, nonce); d != "" {
			step(apitypes.CheckFail, apitypes.TrackingBlocked, "add 'self' to "+d,
				"the page's Content-Security-Policy leaves 'self' out of %s, so the browser blocks the script", d)
		}
	}

	resp, err = c.siteGet(ctx, c.tracking.scheme+"://"+host+ingest.ScriptPath, false)
	switch {
	case err != nil:
		step(apitypes.CheckSkip, "", "", "can't fetch %s: %s", ingest.ScriptPath, err)
	case resp.header.Get(ingest.ScriptHeader) == "":
		step(apitypes.CheckFail, apitypes.TrackingWrongHost, "",
			"%s on %s isn't Dock's script (%d): the domain isn't routed to Dock yet, a proxy in front doesn't pass /_dock/a/, or the server's routes missed analytics, which `dock analytics enable` for its service applies again", ingest.ScriptPath, host, resp.status)
	default:
		step(apitypes.CheckOK, "", "", "%s is Dock's script", ingest.ScriptPath)
	}
	return out
}

// resolveRef resolves a Location against the page it came from.
func resolveRef(base string, ref *url.URL) *url.URL {
	b, _ := url.Parse(base)
	return b.ResolveReference(ref)
}

// sitePage is what a check keeps of an answer: its status and headers,
// and the start of its body when asked for.
type sitePage struct {
	status int
	header http.Header
	body   []byte
}

// siteGet fetches a URL for a check; with read, it keeps up to
// siteCheckBody of the body. Errors are said in plain words (errorLine),
// without the URL.
func (c *Core) siteGet(ctx context.Context, target string, read bool) (sitePage, error) {
	ctx, cancel := context.WithTimeout(ctx, siteCheckTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return sitePage{}, err
	}
	req.Header.Set("User-Agent", "dock-tracking-check/"+version.Version)
	req.Header.Set("Accept", "text/html,*/*")
	resp, err := c.tracking.client.Do(req)
	if err != nil {
		if ne, ok := errors.AsType[net.Error](err); ok && ne.Timeout() {
			return sitePage{}, fmt.Errorf("no answer within %s", siteCheckTimeout)
		}
		return sitePage{}, errors.New(c.errorLine(err))
	}
	defer func() { _ = resp.Body.Close() }()
	out := sitePage{status: resp.StatusCode, header: resp.Header}
	if read {
		out.body, _ = io.ReadAll(io.LimitReader(resp.Body, siteCheckBody))
	}
	return out, nil
}

// cspBlocks returns the directive of a Content-Security-Policy that keeps
// the script out (script-src-elem or script-src, or connect-src for its
// events), falling back to default-src as browsers do; "" when it allows them. The script
// tag's nonce lets the script in when script-src names it, never its
// events.
func cspBlocks(csp, nonce string) string {
	dirs := map[string][]string{}
	for part := range strings.SplitSeq(csp, ";") {
		f := strings.Fields(strings.TrimSpace(part))
		if len(f) > 0 {
			dirs[strings.ToLower(f[0])] = f[1:]
		}
	}
	// allows: the tag's nonce first; then 'self', * or https:, which
	// 'strict-dynamic' turns off for a script tag in the page's HTML.
	allows := func(vals []string, nonce string, script bool) bool {
		host, strict := false, false
		for _, v := range vals {
			switch strings.ToLower(v) {
			case "'self'", "*", "https:":
				host = true
			case "'strict-dynamic'":
				strict = script
			}
			if nonce != "" && v == "'nonce-"+nonce+"'" {
				return true
			}
		}
		return host && !strict
	}
	// Each governs with the first of its directives the policy has:
	// script-src-elem goes before script-src for a script tag.
	for _, c := range []struct {
		dirs   []string
		script bool // a nonce covers the script, never its events
	}{{[]string{"script-src-elem", "script-src", "default-src"}, true}, {[]string{"connect-src", "default-src"}, false}} {
		for _, name := range c.dirs {
			if vals, ok := dirs[name]; ok {
				n := ""
				if c.script {
					n = nonce
				}
				if !allows(vals, n, c.script) {
					return name
				}
				break
			}
		}
	}
	return ""
}
