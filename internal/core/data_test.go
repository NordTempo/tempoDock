package core

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/data"
	"github.com/NordTempo/tempoDock/internal/data/pg"
	"github.com/NordTempo/tempoDock/internal/datapanel"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// user creates a user with a role in the test org and returns them as an
// actor there. They aren't an operator.
func (h *harness) user(email string, role apitypes.Scope) Actor {
	h.t.Helper()
	u, err := h.c.createUser(h.ctx, apitypes.CreateUserRequest{Email: email, Name: email, Password: "password-123456", Role: role}, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	a, err := h.c.actIn(h.ctx, userActor(u), "", apitypes.ScopeAdmin)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// token creates a token as by and returns it as an authenticated actor.
func (h *harness) token(by Actor, req apitypes.CreateTokenRequest) Actor {
	h.t.Helper()
	created, err := h.c.createToken(h.ctx, by, req)
	if err != nil {
		h.t.Fatal(err)
	}
	a, err := h.c.AuthenticateToken(h.ctx, created.Secret, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

// setVars sets a service's variables and applies them, waiting for the
// config releases.
func (h *harness) setVars(ref ServiceRef, vars map[string]string) {
	h.t.Helper()
	if _, err := h.c.SetVariables(h.ctx, testSystem, ref.Env(), apitypes.SetVariablesRequest{Service: ref.Service, Set: vars}); err != nil {
		h.t.Fatal(err)
	}
	applied, err := h.c.ApplyChanges(h.ctx, testSystem, ref.Env())
	if err != nil {
		h.t.Fatal(err)
	}
	for _, r := range applied.Releases {
		h.wait(ServiceRef{ref.Project, ref.Environment, r.Service}, r.Number)
	}
}

func rule(scope apitypes.DataPolicyScope, subject apitypes.DataSubject, kind apitypes.EnvironmentKind, level apitypes.DataLevel, sens apitypes.DataSensitivity) store.DataPolicyRule {
	r := store.DataPolicyRule{Subject: string(subject), Kind: string(kind), Level: string(level), Sensitive: string(sens)}
	one := int64(1)
	switch scope {
	case apitypes.PolicyEnvironment:
		r.ProjectID, r.EnvironmentID = &one, &one
	case apitypes.PolicyProject:
		r.ProjectID = &one
	}
	return r
}

func TestDecidePermission(t *testing.T) {
	const (
		prod    = apitypes.KindProduction
		nonProd = apitypes.KindNonProduction
		masked  = apitypes.SensitiveMasked
		visible = apitypes.SensitiveVisible
	)
	role := func(s apitypes.Scope) *apitypes.Scope { return &s }
	lvl := func(l apitypes.DataLevel) *string { s := string(l); return &s }
	user := func(s apitypes.Scope) Actor { return Actor{Kind: "user", OrgID: 1, Name: "u@x", UserID: 1, Scope: s} }
	agent := func(s apitypes.Scope) Actor { return Actor{Kind: "token", OrgID: 1, Name: "bot", TokenID: 1, Scope: s} }
	cliLogin := func(s apitypes.Scope) Actor {
		return Actor{Kind: "token", OrgID: 1, Name: "cli", TokenID: 2, Scope: s, Email: "u@x"}
	}
	override := func(prodLevel, nonProdLevel apitypes.DataLevel, sens apitypes.DataSensitivity) *store.Token {
		s := string(sens)
		return &store.Token{Name: "bot", CreatedBy: "admin@x", DataProduction: lvl(prodLevel), DataNonProduction: lvl(nonProdLevel), DataSensitive: &s}
	}
	plain := &store.Token{Name: "bot", CreatedBy: "admin@x"}
	layered := policyRules{
		rule(apitypes.PolicyDefault, apitypes.SubjectDeployUsers, nonProd, apitypes.LevelPropose, masked),
		rule(apitypes.PolicyProject, apitypes.SubjectDeployUsers, nonProd, apitypes.LevelWrite, visible),
		rule(apitypes.PolicyEnvironment, apitypes.SubjectDeployUsers, prod, apitypes.LevelSchema, masked),
		rule(apitypes.PolicyDefault, apitypes.SubjectDeployUsers, prod, apitypes.LevelWrite, masked),
		rule(apitypes.PolicyDefault, apitypes.SubjectTokens, nonProd, apitypes.LevelWrite, visible),
		rule(apitypes.PolicyProject, apitypes.SubjectReadUsers, nonProd, apitypes.LevelRead, visible),
	}
	deployWritesProd := policyRules{rule(apitypes.PolicyProject, apitypes.SubjectDeployUsers, prod, apitypes.LevelWrite, visible)}

	for _, tc := range []struct {
		name    string
		in      permissionInput
		level   apitypes.DataLevel
		sens    apitypes.DataSensitivity
		approve bool
		sources []apitypes.DataPermissionSource
	}{
		{"admin user", permissionInput{actor: user(apitypes.ScopeAdmin), kind: prod},
			apitypes.LevelWrite, visible, true, []apitypes.DataPermissionSource{"admin"}},
		{"admin token on production only proposes", permissionInput{actor: agent(apitypes.ScopeAdmin), kind: prod},
			apitypes.LevelPropose, visible, false, []apitypes.DataPermissionSource{"admin", "production"}},
		{"admin token writes non-production", permissionInput{actor: agent(apitypes.ScopeAdmin), kind: nonProd},
			apitypes.LevelWrite, visible, true, []apitypes.DataPermissionSource{"admin"}},
		{"admin dock login token writes production", permissionInput{actor: cliLogin(apitypes.ScopeAdmin), kind: prod},
			apitypes.LevelWrite, visible, true, []apitypes.DataPermissionSource{"admin"}},
		{"built-in: deploy reads non-production", permissionInput{actor: user(apitypes.ScopeDeploy), kind: nonProd},
			apitypes.LevelRead, masked, false, []apitypes.DataPermissionSource{"policy"}},
		{"built-in: deploy gets nothing on production", permissionInput{actor: user(apitypes.ScopeDeploy), kind: prod},
			apitypes.LevelNone, masked, false, []apitypes.DataPermissionSource{"policy"}},
		{"built-in: read users get nothing", permissionInput{actor: user(apitypes.ScopeRead), kind: nonProd},
			apitypes.LevelNone, masked, false, []apitypes.DataPermissionSource{"policy"}},
		{"built-in: tokens get nothing", permissionInput{actor: agent(apitypes.ScopeDeploy), token: plain, kind: nonProd},
			apitypes.LevelNone, masked, false, []apitypes.DataPermissionSource{"policy"}},
		{"project beats default", permissionInput{actor: user(apitypes.ScopeDeploy), kind: nonProd, rules: layered},
			apitypes.LevelWrite, visible, false, []apitypes.DataPermissionSource{"policy"}},
		{"environment beats default", permissionInput{actor: user(apitypes.ScopeDeploy), kind: prod, rules: layered},
			apitypes.LevelSchema, masked, false, []apitypes.DataPermissionSource{"policy"}},
		{"tokens rule capped by the token's scope", permissionInput{actor: agent(apitypes.ScopeRead), token: plain, kind: nonProd, rules: layered},
			apitypes.LevelRead, visible, false, []apitypes.DataPermissionSource{"policy", "cap"}},
		{"tokens rule within its scope", permissionInput{actor: agent(apitypes.ScopeDeploy), token: plain, kind: nonProd, rules: layered},
			apitypes.LevelWrite, visible, false, []apitypes.DataPermissionSource{"policy"}},
		{"override capped by scope", permissionInput{actor: agent(apitypes.ScopeDeploy), token: override(apitypes.LevelWrite, apitypes.LevelWrite, visible), kind: nonProd},
			apitypes.LevelRead, masked, false, []apitypes.DataPermissionSource{"token", "cap"}},
		{"override write on production proposes", permissionInput{actor: agent(apitypes.ScopeDeploy), token: override(apitypes.LevelWrite, apitypes.LevelNone, visible), kind: prod, rules: deployWritesProd},
			apitypes.LevelPropose, visible, false, []apitypes.DataPermissionSource{"token", "production"}},
		{"override capped by its creator's role", permissionInput{actor: agent(apitypes.ScopeDeploy), token: override(apitypes.LevelWrite, apitypes.LevelNone, visible), creator: role(apitypes.ScopeRead), kind: prod, rules: deployWritesProd},
			apitypes.LevelNone, masked, false, []apitypes.DataPermissionSource{"token", "cap"}},
		{"admin creators don't cap", permissionInput{actor: agent(apitypes.ScopeDeploy), token: override(apitypes.LevelRead, apitypes.LevelNone, masked), creator: role(apitypes.ScopeAdmin), kind: prod, rules: deployWritesProd},
			apitypes.LevelRead, masked, false, []apitypes.DataPermissionSource{"token"}},
		{"dock login token is its user", permissionInput{actor: cliLogin(apitypes.ScopeDeploy), token: plain, creator: role(apitypes.ScopeDeploy), kind: prod, rules: deployWritesProd},
			apitypes.LevelWrite, visible, false, []apitypes.DataPermissionSource{"policy"}},
		{"dock login token capped by its user's current role", permissionInput{actor: cliLogin(apitypes.ScopeDeploy), token: plain, creator: role(apitypes.ScopeRead), kind: nonProd, rules: layered},
			apitypes.LevelRead, visible, false, []apitypes.DataPermissionSource{"policy", "cap"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.in.project, tc.in.env = "shop", "staging"
			got := decidePermission(tc.in)
			var sources []apitypes.DataPermissionSource
			for _, s := range got.Why {
				sources = append(sources, s.Source)
			}
			if got.Level != tc.level || got.Sensitive != tc.sens || got.Approve != tc.approve || got.Manage != (tc.in.actor.Scope == apitypes.ScopeAdmin) ||
				got.ConfirmCommit != (tc.in.kind == prod) || strings.Join(toStrings(sources), ",") != strings.Join(toStrings(tc.sources), ",") {
				t.Fatalf("got %s/%s approve=%t confirm=%t why=%v; want %s/%s approve=%t %v", got.Level, got.Sensitive, got.Approve, got.ConfirmCommit, sources, tc.level, tc.sens, tc.approve, tc.sources)
			}
			last := got.Why[len(got.Why)-1]
			if last.Level != got.Level || last.Sensitive != got.Sensitive || last.Detail == "" {
				t.Fatalf("last step %+v doesn't match %s/%s", last, got.Level, got.Sensitive)
			}
		})
	}
}

func toStrings[T ~string](s []T) []string {
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = string(v)
	}
	return out
}

func TestPermissionWhy(t *testing.T) {
	in := permissionInput{
		actor: Actor{Kind: "token", OrgID: 1, Name: "bot", TokenID: 1, Scope: apitypes.ScopeRead},
		token: &store.Token{Name: "bot", CreatedBy: "ada@x"}, kind: apitypes.KindNonProduction, project: "shop", env: "staging",
		rules: policyRules{rule(apitypes.PolicyProject, apitypes.SubjectTokens, apitypes.KindNonProduction, apitypes.LevelWrite, apitypes.SensitiveMasked)},
	}
	got := decidePermission(in)
	want := []apitypes.DataPermissionStep{
		{Source: apitypes.PermissionPolicy, Policy: apitypes.PolicyProject, Subject: apitypes.SubjectTokens, Level: apitypes.LevelWrite, Sensitive: apitypes.SensitiveMasked,
			Detail: "project shop: tokens gets write on non-production, sensitive masked"},
		{Source: apitypes.PermissionCap, Policy: apitypes.PolicyBuiltIn, Subject: apitypes.SubjectReadUsers, Level: apitypes.LevelNone, Sensitive: apitypes.SensitiveMasked,
			Detail: "capped by the token's scope read: built-in default: role:read gets none on non-production, sensitive masked"},
	}
	if len(got.Why) != len(want) || got.Why[0] != want[0] || got.Why[1] != want[1] {
		t.Fatalf("why = %+v", got.Why)
	}
	err := datapanel.RequireLevel(got, apitypes.LevelRead, "reading rows")
	if CodeOf(err) != apitypes.CodeForbidden || !strings.Contains(err.Error(), "needs data access level read; you have none") || !strings.Contains(err.Error(), "capped by the token's scope read") {
		t.Fatalf("error: %v", err)
	}
}

func TestDataPolicyScopes(t *testing.T) {
	h := newHarness(t)
	h.service("db", apitypes.CreateServiceRequest{Image: "postgres:17"})
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	deploy := h.user("dev@x", apitypes.ScopeDeploy)
	staging := EnvRef{"shop", "staging"}
	set := func(a Actor, ref EnvRef, rules ...apitypes.DataPolicyRule) error {
		_, err := h.c.SetDataPolicy(h.ctx, a, ref, apitypes.UpdateDataPolicyRequest{Rules: rules})
		return err
	}
	r := func(s apitypes.DataSubject, k apitypes.EnvironmentKind, l apitypes.DataLevel) apitypes.DataPolicyRule {
		return apitypes.DataPolicyRule{Subject: s, Kind: k, Level: l, Sensitive: apitypes.SensitiveMasked}
	}

	if err := set(deploy, EnvRef{}, r(apitypes.SubjectDeployUsers, apitypes.KindProduction, apitypes.LevelRead)); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy user sets policy: %v", err)
	}
	for _, bad := range [][]apitypes.DataPolicyRule{
		{r("role:admin", apitypes.KindProduction, apitypes.LevelRead)},
		{r(apitypes.SubjectTokens, apitypes.KindProduction, "everything")},
		{r(apitypes.SubjectTokens, apitypes.KindProduction, apitypes.LevelRead), r(apitypes.SubjectTokens, apitypes.KindProduction, apitypes.LevelNone)},
	} {
		if err := set(admin, EnvRef{}, bad...); CodeOf(err) != apitypes.CodeValidation {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if err := set(admin, staging, r(apitypes.SubjectTokens, apitypes.KindProduction, apitypes.LevelRead)); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "non-production") {
		t.Fatalf("production rule on staging: %v", err)
	}

	if err := set(admin, EnvRef{}, r(apitypes.SubjectDeployUsers, apitypes.KindNonProduction, apitypes.LevelPropose), r(apitypes.SubjectTokens, apitypes.KindNonProduction, apitypes.LevelSchema)); err != nil {
		t.Fatal(err)
	}
	if err := set(admin, EnvRef{Project: "shop"}, r(apitypes.SubjectDeployUsers, apitypes.KindNonProduction, apitypes.LevelWrite)); err != nil {
		t.Fatal(err)
	}
	if err := set(admin, staging, r(apitypes.SubjectDeployUsers, apitypes.KindNonProduction, apitypes.LevelSchema)); err != nil {
		t.Fatal(err)
	}

	pol, err := h.c.GetDataPolicy(h.ctx, deploy, staging)
	if err != nil {
		t.Fatal(err)
	}
	from := map[apitypes.DataSubject]apitypes.DataPolicyRule{}
	for _, e := range pol.Effective {
		if e.Kind != apitypes.KindNonProduction {
			t.Fatalf("environment policy shows another kind: %+v", e)
		}
		from[e.Subject] = e
	}
	if pol.Scope != apitypes.PolicyEnvironment || len(pol.Rules) != 1 || len(pol.Effective) != 3 ||
		from[apitypes.SubjectDeployUsers].From != apitypes.PolicyEnvironment || from[apitypes.SubjectDeployUsers].Level != apitypes.LevelSchema ||
		from[apitypes.SubjectTokens].From != apitypes.PolicyDefault || from[apitypes.SubjectReadUsers].From != apitypes.PolicyBuiltIn {
		t.Fatalf("staging policy: %+v", pol)
	}
	proj, _ := h.c.GetDataPolicy(h.ctx, deploy, EnvRef{Project: "shop"})
	if len(proj.Effective) != 6 {
		t.Fatalf("project policy: %+v", proj)
	}

	es, _ := h.c.loadEnv(h.ctx, 1, staging)
	perm, err := h.c.dataPermission(h.ctx, deploy, es)
	if err != nil || perm.Level != apitypes.LevelSchema || perm.Why[0].Detail != "environment shop/staging: role:deploy gets schema on non-production, sensitive masked" {
		t.Fatalf("deploy user on staging: %+v %v", perm, err)
	}
	// Clearing the environment's rules falls back to the project's.
	if err := set(admin, staging); err != nil {
		t.Fatal(err)
	}
	if perm, _ := h.c.dataPermission(h.ctx, deploy, es); perm.Level != apitypes.LevelWrite || perm.Why[0].Policy != apitypes.PolicyProject {
		t.Fatalf("after clearing staging: %+v", perm)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: apitypes.AuditDataPolicy})
	if len(evs) != 4 || !strings.Contains(evs[0].Summary, "cleared the data access policy of shop/staging") {
		t.Fatalf("policy audit: %+v", evs)
	}
}

func TestTokenDataAccess(t *testing.T) {
	h := newHarness(t)
	h.service("db", apitypes.CreateServiceRequest{Image: "postgres:17"})
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	deploy := h.user("dev@x", apitypes.ScopeDeploy)
	if _, err := h.c.CreateEnvironment(h.ctx, testSystem, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	es, _ := h.c.loadEnv(h.ctx, 1, EnvRef{"shop", "staging"})

	read := apitypes.DataTokenAccess{Production: apitypes.LevelNone, NonProduction: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked}
	if _, err := h.c.createToken(h.ctx, admin, apitypes.CreateTokenRequest{Name: "bad", Scope: apitypes.ScopeDeploy, Data: &apitypes.DataTokenAccess{Production: "all", NonProduction: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked}}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("bad level: %v", err)
	}
	bot := h.token(admin, apitypes.CreateTokenRequest{Name: "bot", Scope: apitypes.ScopeDeploy, Data: &read})
	if perm, err := h.c.dataPermission(h.ctx, bot, es); err != nil || perm.Level != apitypes.LevelRead || perm.Why[0].Source != apitypes.PermissionToken {
		t.Fatalf("token with read override: %+v %v", perm, err)
	}
	toks, _ := h.c.ListTokens(h.ctx, admin)
	if toks[len(toks)-1].Data == nil || *toks[len(toks)-1].Data != read {
		t.Fatalf("listed token data: %+v", toks[len(toks)-1].Data)
	}

	// A token created by a deploy user is capped by what deploy users get.
	write := apitypes.DataTokenAccess{Production: apitypes.LevelWrite, NonProduction: apitypes.LevelWrite, Sensitive: apitypes.SensitiveVisible}
	devBot := h.token(deploy, apitypes.CreateTokenRequest{Name: "dev-bot", Scope: apitypes.ScopeDeploy, Data: &write})
	perm, _ := h.c.dataPermission(h.ctx, devBot, es)
	if perm.Level != apitypes.LevelRead || perm.Sensitive != apitypes.SensitiveMasked || perm.Why[len(perm.Why)-1].Source != apitypes.PermissionCap {
		t.Fatalf("capped token: %+v", perm)
	}

	if _, err := h.c.UpdateToken(h.ctx, deploy, bot.TokenID, apitypes.UpdateTokenRequest{}); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("deploy user updates token: %v", err)
	}
	updated, err := h.c.UpdateToken(h.ctx, admin, bot.TokenID, apitypes.UpdateTokenRequest{Data: nil})
	if err != nil || updated.Data != nil {
		t.Fatalf("clear override: %+v %v", updated, err)
	}
	if perm, _ := h.c.dataPermission(h.ctx, bot, es); perm.Level != apitypes.LevelNone || perm.Why[0].Subject != apitypes.SubjectTokens {
		t.Fatalf("token following the policy: %+v", perm)
	}
	evs, _ := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: apitypes.AuditDataPolicy})
	if len(evs) != 1 || evs[0].Target != "token bot" || !strings.Contains(evs[0].Summary, "follows the data policy") {
		t.Fatalf("audit: %+v", evs)
	}
}

