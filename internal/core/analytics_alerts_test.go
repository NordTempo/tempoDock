package core

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/analytics"
	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// siteAlerts are the open alerts of kind.
func siteAlerts(h *harness, kind apitypes.AlertKind) []apitypes.Alert {
	h.t.Helper()
	all, err := h.c.ListAlerts(h.ctx, testSystem, false, false, 100, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []apitypes.Alert
	for _, a := range all {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

// countingSite enables analytics for shop.example.com and returns its
// site, service and release IDs.
func countingSite(h *harness) (site, svc, rel int64) {
	h.t.Helper()
	web := h.service("web", apitypes.CreateServiceRequest{Port: 8080})
	h.deploy(web, "nginx:1")
	h.domain(web, "shop.example.com", "")
	if _, err := h.c.EnableHostnameAnalytics(h.ctx, testSystem, "shop.example.com", apitypes.EnableAnalyticsRequest{}); err != nil {
		h.t.Fatal(err)
	}
	r, _ := analyticsRoute(h, "shop.example.com")
	_, _ = fmt.Sscanf(r.Tag, "%d.%d.%d", &site, &svc, &rel)
	return site, svc, rel
}

// spooledError is an error event of group g at t, as the ingest spools it.
func spooledErrorLine(site, svc, rel int64, t time.Time, g int) []byte {
	return fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":4,"p":"/cart","n":"boom %d","v":1,"er":{"ty":"TypeError","g":%d}}`,
		t.UnixMilli(), site, svc, rel, g, g)
}

func TestNewErrorAlertsOnce(t *testing.T) {
	h := newHarness(t)
	site, svc, rel := countingSite(h)
	h.rt.AppendSpool(spooledErrorLine(site, svc, rel, now(), 1), spooledErrorLine(site, svc, rel, now(), 1))
	eventually(t, "a new error alert", func() bool { return len(siteAlerts(h, apitypes.AlertNewError)) == 1 })
	a := siteAlerts(h, apitypes.AlertNewError)[0]
	if a.Title != "New error on shop.example.com: TypeError: boom 1" || a.Project != "shop" || !strings.Contains(a.Message, "/cart (web r1)") {
		t.Errorf("alert = %+v", a)
	}
	// The same group again: no second alert.
	h.rt.AppendSpool(spooledErrorLine(site, svc, rel, now(), 1))
	eventually(t, "the store to have all three", func() bool { return h.rt.SpoolAcked() == 3 })
	if n := len(siteAlerts(h, apitypes.AlertNewError)); n != 1 {
		t.Errorf("%d new error alerts after the same error again", n)
	}
	// A flood of new groups opens at most a handful.
	for g := 2; g < 12; g++ {
		h.rt.AppendSpool(spooledErrorLine(site, svc, rel, now(), g))
	}
	eventually(t, "the flood forwarded", func() bool { return h.rt.SpoolAcked() == 13 })
	if n := len(siteAlerts(h, apitypes.AlertNewError)); n != maxNewErrorAlert {
		t.Errorf("%d new error alerts open, want %d", n, maxNewErrorAlert)
	}
	// A day later they are no longer new.
	h.c.analyticsAlertTick(h.ctx, now().Add(newErrorFor))
	if n := len(siteAlerts(h, apitypes.AlertNewError)); n != 0 {
		t.Errorf("%d new error alerts a day later", n)
	}
}

func TestErrorSpikeAlert(t *testing.T) {
	h := newHarness(t)
	site, svc, rel := countingSite(h)
	var lines [][]byte
	for range spikeMin {
		lines = append(lines, spooledErrorLine(site, svc, rel, now(), 1))
	}
	h.rt.AppendSpool(lines...)
	eventually(t, "a spike alert", func() bool { return len(siteAlerts(h, apitypes.AlertErrorSpike)) == 1 })
	if a := siteAlerts(h, apitypes.AlertErrorSpike)[0]; a.Title != "Errors spiking on shop.example.com" || !strings.Contains(a.Message, "20 errors in the last 5 minutes") {
		t.Errorf("spike = %+v", a)
	}
	// Ten minutes later the errors stopped: resolved.
	h.c.analyticsAlertTick(h.ctx, now().Add(10*time.Minute))
	if n := len(siteAlerts(h, apitypes.AlertErrorSpike)); n != 0 {
		t.Errorf("%d spike alerts after the errors stopped", n)
	}
}

func TestErrorRate(t *testing.T) {
	var r errorRate
	const m = 1_000_000
	for i := range int64(55) {
		r.add(m-5-i, m, 2) // two a minute for the hour before
	}
	r.add(m, m, 40)
	r.add(m-90, m, 500) // a backlog from before the hour: not counted
	recent, usual := r.recent(m)
	if recent != 40 || usual != 10 || spike(recent, usual) {
		t.Errorf("recent %d, usual %d", recent, usual)
	}
	r.add(m-1, m, 20)
	if recent, usual := r.recent(m); !spike(recent, usual) {
		t.Errorf("60 against 10 usually isn't a spike")
	}
}

func TestTrafficDropAlert(t *testing.T) {
	h := newHarness(t)
	site, svc, rel := countingSite(h)
	if err := h.c.EnableAnalytics(h.ctx, testSystem); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the store", func() bool { return h.rt.AnalyticsStore() != nil })
	hour := now().Truncate(time.Hour).Add(-time.Hour)
	var lines [][]byte
	for d := 1; d <= trafficDays; d++ {
		for i := range 100 {
			lines = append(lines, fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":1,"p":"/","v":%d}`,
				hour.AddDate(0, 0, -d).Add(time.Duration(i)*time.Second).UnixMilli(), site, svc, rel, i))
		}
	}
	for i := range 10 {
		lines = append(lines, fmt.Appendf(nil, `{"t":%d,"s":%d,"sv":%d,"rl":%d,"k":1,"p":"/","v":%d}`, hour.Add(time.Duration(i)*time.Minute).UnixMilli(), site, svc, rel, i))
	}
	h.rt.AppendSpool(lines...)
	eventually(t, "the page views forwarded", func() bool { return h.rt.SpoolAcked() == uint64(len(lines)) })
	// Right after servers changed, the hour isn't judged.
	check := hour.Add(time.Hour + trafficCheckAt)
	h.c.checkTraffic(h.ctx, check)
	if n := len(siteAlerts(h, apitypes.AlertTrafficDrop)); n != 0 {
		t.Fatalf("judged traffic while servers were settling: %d alerts", n)
	}
	h.c.analytics.alerts.mu.Lock()
	h.c.analytics.alerts.changedAt, h.c.analytics.alerts.checkedHour = check.Add(-trafficQuiet), 0
	h.c.analytics.alerts.mu.Unlock()
	h.c.checkTraffic(h.ctx, check)
	eventually(t, "a traffic drop alert", func() bool { return len(siteAlerts(h, apitypes.AlertTrafficDrop)) == 1 })
	if a := siteAlerts(h, apitypes.AlertTrafficDrop)[0]; !strings.Contains(a.Message, "10 page views") || !strings.Contains(a.Message, "usually 100") {
		t.Errorf("drop = %+v", a)
	}
	// An hour like the usual ones resolves it.
	h.c.judgeTraffic(h.ctx, []analytics.TrafficRow{{Site: site, DaysAgo: 0, Pageviews: 90}, {Site: site, DaysAgo: 1, Pageviews: 100}}, hour, check)
	if n := len(siteAlerts(h, apitypes.AlertTrafficDrop)); n != 0 {
		t.Errorf("%d drop alerts after a normal hour", n)
	}
}
