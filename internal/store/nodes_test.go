package store

import (
	"context"
	"testing"
	"time"
)

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
