package core

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/NordTempo/tempoDock/internal/access"
	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// routeOf is the route the fake runtime last got for host.
func (h *harness) routeOf(host string) node.Route {
	h.t.Helper()
	for _, r := range h.rt.Routes() {
		for _, n := range r.Hostnames {
			if n == host {
				return r
			}
		}
	}
	h.t.Fatalf("no route for %s in %+v", host, h.rt.Routes())
	return node.Route{}
}

// Access applies to the service's routes at once: a password as a bcrypt
// hash, a login as a key and Dock's /access page, public as no access.
func TestServiceAccessRoutes(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	h.deploy(web, "app:1")
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	if a, err := h.c.ServiceAccess(h.ctx, testSystem, web); err != nil || a.Mode != apitypes.AccessPublic {
		t.Fatalf("new service: %+v %v", a, err)
	}

	set := func(req apitypes.SetAccessRequest) apitypes.Access {
		t.Helper()
		a, err := h.c.SetServiceAccess(h.ctx, testSystem, web, req)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := set(apitypes.SetAccessRequest{Mode: apitypes.AccessPassword, Username: "team", Password: "correct horse battery"})
	r := h.routeOf("shop.example.com")
	if a.Mode != apitypes.AccessPassword || a.Username != "team" || r.Access == nil || r.Access.Username != "team" ||
		bcrypt.CompareHashAndPassword([]byte(r.Access.PasswordHash), []byte("correct horse battery")) != nil {
		t.Fatalf("password: %+v %+v", a, r.Access)
	}
	// An empty password keeps the current one.
	set(apitypes.SetAccessRequest{Mode: apitypes.AccessPassword, Username: "crew"})
	if r2 := h.routeOf("shop.example.com"); r2.Access.Username != "crew" || r2.Access.PasswordHash != r.Access.PasswordHash {
		t.Fatalf("username change: %+v", r2.Access)
	}

	set(apitypes.SetAccessRequest{Mode: apitypes.AccessLogin})
	r = h.routeOf("shop.example.com")
	if !r.Access.Login() || r.Access.PasswordHash != "" || r.Access.LoginURL != "http://"+h.opts.Config.Listen+"/access" {
		t.Fatalf("login: %+v", r.Access)
	}
	// Saving the login again keeps everyone signed in; leaving it and
	// coming back signs them out.
	key := r.Access.Key
	set(apitypes.SetAccessRequest{Mode: apitypes.AccessLogin})
	if string(h.routeOf("shop.example.com").Access.Key) != string(key) {
		t.Fatal("saving login again changed the key")
	}
	h.restart()
	if string(h.routeOf("shop.example.com").Access.Key) != string(key) {
		t.Fatal("the key didn't survive a restart")
	}
	set(apitypes.SetAccessRequest{Mode: apitypes.AccessPublic})
	if r := h.routeOf("shop.example.com"); r.Access != nil {
		t.Fatalf("public: %+v", r.Access)
	}
	set(apitypes.SetAccessRequest{Mode: apitypes.AccessLogin})
	if string(h.routeOf("shop.example.com").Access.Key) == string(key) {
		t.Fatal("login after public reused the old key")
	}
}

func TestSetServiceAccessRejects(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	reader := Actor{Kind: "token", OrgID: 1, Name: "ro", TokenID: 1, Scope: apitypes.ScopeRead}
	for name, tc := range map[string]struct {
		a    Actor
		req  apitypes.SetAccessRequest
		code apitypes.ErrorCode
	}{
		"read scope":         {reader, apitypes.SetAccessRequest{Mode: apitypes.AccessLogin}, apitypes.CodeForbidden},
		"unknown mode":       {testSystem, apitypes.SetAccessRequest{Mode: "vpn"}, apitypes.CodeValidation},
		"no username":        {testSystem, apitypes.SetAccessRequest{Mode: apitypes.AccessPassword, Password: "long enough password"}, apitypes.CodeValidation},
		"colon in username":  {testSystem, apitypes.SetAccessRequest{Mode: apitypes.AccessPassword, Username: "a:b", Password: "long enough password"}, apitypes.CodeValidation},
		"short password":     {testSystem, apitypes.SetAccessRequest{Mode: apitypes.AccessPassword, Username: "team", Password: "short"}, apitypes.CodeValidation},
		"no password at all": {testSystem, apitypes.SetAccessRequest{Mode: apitypes.AccessPassword, Username: "team"}, apitypes.CodeValidation},
	} {
		if _, err := h.c.SetServiceAccess(h.ctx, tc.a, web, tc.req); CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
	if a, _ := h.c.ServiceAccess(h.ctx, reader, web); a.Mode != apitypes.AccessPublic {
		t.Errorf("after rejected changes: %+v", a)
	}
}

// A grant names the caller and opens only a login-protected domain, for
// members who can read its environment, whichever org they act in.
func TestGrantAccess(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1", Port: 8080})
	h.deploy(web, "app:1")
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	ada := h.member("ada@x", map[string]apitypes.Scope{"test": apitypes.ScopeRead, "acme": apitypes.ScopeAdmin})
	req := apitypes.AccessGrantRequest{Hostname: "Shop.Example.com", To: "/orders?id=7"}
	if _, err := h.c.GrantAccess(h.ctx, ada, req); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("public service: %v", err)
	}
	if _, err := h.c.SetServiceAccess(h.ctx, testSystem, web, apitypes.SetAccessRequest{Mode: apitypes.AccessLogin}); err != nil {
		t.Fatal(err)
	}

	// Ada has two orgs and acts in neither, or in the other one.
	for _, a := range []Actor{ada, h.in(ada, "acme")} {
		g, err := h.c.GrantAccess(h.ctx, a, req)
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(g.URL)
		if err != nil || u.Scheme != "https" || u.Host != "shop.example.com" || u.Path != access.CallbackPath || u.Query().Get("to") != "/orders?id=7" {
			t.Fatalf("grant URL %q", g.URL)
		}
		key := h.routeOf("shop.example.com").Access.Key
		if user, ok := access.Verify(key, u.Query().Get("grant"), access.Grant, "shop.example.com", time.Now()); !ok || user != "ada@x" {
			t.Fatalf("grant names %q (%v)", user, ok)
		}
	}
	if g, _ := h.c.GrantAccess(h.ctx, ada, apitypes.AccessGrantRequest{Hostname: "shop.example.com", To: "https://evil.example"}); !strings.Contains(g.URL, "to=%2F&") && !strings.HasSuffix(g.URL, "to=%2F") {
		t.Errorf("off-site to: %s", g.URL)
	}

	bob := h.member("bob@x", map[string]apitypes.Scope{"acme": apitypes.ScopeAdmin})
	p, _ := h.store.GetProjectByName(h.ctx, store.GetProjectByNameParams{OrgID: 1, Name: "shop"})
	otherEnv := Actor{Kind: "token", OrgID: 1, Name: "dev", Scope: apitypes.ScopeAdmin, ProjectID: p.ID, ProjectName: "shop", EnvironmentID: -1, EnvironmentName: "dev"}
	for name, tc := range map[string]struct {
		a    Actor
		host string
		code apitypes.ErrorCode
	}{
		"not a member":      {bob, "shop.example.com", apitypes.CodeForbidden},
		"other environment": {otherEnv, "shop.example.com", apitypes.CodeForbidden},
		"unknown host":      {ada, "nope.example.com", apitypes.CodeNotFound},
		"not a hostname":    {ada, "{x}", apitypes.CodeNotFound},
	} {
		if _, err := h.c.GrantAccess(h.ctx, tc.a, apitypes.AccessGrantRequest{Hostname: tc.host}); CodeOf(err) != tc.code {
			t.Errorf("%s: %v, want %s", name, err, tc.code)
		}
	}
}
