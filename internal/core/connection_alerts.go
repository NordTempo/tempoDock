package core

import (
	"context"
	"strconv"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Connection alerts (ADR 0062): the platform's connections to Cloudflare,
// the GitHub App, backup storage and the platform registry raise a
// platform alert when a call Dock makes anyway fails, and resolve it when
// one succeeds. Nothing checks them on a timer: a connection nobody uses
// can't be seen failing, and isn't hurting anyone either. An org's own
// Cloudflare connection raises its org's alert instead.
//
// Keys: cloudflare:<connection ID>, github:app, storage:<repository URL>,
// registry:prune, registry:push and registry:bucket (RegistryBucketChanged),
// and alert_channel:<channel ID> for a broken alert channel (channelFailed).
// firewall:<server ID> is the same kind of alert, for a server without its
// app firewall (firewall_alert.go).

// connectionFailed opens (or updates) the platform alert for a connection
// that just failed: conn names it for clients (AlertTarget.Connection),
// title for people, msg says what failed and what to do.
func (c *Core) connectionFailed(ctx context.Context, key, conn, title, msg, command string) {
	c.connectionFailedIn(ctx, 0, key, conn, title, msg, command)
}

// connectionFailedIn is connectionFailed for a connection of org's (0:
// the platform's).
func (c *Core) connectionFailedIn(ctx context.Context, org int64, key, conn, title, msg, command string) {
	if ctx.Err() != nil {
		return // shutting down or the caller gave up: not the connection's fault
	}
	c.signalAlert(ctx, alertSignal{
		Key: key, Kind: apitypes.AlertConnection, Severity: apitypes.SeverityWarning,
		Title: title, Message: msg, Command: command, Target: &apitypes.AlertTarget{Connection: conn}, orgID: org,
	})
}

// cloudflareFailed opens the alert of a Cloudflare connection that just
// failed: the platform's for a platform connection, its org's for an
// org's own.
func (c *Core) cloudflareFailed(ctx context.Context, conn store.CloudflareConnection, msg, command string) {
	c.connectionFailedIn(ctx, cfOwner(conn), cloudflareAlertKey(conn.ID), "cloudflare:"+conn.Name,
		"Cloudflare connection "+conn.Name+" is failing", msg, command)
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
