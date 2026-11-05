package core

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/node/remote"
	"github.com/NordTempo/tempoDock/internal/store"
)

// uptimeService creates service web with domain shop.example.com.
func (h *harness) uptimeService() ServiceRef {
	h.t.Helper()
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		h.t.Fatal(err)
	}
	return web
}

// fastUptime restarts the daemon with a check's second lasting a
// millisecond, so a 30-second interval is 30 ms.
func (h *harness) fastUptime() {
	h.opts.UptimeSecond = time.Millisecond
	h.restart()
}

func TestAddUptimeCheckValidates(t *testing.T) {
	h := newHarness(t)
	web := h.uptimeService()
	for _, tc := range []struct {
		req  apitypes.AddUptimeCheckRequest
		want string
	}{
		{apitypes.AddUptimeCheckRequest{URL: "https://elsewhere.example.com/"}, "not a domain of service shop/production/web"},
		{apitypes.AddUptimeCheckRequest{URL: "ftp://shop.example.com/"}, "not an http:// or https:// URL"},
		{apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com:8443/"}, "drop the port"},
		{apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/", IntervalSeconds: 10}, "30 seconds to 1 hour"},
		{apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/", IntervalSeconds: 30, TimeoutSeconds: 30}, "shorter than the interval"},
		{apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/", ExpectStatus: "4xx"}, "2xx or 3xx"},
	} {
		if _, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, tc.req); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.req, err, tc.want)
		}
	}

	chk, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://Shop.Example.com/health#top", ExpectStatus: "2xx"})
	if err != nil {
		t.Fatal(err)
	}
	if chk.URL != "https://shop.example.com/health" || chk.ExpectStatus != "2xx" || chk.IntervalSeconds != 60 || chk.TimeoutSeconds != 10 ||
		chk.State != apitypes.UptimeUnknown || chk.Server != h.c.local.Name || len(chk.Series) != 48 {
		t.Fatalf("check = %+v", chk)
	}
	for range uptimeMaxChecks - 1 {
		if _, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"}); CodeOf(err) != apitypes.CodeConflict {
		t.Fatalf("an 11th check: %v, want conflict", err)
	}
}

func TestUptimeChecksPermissions(t *testing.T) {
	h := newHarness(t)
	web := h.uptimeService()
	if _, err := h.c.CreateProject(h.ctx, testSystem, apitypes.CreateProjectRequest{Name: "blog"}); err != nil {
		t.Fatal(err)
	}
	chk, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	add := apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"}

	// Readers and deployers see results; only admins add and remove.
	for _, a := range []Actor{h.user("reader@example.com", apitypes.ScopeRead), h.user("dev@example.com", apitypes.ScopeDeploy)} {
		if got, err := h.c.ListUptimeChecks(h.ctx, a, web); err != nil || len(got) != 1 {
			t.Errorf("%s lists %v, %v", a.Name, got, err)
		}
		if _, err := h.c.AddUptimeCheck(h.ctx, a, web, add); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s adds: %v, want forbidden", a.Name, err)
		}
		if err := h.c.RemoveUptimeCheck(h.ctx, a, web, chk.ID); CodeOf(err) != apitypes.CodeForbidden {
			t.Errorf("%s removes: %v, want forbidden", a.Name, err)
		}
	}
	// An admin token of another project sees nothing of this one.
	blog := h.token(testSystem, apitypes.CreateTokenRequest{Name: "blog", Scope: apitypes.ScopeAdmin, Project: "blog"})
	if _, err := h.c.ListUptimeChecks(h.ctx, blog, web); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("another project's token lists: %v, want forbidden", err)
	}
	if err := h.c.RemoveUptimeCheck(h.ctx, blog, web, chk.ID); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("another project's token removes: %v, want forbidden", err)
	}
	// A check is only found through its own service.
	api := h.service("api", apitypes.CreateServiceRequest{Port: 3000})
	if err := h.c.RemoveUptimeCheck(h.ctx, testSystem, api, chk.ID); CodeOf(err) != apitypes.CodeNotFound {
		t.Errorf("removing through another service: %v, want not found", err)
	}
	if err := h.c.RemoveUptimeCheck(h.ctx, testSystem, web, chk.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := h.c.ListUptimeChecks(h.ctx, testSystem, web); err != nil || len(got) != 0 {
		t.Fatalf("after removing: %v, %v", got, err)
	}
	if !slices.ContainsFunc(h.shopAudit(), func(e apitypes.AuditEvent) bool { return e.Action == "uptime.remove" }) {
		t.Error("no uptime.remove audit event")
	}
}

