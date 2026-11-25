package core

import (
	"context"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Base image hints (ADR 0074, phase 3). A Dockerfile build reports the
// image its final stage was built on and the digest that resolved to
// (node.Built); the release keeps both and its image's scan takes them.
// Once a day, after a builder refreshed its database, the scanner asks
// each live base image's registry what its tag points at now, one
// manifest request per base image whatever the number of images built on
// it. When the tag moved, a rebuild picks up the newer base image, which
// is what the image's page says next to the system packages' fixes.
//
// An image deployed by tag (postgres:17-alpine@sha256:..., as the
// database templates are) is its own base: the same check follows its
// tag, and when it moved the service offers to update to the newer
// image, deployed by digest. A new template database starts on the
// newest digest the check has seen (templateImage).

// maxBaseChecks bounds the base images one daily check asks about.
const maxBaseChecks = 200

// checkBaseImages asks rt's registries which digest each live base
// image's tag points at now and records whether the images built on it
// are behind. It runs inside a scan step: a build or deploy cancels it,
// and the next day's check asks again.
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
		opts, err := c.pullOptionsFor(ctx, b.OrgID, b.BaseImage, srv)
		if err != nil {
			continue
		}
		digest, err := rt.RemoteDigest(ctx, b.BaseImage, opts)
		if err != nil {
			// A registry that won't answer leaves the last answer standing.
			if ctx.Err() == nil {
				c.log.Info("check a base image", "image", b.BaseImage, "err", err)
			}
			continue
		}
		if err := c.store.SetBaseImageDigest(context.WithoutCancel(ctx), store.SetBaseImageDigestParams{
			Digest: digest, CheckedAt: ptr(time.Now().Unix()), BaseImage: b.BaseImage,
		}); err != nil {
			c.log.Error("record a base image's digest", "image", b.BaseImage, "err", err)
		}
	}
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
