package core

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// goAllowed are the goroutine starts in core that don't go through
// caught.go's helpers, by file and enclosing function, each with why a
// panic in it can't come from what a tenant or a repository sent, or how
// it is recovered there. An entry covers every start in its function.
var goAllowed = map[string]string{
	"backups_controlplane.go:controlPlaneJob": "writes Dock's own database snapshot and data directory into a pipe; reads no tenant input",
	"data_connect.go:Pipe":                    "copies bytes between a client's connection and a service's without reading them",
	"drains_deliver.go:addLogDrain":           "context.AfterFunc: only closes the drain's connection when its context ends",
	"drains_sink.go:sendSyslog":               "discards what a syslog receiver sends back, to notice it closing; reads none of it. Its context.AfterFunc only sets the connection's deadline",
	"env_move.go:syncVolume":                  "runs Dock's own copier (dock-sync) between two pipes; the caller waits for its result on a channel",
	"forwards.go:forwardConn":                 "copies bytes between two connections without reading them; its context.AfterFunc only closes them",
	"forwards.go:serveForward":                "forwardConn defers recovered itself, so a connection costs no second closure; the context.AfterFunc only closes the listener",
	"git_github.go:tarball":                   "re-roots a repository's archive inside caught: a panic closes the pipe with an error, which fails the deploy reading it",
	"placement.go:fanOut":                     "each call defers recovered itself (run), so a server costs no second closure",
	"runs.go:wait":                            "context.AfterFunc: only cancels the run's context when the daemon stops",
	"exec.go:wait":                            "context.AfterFunc: only cancels the session's context when the daemon stops",
}

// goStart reports whether n starts a goroutine, and how: a go statement,
// a WaitGroup's or errgroup's Go (any method named Go), or time's or
// context's AfterFunc, whose function runs in a goroutine of its own.
func goStart(n ast.Node) (string, bool) {
	switch n := n.(type) {
	case *ast.GoStmt:
		return "go statement", true
	case *ast.CallExpr:
		sel, ok := n.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		if sel.Sel.Name == "Go" {
			return "Go call", true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && sel.Sel.Name == "AfterFunc" && (pkg.Name == "time" || pkg.Name == "context") {
			return pkg.Name + ".AfterFunc", true
		}
	}
	return "", false
}

// goHelpers are caught.go's functions that start a goroutine, each once.
var goHelpers = map[string]bool{"goCaught": true, "goLoop": true, "goIn": true, "goErr": true, "goGroup": true, "afterCaught": true}

// Nothing above a goroutine recovers a panic in it, so one takes the
// daemon down. Every goroutine core starts (a go statement, wg.Go,
// errgroup's Go, an AfterFunc) therefore starts through caught.go's
// helpers, or is listed in goAllowed with the reason it needs none.
func TestEveryGoroutineRecovers(t *testing.T) {
	fset, funcs := coreFuncs(t)
	used := map[string]bool{}
	helpers := 0
	for _, fd := range funcs {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			form, ok := goStart(n)
			if !ok {
				return true
			}
			pos := fset.Position(n.Pos())
			file := filepath.Base(pos.Filename)
			if file == "caught.go" && goHelpers[fd.Name.Name] {
				helpers++
				return true
			}
			key := file + ":" + fd.Name.Name
			if why, ok := goAllowed[key]; ok {
				used[key] = true
				if why == "" {
					t.Errorf("goAllowed lists %s without saying why", key)
				}
				return true
			}
			t.Errorf("%s: a %s in %s that recovers no panic: start it with caught.go's helpers (c.goCaught, c.goCounted for c.wg.Go, goIn or goErr for a local WaitGroup, goGroup for an errgroup, c.afterCaught for time.AfterFunc, c.goLoop for a loop that must be started again), and recover per item inside a loop; or, if it reads nothing a tenant or a repository sent, add %q to goAllowed with the reason", pos, form, fd.Name.Name, key)
			return true
		})
	}
	for key := range goAllowed {
		if !used[key] {
			t.Errorf("goAllowed names %s, which starts no goroutine now", key)
		}
	}
	if helpers != len(goHelpers) {
		t.Fatalf("found %d goroutine starts in caught.go's helpers, want %d; the scan is broken", helpers, len(goHelpers))
	}
}

