package uptime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if res.Up || !strings.Contains(res.Error, "this server itself") {
		t.Fatalf("probe of 127.0.0.1 = %+v, want refused", res)
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
			time.Sleep(300 * time.Millisecond)
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
