package core

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// echoServer stands in for a service: it echoes every connection and
// reports on ended when one closes.
func echoServer(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ended := make(chan struct{}, 64)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.Close()
				ended <- struct{}{}
			}()
		}
	}()
	return ln.Addr().String(), ended
}

// tcpPair is a connected pair of TCP connections, the client's end and
// the daemon's (as a hijacked request would give it).
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if server, err = ln.Accept(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = client.Close(), server.Close() })
	return client, server
}

// connectFixture is shop with a running Postgres db in production and
// a web service in staging, dialing an echo server.
func connectFixture(t *testing.T) (*harness, ServiceRef, ServiceRef, <-chan struct{}) {
	h := newHarness(t)
	addr, ended := echoServer(t)
	h.rt.DialTo(addr)
	db := h.service("db", apitypes.CreateServiceRequest{Image: "postgres:17"})
	h.setVars(db, map[string]string{"POSTGRES_PASSWORD": "s3cret", "POSTGRES_DB": "app"})
	h.deploy(db, "postgres:17")
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	web := ServiceRef{"shop", "staging", "web"}
	if _, err := h.c.CreateService(h.ctx, testSystem, web.Env(), apitypes.CreateServiceRequest{Name: "web", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	h.deploy(web, "nginx:1")
	return h, db, web, ended
}

// TestConnectPermissions: tunnels need level write with sensitive
// visible; agents never get them on production; production needs the
// environment's name.
func TestConnectPermissions(t *testing.T) {
	h, db, web, _ := connectFixture(t)
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	full := apitypes.DataTokenAccess{Production: apitypes.LevelWrite, NonProduction: apitypes.LevelWrite, Sensitive: apitypes.SensitiveVisible}
	agent := h.token(admin, apitypes.CreateTokenRequest{Name: "agent", Scope: apitypes.ScopeDeploy, Data: &full})
	adminAgent := h.token(admin, apitypes.CreateTokenRequest{Name: "admin-agent", Scope: apitypes.ScopeAdmin})

	check := func(name string, a Actor, ref ServiceRef, confirm string, want apitypes.ErrorCode) {
		t.Helper()
		_, err := h.c.ConnectInfo(h.ctx, a, ref, ConnectRequest{Confirm: confirm})
		if got := CodeOf(err); err == nil && want != "" || err != nil && got != want {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	check("admin, production", admin, db, "production", "")
	check("admin, production without confirm", admin, db, "", apitypes.CodeValidation)
	check("admin, non-production", admin, web, "", "")
	check("admin token, non-production", adminAgent, web, "", "")
	check("admin token on production: write acts as propose", adminAgent, db, "production", apitypes.CodeForbidden)
	check("deploy user by default (read, masked)", dev, web, "", apitypes.CodeForbidden)
	check("agent capped by deploy users' default", agent, web, "", apitypes.CodeForbidden)

	setRules := func(sens apitypes.DataSensitivity) {
		t.Helper()
		var rules []apitypes.DataPolicyRule
		for _, kind := range []apitypes.EnvironmentKind{apitypes.KindProduction, apitypes.KindNonProduction} {
			rules = append(rules, apitypes.DataPolicyRule{Subject: apitypes.SubjectDeployUsers, Kind: kind, Level: apitypes.LevelWrite, Sensitive: sens})
		}
		if _, err := h.c.SetDataPolicy(h.ctx, testSystem, EnvRef{}, apitypes.UpdateDataPolicyRequest{Rules: rules}); err != nil {
			t.Fatal(err)
		}
	}
	setRules(apitypes.SensitiveMasked)
	check("deploy user with write, masked", dev, web, "", apitypes.CodeForbidden)
	setRules(apitypes.SensitiveVisible)
	check("deploy user with write, visible", dev, web, "", "")
	check("deploy user with write, visible, production", dev, db, "production", "")
	check("agent with write, visible, non-production", agent, web, "", "")
	check("agent on production", agent, db, "production", apitypes.CodeForbidden)

	_, err := h.c.ConnectInfo(h.ctx, agent, db, ConnectRequest{Confirm: "production"})
	if err == nil || !strings.Contains(err.Error(), "tokens only propose") {
		t.Errorf("agent's error doesn't say why: %v", err)
	}
	read := h.user("viewer@x", apitypes.ScopeRead)
	if _, err := h.c.OpenTunnel(h.ctx, read, db, TunnelRequest{Confirm: "production"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Errorf("open without access: %v", err)
	}
}

// TestConnectInfo: what a tunnel reaches, and the URL only on request,
// audited.
func TestConnectInfo(t *testing.T) {
	h, db, web, _ := connectFixture(t)
	info, err := h.c.ConnectInfo(h.ctx, testSystem, db, ConnectRequest{Confirm: "production"})
	if err != nil || info.Port != 5432 || info.Engine != apitypes.EnginePostgres || info.User != "postgres" || info.Database != "app" || info.URL != "" || !info.Confirm {
		t.Fatalf("db: %+v %v", info, err)
	}
	if evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: apitypes.AuditDataReveal}); len(evs) != 0 {
		t.Fatalf("audited without reveal: %+v", evs)
	}
	info, err = h.c.ConnectInfo(h.ctx, testSystem, db, ConnectRequest{Confirm: "production", Reveal: true})
	if err != nil || info.URL != "postgres://postgres:s3cret@db:5432/app" {
		t.Fatalf("revealed: %+v %v", info, err)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: apitypes.AuditDataReveal})
	if len(evs) != 1 || evs[0].Target != "shop/production/db" || strings.Contains(evs[0].Summary, "s3cret") {
		t.Fatalf("reveal audit: %+v", evs)
	}

	info, err = h.c.ConnectInfo(h.ctx, testSystem, web, ConnectRequest{})
	if err != nil || info.Port != 3000 || info.Engine != "" || info.Confirm {
		t.Fatalf("web: %+v %v", info, err)
	}
	if _, err := h.c.ConnectInfo(h.ctx, testSystem, web, ConnectRequest{Reveal: true}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("no URL to reveal: %v", err)
	}
	h.setVars(web, map[string]string{"REDIS_URL": "redis://default:pw@web:6379"})
	if info, err := h.c.ConnectInfo(h.ctx, testSystem, web, ConnectRequest{Reveal: true}); err != nil || info.URL != "redis://default:pw@web:6379" {
		t.Fatalf("REDIS_URL: %+v %v", info, err)
	}

	stopped := h.service("idle", apitypes.CreateServiceRequest{Port: 80})
	if _, err := h.c.ConnectInfo(h.ctx, testSystem, stopped, ConnectRequest{Confirm: "production"}); CodeOf(err) != apitypes.CodeConflict || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("not deployed: %v", err)
	}
}

// TestTunnelRoundTrip: bytes go both ways, a closed client closes the
// service's connection at once, and open and close are audited without
// the traffic.
func TestTunnelRoundTrip(t *testing.T) {
	h, _, web, ended := connectFixture(t)
	tun, err := h.c.OpenTunnel(h.ctx, testSystem, web, TunnelRequest{From: "100.64.0.2"})
	if err != nil {
		t.Fatal(err)
	}
	client, server := tcpPair(t)
	piped := make(chan struct{})
	go func() { tun.Pipe(server, []byte("early ")); close(piped) }()

	if _, err := client.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len("early hello"))
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(client, buf); err != nil || string(buf) != "early hello" {
		t.Fatalf("echo: %q %v", buf, err)
	}

	_ = client.Close()
	for _, ch := range []<-chan struct{}{ended, piped} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("closing the client didn't end the tunnel")
		}
	}

	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: "data."})
	if len(evs) != 2 {
		t.Fatalf("audit: %+v", evs)
	}
	closed, opened := evs[0], evs[1]
	if opened.Action != apitypes.AuditDataConnect || opened.Target != "shop/staging/web" || opened.Summary != "opened a tunnel to port 3000 from 100.64.0.2" {
		t.Errorf("open: %+v", opened)
	}
	if closed.Action != apitypes.AuditDataDisconnect || !strings.Contains(closed.Summary, "11 B sent, 11 B received") || strings.Contains(closed.Summary, "hello") {
		t.Errorf("close: %+v", closed)
	}
}

