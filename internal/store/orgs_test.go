package store

import (
	"context"
	"math"
	"slices"
	"testing"
)

// An install from before organizations moves into one org, "default":
// users keep their role as members and admins become operators, unlimited
// admin tokens become operator tokens, and projects, manual tokens, policy
// rules and audit events belong to it. Foreign keys still hold.
func TestOrgsMigrationMovesAnInstallIntoOneOrg(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_orgs.sql",
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO users (id, email, name, password_hash, role) VALUES (1, 'ada@x', 'Ada', 'h', 'admin'), (2, 'bob@x', 'Bob', 'h', 'deploy')`,
		`INSERT INTO projects (id, name) VALUES (1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1)`,
		`INSERT INTO tokens (id, name, hash, scope, created_by) VALUES (1, 'ops', 'h1', 'admin', 'ada@x'), (2, 'ci', 'h2', 'deploy', 'ada@x')`,
		`INSERT INTO tokens (id, name, hash, scope, created_by, origin, user_id) VALUES (3, 'cli', 'h3', 'deploy', 'bob@x', 'cli_login', 2)`,
		`INSERT INTO tokens (id, name, hash, scope, created_by, project_id) VALUES (4, 'shop-admin', 'h4', 'admin', 'ada@x', 1)`,
		`INSERT INTO data_policy_rules (project_id, subject, kind, level, sensitive) VALUES (NULL, 'tokens', 'production', 'read', 'masked'), (1, 'role:read', 'production', 'none', 'masked')`,
		`INSERT INTO audit_events (actor, actor_kind, action, target, summary) VALUES ('ada@x', 'user', 'project.create', 'shop', 'created')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	if o, err := s.GetOrg(ctx, 1); err != nil || o.Slug != "default" {
		t.Fatalf("org = %+v, %v", o, err)
	}
	for id, want := range map[int64]struct {
		role     string
		operator bool
	}{1: {"admin", true}, 2: {"deploy", false}} {
		u, _ := s.GetUser(ctx, id)
		role, err := s.GetMemberRole(ctx, GetMemberRoleParams{OrgID: 1, UserID: id})
		if err != nil || role != want.role || u.Operator != want.operator {
			t.Errorf("user %d: role %q operator %v, %v; want %+v", id, role, u.Operator, err, want)
		}
	}
	if p, err := s.GetProjectByName(ctx, GetProjectByNameParams{OrgID: 1, Name: "shop"}); err != nil || p.ID != 1 {
		t.Errorf("project = %+v, %v", p, err)
	}
	for id, want := range map[int64]struct {
		org      bool
		operator bool
	}{1: {true, true}, 2: {true, false}, 3: {false, false}, 4: {true, false}} {
		tok, err := s.GetToken(ctx, id)
		if err != nil || (tok.OrgID != nil) != want.org || tok.Operator != want.operator {
			t.Errorf("token %d: org %v operator %v, %v; want %+v", id, tok.OrgID, tok.Operator, err, want)
		}
	}
	if rules, err := s.ListDataPolicyRules(ctx, ListDataPolicyRulesParams{OrgID: 1}); err != nil || len(rules) != 1 || rules[0].Subject != "tokens" {
		t.Errorf("default rules = %+v, %v", rules, err)
	}
	org := int64(1)
	if evs, err := s.ListAuditEvents(ctx, ListAuditEventsParams{OrgID: &org, BeforeID: math.MaxInt64, MaxRows: 10}); err != nil || len(evs) != 1 {
		t.Errorf("audit = %+v, %v", evs, err)
	}
	// Names are unique per org now, not per install.
	o2, err := s.CreateOrg(ctx, CreateOrgParams{Slug: "acme", Name: "Acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject(ctx, CreateProjectParams{OrgID: o2.ID, Name: "shop"}); err != nil {
		t.Errorf("shop in another org: %v", err)
	}
	if _, err := s.CreateProject(ctx, CreateProjectParams{OrgID: 1, Name: "shop"}); !IsUnique(err) {
		t.Errorf("second shop in one org: %v, want unique violation", err)
	}
}

// A fresh install gets no org from the migration; bootstrap creates one.
func TestOrgsMigrationLeavesAnEmptyInstallEmpty(t *testing.T) {
	s, err := Open(context.Background(), openBefore(t, "_orgs.sql"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.GetOrg(context.Background(), 1); !IsNotFound(err) {
		t.Fatalf("org 1: %v, want none", err)
	}
}

// What authentication and org-scoped lists run reads through an index.
func TestOrgQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	checkPlans(t, f.s, []planCase{
		{"GetMembership", getMembership, []any{1, "test"}},
		{"ListUserOrgs", listUserOrgs, []any{1}},
		{"GetMemberRole", getMemberRole, []any{1, 1}},
		{"GetMemberRoleByEmail", getMemberRoleByEmail, []any{1, "a@example.com"}},
		{"ListOrgTokens", listOrgTokens, []any{1}},
		{"ListUserTokens", listUserTokens, []any{1, 1}},
		{"ListOrgServerIDs", listOrgServerIDs, []any{1}},
		{"GetProjectByName", getProjectByName, []any{1, "shop"}},
		{"ListProjects", listProjects, []any{1}},
		{"ListAuditEvents", listAuditEvents, []any{1, math.MaxInt64, nil, nil, nil, nil, 100}},
		{"ListOrgInvites", listOrgInvites, []any{1}},
		{"GetInviteByHash", getInviteByHash, []any{"h"}},
		{"ListOrgVariables", listOrgVariables, []any{1}},
		{"ListOrgVariableUses", listOrgVariableUses, []any{1}},
		{"CountOtherOrgAdmins", countOtherOrgAdmins, []any{1, 1}},
		{"CountOrgProjects", countOrgProjects, []any{1}},
	})

	// An action prefix is a range, so the page's IDs (only the matching
	// events') are sorted; they come from the covering index, as before
	// orgs.
	rows, err := f.s.DB.Query("EXPLAIN QUERY PLAN "+listAuditEventsByAction, 1, "data.", "data/", math.MaxInt64, 100)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if !slices.Contains(plan, "SEARCH page USING COVERING INDEX audit_events_org_action (org_id=? AND action>? AND action<?)") {
		t.Errorf("ListAuditEventsByAction: %v", plan)
	}
}
