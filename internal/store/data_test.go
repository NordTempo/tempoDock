package store

import (
	"context"
	"database/sql"
	"io/fs"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dataFixture is a store with one server, project "shop" (environments
// staging and production, a postgres service in staging) and a user.
type dataFixture struct {
	s             *Store
	project       Project
	staging, prod Environment
	svc           Service
	user          User
}

func newDataFixture(t *testing.T) dataFixture {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	srv, err := s.CreateServer(ctx, CreateServerParams{Name: "local", AcceptsProduction: true, AcceptsNonProduction: true, Local: true})
	if err != nil {
		t.Fatal(err)
	}
	f := dataFixture{s: s}
	f.project, _ = s.CreateProject(ctx, "shop")
	f.staging, _ = s.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: f.project.ID, Name: "staging", Kind: "non_production", ServerID: srv.ID})
	f.prod, _ = s.CreateEnvironment(ctx, CreateEnvironmentParams{ProjectID: f.project.ID, Name: "production", Kind: "production", ServerID: srv.ID})
	if f.svc, err = s.CreateService(ctx, CreateServiceParams{EnvironmentID: f.staging.ID, Name: "postgres", Image: "postgres:16"}); err != nil {
		t.Fatal(err)
	}
	if f.user, err = s.CreateUser(ctx, CreateUserParams{Email: "a@example.com", Name: "A", PasswordHash: "x", Role: "deploy"}); err != nil {
		t.Fatal(err)
	}
	return f
}

