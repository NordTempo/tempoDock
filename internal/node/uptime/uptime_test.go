package uptime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

func TestProbeRefusesThisServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the probe reached a loopback site")
	}))
	defer srv.Close()
	res, err := New(false).Probe(context.Background(), node.ProbeRequest{URL: srv.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if res.Up || !strings.Contains(res.Error, "isn't public") || strings.Contains(res.Error, "127.0.0.1") {
		t.Fatalf("probe of 127.0.0.1 = %+v, want refused without naming the address", res)
	}
}

// Probes reach public addresses on 80 and 443 only: never private, CGNAT
// (tailnets), ULA, Docker bridge or metadata addresses a domain's DNS
// could point at.
func TestProbeOnlyDialsPublicAddresses(t *testing.T) {
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"203.0.113.7:443", true}, {"[2001:db8::1]:80", true},
		{"203.0.113.7:8080", false}, {"10.1.2.3:443", false}, {"192.168.10.50:80", false},
		{"172.17.0.2:80", false}, {"100.101.102.103:443", false}, {"[fd12::1]:443", false},
		{"169.254.169.254:80", false}, {"127.0.0.1:443", false}, {"0.0.0.0:80", false},
	} {
		ap := netip.MustParseAddrPort(tc.addr)
		if got := refused(ap.Addr(), ap.Port(), false) == ""; got != tc.ok {
			t.Errorf("%s allowed = %v, want %v", tc.addr, got, tc.ok)
		}
	}
}

func TestProbeStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moved":
			http.Redirect(w, r, "/gone", http.StatusFound)
		case "/gone":
			w.WriteHeader(http.StatusBadGateway)
		case "/empty":
			w.WriteHeader(http.StatusNoContent)
		case "/slow":
			<-r.Context().Done() // until the probe gives up
		}
	}))
	defer srv.Close()
	p := New(true)
	for _, tc := range []struct {
		path   string
		expect int
		up     bool
		status int
		err    string
	}{
		{"/", 0, true, 200, ""},
		{"/gone", 0, false, 502, "answered 502, expected 200"},
		{"/empty", node.ExpectClass2xx, true, 204, ""},
		{"/empty", 200, false, 204, "answered 204, expected 200"},
		// Redirects aren't followed: the 302 itself is the answer.
		{"/moved", node.ExpectClass3xx, true, 302, ""},
		{"/moved", node.ExpectClass2xx, false, 302, "expected 2xx"},
		{"/slow", 0, false, 0, "no answer within 100ms"},
	} {
		res, err := p.Probe(context.Background(), node.ProbeRequest{URL: srv.URL + tc.path, ExpectStatus: tc.expect, Timeout: 100 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		if res.Up != tc.up || res.Status != tc.status || !strings.Contains(res.Error, tc.err) || (tc.err == "") != (res.Error == "") {
			t.Errorf("%s expecting %d = %+v, want up %v, status %d, error %q", tc.path, tc.expect, res, tc.up, tc.status, tc.err)
		}
	}
}

func TestProbeRejectsOtherSchemes(t *testing.T) {
	if _, err := New(true).Probe(context.Background(), node.ProbeRequest{URL: "ftp://example.com/"}); err == nil {
		t.Fatal("an ftp URL was probed")
	}
}

// A probe's cookie goes to the probed URL and no further: the redirect
// it answers with isn't followed.
func TestProbeCookieStaysOnItsHost(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	var got atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Store(r.Header.Get("Cookie"))
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	res, err := New(true).Probe(context.Background(), node.ProbeRequest{URL: srv.URL + "/", Timeout: time.Second, Cookie: "dock_access=token"})
	if err != nil || res.Status != http.StatusFound {
		t.Fatalf("probe = %+v, %v", res, err)
	}
	if got.Load() != "dock_access=token" || elsewhere.Load() != 0 {
		t.Fatalf("cookie sent %q, %d requests followed the redirect", got.Load(), elsewhere.Load())
	}
}
