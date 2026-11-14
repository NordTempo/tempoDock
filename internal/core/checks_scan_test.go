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
}

// scannedCheck is one call site's check.
type scannedCheck struct {
	pos string
	fn  string
	key actionKey
}

// scanChecks finds every check in the package's code with its scope and
// action, and the call sites it can't resolve.
func scanChecks(t *testing.T) ([]scannedCheck, []string) {
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
	sigs := map[string][]checkSig{}
	for _, name := range baseChecks {
		sigs[name] = []checkSig{{scope: 0, action: 1}}
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
					checks = append(checks, scannedCheck{pos, fn, actionKey{need, act + sig.suffix}})
				}
			case prefix != "":
				checks = append(checks, scannedCheck{pos, fn, actionKey{need, prefix}})
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
	out := checkSig{scope: -1, fixed: sig.fixed, action: -1, fixedAction: sig.fixedAction, suffix: sig.suffix}
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
