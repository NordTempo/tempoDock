package core

import (
	"slices"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Domains carry their URL and what the last DNS check saw.
func TestDomainURLAndLastCheck(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	h.rt.SetDomainCheck(func(string) node.DomainCheck {
		return node.DomainCheck{ExpectedIPs: []string{"203.0.113.10"}, ResolvedIPs: []string{"198.51.100.7"}}
	})
	dc, err := h.c.AddDomain(h.ctx, System, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	// The background checker may already have run, so only the URL is fixed.
	if dc.Domain.URL != "https://shop.example.com" {
		t.Fatalf("new domain: %+v", dc.Domain)
	}

	ss, _ := h.c.serviceFor(h.ctx, System, web, apitypes.ScopeRead, "testing")
	rows, _ := h.store.ListDomainsByService(h.ctx, ss.svc.ID)
	var dnsOK time.Time
	h.c.checkDomain(h.ctx, rows[0], &dnsOK)
	ds, err := h.c.ListDomains(h.ctx, System, web)
	if err != nil {
		t.Fatal(err)
	}
	d := ds[0]
	if d.Status != apitypes.DomainWaitingForDNS || d.CheckedAt == nil || !slices.Equal(d.ResolvedIPs, []string{"198.51.100.7"}) {
		t.Fatalf("checked domain: %+v", d)
	}

	h.opts.Config.Caddy.DisableTLS = true
	h.restart()
	if ds, _ := h.c.ListDomains(h.ctx, System, web); ds[0].URL != "http://shop.example.com" {
		t.Fatalf("URL without TLS: %s", ds[0].URL)
	}
}