func TestDatabaseDetection(t *testing.T) {
	for _, tc := range []struct {
		image, setting string
		want           apitypes.DataEngine
		by             apitypes.DataDetection
	}{
		{"postgres:17", "", apitypes.EnginePostgres, apitypes.DetectedByImage},
		{"docker.io/library/postgres:17-alpine@sha256:abc", "", apitypes.EnginePostgres, apitypes.DetectedByImage},
		{"ghcr.io/supabase/postgres:15", "", apitypes.EnginePostgres, apitypes.DetectedByImage},
		{"bitnami/postgresql-repmgr:16", "", "", ""},
		{"supabase/postgres:15.1", "", apitypes.EnginePostgres, apitypes.DetectedByImage},
		{"timescale/timescaledb-ha:pg16", "", apitypes.EnginePostgres, apitypes.DetectedByImage},
		{"localhost:5000/pgvector/pgvector:pg17", "", apitypes.EnginePostgres, apitypes.DetectedByImage},
		{"postgres:17", "none", "", ""},
		{"myorg/db:1", "postgres", apitypes.EnginePostgres, apitypes.DetectedBySetting},
		{"redis:7", "", "", ""},
		{"", "", "", ""},
	} {
		engine, by := detectDatabase(store.Service{Image: tc.image, Database: tc.setting})
		if engine != tc.want || by != tc.by {
			t.Errorf("%s (%q): %s %s", tc.image, tc.setting, engine, by)
		}
	}
}

