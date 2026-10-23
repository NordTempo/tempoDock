package core

import (
	"slices"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// addressRoute is the local server's route for host, if it has one.
func (h *harness) addressRoute(host string) (node.Route, bool) {
	for _, r := range h.rt.Routes() {
		if slices.Contains(r.Hostnames, host) {
			return r, true
		}
	}
	return node.Route{}, false
}

func TestAddress(t *testing.T) {
	h := newHarness(t)
	admin := h.user("ada@x", apitypes.ScopeAdmin)
	if _, err := h.c.SetAddress(h.ctx, admin, apitypes.SetAddressRequest{Hostname: "dock.example.com"}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin who isn't an operator: %v", err)
	}
	if _, err := h.c.Address(h.ctx, admin); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("an org admin reading it: %v", err)
	}

	a, err := h.c.SetAddress(h.ctx, System, apitypes.SetAddressRequest{Hostname: "Dock.Example.com."})
	if err != nil {
		t.Fatal(err)
	}
	want := apitypes.DNSRecord{Type: "A", Name: "dock.example.com", Value: "203.0.113.10"}
	if a.Hostname != "dock.example.com" || a.URL != "https://dock.example.com" || a.Status != apitypes.DomainActive ||
		len(a.DNSRecords) != 1 || a.DNSRecords[0] != want || a.PrivateURL != "http://127.0.0.1:7070" {
		t.Fatalf("address = %+v", a)
	}
	r, ok := h.addressRoute("dock.example.com")
	if !ok || r.Upstream != h.opts.Config.PublicListen || r.ClientIPHeader != ClientIPHeader || r.PathPrefix != "" {
		t.Fatalf("route = %+v (found %v)", r, ok)
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, "dock.example.com"); !ok {
		t.Fatal("no certificate for Dock's address")
	}
	if evs := h.platformAudit("platform.address_set"); len(evs) != 1 || evs[0].Target != "dock.example.com" {
		t.Fatalf("audit = %+v", evs)
	}

	// Invite links use it.
	inv, err := h.c.CreateInvite(h.ctx, admin, apitypes.CreateInviteRequest{Email: "bob@x", Role: apitypes.ScopeDeploy})
	if err != nil || inv.URL != "https://dock.example.com/invite/"+inv.Code {
		t.Fatalf("invite = %+v, %v", inv, err)
	}

	// A service can't take it, and it can't take a service's domain.
	ref := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.AddDomain(h.ctx, testSystem, ref, apitypes.AddDomainRequest{Hostname: "dock.example.com"}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Fatalf("a domain on Dock's address: %v", err)
	}
	if _, err := h.c.AddDomain(h.ctx, testSystem, ref, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetAddress(h.ctx, System, apitypes.SetAddressRequest{Hostname: "shop.example.com"}); CodeOf(err) != apitypes.CodeHostnameTaken {
		t.Fatalf("Dock's address on a domain: %v", err)
	}

	// It survives a restart, and removing it unroutes it.
	h.restart()
	if _, ok := h.addressRoute("dock.example.com"); !ok {
		t.Fatal("no route after a restart")
	}
	if a, err = h.c.RemoveAddress(h.ctx, System); err != nil || a.Hostname != "" || a.Status != "" {
		t.Fatalf("removed = %+v, %v", a, err)
	}
	if _, ok := h.addressRoute("dock.example.com"); ok {
		t.Fatal("still routed after removing it")
	}
	if ok, _ := h.c.AllowCertificate(h.ctx, "dock.example.com"); ok {
		t.Fatal("a certificate for a removed address")
	}
	if len(h.platformAudit("platform.address_removed")) != 1 {
		t.Fatal("no audit event for removing it")
	}
}

func TestAddressStatus(t *testing.T) {
	h := newHarness(t)
	h.rt.SetDomainCheck(func(string) node.DomainCheck {
		return node.DomainCheck{ExpectedIPs: []string{"203.0.113.10"}, ResolvedIPs: []string{"198.51.100.7"}}
	})
	a, err := h.c.SetAddress(h.ctx, System, apitypes.SetAddressRequest{Hostname: "dock.example.com"})
	if err != nil || a.Status != apitypes.DomainWaitingForDNS || a.CheckedAt == nil {
		t.Fatalf("address = %+v, %v", a, err)
	}
	h.rt.SetDomainCheck(func(string) node.DomainCheck { return node.DomainCheck{DNSOK: true} })
	if a, err = h.c.Address(h.ctx, System); err != nil || a.Status != apitypes.DomainIssuingCertificate {
		t.Fatalf("address = %+v, %v", a, err)
	}
}
