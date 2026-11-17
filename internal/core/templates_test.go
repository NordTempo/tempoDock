//go:build !integration

package core

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Without a real database no try at a template's setup succeeds, so the
// wait between tries is short. The integration build keeps the real one:
// its Postgres needs it.
func init() { setupRetryDelay = 5 * time.Millisecond }

// A new template database's setup is tried again after its deploy ended:
// Postgres takes logins a moment after its health check passes, and the
// first try, inside the deploy, is often too early. Here no try gets an
// answer. One try dials the database at most twice (with TLS, then
// without), so a third dial is a second try.
func TestTemplateSetupIsRetriedAfterTheDeploy(t *testing.T) {
	h := newHarness(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var dials atomic.Int64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			dials.Add(1)
			_ = c.Close()
		}
	}()
	h.rt.DialTo(ln.Addr().String())
	h.templateDB("db", apitypes.TemplatePostgres)
	eventually(t, "a second try at the setup", func() bool { return dials.Load() > 2 })
}
