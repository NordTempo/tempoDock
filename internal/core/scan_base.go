package core

import (
	"context"
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