func TestDataConnection(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{Image: "postgres:17"})
	api := h.service("api", apitypes.CreateServiceRequest{})
	conn := func() (dbConn, error) {
		t.Helper()
		ss, err := h.c.loadService(h.ctx, 1, db)
		if err != nil {
			t.Fatal(err)
		}
		return h.c.dbConnection(h.ctx, ss)
	}
	if _, err := conn(); !errors.Is(err, errNotRunning) {
		t.Fatalf("not deployed: %v", err)
	}
	if info, err := h.c.DataInfo(h.ctx, testSystem, db); err != nil || info.Status != apitypes.DataNotRunning || info.DetectedBy != apitypes.DetectedByImage {
		t.Fatalf("info before deploy: %+v %v", info, err)
	}

	h.setVars(db, map[string]string{"POSTGRES_USER": "app", "POSTGRES_PASSWORD": "pw"})
	h.deploy(db, "postgres:17")
	// The service turns running when its container's start event is
	// handled, which can trail the release going live.
	eventually(t, "the database to run", func() bool { _, err := conn(); return !errors.Is(err, errNotRunning) })
	got, err := conn()
	if err != nil || got.owner.User != "app" || got.owner.Password != "pw" || got.database != "app" || got.port != 5432 || got.containerID == "" || got.serverID != h.c.local.ID {
		t.Fatalf("from POSTGRES_*: %+v %v", got, err)
	}
	if info, _ := h.c.DataInfo(h.ctx, testSystem, db); info.Status != apitypes.DataSetupNeeded {
		t.Fatalf("info before setup: %+v", info)
	}

	h.setVars(db, map[string]string{"DATABASE_URL": "postgres://owner:s%40cret@db:6000/shop"})
	if got, err = conn(); err != nil || got.owner.User != "owner" || got.owner.Password != "s@cret" || got.database != "shop" || got.port != 6000 {
		t.Fatalf("from DATABASE_URL: %+v %v", got, err)
	}

	// Another service's variable, resolved with references.
	h.setVars(api, map[string]string{"DATABASE_URL": "postgresql://${{db.POSTGRES_USER}}:x@db/api"})
	url := "api.DATABASE_URL"
	if _, err := h.c.UpdateDatabaseSettings(h.ctx, testSystem, db, apitypes.UpdateDatabaseSettingsRequest{DatabaseURLVariable: &url}); err != nil {
		t.Fatal(err)
	}
	if got, err = conn(); err != nil || got.owner.User != "app" || got.database != "api" || got.port != 5432 {
		t.Fatalf("from api.DATABASE_URL: %+v %v", got, err)
	}
	info, err := h.c.DataInfo(h.ctx, testSystem, db)
	if err != nil || len(info.UsedBy) != 1 || info.UsedBy[0] != "api" {
		t.Fatalf("used by: %+v %v", info, err)
	}

	h.setVars(api, map[string]string{"DATABASE_URL": "postgres://u:p@neon.example.com/db"})
	if _, err := conn(); CodeOf(err) != apitypes.CodeValidation || !strings.Contains(err.Error(), "external databases") {
		t.Fatalf("external host: %v", err)
	}
	if info, _ := h.c.DataInfo(h.ctx, testSystem, db); info.Status != apitypes.DataUnreachable || !strings.Contains(info.Reason, "api.DATABASE_URL") {
		t.Fatalf("external host info: %+v", info)
	}

	bad := "1x"
	if _, err := h.c.UpdateDatabaseSettings(h.ctx, testSystem, db, apitypes.UpdateDatabaseSettingsRequest{DatabaseURLVariable: &bad}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("bad variable name: %v", err)
	}
	none := apitypes.DatabaseNone
	svc, err := h.c.UpdateDatabaseSettings(h.ctx, testSystem, db, apitypes.UpdateDatabaseSettingsRequest{Database: &none})
	if err != nil || svc.Database != apitypes.DatabaseNone || svc.DataEngine != "" || svc.DatabaseURLVariable != url {
		t.Fatalf("database none: %+v %v", svc, err)
	}
	if _, err := h.c.DataInfo(h.ctx, testSystem, db); CodeOf(err) != apitypes.CodeNotFound {
		t.Fatalf("info of a non-database: %v", err)
	}
}

