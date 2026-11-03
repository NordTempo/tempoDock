package store

import (
	"context"
	"testing"
	"time"
)

// Manual tokens get their creator's user_id from the email they recorded,
// unless that user came after the token; login tokens keep theirs.
func TestTokenCreatorMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_token_creator.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One')`,
		`INSERT INTO users (id, email, name, password_hash, created_at) VALUES
			(1, 'a@example.com', 'A', 'x', '2026-01-01'),
			(2, 'b@example.com', 'B', 'x', '2026-06-01')`,
		`INSERT INTO tokens (id, name, hash, scope, org_id, created_by, created_at, origin, user_id) VALUES
			(1, 'ci', 'h1', 'deploy', 1, 'a@example.com', '2026-02-01', 'manual', NULL),
			(2, 'old', 'h2', 'deploy', 1, 'b@example.com', '2026-02-01', 'manual', NULL),
			(3, 'boot', 'h3', 'admin', 1, 'bootstrap', '2026-02-01', 'manual', NULL),
			(4, 'cli: mac', 'h4', 'admin', NULL, 'a@example.com', '2026-02-01', 'cli_login', 1)`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	one, two := int64(1), int64(2)
	mine, err := s.ListUserTokens(ctx, ListUserTokensParams{UserID: &one, OrgID: &one})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, tk := range mine {
		ids = append(ids, tk.ID)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 4 {
		t.Errorf("user 1's tokens = %v, want [1 4]", ids)
	}
	if theirs, err := s.ListUserTokens(ctx, ListUserTokensParams{UserID: &two, OrgID: &one}); err != nil || len(theirs) != 0 {
		t.Errorf("user 2's tokens = %+v, %v; want none (the token predates them)", theirs, err)
	}
}

// The members list counts the tokens a user created by user_id, so a token
// recorded under their email by an earlier user of that address isn't
// theirs, as on their account page.
func TestOrgMembersCountTheirTokens(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	org := f.project.OrgID
	if err := f.s.AddOrgMember(ctx, AddOrgMemberParams{OrgID: org, UserID: f.user.ID, Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	for i, by := range []*int64{&f.user.ID, nil} {
		if _, err := f.s.CreateToken(ctx, CreateTokenParams{Name: "t", Hash: string(rune('a' + i)), Scope: "read", OrgID: &org, CreatedBy: f.user.Email, UserID: by}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.s.ListOrgMembers(ctx, ListOrgMembersParams{Now: time.Now(), OrgID: org})
	if err != nil || len(rows) != 1 || rows[0].Tokens != 1 {
		t.Fatalf("members = %+v, %v; want one with 1 token", rows, err)
	}
}