// TestTunnelLimit: an actor has at most TunnelsPerActor open; others
// aren't affected, and a closed tunnel frees its slot. Stopping the
// daemon closes the rest and audits them.
func TestTunnelLimit(t *testing.T) {
	h, _, web, _ := connectFixture(t)
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	var open []*Tunnel
	for range apitypes.TunnelsPerActor {
		tun, err := h.c.OpenTunnel(h.ctx, admin, web, TunnelRequest{From: "x"})
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, tun)
	}
	if _, err := h.c.OpenTunnel(h.ctx, admin, web, TunnelRequest{From: "x"}); CodeOf(err) != apitypes.CodeRateLimited {
		t.Fatalf("over the limit: %v", err)
	}
	other, err := h.c.OpenTunnel(h.ctx, testSystem, web, TunnelRequest{From: "x"})
	if err != nil {
		t.Fatalf("another actor: %v", err)
	}
	other.Close()
	open[0].Close()
	if open[0], err = h.c.OpenTunnel(h.ctx, admin, web, TunnelRequest{From: "x"}); err != nil {
		t.Fatalf("after one closed: %v", err)
	}

	// Stopping the daemon ends a piping tunnel and audits its close.
	open[0].Close()
	for _, tun := range open[2:] {
		tun.Close()
	}
	client, server := tcpPair(t)
	piped := make(chan struct{})
	go func() { open[1].Pipe(server, nil); close(piped) }()
	h.c.Close()
	select {
	case <-piped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the daemon didn't end the tunnel")
	}
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Error("client still open after shutdown")
	}
	h.c = h.start()
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: apitypes.AuditDataDisconnect})
	if len(evs) != apitypes.TunnelsPerActor+2 {
		t.Errorf("%d closes audited, want %d", len(evs), apitypes.TunnelsPerActor+2)
	}
}
