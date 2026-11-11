package core

import (
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// A page's custom hostname is served, routed and given certificates only
// once its TXT record proves the org controls it; then no service and no
// other page can take it. An operator's needs no proof.
func TestStatusHostname(t *testing.T) {
	h, _, _ := incidentPage(t)
	admin := h.user("admin@example.com", apitypes.ScopeAdmin)
	const host = "status.acme.com"

	p, err := h.c.UpdateStatusPage(h.ctx, admin, "acme", apitypes.UpdateStatusPageRequest{Hostname: new("Status.Acme.com")})
	if err != nil {
		t.Fatal(err)
	}
	hn := p.Hostname
	if hn == nil || hn.Hostname != host || hn.Verified || len(hn.Records) != 2 || hn.Records[0].Type != "TXT" ||
		hn.Records[0].Name != "_dock-challenge."+host || hn.Records[1] != (apitypes.DNSRecord{Type: "CNAME", Name: host, Value: "status-acme.dock.example.net"}) {
		t.Fatalf("hostname = %+v", hn)
	}
	if _, _, ok := h.c.StatusPageAt(host); ok || h.routed(host) {
		t.Error("an unverified hostname is served")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, host); ok {
		t.Error("an unverified hostname gets a certificate")
	}
	if p, err = h.c.CheckStatusHostname(h.ctx, admin, "acme"); err != nil || p.Hostname.Verified {
		t.Fatalf("checked without the record: %+v, %v", p.Hostname, err)
	}

	h.dns.set(hn.Records[0].Name, hn.Records[0].Value)
	if p, err = h.c.CheckStatusHostname(h.ctx, admin, "acme"); err != nil || !p.Hostname.Verified || p.Hostname.URL != "https://"+host ||
		len(p.Hostname.Records) != 1 {
		t.Fatalf("checked with the record: %+v, %v", p.Hostname, err)
	}
	if _, _, ok := h.c.StatusPageAt(host); !ok || !h.routed(host) {
		t.Error("a verified hostname isn't served")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, host); !ok {
		t.Error("a verified hostname gets no certificate")
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}, apitypes.AddDomainRequest{Hostname: host}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("a service took the page's hostname: %v", err)
	}
	if _, err := h.c.CreateStatusPage(h.ctx, testSystem, apitypes.CreateStatusPageRequest{Slug: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.UpdateStatusPage(h.ctx, testSystem, "other", apitypes.UpdateStatusPageRequest{Hostname: new(host)}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("another page took the hostname: %v", err)
	}

	// A service's verified domain is taken; an operator's claim needs no
	// proof.
	if _, err := h.c.AddDomain(h.ctx, testSystem, ServiceRef{"shop", "production", "web"}, apitypes.AddDomainRequest{Hostname: "shop.example.org"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.UpdateStatusPage(h.ctx, admin, "other", apitypes.UpdateStatusPageRequest{Hostname: new("shop.example.org")}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Errorf("a page took a service's domain: %v", err)
	}
	if p, err := h.c.UpdateStatusPage(h.ctx, testSystem, "other", apitypes.UpdateStatusPageRequest{Hostname: new("status.example.org")}); err != nil || !p.Hostname.Verified {
		t.Errorf("an operator's hostname: %+v, %v", p.Hostname, err)
	}

	if p, err = h.c.UpdateStatusPage(h.ctx, admin, "acme", apitypes.UpdateStatusPageRequest{Hostname: new("")}); err != nil || p.Hostname != nil {
		t.Fatalf("removed: %+v, %v", p.Hostname, err)
	}
	if _, _, ok := h.c.StatusPageAt(host); ok || h.routed(host) {
		t.Error("a removed hostname is still served")
	}
}
