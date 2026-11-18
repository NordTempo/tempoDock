package core

import (
	"go/ast"
	"testing"
)

// valueOpeners are the functions that turn a stored variable into its
// plain value.
var valueOpeners = []string{"Core.openValue", "Core.currentValue"}

// revealChecks are the Actor's checks that say a caller may read values
// at a place. approves, decides, seesEnv and the env.view checks are not
// among them: who may see an environment or decide its requests may not
// read its values for that.
var revealChecks = []string{"Actor.mayReveal", "Actor.revealsIn", "Actor.revealsAt", "Actor.requireReveal"}

// valueReaders are the functions that open values for Dock's own work
// and hand no value back to a caller, each with why. The scan stops at
// them: what calls them needs no reveal check for that.
var valueReaders = map[string]string{
	"Core.envResolver":      "resolves references for a release, a connection or a preview of one; what a caller is shown of it is redacted or checked where it is returned (ConnectInfo, redactedFor)",
	"Core.stagedChanges":    "opens values only when its caller asks: Changes after requireReveal, ApplyProject to compare before and after, which it returns as a count",
	"Core.stageSetAs":       "compares a new value with the live one to un-stage it; never for a caller who may not reveal (blind)",
	"Core.orgRefsAllowed":   "reads which organization variables a value references already; returns a refusal naming keys",
	"Core.orgVariableUses":  "reads which organization variables values reference; returns keys and places",
	"Core.usedBy":           "reads which services reference a database; returns their names",
	"Core.dbUsers":          "reads which services reference a database; returns their names",
	"Core.templateLiterals": "carries a database's own credentials to its restored copy, sealed again; returns them to restore code only",
	"Core.clonedValue":      "seals a source's value again for its clone; literals are copied only for who may reveal the source (CloneEnvironment)",
	"Core.SwitchReferences": "rewrites references from one service to another; returns the keys it changed",
	"Core.ExportProject":    "writes values that are nothing but references, and the keys of the rest",
	".loadLiveEnv":          "loads live values to plan a project file against; planVars hides them from who may not reveal (blind)",
}

// A stored value is opened only for who may read it: every chain of
// calls from an operation to openValue or currentValue passes a function
// that asks a reveal check (revealChecks), or one that opens values for
// Dock's own work and is listed with why (valueReaders). A check that
// only says the caller sees the environment, or decides its requests,
// lets values through to people who hold no permission to read them:
// that is what this finds.
func TestValuesOpenedBehindReveal(t *testing.T) {
	calls := callGraph(t)
	checked := func(fn string) bool {
		for _, c := range revealChecks {
			if calls[fn][c] {
				return true
			}
		}
		return false
	}
	// open holds the functions a value reaches with no reveal check on
	// the way, with the callee it comes through; grown until none joins.
	open := map[string]string{}
	for _, o := range valueOpeners {
		if calls[o] == nil {
			t.Fatalf("%s is gone: name what opens values now in valueOpeners", o)
		}
		open[o] = ""
	}
	used := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for fn, callees := range calls {
			if _, ok := open[fn]; ok || checked(fn) {
				continue
			}
			for callee := range callees {
				if _, ok := open[callee]; !ok {
					continue
				}
				if _, ok := valueReaders[fn]; ok {
					used[fn] = true
					break
				}
				open[fn], changed = callee, true
				break
			}
		}
	}
	for fn, why := range valueReaders {
		if why == "" {
			t.Errorf("valueReaders lists %s without saying why", fn)
		}
		if !used[fn] {
			t.Errorf("valueReaders names %s, which opens no value unchecked now: take it off", fn)
		}
	}
	for fn, via := range open {
		typ, name, _ := cutDot(fn)
		if typ != "Core" || !ast.IsExported(name) {
			continue
		}
		chain := fn
		for at := via; at != ""; at = open[at] {
			chain += " > " + at
		}
		t.Errorf("%s opens variable values with no reveal check on the way (%s): ask a.mayReveal or a.revealsIn where the value is returned, or list the function that keeps it from the caller in valueReaders with why", fn, chain)
	}
	if len(calls) < 1000 {
		t.Fatalf("only %d functions scanned; the scan is broken", len(calls))
	}
}

// cutDot splits a callGraph name into its receiver type and its name.
func cutDot(fn string) (typ, name string, ok bool) {
	for i := range fn {
		if fn[i] == '.' {
			return fn[:i], fn[i+1:], true
		}
	}
	return "", fn, false
}

// recvOf is fd's receiver type ("" for a plain function) and the name
// the receiver goes by.
func recvOf(fd *ast.FuncDecl) (typ, name string) {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return "", ""
	}
	f := fd.Recv.List[0]
	e := f.Type
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	if len(f.Names) == 1 {
		name = f.Names[0].Name
	}
	return identName(e), name
}

// callGraph is what each of the package's functions calls, both named
// "Type.name" ("" for a plain function's type). Without types, a method
// call is resolved by what it is called on: the function's own receiver,
// the Core (c, x.core), or otherwise every other type with a method of
// that name. Calls on the store (store, q, tx) are left out: its methods
// share names with Core's.
func callGraph(t *testing.T) map[string]map[string]bool {
	t.Helper()
	_, funcs := coreFuncs(t)
	types := map[string][]string{}
	for _, fd := range funcs {
		typ, _ := recvOf(fd)
		types[fd.Name.Name] = append(types[fd.Name.Name], typ)
	}
	calls := map[string]map[string]bool{}
	for _, fd := range funcs {
		typ, self := recvOf(fd)
		callees := map[string]bool{}
		calls[typ+"."+fd.Name.Name] = callees
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch f := call.Fun.(type) {
			case *ast.Ident:
				callees["."+f.Name] = true
			case *ast.SelectorExpr:
				on := identName(f.X)
				if sel, ok := f.X.(*ast.SelectorExpr); ok {
					on = sel.Sel.Name
				}
				switch {
				case on != "" && on == self:
					callees[typ+"."+f.Sel.Name] = true
				case on == "c" || on == "core":
					callees["Core."+f.Sel.Name] = true
				case on == "store" || on == "q" || on == "tx":
				default:
					for _, other := range types[f.Sel.Name] {
						if other != "Core" && other != "" {
							callees[other+"."+f.Sel.Name] = true
						}
					}
				}
			}
			return true
		})
	}
	return calls
}
