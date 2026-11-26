package core

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Every permission check in core names a permission (ADR 0080): the
// people's grants decide by it, so an unmapped check would refuse them
// what today allows. The scan starts from the Actor's checks and follows
// every function that passes its scope or action on to one (loadSite,
// projectFor, statusPagesIn, ...), so a new loader is covered without
// being listed. An action is a literal, a constant, a local only ever set
// to literals, or a concatenation of those; anything else fails the test,
// so it can't hide from it.
func TestEveryCheckHasAPermission(t *testing.T) {
	checks, problems := scanChecks(t)
	for _, p := range problems {
		t.Error(p)
	}
	found := map[actionKey]bool{}
	for _, c := range checks {
		found[c.key] = true
		if _, ok := permFor(c.key); !ok {
			t.Errorf("%s: %s(%s, %q) has no permission: add it to actionPerms (shadow.go)", c.pos, c.fn, c.key.need, c.key.action)
		}
	}
	for key := range actionPerms {
		if !found[key] {
			t.Errorf("actionPerms names %s %q, which no check asks: a renamed or removed check left it behind", key.need, key.action)
		}
	}
	if len(found) < 200 {
		t.Fatalf("found only %d checks; the scan is broken", len(found))
	}
}

// baseChecks are the Actor's checks; each takes (need, action) first.
var baseChecks = []string{"require", "requireOnly", "requireScope", "requireUnlimited", "checkProject", "checkEnv", "checkEnvAccess"}

// checkSig is where a check, or a function passing its arguments on to
// one, takes the scope and the action: argument indexes (-1 for none),
// or a fixed scope or action; suffix is what it appends to the action it
// was given.
type checkSig struct {
	scope       int
	fixed       apitypes.Scope
	action      int
	fixedAction string
	suffix      string
	// base is the Actor check it ends in.
	base string
}

// scannedCheck is one call site's check.
type scannedCheck struct {
	pos  string
	fn   string
	key  actionKey
	base string
	// in is the function the call is in.
	in string
}

// scanChecks finds every check in the package's code with its scope and
// action, and the call sites it can't resolve.
func scanChecks(t *testing.T) ([]scannedCheck, []string) {
	t.Helper()
	fset, funcs := coreFuncs(t)
	sigs := map[string][]checkSig{}
	for _, name := range baseChecks {
		sigs[name] = []checkSig{{scope: 0, action: 1, base: name}}
	}
	// Grow the set of checks until no function joins: one that passes a
	// parameter on as a check's scope or action is a check too, once for
	// each check it passes them to.
	for changed := true; changed; {
		changed = false
		for _, fd := range funcs {
			if slices.Contains(baseChecks, fd.Name.Name) {
				continue
			}
			params := paramIndex(fd)
			eachCheck(fd, sigs, func(call *ast.CallExpr, sig checkSig) {
				for _, s := range passedOn(call, sig, params, fd) {
					if !slices.Contains(sigs[fd.Name.Name], s) {
						sigs[fd.Name.Name] = append(sigs[fd.Name.Name], s)
						changed = true
					}
				}
			})
		}
	}
	var checks []scannedCheck
	var problems []string
	for _, fd := range funcs {
		params := paramIndex(fd)
		eachCheck(fd, sigs, func(call *ast.CallExpr, sig checkSig) {
			if len(passedOn(call, sig, params, fd)) > 0 {
				return // its callers are scanned
			}
			fn := calleeName(call)
			pos := fset.Position(call.Pos()).String()
			need := sig.fixed
			if need == "" {
				sc, ok := scopeLit(call.Args[sig.scope])
				if !ok {
					problems = append(problems, fmt.Sprintf("%s: %s's scope isn't a literal or a parameter passed on", pos, fn))
					return
				}
				need = sc
			}
			actions, prefix := []string{sig.fixedAction}, ""
			if sig.action >= 0 {
				actions, prefix = actionValues(call.Args[sig.action], fd)
			}
			switch {
			case len(actions) > 0:
				for _, act := range actions {
					checks = append(checks, scannedCheck{pos, fn, actionKey{need, act + sig.suffix}, sig.base, fd.Name.Name})
				}
			case prefix != "":
				checks = append(checks, scannedCheck{pos, fn, actionKey{need, prefix}, sig.base, fd.Name.Name})
			default:
				problems = append(problems, fmt.Sprintf("%s: %s's action can't be read from the code: write it as a literal or a constant", pos, fn))
			}
		})
	}
	return checks, problems
}

// eachCheck calls f for every call in fd's body to a known check, once
// per signature.
func eachCheck(fd *ast.FuncDecl, sigs map[string][]checkSig, f func(*ast.CallExpr, checkSig)) {
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, sig := range sigs[calleeName(call)] {
			if sig.action < len(call.Args) && sig.scope < len(call.Args) {
				f(call, sig)
			}
		}
		return true
	})
}

