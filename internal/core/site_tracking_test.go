package core

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics/ingest"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// site is a test site: its page's HTML and headers, and whether
// /_dock/a/s.js is Dock's.
func site(t *testing.T, html string, header http.Header, docks bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == ingest.ScriptPath {
			if docks {
				w.Header().Set(ingest.ScriptHeader, "analytics")
			}
			_, _ = w.Write([]byte("(function(){})()"))
			return
		}
		for k, v := range header {
			w.Header()[k] = v
		}
		if loc := header.Get("Location"); loc != "" {
			w.WriteHeader(http.StatusMovedPermanently)
			return
		}
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// A check finds a missing tag, a script that isn't Dock's, a
// Content-Security-Policy that blocks it and a redirect away from the
// site's hostnames; a page with all in place passes.
func TestSiteCheck(t *testing.T) {
	h := newHarness(t)
	h.c.tracking.scheme = "http"
	h.c.AllowLoopbackSiteChecks(true)
	tag := `<html><head><script defer src="/_dock/a/s.js"></script></head></html>`
	other := site(t, tag, nil, true)
	cases := map[string]struct {
		host string
		want apitypes.TrackingState
	}{
		"in place":     {site(t, tag, nil, true), ""},
		"no tag":       {site(t, `<html><head><script src="/app.js"></script></head></html>`, nil, true), apitypes.TrackingNoScript},
		"not Dock's":   {site(t, tag, nil, false), apitypes.TrackingWrongHost},
		"blocked":      {site(t, tag, http.Header{"Content-Security-Policy": {"default-src 'none'; img-src *"}}, true), apitypes.TrackingBlocked},
		"redirected":   {site(t, "", http.Header{"Location": {"http://elsewhere.example/"}}, true), apitypes.TrackingWrongHost},
		"other host":   {site(t, `<script src="https://cdn.example/_dock/a/s.js"></script>`, nil, true), apitypes.TrackingWrongHost},
		"to a sibling": {site(t, "", http.Header{"Location": {"http://" + other + "/"}}, true), ""},
	}
	for name, tc := range cases {
		ch := h.c.checkSite(h.ctx, []string{tc.host, other})
		if got := checkProblem(apitypes.SiteCheck{Hosts: ch.Hosts[:1]}); got != tc.want {
			t.Errorf("%s: problem %q, want %q; steps %+v", name, got, tc.want, ch.Hosts[0].Steps)
		}
	}
	// One hostname in place means the script works on the site.
	ch := h.c.checkSite(h.ctx, []string{cases["no tag"].host, cases["in place"].host})
	if p := checkProblem(ch); p != "" {
		t.Fatalf("a site with one good hostname: %q", p)
	}
	// An unreachable host is skipped, not failed.
	ch = h.c.checkSite(h.ctx, []string{"127.0.0.1:1"})
	if st := ch.Hosts[0].Steps; len(st) != 1 || st[0].Result != apitypes.CheckSkip {
		t.Fatalf("unreachable: %+v", st)
	}
}

func TestCSPBlocks(t *testing.T) {
	for csp, want := range map[string]string{
		"default-src 'self'":                         "",
		"script-src 'self' https://x; connect-src *": "",
		"script-src https://cdn.example":             "script-src",
		"default-src 'none'; script-src 'self'":      "default-src",
		"img-src *":                                  "",
	} {
		if got := cspBlocks(csp); got != want {
			t.Errorf("%q: %q, want %q", csp, got, want)
		}
	}
}

// A site without events waits and asks for a check; its first event
// makes it active, with the event's time.
func TestSiteTrackingState(t *testing.T) {
	h := newHarness(t)
	site, svc, rel := countingSite(h)
	var tr apitypes.SiteTracking
	eventually(t, "the store answers", func() bool {
		var err error
		tr, err = h.c.SiteTracking(h.ctx, testSystem, "shop", "shop.example.com")
		return err == nil && tr.State != apitypes.TrackingUnknown
	})
	if tr.State != apitypes.TrackingWaiting || !tr.CheckDue || tr.LastEventAt != nil {
		t.Fatalf("before any event: %+v", tr)
	}
	at := now().Add(-time.Minute)
	h.rt.AppendSpool(fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"h":"shop.example.com","k":1,"p":"/","v":1}`, at.UnixMilli(), site, svc, rel))
	eventually(t, "active", func() bool {
		tr, _ = h.c.SiteTracking(h.ctx, testSystem, "shop", "shop.example.com")
		return tr.State == apitypes.TrackingActive
	})
	if tr.LastEventAt == nil || tr.LastEventAt.UnixMilli() != at.UnixMilli() || tr.CheckDue {
		t.Fatalf("after an event: %+v", tr)
	}
	reader := Actor{Kind: "user", Name: "ro", UserID: 5, OrgID: 2, Scope: apitypes.ScopeRead}
	if _, err := h.c.SiteTracking(h.ctx, reader, "shop", "shop.example.com"); err == nil {
		t.Fatal("another org read a site's tracking")
	}
}
