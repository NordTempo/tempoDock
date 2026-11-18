package core

import (
	"bytes"
	"go/ast"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// goAllowed are the `go` statements in core that don't start through
// goCaught or goLoop, by file and enclosing function, each with why a
// panic in it can't come from what a tenant or a repository sent, or how
// it is recovered there.
var goAllowed = map[string]string{
	"backups_controlplane.go:controlPlaneJob": "writes Dock's own database snapshot and data directory into a pipe; reads no tenant input",
	"data_connect.go:Pipe":                    "copies bytes between a client's connection and a service's without reading them",
	"drains_sink.go:sendSyslog":               "discards what a syslog receiver sends back, to notice it closing; reads none of it",
	"env_move.go:syncVolume":                  "runs Dock's own copier (dock-sync) between two pipes; the caller waits for its result on a channel",
	"forwards.go:forwardConn":                 "copies bytes between two connections without reading them",
	"git_github.go:tarball":                   "re-roots a repository's archive inside caught: a panic closes the pipe with an error, which fails the deploy reading it",
}

// Nothing above a goroutine recovers a panic in it, so one takes the
// daemon down. Every `go` statement in core therefore starts through
// caught.go's helpers (goCaught, or goLoop for a loop that is started
// again), or is listed in goAllowed with the reason it needs neither.
func TestEveryGoroutineRecovers(t *testing.T) {
	fset, funcs := coreFuncs(t)
	used := map[string]bool{}
	helpers := 0
	for _, fd := range funcs {
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			stmt, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			pos := fset.Position(stmt.Pos())
			file := filepath.Base(pos.Filename)
			if file == "caught.go" && (fd.Name.Name == "goCaught" || fd.Name.Name == "goLoop") {
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
			t.Errorf("%s: a go statement in %s that recovers no panic: start it with c.goCaught(what, fn) (c.goLoop for a loop that must be started again), and recover per item inside a loop (caught.go); or, if it reads nothing a tenant or a repository sent, add %q to goAllowed with the reason", pos, fd.Name.Name, key)
			return true
		})
	}
	for key := range goAllowed {
		if !used[key] {
			t.Errorf("goAllowed names %s, which has no go statement now", key)
		}
	}
	if helpers != 2 {
		t.Fatalf("found %d go statements in caught.go's helpers, want 2; the scan is broken", helpers)
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

// The domain checker is one loop for every domain: a panic while checking
// one must not end it. It is started again, and checks the domain again.
func TestDomainCheckerOutlivesAPanic(t *testing.T) {
	oldMin := loopRestartMin
	loopRestartMin = 10 * time.Millisecond
	t.Cleanup(func() { loopRestartMin = oldMin })
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	var mu sync.Mutex
	calls := 0
	again := make(chan struct{})
	h.rt.SetDomainCheck(func(string) node.DomainCheck {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		switch n {
		case 1:
			panic("a reply the checker can't read")
		case 2:
			close(again)
		}
		return node.DomainCheck{DNSOK: true, CertificateOK: true}
	})
	if _, err := h.c.AddDomain(h.ctx, testSystem, web, apitypes.AddDomainRequest{Hostname: "shop.example.com"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-again:
	case <-time.After(20 * time.Second):
		t.Fatal("the domain checker never checked again after a panic")
	}
}
