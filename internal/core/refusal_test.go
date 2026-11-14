package core

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// refusalOf returns err's refusal, failing unless err is a forbidden
// error that carries one.
func refusalOf(t *testing.T, err error) apitypes.Refusal {
	t.Helper()
	ce, ok := errors.AsType[*Error](err)
	if !ok || ce.Code != apitypes.CodeForbidden || ce.Refusal == nil {
		t.Fatalf("want a forbidden error with a refusal, got %v", err)
	}
	return *ce.Refusal
}

// TestRefusalsExplainThemselves: a refused call says what decided it
// and what would allow it: a person's role (an admin changes it), a
// token's scope (a new token), a token's limit, or the platform (an
// operator's credential, not an org token). Explain names the org's
// admins for a person, never for a manual token.
func TestRefusalsExplainThemselves(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	admin := h.cliUser("ada@example.com", apitypes.ScopeAdmin)
	if _, err := h.c.UpdateMe(h.ctx, admin, apitypes.UpdateMeRequest{Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	viewer := h.cliUser("ola@example.com", apitypes.ScopeRead)

	// A person whose role is too low.
	_, err := h.c.Restart(h.ctx, viewer, web)
	err = h.c.Explain(h.ctx, viewer, err)
	r := refusalOf(t, err)
	if r.DecidedBy != apitypes.RefusedRole || r.Need != "deploy" || r.Has != apitypes.ScopeRead || r.Action != "restarting" ||
		!strings.Contains(r.Fix, "dock org role ola@example.com deploy") || !slices.Equal(r.Ask, []string{"Ada"}) {
		t.Fatalf("role refusal = %+v", r)
	}
	if !strings.Contains(err.Error(), r.Reason+"; "+r.Fix) {
		t.Fatalf("message %q doesn't say why and what to do", err)
	}

	// A token's scope never changes: the fix is a new token, and nobody
	// is named to a token.
	ci := Actor{Kind: "token", OrgID: 1, OrgSlug: "default", Name: "ci", TokenID: 9, Scope: apitypes.ScopeDeploy}
	_, err = h.c.ArchiveService(h.ctx, ci, web)
	r = refusalOf(t, h.c.Explain(h.ctx, ci, err))
	if r.DecidedBy != apitypes.RefusedScope || r.Need != "admin" || r.Actor != `token "ci"` || len(r.Ask) != 0 ||
		!strings.Contains(r.Fix, "--scope admin") {
		t.Fatalf("scope refusal = %+v", r)
	}

	// A token limited to another environment: where it was refused, its
	// limit, and a token for that place.
	es, err := h.c.loadEnv(h.ctx, 1, web.Env())
	if err != nil {
		t.Fatal(err)
	}
	staging := Actor{Kind: "token", OrgID: 1, OrgSlug: "default", Name: "agent", TokenID: 10, Scope: apitypes.ScopeDeploy,
		ProjectID: es.project.ID, ProjectName: web.Project, EnvironmentID: -1, EnvironmentName: "staging"}
	_, err = h.c.Restart(h.ctx, staging, web)
	r = refusalOf(t, err)
	if r.DecidedBy != apitypes.RefusedLimit || r.Place != web.Project+"/"+web.Environment || r.Limit != web.Project+"/staging" ||
		!strings.Contains(r.Fix, "--project "+web.Project+" --environment "+web.Environment) {
		t.Fatalf("limit refusal = %+v", r)
	}

	// An org's admin token can't run the platform, and says which
	// credential can.
	orgAdmin := Actor{Kind: "token", OrgID: 1, OrgSlug: "default", Name: "vm", TokenID: 11, Scope: apitypes.ScopeAdmin}
	_, err = h.c.ListServers(h.ctx, orgAdmin)
	r = refusalOf(t, err)
	if r.DecidedBy != apitypes.RefusedOperator || !strings.Contains(r.Reason, "token of organization default") ||
		!strings.Contains(r.Fix, "operator") {
		t.Fatalf("operator refusal = %+v", r)
	}
}
