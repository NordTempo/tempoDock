package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// cacheService deploys shop/staging/cache (port 6379) on the fake
// runtime, dialing an echo server that reports each connection's end.
// API tokens are agents: they never tunnel into production.
func (f *fixture) cacheService() <-chan struct{} {
	f.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = ln.Close() })
	ended := make(chan struct{}, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close(); ended <- struct{}{} }()
		}
	}()
	f.rt.DialTo(ln.Addr().String())

	ctx := context.Background()
	ref := core.ServiceRef{Project: "shop", Environment: "staging", Service: "cache"}
	if _, err := f.core.CreateEnvironment(ctx, core.System, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.core.CreateService(ctx, core.System, ref.Env(), apitypes.CreateServiceRequest{Name: "cache", Port: 6379}); err != nil {
		f.t.Fatal(err)
	}
	rel, err := f.core.DeployImage(ctx, core.System, ref, apitypes.DeployImageRequest{Image: "redis:8"})
	if err != nil {
		f.t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(2 * time.Millisecond) {
		r, err := f.core.GetRelease(ctx, core.System, ref, rel.Number)
		if err != nil || r.Status == apitypes.ReleaseActive {
			break
		}
		if time.Now().After(deadline) || r.Status.Done() {
			f.t.Fatalf("deploy: %s", r.Status)
		}
	}
	return ended
}

// tunnelRequest sends a tunnel request on a new connection and returns the
// connection with the response read from it.
func (f *fixture) tunnelRequest(header string) (net.Conn, *bufio.Reader, *http.Response) {
	f.t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(f.srv.URL, "http://"))
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET " + shopStaging + "/services/cache/tunnel HTTP/1.1\r\nHost: dock\r\n" + header + "\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		f.t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return conn, br, resp
}

const shopStaging = "/api/v1/projects/shop/environments/staging"

func TestTunnel(t *testing.T) {
	f := newFixture(t)
	ended := f.cacheService()
	upgrade := "Connection: Upgrade\r\nUpgrade: dock-tunnel\r\n"
	bearer := func(name string) string { return "Authorization: Bearer " + f.tokens[name] + "\r\n" }

	for _, tc := range []struct {
		name, header string
		status       int
	}{
		{"no upgrade", bearer("admin"), http.StatusUpgradeRequired},
		{"no token", upgrade, http.StatusUnauthorized},
		{"session cookie only", upgrade + "Cookie: dock_session=x\r\n", http.StatusUnauthorized},
		{"deploy token", upgrade + bearer("deploy"), http.StatusForbidden},
	} {
		_, _, resp := f.tunnelRequest(tc.header)
		var p apitypes.Error
		_ = json.NewDecoder(resp.Body).Decode(&p)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.status || p.Detail == "" {
			t.Errorf("%s: %d %+v, want %d", tc.name, resp.StatusCode, p, tc.status)
		}
	}

	conn, br, resp := f.tunnelRequest(upgrade + bearer("admin"))
	_ = resp.Body.Close() // a 101 has none; the bytes follow on conn
	if resp.StatusCode != http.StatusSwitchingProtocols || resp.Header.Get("Upgrade") != apitypes.TunnelProtocol {
		t.Fatalf("upgrade: %d %v", resp.StatusCode, resp.Header)
	}
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		t.Fatal(err)
	}
	line, err := br.ReadString('\n')
	if err != nil || line != "PING\r\n" {
		t.Fatalf("echo: %q %v", line, err)
	}
	_ = conn.Close()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("closing the client didn't close the service's connection")
	}

	var evs []apitypes.AuditEvent
	for deadline := time.Now().Add(5 * time.Second); len(evs) < 2 && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		evs, _ = f.core.ListAudit(context.Background(), core.System, core.AuditFilter{Action: "data."})
	}
	if len(evs) != 2 || evs[1].Action != apitypes.AuditDataConnect || evs[1].Summary != "opened a tunnel to port 6379 from 127.0.0.1" ||
		evs[0].Action != apitypes.AuditDataDisconnect || evs[0].Actor != "admin" {
		t.Fatalf("audit: %+v", evs)
	}
}

func TestConnectInfoEndpoint(t *testing.T) {
	f := newFixture(t)
	f.cacheService()
	resp, body := f.do("admin", "GET", shopStaging+"/services/cache/connect", nil)
	var info apitypes.ConnectInfo
	if err := json.Unmarshal(body, &info); err != nil || resp.StatusCode != http.StatusOK || info.Port != 6379 || info.Service != "shop/staging/cache" || info.Confirm {
		t.Fatalf("info: %d %s", resp.StatusCode, body)
	}
	if resp, body := f.do("admin", "GET", shopProd+"/services/web/connect?confirm=production", nil); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "tokens only propose") {
		t.Fatalf("an API token on production: %d %s", resp.StatusCode, body)
	}
}