func TestDataInfoWithoutAccess(t *testing.T) {
	h := newHarness(t)
	db := h.service("db", apitypes.CreateServiceRequest{Image: "postgres:17"})
	h.deploy(db, "postgres:17")
	ss, _ := h.c.loadService(h.ctx, 1, db)
	sealed, _ := h.c.box.Seal([]byte("pw"))
	if _, err := h.c.store.UpsertDataAccess(h.ctx, store.UpsertDataAccessParams{
		ServiceID: ss.svc.ID, Engine: "postgres", ServerVersion: "17.2", ReaderSecret: sealed, WriterSecret: sealed, SetUpAt: now(), SetUpBy: "admin@x",
	}); err != nil {
		t.Fatal(err)
	}
	// A deploy user has no access on production by default: the info
	// comes from Dock's records, without contacting the database (the
	// fake runtime has no dial target, so a probe would fail).
	deploy := h.user("dev@x", apitypes.ScopeDeploy)
	info, err := h.c.DataInfo(h.ctx, deploy, db)
	if err != nil || info.Status != apitypes.DataReady || info.ServerVersion != "17.2" || info.Access.Level != apitypes.LevelNone || info.Access.Manage {
		t.Fatalf("info: %+v %v", info, err)
	}
	if _, err := h.c.DataSchema(h.ctx, deploy, db); CodeOf(err) != apitypes.CodeForbidden || !strings.Contains(err.Error(), "role:deploy gets none on production") {
		t.Fatalf("schema without access: %v", err)
	}
	if _, err := h.c.SetupData(h.ctx, deploy, db); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("setup by deploy user: %v", err)
	}
	// An admin's info probes the database; unreachable says so.
	info, err = h.c.DataInfo(h.ctx, testSystem, db)
	if err != nil || info.Status != apitypes.DataUnreachable {
		t.Fatalf("admin info: %+v %v", info, err)
	}
}

