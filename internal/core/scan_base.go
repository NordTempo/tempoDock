package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Base image hints (ADR 0074, phase 3). A Dockerfile build reports the
// image its final stage was built on and the digest that resolved to
// (node.Built); the release keeps both and its image's scan takes them.
// Once a day the scanner asks each live base image's registry what its
// tag points at now, one manifest request per base image whatever the
// number of images built on it. When the tag moved, a rebuild picks up
// the newer base image, which is what the image's page says next to the
// system packages' fixes.
//
// An image deployed by tag (postgres:17-alpine@sha256:..., as the
// database templates are) is its own base: the same check follows its
// tag, and when it moved the service offers to update to the newer
// image, deployed by digest, or updates on its own (auto_update,
// image_updates.go). A new template database starts on the newest digest
// the check has seen (templateImage).
//
// The check is a scan step of its own (amendment 2026-11-26), due a day
// after the last one finished: a build or deploy that cuts it short
// leaves it due, so it runs again after the next quiet minute instead of
// a day later. CheckImageTag asks for one service's tag at once.

// maxBaseChecks bounds the base images one daily check asks about.
const maxBaseChecks = 200

// tagCheckEvery is how often the scanner checks base images' tags.
const tagCheckEvery = 24 * time.Hour

// tagCheckDue reports whether the daily check of base images' tags is
// due at at. When it isn't, it lowers scans.refreshAt to when it will be,
// so the hourly pass (kickScanRefresh) starts the scanner for it; call it
// after scanRefreshDue, which sets refreshAt for the databases. Offline
// installs (only imported databases) don't check.
func (c *Core) tagCheckDue(at time.Time) bool {
	if c.cfg.Scan.Offline {
		return false
	}
	s := &c.scans
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.tagsChecked.Add(tagCheckEvery)
	if !next.After(at) {
		return true
	}
	if s.refreshAt.IsZero() || next.Before(s.refreshAt) {
		s.refreshAt = next
	}
	return false
}

// checkTags is the daily check as a scan step on srv: every live base
// image's tag, then the services that follow theirs are updated
// (autoUpdateImages). Cut short by a build or deploy, it stays due.
// Registries that didn't answer wait for the next day, like a failed
// database refresh. The updates run under the daemon's context, not the
// step's: their own deploys count as busy, which cancels the step.
func (c *Core) checkTags(ctx context.Context, srv store.Server) {
	if rt := c.connected(srv.ID); rt != nil {
		c.checkBaseImages(ctx, srv, rt)
	}
	if c.scanPreempted(ctx) || c.ctx.Err() != nil {
		return
	}
	c.autoUpdateImages(c.ctx)
	c.scans.mu.Lock()
	c.scans.tagsChecked = now()
	c.scans.mu.Unlock()
}

// checkBaseImages asks rt's registries which digest each live base
// image's tag points at now and records whether the images built on it
// are behind. It runs inside a scan step: a build or deploy cancels it.
func (c *Core) checkBaseImages(ctx context.Context, srv store.Server, rt node.Runtime) {
	bases, err := c.store.ListLiveBaseImages(ctx, maxBaseChecks)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("list base images", "err", err)
		}
		return
	}
	for _, b := range bases {
		if ctx.Err() != nil {
			return
		}
		if _, err := c.checkTag(ctx, srv, rt, b.OrgID, b.BaseImage); err != nil && ctx.Err() == nil {
			// A registry that won't answer leaves the last answer standing.
			c.log.Info("check a base image", "image", b.BaseImage, "err", err)
		}
	}
}

// checkTag asks the registry (from srv, with orgID's credentials) what
// tag points at now and records it on every scan whose base it is,
// telling their open pages. It returns those scans.
func (c *Core) checkTag(ctx context.Context, srv store.Server, rt node.Runtime, orgID int64, tag string) ([]store.ImageScan, error) {
	opts, err := c.pullOptionsFor(ctx, orgID, tag, srv)
	if err != nil {
		return nil, err
	}
	digest, err := rt.RemoteDigest(ctx, tag, opts)
	if err != nil {
		return nil, err
	}
	rows, err := c.store.SetBaseImageDigest(context.WithoutCancel(ctx), store.SetBaseImageDigestParams{
		Digest: digest, CheckedAt: ptr(now().Unix()), BaseImage: tag,
	})
	if err != nil {
		return nil, fmt.Errorf("record %s's digest: %w", tag, err)
	}
	for _, row := range rows {
		c.publishScan(ctx, row)
	}
	return rows, nil
}

// tagCheckFresh is how recent a tag's check must be for a new template
// database to start on what it saw: older (scanning off, an offline
// install) may be behind the template's pinned image.
const tagCheckFresh = 48 * time.Hour

// imageTag is ref without its digest when it names a tag
// ("postgres:17-alpine@sha256:..." is "postgres:17-alpine"), else "":
// "postgres@sha256:..." and "localhost:5000/app" follow no tag.
func imageTag(ref string) string {
	name, _, _ := strings.Cut(ref, "@")
	if strings.LastIndexByte(name, ':') <= strings.LastIndexByte(name, '/') {
		return ""
	}
	return name
}

// templateImage is the image a new database from t starts on: the
// template's tag at the digest its last daily check saw, when that is
// recent, else the template's pinned image. q may be a transaction's.
func (c *Core) templateImage(ctx context.Context, q *store.Queries, t dbTemplate) string {
	tag := imageTag(t.image)
	if tag == "" {
		return t.image
	}
	d, err := q.GetTagDigest(ctx, store.GetTagDigestParams{BaseImage: tag, CheckedAfter: ptr(now().Add(-tagCheckFresh).Unix())})
	if err != nil {
		if !store.IsNotFound(err) {
			c.log.Warn("read a template tag's digest", "image", tag, "err", err)
		}
		return t.image
	}
	return tag + "@" + d
}

// pullOptionsFor are the credentials a server uses for ref on behalf of
// an org: a token for the platform registry, else the org's credential
// for ref's registry (ADR 0051), else none.
func (c *Core) pullOptionsFor(ctx context.Context, orgID int64, ref string, srv store.Server) (node.PullOptions, error) {
	if repo, ok := c.inRegistry(ref); ok {
		return node.PullOptions{Token: c.reg.Token("server "+srv.Name, repo, false)}, nil
	}
	login, _, err := c.registryLogin(ctx, orgID, ref)
	return login, err
}
