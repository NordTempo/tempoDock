package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Docker's view of where an image comes from: no host is Docker Hub, and
// Docker Hub's other names are the same registry.
func TestImageRegistry(t *testing.T) {
	for ref, want := range map[string]string{
		"nginx":                           "docker.io",
		"nginx:1.27":                      "docker.io",
		"acme/app:1":                      "docker.io",
		"docker.io/acme/app":              "docker.io",
		"index.docker.io/library/nginx":   "docker.io",
		"registry-1.docker.io/acme/app:2": "docker.io",
		"ghcr.io/acme/app:1":              "ghcr.io",
		"GHCR.io/acme/app":                "ghcr.io",
		"registry.example.com:5000/app@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": "registry.example.com:5000",
		"localhost/app": "localhost",
		"Not A Ref":     "",
	} {
		if got := imageRegistry(ref); got != want {
			t.Errorf("imageRegistry(%q) = %q, want %q", ref, got, want)
		}
	}
}

// A credential's host is a registry host as an image reference names it,
// never Dock's own registry.
func TestParseRegistryHost(t *testing.T) {
	for in, want := range map[string]string{
		"ghcr.io":                   "ghcr.io",
		" GHCR.IO ":                 "ghcr.io",
		"registry.example.com:5000": "registry.example.com:5000",
		"docker.io":                 "docker.io",
		"index.docker.io":           "docker.io",
		"registry-1.docker.io":      "docker.io",
		"localhost:5000":            "localhost:5000",
	} {
		if got, err := parseRegistryHost(in, "127.0.0.1:5000"); err != nil || got != want {
			t.Errorf("parseRegistryHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "https://ghcr.io", "ghcr.io/acme", "ghcr.io/", "nginx", "acme", "ghcr.io:port", "a b.io", "127.0.0.1:5000"} {
		if got, err := parseRegistryHost(in, "127.0.0.1:5000"); CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("parseRegistryHost(%q) = %q, %v; want a validation error", in, got, err)
		}
	}
}

// Only unlimited admins manage registry credentials. The secret is sealed
// in the database and never comes back: not in the list, the result or
// the audit log.
func TestRegistryCredentials(t *testing.T) {
	h := newHarness(t)
	h.opts.Registry = &fakeRegistry{}
	h.restart()
	const secret = "ghp_s3cret-token"
	req := apitypes.SetRegistryCredentialRequest{Username: "acme-bot", Password: secret}

	deployer := h.in(h.member("dev@x", map[string]apitypes.Scope{"test": apitypes.ScopeDeploy}), "test")
	limited := Actor{Kind: "token", OrgID: 1, Name: "ops", Scope: apitypes.ScopeAdmin, ProjectID: 1, ProjectName: "shop"}
	for _, a := range []Actor{deployer, limited} {
		if _, err := h.c.SetRegistryCredential(h.ctx, a, "ghcr.io", req); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s setting: %v", a, err)
		}
		if _, err := h.c.ListRegistryCredentials(h.ctx, a); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s listing: %v", a, err)
		}
		if err := h.c.DeleteRegistryCredential(h.ctx, a, "ghcr.io"); CodeOf(err) != apitypes.CodeForbidden {
			t.Fatalf("%s removing: %v", a, err)
		}
	}

	if _, err := h.c.SetRegistryCredential(h.ctx, testSystem, "127.0.0.1:5000", req); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "Dock's own registry") {
		t.Fatalf("credentials for Dock's own registry: %v", err)
	}
	if _, err := h.c.SetRegistryCredential(h.ctx, testSystem, "ghcr.io", apitypes.SetRegistryCredentialRequest{Username: "acme-bot"}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("no password: %v", err)
	}
	set, err := h.c.SetRegistryCredential(h.ctx, testSystem, "GHCR.io", req)
	if err != nil || set.Host != "ghcr.io" || set.Username != "acme-bot" {
		t.Fatalf("set = %+v, %v", set, err)
	}
	if _, err := h.c.SetRegistryCredential(h.ctx, testSystem, "ghcr.io", apitypes.SetRegistryCredentialRequest{Username: "acme-bot2", Password: secret + "2"}); err != nil {
		t.Fatal(err)
	}

	list, err := h.c.ListRegistryCredentials(h.ctx, testSystem)
	if err != nil || len(list) != 1 || list[0].Username != "acme-bot2" || list[0].CreatedAt != set.CreatedAt {
		t.Fatalf("list = %+v, %v; want the replaced credential, created when it was first set", list, err)
	}
	if b, _ := json.Marshal(list); bytes.Contains(b, []byte(secret)) {
		t.Fatalf("the list holds the secret: %s", b)
	}
	var stored []byte
	if err := h.store.DB.QueryRowContext(h.ctx, "SELECT secret FROM registry_credentials WHERE host = 'ghcr.io'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(secret)) {
		t.Fatal("the secret is stored in plain text")
	}

	if err := h.c.DeleteRegistryCredential(h.ctx, testSystem, "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	if err := h.c.DeleteRegistryCredential(h.ctx, testSystem, "ghcr.io"); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("removing it again: %v", err)
	}
	events, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range events {
		if strings.Contains(e.Summary, secret) {
			t.Fatalf("the audit log holds the secret: %+v", e)
		}
		if e.Target == "ghcr.io" {
			actions = append(actions, e.Action)
		}
	}
	if want := []string{"org.registry_credential_remove", "org.registry_credential_set", "org.registry_credential_set"}; !slices.Equal(actions, want) {
		t.Fatalf("audit = %v, want %v", actions, want)
	}
}

// A deploy pulls with the org's credential for its image's registry host,
// Docker Hub under any of its names, and anonymously from other hosts.
// Without one, a refused pull says how to add one.
func TestDeployPullsWithRegistryCredentials(t *testing.T) {
	h := newHarness(t)
	for host, user := range map[string]string{"ghcr.io": "ghcr-bot", "index.docker.io": "hub-bot"} {
		if _, err := h.c.SetRegistryCredential(h.ctx, testSystem, host, apitypes.SetRegistryCredentialRequest{Username: user, Password: "pw-" + user}); err != nil {
			t.Fatal(err)
		}
	}
	// Another org's credential is never used here.
	other := h.in(h.member("ada@x", map[string]apitypes.Scope{"other": apitypes.ScopeAdmin}), "other")
	if _, err := h.c.SetRegistryCredential(h.ctx, other, "quay.io", apitypes.SetRegistryCredentialRequest{Username: "theirs", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	web := h.service("web", apitypes.CreateServiceRequest{})
	for _, image := range []string{"ghcr.io/acme/app:1", "acme/app:1", "quay.io/acme/app:1"} {
		if r := h.deploy(web, image); r.Status != apitypes.ReleaseActive {
			t.Fatalf("%s: %s (%s)", image, r.Status, r.Error)
		}
	}
	ops := h.rt.Ops()
	for _, want := range []string{
		"pull ghcr.io/acme/app:1 user=ghcr-bot password=pw-ghcr-bot",
		"pull acme/app:1 user=hub-bot password=pw-hub-bot",
		"pull quay.io/acme/app:1",
	} {
		if !slices.Contains(ops, want) {
			t.Fatalf("no %q in %v", want, ops)
		}
	}

	h.rt.FailPull(errors.New("unauthorized: authentication required"))
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "quay.io/acme/private:1"})
	if err != nil {
		t.Fatal(err)
	}
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseFailed || !strings.Contains(r.Error, "`dock registry login quay.io`") {
		t.Fatalf("refused pull: %s (%s)", r.Status, r.Error)
	}
}