// pageEngine is a data.Engine that records how Page was called and which
// services were forgotten. Only Page, Schema, Query, SyncGrants, Redact,
// Forget and Close are implemented; Page returns next as its cursor,
// Schema an empty schema and Query no rows.
type pageEngine struct {
	data.Engine
	mu     sync.Mutex
	calls  []data.Call
	afters []string
	forgot []int64
	next   string
}

func (e *pageEngine) Page(_ context.Context, _ data.Target, c data.Call, req apitypes.DataPageRequest) (apitypes.DataPage, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, c)
	e.afters = append(e.afters, req.After)
	return apitypes.DataPage{Next: e.next}, nil
}

func (e *pageEngine) Schema(context.Context, data.Target, data.Call, []apitypes.DataColumnRef) (apitypes.DataSchema, error) {
	return apitypes.DataSchema{}, nil
}

func (e *pageEngine) Query(context.Context, data.Target, data.Call, apitypes.DataQueryRequest, func(apitypes.DataQueryFrame) error) (data.QueryStats, error) {
	return data.QueryStats{Outcome: apitypes.OutcomeOK}, nil
}

func (e *pageEngine) SyncGrants(context.Context, data.Target, data.Credentials, []apitypes.DataColumnRef) (string, error) {
	return "synced", nil
}