// passedOn is the caller's own signatures when a check call passes the
// caller's parameters on: its action (perhaps with a literal appended),
// its scope, or both; what isn't passed on is fixed (a literal, a
// constant). None when the call passes nothing on.
func passedOn(call *ast.CallExpr, sig checkSig, params map[string]int, fd *ast.FuncDecl) []checkSig {
	out := checkSig{scope: -1, fixed: sig.fixed, action: -1, fixedAction: sig.fixedAction, suffix: sig.suffix, base: sig.base}
	passes := false
	if sig.fixed == "" {
		if si, isParam := params[identName(call.Args[sig.scope])]; isParam {
			out.scope, out.fixed, passes = si, "", true
		} else if sc, isLit := scopeLit(call.Args[sig.scope]); isLit {
			out.fixed = sc
		} else {
			return nil
		}
	}
	if sig.action < 0 {
		if !passes {
			return nil
		}
		return []checkSig{out}
	}
	arg := call.Args[sig.action]
	suffix := ""
	if b, ok := arg.(*ast.BinaryExpr); ok && b.Op == token.ADD {
		if lit, isLit := b.Y.(*ast.BasicLit); isLit && lit.Kind == token.STRING {
			suffix, _ = strconv.Unquote(lit.Value)
			arg = b.X
		}
	}
	if ai, isParam := params[identName(arg)]; isParam {
		out.action, out.suffix = ai, suffix+sig.suffix
		return []checkSig{out}
	}
	if !passes {
		return nil
	}
	// The scope is passed on with an action of the caller's own.
	values, _ := actionValues(call.Args[sig.action], fd)
	sigs := make([]checkSig, 0, len(values))
	for _, v := range values {
		o := out
		o.fixedAction, o.suffix = v+sig.suffix, ""
		sigs = append(sigs, o)
	}
	return sigs
}

// calleeName is the called function's or method's name.
func calleeName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	}
	return ""
}

// paramIndex maps a function's parameter names to their positions.
func paramIndex(fd *ast.FuncDecl) map[string]int {
	out := map[string]int{}
	i := 0
	for _, f := range fd.Type.Params.List {
		if len(f.Names) == 0 {
			i++
		}
		for _, n := range f.Names {
			out[n.Name] = i
			i++
		}
	}
	return out
}

// identName is e's name when it is a plain identifier.
func identName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return "\x00"
}

// scopeLit reads apitypes.ScopeX.
func scopeLit(e ast.Expr) (apitypes.Scope, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(sel.Sel.Name, "Scope") || sel.Sel.Name == "Scope" {
		return "", false
	}
	return apitypes.Scope(strings.ToLower(strings.TrimPrefix(sel.Sel.Name, "Scope"))), true
}

// actionValues is every value an action expression can take in fd: a
// literal, a constant, a local only ever set to literals, or a
// concatenation of those. Failing that, a concatenation's leading literal
// is its prefix (actionPrefixes).
func actionValues(e ast.Expr, fd *ast.FuncDecl) (values []string, prefix string) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			s, err := strconv.Unquote(e.Value)
			if err == nil {
				return []string{s}, ""
			}
		}
	case *ast.ParenExpr:
		return actionValues(e.X, fd)
	case *ast.Ident:
		return identValues(e, fd), ""
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return nil, ""
		}
		left, lp := actionValues(e.X, fd)
		right, _ := actionValues(e.Y, fd)
		if len(left) == 0 {
			return nil, lp
		}
		if len(right) == 0 {
			if len(left) == 1 {
				return nil, left[0]
			}
			return nil, ""
		}
		for _, l := range left {
			for _, r := range right {
				values = append(values, l+r)
			}
		}
		return values, ""
	}
	return nil, ""
}

// identValues is the literals an identifier holds: a constant's, or
// every value a local variable of fd is set to, if all are literals.
func identValues(id *ast.Ident, fd *ast.FuncDecl) []string {
	if id.Obj == nil {
		return nil
	}
	switch d := id.Obj.Decl.(type) {
	case *ast.ValueSpec:
		var out []string
		for i, n := range d.Names {
			if n.Name == id.Name && i < len(d.Values) {
				out, _ = actionValues(d.Values[i], fd)
			}
		}
		if id.Obj.Kind == ast.Con {
			return out
		}
		return localValues(id, fd, out)
	case *ast.AssignStmt:
		return localValues(id, fd, nil)
	}
	return nil
}

