package store

import (
	"context"
	"math"
	"testing"
)

// Migration 0067 gives existing alerts the org of the project or service
// they name (none for a server's), keeps channels as the platform's, and
// asks for a copy of each in every org with an operator among its admins.
func TestOrgAlertsMigration(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_org_alerts.sql",
		`INSERT INTO orgs (id, slug, name) VALUES (1, 'one', 'One'), (2, 'two', 'Two'), (3, 'three', 'Three')`,
		`INSERT INTO users (id, email, name, password_hash, operator) VALUES (1, 'op@x', 'Op', 'h', 1), (2, 'bob@x', 'Bob', 'h', 0)`,
		`INSERT INTO org_members (org_id, user_id, role) VALUES (1, 1, 'admin'), (2, 2, 'admin'), (3, 1, 'deploy')`,
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, org_id, name) VALUES (1, 1, 'shop'), (2, 2, 'blog')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'production', 1), (2, 2, 'production', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'web', 'app:1'), (2, 2, 'web', 'app:1')`,
		`INSERT INTO alerts (key, kind, severity, title, opened_at, updated_at, project_id, service_id) VALUES
			('deploy:service:1', 'deploy_failed', 'warning', 'a', '2026-01-01', '2026-01-01', 1, 1),
			('backup:service:2', 'backup_failed', 'warning', 'b', '2026-01-01', '2026-01-01', NULL, 2),
			('disk:1', 'disk', 'warning', 'c', '2026-01-01', '2026-01-01', NULL, NULL)`,
		`INSERT INTO alert_channels (id, name, kind, target, config, min_severity, created_at) VALUES (1, 'ops', 'slack', 'https://hooks', x'00', 'warning', '2026-01-01')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	for org, want := range map[int64]string{1: "a", 2: "b", 0: "c"} {
		rows, err := s.ListAlerts(ctx, ListAlertsParams{Org: org, BeforeID: math.MaxInt64, MaxRows: 10})
		if err != nil || len(rows) != 1 || rows[0].Title != want {
			t.Errorf("org %d alerts = %+v, %v; want %s", org, rows, err, want)
		}
	}
	if chs, err := s.ListAlertChannels(ctx, 0); err != nil || len(chs) != 1 || chs[0].OrgID != nil {
		t.Errorf("platform channels = %+v, %v", chs, err)
	}
	copies, err := s.ListAlertChannelCopies(ctx)
	if err != nil || len(copies) != 1 || copies[0].CopyOrgID != 1 {
		t.Errorf("copies = %+v, %v; want ops into org 1 only (the operator is an admin there)", copies, err)
	}
	// Names are unique per org now, not per install.
	if _, err := s.CreateAlertChannel(ctx, CreateAlertChannelParams{OrgID: new(int64(2)), Name: "ops", Kind: "slack", Target: "t", Config: []byte{0}, MinSeverity: "warning"}); err != nil {
		t.Errorf("an org's channel named like a platform one: %v", err)
	}
	if _, err := s.CreateAlertChannel(ctx, CreateAlertChannelParams{Name: "ops", Kind: "slack", Target: "t", Config: []byte{0}, MinSeverity: "warning"}); !IsUnique(err) {
		t.Errorf("a second platform channel ops: %v, want a unique violation", err)
	}
}

// An org's alerts and channels are read through their indexes.
func TestOrgAlertQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	checkPlans(t, f.s, []planCase{
		{"ListAlerts", listAlerts, []any{1, math.MaxInt64, 10}},
		{"ListAlertChannels", listAlertChannels, []any{1}},
		{"GetAlertChannel", getAlertChannel, []any{1, "ops"}},
		{"ListNoticedChannels", listNoticedChannels, []any{1}},
		{"AlertServiceOrg", alertServiceOrg, []any{1}},
	})
}

// An org's services and lapsed operator memberships are found through
// indexes. (The Organizations page's own queries list every org, admin
// and service on purpose, and group bounded ranges of statistics.)
func TestOperatorQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	checkPlans(t, f.s, []planCase{
		{"ListOrgLiveServices", listOrgLiveServices, []any{1}},
		{"ListExpiredOrgMembers", listExpiredOrgMembers, nil},
	})
}