func (e *pageEngine) Redact(sql string) string { return pg.New(pg.Options{}).Redact(sql) }

func (e *pageEngine) Forget(id int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.forgot = append(e.forgot, id)
}

func (e *pageEngine) Close() {}

// last returns the call of the last Page and how many there were.
func (e *pageEngine) last() (data.Call, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.calls) == 0 {
		return data.Call{}, 0
	}
	return e.calls[len(e.calls)-1], len(e.calls)
}

// fakeDatabase deploys a set-up Postgres service db in shop/production on
// a pageEngine, with deploy users given read (masked) on production and
// customers.email marked sensitive.
func fakeDatabase(t *testing.T) (*harness, *pageEngine, ServiceRef, svcScope) {
	h := newHarness(t)
	eng := &pageEngine{}
	h.opts.Data = eng
	h.restart()
	db := h.service("db", apitypes.CreateServiceRequest{Image: "postgres:17"})
	h.deploy(db, "postgres:17")
	ss, err := h.c.loadService(h.ctx, 1, db)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := h.c.box.Seal([]byte("pw"))
	if _, err := h.c.store.UpsertDataAccess(h.ctx, store.UpsertDataAccessParams{
		ServiceID: ss.svc.ID, Engine: "postgres", ServerVersion: "17.2", ReaderSecret: sealed, WriterSecret: sealed, SetUpAt: now(), SetUpBy: "admin@x",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetDataPolicy(h.ctx, testSystem, EnvRef{}, apitypes.UpdateDataPolicyRequest{Rules: []apitypes.DataPolicyRule{
		{Subject: apitypes.SubjectDeployUsers, Kind: apitypes.KindProduction, Level: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetSensitiveColumns(h.ctx, testSystem, db, apitypes.SetSensitiveColumnsRequest{Columns: []apitypes.DataColumnRef{
		{Schema: "public", Table: "customers", Column: "email"},
	}}); err != nil {
		t.Fatal(err)
	}
	return h, eng, db, ss
}

// TestDataCursorsSealed: callers never see the engine's cursor (it may
// hold sensitive key values), and a sealed cursor only opens for the
// caller and database it was made for.
func TestDataCursorsSealed(t *testing.T) {
	h, eng, db, _ := fakeDatabase(t)
	eng.next = `{"c":"x","k":["secret@x.io"]}`
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	other := h.user("other@x", apitypes.ScopeDeploy)
	page, err := h.c.DataRows(h.ctx, dev, db, apitypes.DataPageRequest{Table: "public.customers"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Next == "" || strings.Contains(page.Next, "secret") || page.Next == eng.next {
		t.Fatalf("cursor: %q", page.Next)
	}
	if _, err := h.c.DataRows(h.ctx, dev, db, apitypes.DataPageRequest{Table: "public.customers", After: page.Next}); err != nil {
		t.Fatal(err)
	}
	if got := eng.afters[len(eng.afters)-1]; got != eng.next {
		t.Fatalf("the engine got cursor %q", got)
	}
	tampered := []byte(page.Next)
	tampered[len(tampered)/2] ^= 1
	for name, c := range map[string]struct {
		a     Actor
		after string
	}{
		"another caller's": {other, page.Next},
		"tampered":         {dev, string(tampered)},
		"forged":           {dev, eng.next},
	} {
		_, err := h.c.DataRows(h.ctx, c.a, db, apitypes.DataPageRequest{Table: "public.customers", After: c.after})
		if CodeOf(err) != apitypes.CodeValidation {
			t.Errorf("%s cursor: %v", name, err)
		}
	}
}

// TestDataAuditPrivacy: data events' summaries carry no values, their
// details need data access level read, and masked callers get others'
// SQL with its values redacted.
func TestDataAuditPrivacy(t *testing.T) {
	h, _, db, _ := fakeDatabase(t)
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	viewer := h.user("viewer@x", apitypes.ScopeRead)
	const sql = "SELECT * FROM customers WHERE email = 'a@b.c'"
	for _, a := range []Actor{admin, dev} {
		stream, err := h.c.DataQuery(h.ctx, a, db, apitypes.DataQueryRequest{SQL: sql})
		if err != nil {
			t.Fatal(err)
		}
		if err := stream(h.ctx, func() {}, func(...apitypes.DataQueryFrame) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.c.DataRows(h.ctx, dev, db, apitypes.DataPageRequest{Table: "public.customers", Filters: []apitypes.DataFilter{
		{Column: "email", Op: apitypes.FilterEq, Value: "a@b.c"}, {Column: "id", Op: apitypes.FilterIn, Values: []string{"7", "8"}},
	}}); err != nil {
		t.Fatal(err)
	}
	evs, err := h.c.ListAudit(h.ctx, viewer, AuditFilter{Action: "data."})
	if err != nil {
		t.Fatal(err)
	}
	var queries []apitypes.AuditEvent
	for _, ev := range evs {
		if strings.Contains(ev.Summary, "a@b.c") || strings.Contains(ev.Summary, "7") {
			t.Errorf("%s summary has values: %s", ev.Action, ev.Summary)
		}
		if ev.Action == apitypes.AuditDataBrowse && ev.Summary != "browsed public.customers filter: email eq and id in" {
			t.Errorf("browse summary: %s", ev.Summary)
		}
		if ev.Action == apitypes.AuditDataQuery {
			queries = append(queries, ev)
		}
	}
	if len(queries) != 2 || !strings.HasSuffix(queries[0].Summary, "email = ?") {
		t.Fatalf("query events: %+v", queries)
	}
	byDev, byAdmin := *queries[0].DetailID, *queries[1].DetailID // newest first

	if _, err := h.c.GetAuditDetail(h.ctx, viewer, byAdmin); CodeOf(err) != apitypes.CodeForbidden {
		t.Fatalf("detail for data level none: %v", err)
	}
	for _, c := range []struct {
		a    Actor
		id   int64
		want string
	}{
		{admin, byAdmin, sql},
		{dev, byDev, sql},
		{dev, byAdmin, "SELECT * FROM customers WHERE email = ?"},
	} {
		d, err := h.c.GetAuditDetail(h.ctx, c.a, c.id)
		if err != nil || d.SQL != c.want {
			t.Errorf("%s reads detail %d: %q %v", c.a.Name, c.id, d.SQL, err)
		}
	}
}

// TestContainerEventForget: only the die of the container the store has
// for a release, on its own server, drops the panel's connections to the
// service; events that merely carry its labels don't.
func TestContainerEventForget(t *testing.T) {
	h, eng, _, ss := fakeDatabase(t)
	rel, err := h.c.store.GetActiveRelease(h.ctx, ss.svc.ID)
	if err != nil {
		t.Fatal(err)
	}
	labels := node.Labels{Project: strconv.FormatInt(ss.project.ID, 10), Environment: strconv.FormatInt(ss.env.ID, 10),
		Service: strconv.FormatInt(ss.svc.ID, 10), Release: strconv.FormatInt(rel.ID, 10)}
	forgot := func() int {
		eng.mu.Lock()
		defer eng.mu.Unlock()
		return len(eng.forgot)
	}
	before := forgot()
	h.c.onContainerEvent(h.ctx, h.c.local.ID, node.ContainerEvent{ContainerID: "not-the-container", Labels: labels, Action: "die"})
	h.c.onContainerEvent(h.ctx, h.c.local.ID+1, node.ContainerEvent{ContainerID: rel.ContainerID, Labels: labels, Action: "die"})
	if forgot() != before {
		t.Fatal("a die event that isn't the release's container dropped the connections")
	}
	h.c.onContainerEvent(h.ctx, h.c.local.ID, node.ContainerEvent{ContainerID: rel.ContainerID, Labels: labels, Action: "die"})
	if forgot() != before+1 || eng.forgot[len(eng.forgot)-1] != ss.svc.ID {
		t.Fatalf("the release container's die: forgot %v", eng.forgot)
	}
}

// TestDataReveal: pages mask sensitive columns even for callers with
// sensitive visible until they reveal them; a reveal is audited (once per
// caller and table per interval), and masked callers can't reveal.
func TestDataReveal(t *testing.T) {
	h := newHarness(t)
	eng := &pageEngine{}
	h.opts.Data = eng
	h.restart()
	db := h.service("db", apitypes.CreateServiceRequest{Image: "postgres:17"})
	h.deploy(db, "postgres:17")
	ss, _ := h.c.loadService(h.ctx, 1, db)
	sealed, _ := h.c.box.Seal([]byte("pw"))
	if _, err := h.c.store.UpsertDataAccess(h.ctx, store.UpsertDataAccessParams{
		ServiceID: ss.svc.ID, Engine: "postgres", ServerVersion: "17.2", ReaderSecret: sealed, WriterSecret: sealed, SetUpAt: now(), SetUpBy: "admin@x",
	}); err != nil {
		t.Fatal(err)
	}
	admin := h.user("admin@x", apitypes.ScopeAdmin)
	dev := h.user("dev@x", apitypes.ScopeDeploy)
	viewer := h.user("viewer@x", apitypes.ScopeRead)
	if _, err := h.c.SetDataPolicy(h.ctx, admin, EnvRef{}, apitypes.UpdateDataPolicyRequest{Rules: []apitypes.DataPolicyRule{
		{Subject: apitypes.SubjectDeployUsers, Kind: apitypes.KindProduction, Level: apitypes.LevelRead, Sensitive: apitypes.SensitiveVisible},
		{Subject: apitypes.SubjectReadUsers, Kind: apitypes.KindProduction, Level: apitypes.LevelRead, Sensitive: apitypes.SensitiveMasked},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SetSensitiveColumns(h.ctx, admin, db, apitypes.SetSensitiveColumnsRequest{Columns: []apitypes.DataColumnRef{
		{Schema: "public", Table: "customers", Column: "email"}, {Schema: "public", Table: "customers", Column: "password_hash"},
	}}); err != nil {
		t.Fatal(err)
	}
	rows := func(a Actor, table string, reveal bool) error {
		_, err := h.c.DataRows(h.ctx, a, db, apitypes.DataPageRequest{Table: table, Reveal: reveal})
		return err
	}
	reveals := func() []apitypes.AuditEvent {
		evs, err := h.c.ListAudit(h.ctx, testSystem, AuditFilter{Action: apitypes.AuditDataReveal})
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}

	for _, a := range []Actor{admin, dev} {
		if err := rows(a, "public.customers", false); err != nil {
			t.Fatal(err)
		}
		if call, _ := eng.last(); !call.Masked {
			t.Fatalf("%s's page without reveal is unmasked", a.Name)
		}
	}
	if len(reveals()) != 0 {
		t.Fatal("pages without reveal are audited as reveals")
	}

	if err := rows(dev, "public.customers", true); err != nil {
		t.Fatal(err)
	}
	if call, _ := eng.last(); call.Masked {
		t.Fatal("revealed page is masked")
	}
	evs := reveals()
	if len(evs) != 1 || evs[0].Actor != "dev@x" || evs[0].Summary != "revealed public.customers: email, password_hash" {
		t.Fatalf("reveal audit: %+v", evs)
	}
	// Throttled like browsing; a table without sensitive columns reveals
	// nothing to audit.
	if err := rows(dev, "public.customers", true); err != nil {
		t.Fatal(err)
	}
	if err := rows(dev, "public.orders", true); err != nil {
		t.Fatal(err)
	}
	if call, _ := eng.last(); call.Masked || len(reveals()) != 1 {
		t.Fatalf("repeat reveal: masked %v, %d events", call.Masked, len(reveals()))
	}

	_, before := eng.last()
	err := rows(viewer, "public.customers", true)
	if CodeOf(err) != apitypes.CodeForbidden || !strings.Contains(err.Error(), "needs sensitive visible") || !strings.Contains(err.Error(), "role:read gets read on production, sensitive masked") {
		t.Fatalf("masked caller reveals: %v", err)
	}
	if _, after := eng.last(); after != before || len(reveals()) != 1 {
		t.Fatal("a refused reveal read the table or was audited")
	}
}