// shopAudit is project shop's audit log.
func (h *harness) shopAudit() []apitypes.AuditEvent {
	h.t.Helper()
	evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Project: "shop"})
	if err != nil {
		h.t.Fatal(err)
	}
	return evs
}

// waitState waits for a check's state.
func (h *harness) waitState(ref ServiceRef, id int64, want apitypes.UptimeState) apitypes.UptimeCheck {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		checks, err := h.c.ListUptimeChecks(h.ctx, testSystem, ref)
		if err != nil {
			h.t.Fatal(err)
		}
		i := slices.IndexFunc(checks, func(c apitypes.UptimeCheck) bool { return c.ID == id })
		if i >= 0 && checks[i].State == want {
			return checks[i]
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("check %d: %+v, want %s", id, checks, want)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestUptimeDownOpensAnAlertAndRecoveryResolvesIt(t *testing.T) {
	h := newHarness(t)
	h.fastUptime()
	web := h.uptimeService()
	sub := h.c.bus.subscribe(func(e busEvent) bool { return e.ev.Type == apitypes.EventUptime })
	defer sub.close()
	const url = "https://shop.example.com/health"
	h.rt.SetProbe(url, node.ProbeResult{Status: 502, Latency: 30 * time.Millisecond, Error: "answered 502, expected 200"})
	chk, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: url, IntervalSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	a := h.openAlert(uptimeKey(chk.ID), true)
	if a.Kind != string(apitypes.AlertUptimeDown) || a.Severity != string(apitypes.SeverityCritical) || a.Title != "shop.example.com/health is down" ||
		!strings.HasPrefix(a.Message, "Probes from the control plane keep failing (answered 502, expected 200) since ") || a.Service != "web" {
		t.Fatalf("alert = %+v", a)
	}
	down := h.waitState(web, chk.ID, apitypes.UptimeDown)
	if down.LastProbe == nil || down.LastProbe.Status != 502 || down.LastProbe.Server != h.c.local.Name {
		t.Fatalf("down check = %+v", down)
	}
	ev := <-sub.ch
	if ev.Uptime == nil || ev.Uptime.ID != chk.ID || ev.Uptime.State != apitypes.UptimeDown || ev.Service != "web" {
		t.Fatalf("event = %+v", ev.Event)
	}

	h.rt.SetProbe(url, node.ProbeResult{Up: true, Status: 200, Latency: 20 * time.Millisecond})
	h.openAlert(uptimeKey(chk.ID), false)
	up := h.waitState(web, chk.ID, apitypes.UptimeUp)
	if len(up.Incidents) != 1 || up.Incidents[0].ResolvedAt == nil || up.Uptime24h == nil || *up.Uptime24h <= 0 || *up.Uptime24h >= 100 {
		t.Fatalf("recovered check = %+v", up)
	}
	o, err := h.c.Overview(h.ctx, testSystem, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if u := o.Environments[0].Services[0].Uptime; u == nil || u.Checks != 1 || u.State != apitypes.UptimeUp || u.Uptime24h == nil {
		t.Fatalf("overview uptime = %+v", u)
	}

	// Removing the check stops its probes.
	if err := h.c.RemoveUptimeCheck(h.ctx, testSystem, web, chk.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	n := len(h.rt.Probed())
	time.Sleep(100 * time.Millisecond)
	if more := len(h.rt.Probed()) - n; more > 0 {
		t.Fatalf("%d probes after the check was removed", more)
	}
}

func TestUptimeSkipsADueCheckWhoseProbeRuns(t *testing.T) {
	h := newHarness(t)
	h.fastUptime()
	web := h.uptimeService()
	release := h.rt.HoldProbes()
	defer release()
	if _, err := h.c.AddUptimeCheck(h.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"}); err != nil {
		t.Fatal(err)
	}
	// Ten intervals pass while the first probe hangs.
	deadline := time.Now().Add(5 * time.Second)
	for len(h.rt.Probed()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(600 * time.Millisecond)
	if n := len(h.rt.Probed()); n != 1 {
		t.Fatalf("%d probes started, want 1 while it runs", n)
	}
}

func TestUptimeProbesFromAnotherServer(t *testing.T) {
	p := newPool(t, poolServer{"cp", true, true, 8}, poolServer{"edge", false, false, 8}, poolServer{"old", false, false, 8})
	edge, old := p.ids["edge"], p.ids["old"]
	p.nodes.add(edge, p.rts[edge], remote.Hello{Protocol: remote.Protocol})
	p.nodes.add(old, p.rts[old], remote.Hello{Protocol: remote.ProbeProtocol - 1})
	p.opts.UptimeSecond = time.Millisecond
	p.restart()
	web := p.uptimeService()
	chk, err := p.c.AddUptimeCheck(p.ctx, testSystem, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	// The service runs on cp; old can't probe, so edge does.
	if chk.Server != "edge" {
		t.Fatalf("probed from %q, want edge", chk.Server)
	}
	waitProbed := func(name string, n int) {
		t.Helper()
		rt := p.rt
		if id, ok := p.ids[name]; ok && name != "cp" {
			rt = p.rts[id]
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(rt.Probed()) < n {
			if time.Now().After(deadline) {
				t.Fatalf("%s probed %d times, want %d", name, len(rt.Probed()), n)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	waitProbed("edge", 2)
	if n := len(p.rts[old].Probed()) + len(p.rt.Probed()); n != 0 {
		t.Fatalf("old and cp probed %d times", n)
	}
	got := p.waitState(web, chk.ID, apitypes.UptimeUp)
	if got.LastProbe == nil || got.LastProbe.Server != "edge" {
		t.Fatalf("last probe = %+v", got.LastProbe)
	}

	// With edge offline the control plane probes its own server.
	p.setOffline("edge", true)
	waitProbed("cp", 2)
	if got := p.waitState(web, chk.ID, apitypes.UptimeUp); got.Server != p.c.local.Name {
		t.Fatalf("probed from %q with edge offline, want the control plane", got.Server)
	}
}

func TestUptimeRollsUpHoursAndTrims(t *testing.T) {
	h := newHarness(t)
	web := h.uptimeService()
	// Straight into the store: the scheduler and the statistics tick
	// don't know it, so only this test rolls its rows up.
	chk, err := h.store.CreateUptimeCheck(h.ctx, store.CreateUptimeCheckParams{
		ServiceID: h.serviceID(web), Url: "https://shop.example.com/", ExpectStatus: 200, IntervalS: 60, TimeoutMs: 1000, CreatedAt: now(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// now is 00:00:30 on day 40. The hour before last had 3 of 4 probes
	// up over two minutes, the last hour 1 of 1; minute rows from a day
	// ago and hour rows from 31 days ago are past their retention.
	day := int64(40 * 24 * 3600)
	now := time.Unix(day+30, 0)
	probe := func(at int64, up int64) {
		if err := h.store.AddUptimeProbe(h.ctx, store.AddUptimeProbeParams{CheckID: chk.ID, At: at, Up: up, LatencyMsSum: 100}); err != nil {
			t.Fatal(err)
		}
	}
	probe(day-7200, 1)
	probe(day-7200, 1)
	probe(day-7200+60, 1)
	probe(day-7200+60, 0)
	probe(day-60, 1)
	probe(day-uptimeKeepMinute-60, 1)
	if _, err := h.store.DB.Exec("INSERT INTO uptime_stats (check_id, res, at, up, total, latency_ms_sum) VALUES (?, 3600, ?, 1, 1, 1)", chk.ID, day-31*24*3600); err != nil {
		t.Fatal(err)
	}
	rows := func(res int64) map[int64][2]int64 {
		t.Helper()
		r, err := h.store.DB.Query("SELECT at, up, total FROM uptime_stats WHERE check_id = ? AND res = ?", chk.ID, res)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = r.Close() }()
		out := map[int64][2]int64{}
		for r.Next() {
			var at, up, total int64
			if err := r.Scan(&at, &up, &total); err != nil {
				t.Fatal(err)
			}
			out[at] = [2]int64{up, total}
		}
		return out
	}

	// At 00:00:30 the last hour isn't a minute past its end: only the one
	// before is rolled up.
	end, err := h.c.rollUptime(h.ctx, now, 0)
	if err != nil || end != day-3600 {
		t.Fatalf("rolled up to %d, %v; want %d", end, err, day-3600)
	}
	hours := rows(uptimeHour)
	if len(hours) != 1 || hours[day-7200] != [2]int64{3, 4} {
		t.Fatalf("hour rows at 00:00:30 = %v", hours)
	}
	if minutes := rows(uptimeMinute); len(minutes) != 3 {
		t.Fatalf("minute rows after the trim = %v", minutes)
	}
	if again, err := h.c.rollUptime(h.ctx, now.Add(20*time.Second), end); err != nil || again != 0 {
		t.Fatalf("a second roll-up in the same minute: %d, %v", again, err)
	}
	// At 00:01:30 the last hour is rolled up too.
	if end, err := h.c.rollUptime(h.ctx, now.Add(time.Minute), end); err != nil || end != day {
		t.Fatalf("rolled up to %d, %v; want %d", end, err, day)
	}
	if hours := rows(uptimeHour); len(hours) != 2 || hours[day-3600] != [2]int64{1, 1} {
		t.Fatalf("hour rows at 00:01:30 = %v", hours)
	}
}

// A domain whose DNS isn't proven the service's can't be probed: an org
// admin could point it at private addresses. Removing a domain drops its
// checks.
func TestUptimeNeedsAVerifiedDomain(t *testing.T) {
	h := newHarness(t)
	web := h.uptimeService()
	token, err := h.c.CreateToken(h.ctx, testSystem, apitypes.CreateTokenRequest{Name: "admin", Scope: apitypes.ScopeAdmin, Project: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := h.c.AuthenticateToken(h.ctx, token.Secret, "")
	if _, err := h.c.AddDomain(h.ctx, admin, web, apitypes.AddDomainRequest{Hostname: "unproven.example.org"}); err != nil {
		t.Fatal(err)
	}
	_, err = h.c.AddUptimeCheck(h.ctx, admin, web, apitypes.AddUptimeCheckRequest{URL: "https://unproven.example.org/"})
	if CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "isn't verified yet") {
		t.Fatalf("check on an unverified domain = %v", err)
	}

	if _, err := h.c.AddUptimeCheck(h.ctx, admin, web, apitypes.AddUptimeCheckRequest{URL: "https://shop.example.com/"}); err != nil {
		t.Fatal(err)
	}
	if err := h.c.RemoveDomain(h.ctx, testSystem, web, "shop.example.com"); err != nil {
		t.Fatal(err)
	}
	if checks, err := h.c.ListUptimeChecks(h.ctx, admin, web); err != nil || len(checks) != 0 {
		t.Fatalf("checks after the domain went = %+v, %v", checks, err)
	}
}

// waitUptimeChecks waits until a service has n uptime checks, reading
// them again after each of its events (an added check is audited).
func (h *harness) waitUptimeChecks(ref ServiceRef, n int) {
	h.t.Helper()
	sub := h.c.bus.subscribe(func(e busEvent) bool { return e.ev.Service == ref.Service })
	defer sub.close()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		checks, err := h.c.ListUptimeChecks(h.ctx, testSystem, ref)
		if err != nil {
			h.t.Fatal(err)
		}
		if len(checks) == n {
			return
		}
		select {
		case <-sub.ch:
		case <-timeout.C:
			h.t.Fatalf("%s has %d uptime checks, want %d", ref.Service, len(checks), n)
		}
	}
}

// Verified domains get uptime checks of their own once the service runs
// (ADR 0059): not before its first release, not for a domain on a
// private address, not again after a user removes one; they go with
// their domain, and CheckDomains catches up the rest.
func TestAutoUptimeChecks(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	h.dns.setIP("shop.example.com", "203.0.113.10")
	h.dns.setIP("new.example.com", "203.0.113.11")
	h.dns.setIP("lan.example.com", "192.168.1.5")
	urls := func() []string {
		t.Helper()
		checks, err := h.c.ListUptimeChecks(h.ctx, testSystem, web)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range checks {
			if !c.Auto {
				t.Errorf("%s isn't marked auto", c.URL)
			}
			out = append(out, c.URL)
		}
		return out
	}
	add := func(host string) {
		t.Helper()
		if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: host}); err != nil {
			t.Fatal(err)
		}
	}

	add("shop.example.com")
	if got := urls(); len(got) != 0 {
		t.Fatalf("checks before the first release = %v", got)
	}
	h.deploy(web, "nginx:1")
	// The first release reads as live a moment before its checks exist.
	h.waitUptimeChecks(web, 1)
	if got := urls(); !slices.Equal(got, []string{"https://shop.example.com/"}) {
		t.Fatalf("checks after the first release = %v", got)
	}
	add("lan.example.com")
	add("new.example.com")
	if got := urls(); !slices.Equal(got, []string{"https://shop.example.com/", "https://new.example.com/"}) {
		t.Fatalf("checks after adding domains = %v", got)
	}

	checks, _ := h.c.ListUptimeChecks(h.ctx, testSystem, web)
	if err := h.c.RemoveUptimeCheck(h.ctx, testSystem, web, checks[0].ID); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "nginx:2")
	if err := h.c.RemoveDomain(h.ctx, testSystem, web, "new.example.com"); err != nil {
		t.Fatal(err)
	}
	if got := urls(); len(got) != 0 {
		t.Fatalf("checks after removing one and its domain = %v", got)
	}

	all, err := h.c.CheckDomains(h.ctx, testSystem, web)
	if err != nil || len(all) != 2 {
		t.Fatalf("CheckDomains = %+v, %v", all, err)
	}
	if again, err := h.c.CheckDomains(h.ctx, testSystem, web); err != nil || len(again) != 2 {
		t.Fatalf("CheckDomains again = %d checks, %v", len(again), err)
	}
}
