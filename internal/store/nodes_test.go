package store

import (
	"context"
	"testing"
	"time"
)

// Existing servers and releases survive the nodes migration with the
// defaults: no pinned key, direct ingress, no digest.
func TestNodesMigrationOnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_nodes.sql",
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production, version) VALUES (1, 'local', 1, 1, 1, '0.9.0')`,
		`INSERT INTO projects (id, name) VALUES (1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'staging', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'web', 'nginx')`,
		`INSERT INTO releases (id, service_id, number, status, source, image_ref, config, created_by) VALUES (1, 1, 1, 'active', 'image', 'nginx', x'00', 'admin')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	srv, err := s.GetServer(ctx, 1)
	if err != nil || srv.Version != "0.9.0" || srv.KeyFingerprint != nil || srv.RevokedAt != nil ||
		srv.Ingress != "direct" || srv.PublicIps != "[]" || srv.BaseDomain != "" || srv.ConnectedAt != nil {
		t.Errorf("server = %+v, %v; want node defaults", srv, err)
	}
	r, err := s.GetRelease(ctx, 1)
	if err != nil || r.Status != "active" || r.ImageDigest != "" || r.BuiltOnServerID != nil || r.PromotedFromReleaseID != nil {
		t.Errorf("release = %+v, %v; want no digest or promotion", r, err)
	}
	if _, err := s.GetServerByFingerprint(ctx, "sha256:x"); !IsNotFound(err) {
		t.Errorf("fingerprint lookup = %v, want not found", err)
	}
}