// The guard sees every form a goroutine starts in, and nothing else.
func TestGoStartForms(t *testing.T) {
	for src, want := range map[string]string{
		"go f()":                            "go statement",
		"c.wg.Go(f)":                        "Go call",
		"wg.Go(f)":                          "Go call",
		"g.Go(func() error { return nil })": "Go call",
		"time.AfterFunc(d, f)":              "time.AfterFunc",
		"context.AfterFunc(ctx, f)":         "context.AfterFunc",
		"c.goCounted(what, f)":              "",
		"t.Reset(d)":                        "",
		"f()":                               "",
	} {
		file, err := parser.ParseFile(token.NewFileSet(), "", "package p\nfunc x() {\n"+src+"\n}", 0)
		if err != nil {
			t.Fatal(err)
		}
		got := ""
		ast.Inspect(file, func(n ast.Node) bool {
			if form, ok := goStart(n); ok {
				got = form
			}
			return true
		})
		if got != want {
			t.Errorf("%s: read as %q, want %q", src, got, want)
		}
	}
}

// lockedBuffer is a log buffer goroutines write while the test reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write appends p.
func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String is what was written so far.
func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A panic in a goroutine goCaught started ends that goroutine only: it is
// logged once with its stack, and the goroutine's own defers still run.
func TestGoCaughtRecovers(t *testing.T) {
	var logged lockedBuffer
	c := &Core{log: slog.New(slog.NewTextHandler(&logged, nil))}
	c.wg.Add(1)
	c.goCaught("reading a push", func() {
		defer c.wg.Done()
		panic("a push the worker cannot read")
	})
	c.wg.Wait()
	// recovered runs after the goroutine's own defers.
	deadline := time.Now().Add(5 * time.Second)
	for logged.String() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	out := logged.String()
	if strings.Count(out, "panic in a background worker") != 1 || !strings.Contains(out, "reading a push") || !strings.Contains(out, "cannot read") || !strings.Contains(out, "caught_go_test.go") {
		t.Fatalf("log = %q", out)
	}
}

// Workers started through a WaitGroup, an errgroup or a timer recover a
// panic: the caller's wait returns, and where the worker has an error to
// give, the panic is that error.
func TestWaitedWorkersRecover(t *testing.T) {
	c := &Core{log: slog.New(slog.DiscardHandler)}
	boom := func() error { panic("a reply the worker can't read") }

	c.goCounted("counted work", func() { _ = boom() })
	c.wg.Wait()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	goErr(&wg, c.log, "work side by side", &errs[0], boom)
	goErr(&wg, c.log, "work side by side", &errs[1], func() error { return nil })
	wg.Wait()
	if errs[0] == nil || errs[1] != nil {
		t.Fatalf("goErr: errs = %v, want only the first", errs)
	}

	var g errgroup.Group
	goGroup(&g, c.log, "work in a group", boom)
	if err := g.Wait(); err == nil || !strings.Contains(err.Error(), "work in a group failed") {
		t.Fatalf("goGroup: err = %v", err)
	}

	after := make(chan struct{})
	c.afterCaught(0, "work on a timer", func() {
		defer close(after)
		_ = boom()
	})
	<-after
}

// The domain checker is one loop for every domain: a panic while checking
// one must not end it. The next domain added is checked as usual.
func TestDomainCheckerOutlivesAPanic(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	panicked, next := make(chan struct{}), make(chan struct{})
	h.rt.SetDomainCheck(func(host string) node.DomainCheck {
		switch host {
		case "shop.example.com":
			select {
			case <-panicked: // checked again after its delay
			default:
				close(panicked)
				panic("a reply the checker can't read")
			}
		case "docs.example.com":
			select {
			case <-next:
			default:
				close(next)
			}
		}
		return node.DomainCheck{DNSOK: true, CertificateOK: true}
	})
	for _, step := range []struct {
		host string
		done chan struct{}
	}{{"shop.example.com", panicked}, {"docs.example.com", next}} {
		if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: step.host}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-step.done:
		case <-time.After(20 * time.Second):
			t.Fatalf("the domain checker never checked %s", step.host)
		}
	}
}