// openBefore creates a database migrated up to (not including) the
// migration whose name ends in suffix, with the given rows, like an install
// from before it.
func openBefore(t *testing.T, suffix string, rows ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	names, _ := fs.Glob(migrations, "migrations/*.sql")
	sort.Strings(names)
	version := 0
	for _, name := range names {
		if strings.HasSuffix(name, suffix) {
			break
		}
		body, _ := migrations.ReadFile(name)
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		num, _, _ := strings.Cut(strings.TrimPrefix(name, "migrations/"), "_")
		version, _ = strconv.Atoi(num)
	}
	for _, stmt := range append(rows, "PRAGMA user_version = "+strconv.Itoa(version)) {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// Existing services, tokens and audit events survive the data migration
// with the defaults: detect the engine, follow the policy, no detail.
func TestDataMigrationOnExistingDatabase(t *testing.T) {
	ctx := context.Background()
	path := openBefore(t, "_data.sql",
		`INSERT INTO servers (id, name, local, accepts_production, accepts_non_production) VALUES (1, 'local', 1, 1, 1)`,
		`INSERT INTO projects (id, name) VALUES (1, 'shop')`,
		`INSERT INTO environments (id, project_id, name, server_id) VALUES (1, 1, 'staging', 1)`,
		`INSERT INTO services (id, environment_id, name, image) VALUES (1, 1, 'postgres', 'postgres:16')`,
		`INSERT INTO tokens (id, name, hash, scope, created_by) VALUES (1, 'ci', 'h', 'deploy', 'admin')`,
		`INSERT INTO audit_events (actor, actor_kind, action, target, summary) VALUES ('admin', 'user', 'service.create', 'shop/staging/postgres', 'created')`,
	)
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	svc, err := s.GetService(ctx, 1)
	if err != nil || svc.Database != "" || svc.DatabaseUrlVariable != "" {
		t.Errorf("service = %+v, %v; want empty database settings", svc, err)
	}
	tok, err := s.GetToken(ctx, 1)
	if err != nil || tok.DataProduction != nil || tok.DataNonProduction != nil || tok.DataSensitive != nil {
		t.Errorf("token = %+v, %v; want no data override", tok, err)
	}
	events, err := s.ListAuditEvents(ctx, ListAuditEventsParams{BeforeID: math.MaxInt64, MaxRows: 10})
	if err != nil || len(events) != 1 || events[0].AuditEvent.DetailID != nil {
		t.Errorf("audit events = %+v, %v; want one without detail", events, err)
	}

	if svc, err = s.SetServiceDatabaseSettings(ctx, SetServiceDatabaseSettingsParams{ID: 1, Database: "postgres", DatabaseUrlVariable: "api.DATABASE_URL"}); err != nil || svc.Database != "postgres" {
		t.Errorf("set database settings = %+v, %v", svc, err)
	}
	if _, err := s.SetServiceDatabaseSettings(ctx, SetServiceDatabaseSettingsParams{ID: 1, Database: "mysql"}); err == nil {
		t.Error("database = mysql accepted, want a check violation")
	}
	read, visible := "read", "visible"
	if n, err := s.SetTokenDataAccess(ctx, SetTokenDataAccessParams{ID: 1, DataNonProduction: &read, DataProduction: &read, DataSensitive: &visible}); err != nil || n != 1 {
		t.Fatalf("set token data access = %d, %v", n, err)
	}
	if tok, _ = s.GetToken(ctx, 1); tok.DataNonProduction == nil || *tok.DataNonProduction != "read" || *tok.DataSensitive != "visible" {
		t.Errorf("token = %+v", tok)
	}
	if _, err := s.SetTokenDataAccess(ctx, SetTokenDataAccessParams{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if tok, _ = s.GetToken(ctx, 1); tok.DataNonProduction != nil {
		t.Errorf("cleared token override = %+v", tok)
	}
}

// The effective lookup returns the default's, the project's and the
// environment's rules and nothing from other scopes; each scope holds one
// rule per subject and kind.
func TestDataPolicyLookup(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	s := f.s
	other, _ := s.CreateProject(ctx, "other")
	pid, sid, pid2, prodID := f.project.ID, f.staging.ID, other.ID, f.prod.ID

	add := func(p, e *int64, subject, kind, level string) {
		t.Helper()
		err := s.AddDataPolicyRule(ctx, AddDataPolicyRuleParams{ProjectID: p, EnvironmentID: e, Subject: subject, Kind: kind, Level: level, Sensitive: "masked"})
		if err != nil {
			t.Fatal(err)
		}
	}
	add(nil, nil, "role:deploy", "non_production", "schema")
	add(nil, nil, "tokens", "non_production", "none")
	add(&pid, nil, "role:deploy", "non_production", "read")
	add(&pid, &sid, "role:deploy", "non_production", "write")
	add(&pid, &prodID, "role:deploy", "production", "read")
	add(&pid2, nil, "role:deploy", "non_production", "none")

	scopes := func(rules []DataPolicyRule) string {
		var out []string
		for _, r := range rules {
			out = append(out, r.Subject+"/"+r.Kind+"="+r.Level)
		}
		sort.Strings(out)
		return strings.Join(out, " ")
	}
	for _, tc := range []struct {
		name string
		p, e int64
		want string
	}{
		{"staging", pid, sid, "role:deploy/non_production=read role:deploy/non_production=schema role:deploy/non_production=write tokens/non_production=none"},
		{"project", pid, 0, "role:deploy/non_production=read role:deploy/non_production=schema tokens/non_production=none"},
		{"default", 0, 0, "role:deploy/non_production=schema tokens/non_production=none"},
	} {
		rules, err := s.ListEffectiveDataPolicyRules(ctx, ListEffectiveDataPolicyRulesParams{ProjectID: tc.p, EnvironmentID: tc.e})
		if err != nil {
			t.Fatal(err)
		}
		if got := scopes(rules); got != tc.want {
			t.Errorf("%s: effective = %s\nwant %s", tc.name, got, tc.want)
		}
	}

	at, _ := s.ListDataPolicyRules(ctx, ListDataPolicyRulesParams{ProjectID: pid})
	if got := scopes(at); got != "role:deploy/non_production=read" {
		t.Errorf("project rules = %s", got)
	}

	// One rule per scope, subject and kind, the default included.
	for _, scope := range [][2]*int64{{nil, nil}, {&pid, nil}, {&pid, &sid}} {
		err := s.AddDataPolicyRule(ctx, AddDataPolicyRuleParams{ProjectID: scope[0], EnvironmentID: scope[1], Subject: "role:deploy", Kind: "non_production", Level: "none", Sensitive: "masked"})
		if !IsUnique(err) {
			t.Errorf("duplicate rule at %v: err = %v, want unique violation", scope, err)
		}
	}

	if err := s.DeleteDataPolicyRules(ctx, DeleteDataPolicyRulesParams{ProjectID: pid}); err != nil {
		t.Fatal(err)
	}
	rules, _ := s.ListEffectiveDataPolicyRules(ctx, ListEffectiveDataPolicyRulesParams{ProjectID: pid, EnvironmentID: sid})
	if got := scopes(rules); got != "role:deploy/non_production=schema role:deploy/non_production=write tokens/non_production=none" {
		t.Errorf("after clearing the project's rules: %s", got)
	}
}

// A pending changeset is decided once; expired ones can't be decided and
// the sweep marks them.
func TestChangesetLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	s := f.s
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	create := func(created time.Time) Changeset {
		t.Helper()
		cs, err := s.CreateChangeset(ctx, CreateChangesetParams{
			ServiceID: f.svc.ID, Author: "agent", AuthorKind: "token", Changes: "[]", Sql: "BEGIN; COMMIT;",
			CreatedAt: created, ExpiresAt: created.Add(7 * 24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		return cs
	}
	a, old := create(t0), create(t0.Add(-8*24*time.Hour))

	if _, err := s.GetChangeset(ctx, GetChangesetParams{ID: a.ID, ServiceID: f.svc.ID + 1}); !IsNotFound(err) {
		t.Errorf("changeset read through another service: %v", err)
	}
	decide := func(id int64, status string) (Changeset, error) {
		at := t0.Add(time.Hour)
		return s.DecideChangeset(ctx, DecideChangesetParams{ID: id, Status: status, DecidedAt: &at, DecidedBy: "admin", RowsAffected: "[1]"})
	}
	if cs, err := decide(a.ID, "committed"); err != nil || cs.Status != "committed" || cs.DecidedBy != "admin" {
		t.Fatalf("commit = %+v, %v", cs, err)
	}
	if _, err := decide(a.ID, "rejected"); !IsNotFound(err) {
		t.Errorf("second decision: err = %v, want not found", err)
	}
	if _, err := decide(old.ID, "committed"); !IsNotFound(err) {
		t.Errorf("deciding an expired changeset: err = %v, want not found", err)
	}

	if n, err := s.ExpireChangesets(ctx, t0); err != nil || n != 1 {
		t.Fatalf("expire = %d, %v; want 1", n, err)
	}
	got, _ := s.GetChangeset(ctx, GetChangesetParams{ID: old.ID, ServiceID: f.svc.ID})
	if got.Status != "expired" || got.DecidedAt == nil || !got.DecidedAt.Equal(old.ExpiresAt) {
		t.Errorf("expired changeset = %+v", got)
	}
	if pending, _ := s.ListPendingChangesets(ctx, f.svc.ID); len(pending) != 0 {
		t.Errorf("pending = %+v", pending)
	}
	all, _ := s.ListChangesets(ctx, ListChangesetsParams{ServiceID: f.svc.ID, BeforeID: math.MaxInt64, MaxRows: 10})
	if len(all) != 2 || all[0].ID != old.ID {
		t.Errorf("changesets = %+v, want newest first", all)
	}
}

// Saved queries are per user and database: saving under a name replaces,
// other users can't touch them, and deleting the user removes them.
func TestSavedQueries(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	s := f.s
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	other, _ := s.CreateUser(ctx, CreateUserParams{Email: "b@example.com", Name: "B", PasswordHash: "x", Role: "deploy"})

	q, err := s.SaveQuery(ctx, SaveQueryParams{ServiceID: f.svc.ID, UserID: f.user.ID, Name: "paid", Sql: "select 1", Now: t0})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SaveQuery(ctx, SaveQueryParams{ServiceID: f.svc.ID, UserID: f.user.ID, Name: "paid", Sql: "select 2", Now: t0.Add(time.Minute)})
	if err != nil || again.ID != q.ID || again.Sql != "select 2" || !again.CreatedAt.Equal(t0) {
		t.Errorf("save under the same name = %+v, %v; want the same query replaced", again, err)
	}
	if _, err := s.UpdateSavedQuery(ctx, UpdateSavedQueryParams{ID: q.ID, ServiceID: f.svc.ID, UserID: other.ID, Name: "x", Sql: "y", Now: t0}); !IsNotFound(err) {
		t.Errorf("another user's update: err = %v, want not found", err)
	}
	if n, _ := s.DeleteSavedQuery(ctx, DeleteSavedQueryParams{ID: q.ID, ServiceID: f.svc.ID, UserID: other.ID}); n != 0 {
		t.Error("another user deleted the query")
	}

	if err := s.DeleteUser(ctx, f.user.ID); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.ListSavedQueries(ctx, ListSavedQueriesParams{ServiceID: f.svc.ID, UserID: f.user.ID}); len(left) != 0 {
		t.Errorf("saved queries of a deleted user: %+v", left)
	}
}

// Query history is one actor's data.query events on one database, with
// their details, newest first.
func TestDataQueryHistory(t *testing.T) {
	ctx := context.Background()
	f := newDataFixture(t)
	s := f.s
	target, tokenID := "shop/staging/postgres", int64(7)
	add := func(action, target string, userID, tokenID *int64, sql string) int64 {
		t.Helper()
		var id int64
		err := s.Tx(ctx, func(q *Queries) error {
			detail, err := q.AddAuditDetail(ctx, AddAuditDetailParams{Sql: sql, Rows: 1, Outcome: "ok"})
			if err != nil {
				return err
			}
			ev, err := q.AddAuditEventWithDetail(ctx, AddAuditEventWithDetailParams{
				Actor: "a", ActorKind: "user", UserID: userID, TokenID: tokenID, Action: action,
				Target: target, Summary: "query", CreatedAt: time.Now(), DetailID: &detail,
			})
			id = ev.ID
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := add("data.query", target, &f.user.ID, nil, "select 1")
	second := add("data.query", target, &f.user.ID, nil, "select 2")
	add("data.query", target, nil, &tokenID, "select token")
	add("data.query", "shop/staging/other", &f.user.ID, nil, "select other")
	add("data.commit", target, &f.user.ID, nil, "update")

	rows, err := s.ListDataQueryHistory(ctx, ListDataQueryHistoryParams{Target: target, UserID: &f.user.ID, BeforeID: math.MaxInt64, MaxRows: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].AuditID != second || rows[0].AuditDetail.Sql != "select 2" || rows[1].AuditID != first {
		t.Errorf("history = %+v, want select 2, select 1", rows)
	}
	page, _ := s.ListDataQueryHistory(ctx, ListDataQueryHistoryParams{Target: target, UserID: &f.user.ID, BeforeID: second, MaxRows: 10})
	if len(page) != 1 || page[0].AuditID != first {
		t.Errorf("history before %d = %+v", second, page)
	}
	tok, _ := s.ListDataQueryHistory(ctx, ListDataQueryHistoryParams{Target: target, TokenID: &tokenID, BeforeID: math.MaxInt64, MaxRows: 10})
	if len(tok) != 1 || tok[0].AuditDetail.Sql != "select token" {
		t.Errorf("token history = %+v", tok)
	}
}

// Every data query reads through an index: no table scans and no sorting
// in a temporary b-tree. Run with -v to see the plans.
func TestDataQueryPlans(t *testing.T) {
	f := newDataFixture(t)
	now := time.Now()
	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"ListDataPolicyRules", listDataPolicyRules, []any{1, 2}},
		{"ListEffectiveDataPolicyRules", listEffectiveDataPolicyRules, []any{1, 2}},
		{"DeleteDataPolicyRules", deleteDataPolicyRules, []any{1, 2}},
		{"GetDataAccess", getDataAccess, []any{1}},
		{"SetDataMaskedSyncedHash", setDataMaskedSyncedHash, []any{"h", 1}},
		{"ListSensitiveColumns", listSensitiveColumns, []any{1}},
		{"DeleteSensitiveColumns", deleteSensitiveColumns, []any{1}},
		{"ListSavedQueries", listSavedQueries, []any{1, 1}},
		{"UpdateSavedQuery", updateSavedQuery, []any{"n", "s", now, 1, 1, 1}},
		{"DeleteSavedQuery", deleteSavedQuery, []any{1, 1, 1}},
		{"GetChangeset", getChangeset, []any{1, 1}},
		{"ListChangesets", listChangesets, []any{1, math.MaxInt64, 50}},
		{"ListPendingChangesets", listPendingChangesets, []any{1}},
		{"DecideChangeset", decideChangeset, []any{"committed", now, "a", "", "", "", 1}},
		{"ExpireChangesets", expireChangesets, []any{now}},
		{"GetAuditDetail", getAuditDetail, []any{1}},
		{"GetAuditEventByDetail", getAuditEventByDetail, []any{1}},
		{"ListDataQueryHistory", listDataQueryHistory, []any{"shop/staging/postgres", 1, nil, math.MaxInt64, 50}},
		{"SetServiceDatabaseSettings", setServiceDatabaseSettings, []any{"", "", 1}},
		{"SetTokenDataAccess", setTokenDataAccess, []any{nil, nil, nil, 1}},
	} {
		rows, err := f.s.DB.Query("EXPLAIN QUERY PLAN "+tc.sql, tc.args...)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		_ = rows.Close()
		t.Logf("%s: %s", tc.name, strings.Join(plan, "; "))
		for _, step := range plan {
			if strings.HasPrefix(step, "SCAN") || strings.Contains(step, "TEMP B-TREE") {
				t.Errorf("%s: %s", tc.name, step)
			}
		}
	}
}