// A join token works once and only before it expires; the server it
// registers is found by its key (and by its next key during a rotation),
// and not at all once revoked, until it re-joins under the same name.
func TestJoinRotateRevoke(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	s := f.s
	now := time.Now().UTC()
	expires := now.Add(time.Hour)
	tok, err := s.CreateJoinToken(ctx, CreateJoinTokenParams{Hash: "h1", ServerName: "homelab", AcceptsNonProduction: true, Builder: true, CreatedBy: "admin", CreatedAt: now, ExpiresAt: expires})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateJoinToken(ctx, CreateJoinTokenParams{Hash: "h2", ServerName: "old", CreatedBy: "admin", CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.ListPendingJoinTokens(ctx, now); err != nil || len(pending) != 1 || pending[0].ID != tok.ID {
		t.Errorf("pending tokens = %+v, %v; want only homelab's", pending, err)
	}
	if _, err := s.ConsumeJoinToken(ctx, ConsumeJoinTokenParams{Now: &now, Hash: "h2"}); !IsNotFound(err) {
		t.Errorf("expired token consumed: %v", err)
	}

	join := func(hash, fingerprint string) (Server, error) {
		var srv Server
		err := s.Tx(ctx, func(q *Queries) error {
			tok, err := q.ConsumeJoinToken(ctx, ConsumeJoinTokenParams{Now: &now, Hash: hash})
			if err != nil {
				return err
			}
			if srv, err = q.RegisterNodeServer(ctx, RegisterNodeServerParams{
				Name: tok.ServerName, AcceptsProduction: tok.AcceptsProduction,
				AcceptsNonProduction: tok.AcceptsNonProduction, Builder: tok.Builder, KeyFingerprint: fingerprint,
			}); err != nil {
				return err
			}
			return q.SetJoinTokenServer(ctx, SetJoinTokenServerParams{ServerID: srv.ID, ID: tok.ID})
		})
		return srv, err
	}
	srv, err := join("h1", "sha256:a")
	if err != nil || srv.Name != "homelab" || srv.Local || !srv.Builder || !srv.AcceptsNonProduction || srv.AcceptsProduction {
		t.Fatalf("joined = %+v, %v", srv, err)
	}
	if _, err := join("h1", "sha256:b"); !IsNotFound(err) {
		t.Errorf("token used twice: %v", err)
	}
	if pending, _ := s.ListPendingJoinTokens(ctx, now); len(pending) != 0 {
		t.Errorf("pending after join = %+v", pending)
	}

	if n, err := s.SetServerNextKey(ctx, SetServerNextKeyParams{Fingerprint: "sha256:a2", ID: srv.ID}); err != nil || n != 1 {
		t.Fatalf("set next key = %d, %v", n, err)
	}
	for _, fp := range []string{"sha256:a", "sha256:a2"} {
		if got, err := s.GetServerByFingerprint(ctx, fp); err != nil || got.ID != srv.ID {
			t.Errorf("by %s = %+v, %v", fp, got, err)
		}
	}
	if got, err := s.PromoteServerKey(ctx, PromoteServerKeyParams{ID: srv.ID, Fingerprint: "sha256:a2"}); err != nil || *got.KeyFingerprint != "sha256:a2" || got.NextKeyFingerprint != nil {
		t.Errorf("promoted = %+v, %v", got, err)
	}
	if _, err := s.GetServerByFingerprint(ctx, "sha256:a"); !IsNotFound(err) {
		t.Errorf("old key still accepted: %v", err)
	}

	if _, err := s.RevokeServer(ctx, RevokeServerParams{Now: &now, ID: srv.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetServerByFingerprint(ctx, "sha256:a2"); !IsNotFound(err) {
		t.Errorf("revoked key accepted: %v", err)
	}
	if _, err := s.RevokeServer(ctx, RevokeServerParams{Now: &now, ID: 1}); !IsNotFound(err) {
		t.Errorf("local server revoked: %v", err)
	}

	// Re-joining keeps the row (and so its environments and settings).
	if _, err := s.CreateJoinToken(ctx, CreateJoinTokenParams{Hash: "h3", ServerName: "homelab", AcceptsProduction: true, CreatedBy: "admin", CreatedAt: now, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	again, err := join("h3", "sha256:c")
	if err != nil || again.ID != srv.ID || again.RevokedAt != nil || *again.KeyFingerprint != "sha256:c" || again.AcceptsProduction {
		t.Errorf("re-joined = %+v, %v; want the same row, re-keyed, settings kept", again, err)
	}
	// A live server's name can't be taken over.
	if _, err := s.CreateJoinToken(ctx, CreateJoinTokenParams{Hash: "h4", ServerName: "homelab", CreatedBy: "admin", CreatedAt: now, ExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	if _, err := join("h4", "sha256:d"); !IsNotFound(err) {
		t.Errorf("joined over a live server: %v", err)
	}
	if pending, _ := s.ListPendingJoinTokens(ctx, now); len(pending) != 1 {
		t.Errorf("failed join consumed its token: pending = %+v", pending)
	}
}

// A server is removed only without environments, and never the local one.
func TestDeleteServer(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	srv, err := f.s.RegisterNodeServer(ctx, RegisterNodeServerParams{Name: "lab", KeyFingerprint: "sha256:a"})
	if err != nil {
		t.Fatal(err)
	}
	env, err := f.s.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: f.project.ID, Name: "dev", Kind: "non_production", ServerID: srv.ID})
	if err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.DeleteServer(ctx, srv.ID); err != nil || n != 0 {
		t.Errorf("deleted a server with an environment: %d, %v", n, err)
	}
	if _, err := f.s.DB.Exec(`UPDATE environments SET server_id = 1 WHERE id = ?`, env.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.s.DeleteServer(ctx, srv.ID); err != nil || n != 1 {
		t.Errorf("delete = %d, %v", n, err)
	}
	if n, err := f.s.DeleteServer(ctx, 1); err != nil || n != 0 {
		t.Errorf("deleted the local server: %d, %v", n, err)
	}
}

// Promote reads each live service's active release in the source
// environment and creates the target's release pointing back at it.
func TestPromotedRelease(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	now := time.Now().UTC()
	src, err := f.s.CreateRelease(ctx, CreateReleaseParams{ServiceID: f.svc.ID, Number: 1, Source: "upload", ImageRef: "reg/shop/postgres:staging-1", Config: []byte{0}, CreatedBy: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetReleaseDigest(ctx, SetReleaseDigestParams{ImageDigest: "sha256:d", BuiltOnServerID: new(int64(1)), ID: src.ID}); err != nil {
		t.Fatal(err)
	}
	if err := f.s.ActivateRelease(ctx, ActivateReleaseParams{FinishedAt: &now, ID: src.ID}); err != nil {
		t.Fatal(err)
	}
	rows, err := f.s.ListPromotableReleases(ctx, f.staging.ID)
	if err != nil || len(rows) != 1 || rows[0].ServiceName != "postgres" || rows[0].Release.ImageDigest != "sha256:d" {
		t.Fatalf("promotable = %+v, %v", rows, err)
	}

	target, err := f.s.CreateService(ctx, CreateServiceParams{EnvironmentID: f.prod.ID, Name: "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := f.s.CreatePromotedRelease(ctx, CreatePromotedReleaseParams{
		ServiceID: target.ID, Number: 1, ImageRef: src.ImageRef, ImageDigest: rows[0].Release.ImageDigest,
		BuiltOnServerID: rows[0].Release.BuiltOnServerID, PromotedFromReleaseID: src.ID, Config: []byte{0}, CreatedBy: "a",
	})
	if err != nil || r.Source != "promote" || r.Status != "queued" || r.PromotedFromReleaseID == nil || *r.PromotedFromReleaseID != src.ID || *r.BuiltOnServerID != 1 {
		t.Errorf("promoted release = %+v, %v", r, err)
	}
}

// The node lookups read through an index: the handshake's fingerprint
// lookup, the token consume and the rest.
func TestNodeQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	now := time.Now().UTC()
	checkPlans(t, f.s, []planCase{
		{"ConsumeJoinToken", consumeJoinToken, []any{now, "h"}},
		{"SetJoinTokenServer", setJoinTokenServer, []any{1, 1}},
		{"ListPendingJoinTokens", listPendingJoinTokens, []any{now}},
		{"DeleteJoinToken", deleteJoinToken, []any{1}},
		{"GetServerByFingerprint", getServerByFingerprint, []any{"sha256:a"}},
		{"SetServerHello", setServerHello, []any{"1", "28", "direct", "[]", "", 0, false, now, 1}},
		{"SetServerDisconnected", setServerDisconnected, []any{now, "", 1}},
		{"RevokeServer", revokeServer, []any{now, 1}},
		{"PromoteServerKey", promoteServerKey, []any{1, "sha256:a"}},
		{"DeleteServerPlacements", deleteServerPlacements, []any{1}},
		{"ClearArchivedHostPorts", clearArchivedHostPorts, []any{1}},
		{"MoveArchivedEnvironments", moveArchivedEnvironments, []any{1, 2}},
		{"DeleteServer", deleteServer, []any{1}},
		{"SetReleaseDigest", setReleaseDigest, []any{"sha256:d", 1, 1}},
		{"ListPromotableReleases", listPromotableReleases, []any{1}},
	})
}
