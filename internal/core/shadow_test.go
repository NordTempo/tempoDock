package core

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Every check's permission fits today's scopes exactly (ADR 0080): the
// seeded group of the scope a check asks for holds it, and the group
// below doesn't. So Developers and Viewers are today's deploy and read
// for every check in actionPerms, also those no test reaches.
func TestPermissionsFitScopes(t *testing.T) {
	below := map[apitypes.Scope]apitypes.Scope{apitypes.ScopeDeploy: apitypes.ScopeRead, apitypes.ScopeAdmin: apitypes.ScopeDeploy}
	for key, perm := range actionPerms {
		if !seededPerms(key.need).has(perm) {
			t.Errorf("%s %q: %s isn't in the %s group", key.need, key.action, perm, key.need)
		}
		if b, ok := below[key.need]; ok && seededPerms(b).has(perm) {
			t.Errorf("%s %q: %s is in the %s group, which today can't", key.need, key.action, perm, b)
		}
	}
	if seededPerms(apitypes.ScopeAdmin) != allPerms&^dataPerms {
		t.Error("Admins aren't Full access")
	}
}

// checkFuncs are the checks the shadow compares, and the loaders that
// pass their scope and action on to one.
var checkFuncs = map[string]bool{
	"require": true, "requireUnlimited": true, "checkProject": true, "checkEnv": true, "checkEnvAccess": true,
	"envFor": true, "serviceFor": true, "projectFor": true, "loadStatusPage": true, "jobFor": true, "policyScope": true,
}

// Every check written with a literal scope and action names a
// permission, so a new check can't slip past the model unseen.
func TestEveryCheckHasAPermission(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	n := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			var fn string
			switch f := call.Fun.(type) {
			case *ast.SelectorExpr:
				fn = f.Sel.Name
			case *ast.Ident:
				fn = f.Name
			}
			if !checkFuncs[fn] {
				return true
			}
			for i, arg := range call.Args[:max(0, len(call.Args)-1)] {
				sel, ok := arg.(*ast.SelectorExpr)
				if !ok || !strings.HasPrefix(sel.Sel.Name, "Scope") {
					continue
				}
				lit, ok := call.Args[i+1].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					break
				}
				action, _ := strconv.Unquote(lit.Value)
				key := actionKey{apitypes.Scope(strings.ToLower(strings.TrimPrefix(sel.Sel.Name, "Scope"))), action}
				n++
				if _, ok := permFor(key); !ok {
					t.Errorf("%s: %s(%s, %q) has no permission in actionPerms", fset.Position(call.Pos()), fn, key.need, action)
				}
				break
			}
			return true
		})
	}
	if n < 150 {
		t.Fatalf("found only %d checks; the scan is broken", n)
	}
}

// A data policy rule that gives less than a wider one can't be a grant:
// the shadow reports it as a lowering rule, which the migration's
// preflight stops on.
func TestShadowFindsLoweringRules(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Image: "app:1"})
	const staging = "lowering-staging"
	if _, err := h.c.CloneEnvironment(h.ctx, testSystem, web.Env(), apitypes.CloneEnvironmentRequest{Name: staging}); err != nil {
		t.Fatal(err)
	}
	set := func(ref EnvRef, rule apitypes.DataPolicyRule) {
		if _, err := h.c.SetDataPolicy(h.ctx, testSystem, ref, apitypes.UpdateDataPolicyRequest{Rules: []apitypes.DataPolicyRule{rule}}); err != nil {
			t.Fatal(err)
		}
	}
	set(EnvRef{}, apitypes.DataPolicyRule{Subject: apitypes.SubjectDeployUsers, Kind: apitypes.KindNonProduction, Level: apitypes.LevelWrite, Sensitive: apitypes.SensitiveMasked})
	set(EnvRef{Project: web.Project}, apitypes.DataPolicyRule{Subject: apitypes.SubjectDeployUsers, Kind: apitypes.KindNonProduction, Level: apitypes.LevelSchema, Sensitive: apitypes.SensitiveMasked})
	dev := h.user("lowering@example.com", apitypes.ScopeDeploy)
	if _, err := h.c.DataLevel(h.ctx, dev, EnvRef{web.Project, staging}); err != nil {
		t.Fatal(err)
	}
	for _, d := range ShadowReport() {
		if strings.Contains(d.Check, loweringRule) && strings.Contains(d.Check, web.Project+"/"+staging) {
			return
		}
	}
	t.Fatalf("the lowering rule wasn't reported: %+v", ShadowReport())
}
