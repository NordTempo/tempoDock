package httpkit

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// serversWithoutTimeouts are the http.Server literals that may leave out
// IdleTimeout and ReadHeaderTimeout, by file (from the module root), each
// with its reason.
var serversWithoutTimeouts = map[string]string{
	// Serves the one connection the control plane's link is, already
	// authenticated by mutual TLS and kept alive by HTTP/2 pings: closing
	// it when idle would drop the link to the server.
	"internal/node/remote/node.go": "the node link's single connection",
}

// Every listener closes connections a client leaves idle and ones that
// never finish their request line: an http.Server without IdleTimeout
// and ReadHeaderTimeout keeps them for good.
func TestEveryServerHasIdleAndHeaderTimeouts(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	found := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == "web" || n == "site" || n == "e2e" || n == "testdata" || (strings.HasPrefix(n, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, lit := range serverLits(file) {
			found++
			if _, ok := serversWithoutTimeouts[filepath.ToSlash(rel)]; ok {
				continue
			}
			for _, field := range []string{"IdleTimeout", "ReadHeaderTimeout"} {
				if !hasField(lit, field) {
					t.Errorf("%s: http.Server without %s: set it (httpkit.IdleTimeout), or add the file to serversWithoutTimeouts with the reason", fset.Position(lit.Pos()), field)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 8 {
		t.Fatalf("found only %d http.Server literals; the scan is broken", found)
	}
}

// serverLits returns file's http.Server composite literals, including
// the elements of a []*http.Server or []http.Server literal, which leave
// the type out.
func serverLits(file *ast.File) []*ast.CompositeLit {
	var out []*ast.CompositeLit
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		if isHTTPServer(lit.Type) {
			out = append(out, lit)
		}
		if arr, ok := lit.Type.(*ast.ArrayType); ok && isHTTPServer(arr.Elt) {
			for _, e := range lit.Elts {
				if el, ok := e.(*ast.CompositeLit); ok && el.Type == nil {
					out = append(out, el)
				}
			}
		}
		return true
	})
	return out
}

// isHTTPServer reports whether e is the type http.Server or *http.Server.
func isHTTPServer(e ast.Expr) bool {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Server" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http"
}

// hasField reports whether lit sets the field name.
func hasField(lit *ast.CompositeLit, name string) bool {
	for _, e := range lit.Elts {
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			if id, ok := kv.Key.(*ast.Ident); ok && id.Name == name {
				return true
			}
		}
	}
	return false
}