// localValues adds every literal a local variable is assigned in fd; nil
// if one assignment isn't a literal.
func localValues(id *ast.Ident, fd *ast.FuncDecl, out []string) []string {
	ok := true
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		as, isAssign := n.(*ast.AssignStmt)
		if !isAssign {
			return true
		}
		for i, l := range as.Lhs {
			li, isIdent := l.(*ast.Ident)
			if !isIdent || li.Obj != id.Obj || i >= len(as.Rhs) {
				continue
			}
			lit, isLit := as.Rhs[i].(*ast.BasicLit)
			if !isLit || lit.Kind != token.STRING {
				ok = false
				continue
			}
			s, _ := strconv.Unquote(lit.Value)
			out = append(out, s)
		}
		return true
	})
	if !ok {
		return nil
	}
	return out
}

// orgChecksOfEnvPerms are the functions that check, at the org, an
// environment permission that changes or reveals something, and why
// that is right there. Asked at the org such a permission holds wherever
// it is held at all (modelAllows), so anywhere else it would let a grant
// on one environment act on every one: check it at the place instead
// (checkEnv, checkEnvAccess, envFor, serviceFor) or add the function
// here with its reason.
var orgChecksOfEnvPerms = map[string]string{
	"ApplyProject":        "pre-check; every environment of the file is checked with checkEnv",
	"CheckRequest":        "pre-check; checkEnvAccess where the request is follows",
	"DecideRequest":       "pre-check; checkEnvAccess where the request is follows",
	"LockEnvironment":     "pre-check; checkEnvAccess where it locks follows",
	"ResolveAlert":        "pre-check; holdsAt where the alert is follows",
	"RedeployOrgVariable": "pre-check; each service's redeploy is checked where it is, and skipped otherwise",
	"OpenIncident":        "status pages are the org's; checkIncidentScope holds each component to where the caller posts",
	"PostIncidentUpdate":  "status pages are the org's; checkIncidentScope holds each component to where the caller posts",
	"UpdateIncident":      "status pages are the org's; checkIncidentScope holds each component to where the caller posts",
	"EditIncidentUpdate":  "status pages are the org's; checkIncidentScope holds each component to where the caller posts",
	"ScheduleMaintenance": "status pages are the org's; checkIncidentScope holds each component to where the caller posts",
	"ListCloudflareZones": "lists the org's zones to add a domain to; adding one is checked where its service is",
	"GitHubRepos":         "lists repositories to connect; connecting one is checked where its service is",
	"GitHubBranches":      "lists branches to connect; connecting one is checked where its service is",
	"GitHubPaths":         "lists directories to connect; connecting one is checked where its service is",
	"revealsAt":           "only for actors grants don't decide; grants are asked at the place",
	"requireReveal":       "through mayReveal, only for actors grants don't decide",
	"CloneEnvironment":    "through mayReveal, only for actors grants don't decide",
	"ConnectInfo":         "through mayReveal, only for actors grants don't decide",
}

// An environment permission that changes or reveals something is checked
// where it applies, not at the org (orgChecksOfEnvPerms): the security
// review found reveal on one environment revealing every one's secrets.
func TestEnvPermsCheckedAtTheirPlace(t *testing.T) {
	checks, _ := scanChecks(t)
	used := map[string]bool{}
	for _, c := range checks {
		p, ok := permFor(c.key)
		if !ok || c.base != "require" && c.base != "requireUnlimited" {
			continue
		}
		if info := permTable[p]; info.level != PermEnv || info.read {
			continue
		}
		if _, ok := orgChecksOfEnvPerms[c.in]; ok {
			used[c.in] = true
			continue
		}
		t.Errorf("%s: %s(%s, %q) checks %s at the org, where a grant on any environment passes; check it where it applies (checkEnv), or say why in orgChecksOfEnvPerms", c.pos, c.fn, c.key.need, c.key.action, p)
	}
	for in := range orgChecksOfEnvPerms {
		if !used[in] {
			t.Errorf("orgChecksOfEnvPerms names %s, which checks no such permission at the org now", in)
		}
	}
}

