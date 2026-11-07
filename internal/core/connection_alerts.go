package core

import (
	"context"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// Connection alerts (ADR 0062): the platform's connections to Cloudflare,
// the GitHub App, backup storage and the platform registry raise a
// platform alert when a call Dock makes anyway fails, and resolve it when
// one succeeds. Nothing checks them on a timer: a connection nobody uses
// can't be seen failing, and isn't hurting anyone either.
//
// Keys: cloudflare:<connection ID>, github:app, storage:<repository URL>,
// registry:prune and registry:push.

// connectionFailed opens (or updates) the platform alert for a connection
// that just failed: conn names it for clients (AlertTarget.Connection),
// title for people, msg says what failed and what to do.
func (c *Core) connectionFailed(ctx context.Context, key, conn, title, msg, command string) {
	if ctx.Err() != nil {
		return // shutting down or the caller gave up: not the connection's fault
	}
	c.signalAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertConnection, Severity: apitypes.SeverityWarning,
		Title: title, Message: msg, Command: command, Target: &apitypes.AlertTarget{Connection: conn},
	})
}

// connectionWorks resolves a connection's alert after a call that worked.
// Without an open alert it is a map lookup.
func (c *Core) connectionWorks(ctx context.Context, key string) {
	c.signalAlert(ctx, alertSignal{Key: key, Resolve: true})
}

// cloudflareAlertKey names a Cloudflare connection's alert.
func cloudflareAlertKey(id int64) string { return "cloudflare:" + strconv.FormatInt(id, 10) }

// githubAppAlertKey names the GitHub App's alert.
const githubAppAlertKey = "github:app"