// Every operation checks its caller (internal/core/AGENTS.md): an
// exported Core method that takes an Actor reaches one of the Actor's
// checks, itself or through what it hands the actor to, or acts on the
// caller's own account and may refuse (it reads who the actor is: their
// user, session or token). TestEveryCheckHasAPermission only sees checks
// that exist; this finds the operation that has none, which would serve
// anyone the middleware placed in the org. The few that need none are
// listed with why.
func TestEveryOperationChecksItsActor(t *testing.T) {
	_, funcs := coreFuncs(t)
	// checks holds the functions that reach a check, by name, grown until
	// none joins: the Actor's own checks first.
	checks := map[string]bool{}
	for _, name := range actorChecks {
		checks[name] = true
	}
	for changed := true; changed; {
		changed = false
		for _, fd := range funcs {
			if !checks[fd.Name.Name] && reachesCheck(fd, checks, returnsError(fd)) {
				checks[fd.Name.Name], changed = true, true
			}
		}
	}
	seen := map[string]bool{}
	for _, fd := range funcs {
		name := fd.Name.Name
		if !fd.Name.IsExported() || !onCore(fd) || len(actorNames(fd)) == 0 && !takesActor(fd) {
			continue
		}
		if why, ok := uncheckedOperations[name]; ok {
			seen[name] = true
			if why == "" {
				t.Errorf("uncheckedOperations lists %s without saying why", name)
			}
			if reachesCheck(fd, checks, true) {
				t.Errorf("%s checks its actor now: take it off uncheckedOperations", name)
			}
			continue
		}
		if !reachesCheck(fd, checks, true) {
			t.Errorf("Core.%s takes an Actor and never checks it: start with a.require, a loader (envFor, serviceFor, projectFor) or requirePlatform, or list it in uncheckedOperations with the reason", name)
		}
	}
	for name := range uncheckedOperations {
		if !seen[name] {
			t.Errorf("uncheckedOperations names %s, which is no operation now", name)
		}
	}
	if len(checks) < 300 {
		t.Fatalf("only %d functions reach a check; the scan is broken", len(checks))
	}
}

// actorChecks are the Actor's checks an operation ends in: the org's
// (baseChecks) and the platform's.
var actorChecks = append(slices.Clone(baseChecks), "requirePlatform", "requireAccountManager")

// ownFields are the Actor's fields that say who the caller is. A
// function that reads one and returns an error acts on the caller's own
// account and may refuse: their password, factors, tokens, memberships.
// OrgID isn't one: it says where the middleware placed the caller, not
// that they hold anything there.
var ownFields = []string{"UserID", "SessionID", "TokenID", "Kind"}

// uncheckedOperations are the operations that take an Actor and check
// nothing, each with why that is right.
var uncheckedOperations = map[string]string{
	"Explain":        "completes a refusal a check already made; it decides nothing",
	"ListTemplates":  "the database templates Dock ships, the same for every caller, an Actor only by convention",
	"SnapshotBefore": "core's own step of a deploy or a run: it takes the service its caller loaded and checked (a svcScope)",
}

// coreFuncs parses the package's code (tests left out) and returns its
// functions with bodies.
func coreFuncs(t *testing.T) (*token.FileSet, []*ast.FuncDecl) {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var funcs []*ast.FuncDecl
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				funcs = append(funcs, fd)
			}
		}
	}
	return fset, funcs
}

// isActor reports whether a parameter's or receiver's type is Actor.
func isActor(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "Actor"
}

// actorNames are the names fd knows an Actor by: its parameters and its
// receiver of that type.
func actorNames(fd *ast.FuncDecl) []string {
	var out []string
	lists := []*ast.FieldList{fd.Type.Params}
	if fd.Recv != nil {
		lists = append(lists, fd.Recv)
	}
	for _, l := range lists {
		for _, f := range l.List {
			if isActor(f.Type) {
				for _, n := range f.Names {
					out = append(out, n.Name)
				}
			}
		}
	}
	return out
}

// takesActor reports whether fd has an Actor parameter, named or not.
func takesActor(fd *ast.FuncDecl) bool {
	for _, f := range fd.Type.Params.List {
		if isActor(f.Type) {
			return true
		}
	}
	return false
}

// onCore reports whether fd is a method of *Core.
func onCore(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Core"
}

// returnsError reports whether fd's last result is an error: only then
// can it refuse.
func returnsError(fd *ast.FuncDecl) bool {
	if fd.Type.Results == nil || len(fd.Type.Results.List) == 0 {
		return false
	}
	last := fd.Type.Results.List[len(fd.Type.Results.List)-1]
	id, ok := last.Type.(*ast.Ident)
	return ok && id.Name == "error"
}

// reachesCheck reports whether fd checks an Actor it knows: it calls a
// function in checks on it (or takes one as a value, a.require) or with
// it (also as a.InOrg(...), a derived actor), or, when own counts (fd
// may refuse), reads who it is (ownFields).
func reachesCheck(fd *ast.FuncDecl, checks map[string]bool, own bool) bool {
	names := actorNames(fd)
	if len(names) == 0 {
		return false
	}
	isActorExpr := func(e ast.Expr) bool {
		if call, ok := e.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				e = sel.X // a.operatorIn(o), a.platform()
			}
		}
		return slices.Contains(names, identName(e))
	}
	found := false
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			if isActorExpr(n.X) && (checks[n.Sel.Name] || own && slices.Contains(ownFields, n.Sel.Name)) {
				found = true
			}
		case *ast.CallExpr:
			if checks[calleeName(n)] && slices.ContainsFunc(n.Args, isActorExpr) {
				found = true
			}
		}
		return !found
	})
	return found
}
