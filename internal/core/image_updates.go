package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Image updates (ADR 0074, amendment 2026-11-26). A service deployed by
// tag (postgres:17-alpine@sha256:...) follows the tag: the daily check
// (checkTags) records when it points at a newer image, and the service
// offers Update. With auto_update on, the check updates it itself: the
// service's volumes are snapshotted first, then the newer image deploys
// by digest like any image deploy, so a release that doesn't come up
// leaves the previous one serving (a stateful service's is started
// again). The tag never changes, so 17-alpine stays on Postgres 17.

// tagCheckAgain is how soon CheckImageTag asks a registry again for the
// same tag: a button pressed twice asks once.
const tagCheckAgain = time.Minute

// tagCheckTimeout bounds CheckImageTag's registry request.
const tagCheckTimeout = 30 * time.Second

// autoUpdateActor is who an automatic image update's release and audit
// events name.
const autoUpdateActor = "image updates"

// CheckImageTag asks the registry now what the tag the service's live
// image follows points at, instead of waiting for the daily check: the
// image's own tag when it was deployed by tag, else the base image a
// Dockerfile build was built on. The answer is recorded for every image
// on that tag. Scope: deploy.
func (c *Core) CheckImageTag(ctx context.Context, a Actor, ref ServiceRef) (apitypes.ImageScan, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "checking for a newer image")
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	r, err := c.scanRelease(ctx, ss, 0)
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	if r.ImageScanID == nil {
		return apitypes.ImageScan{}, conflict("%s's live image isn't scanned yet, so Dock doesn't know which tag it follows; check again once it is (`dock vulns %s`)", ss, ss.svc.Name)
	}
	row, err := c.store.GetImageScan(ctx, *r.ImageScanID)
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	use := apitypes.ImageUse{Project: ss.project.Name, Environment: ss.env.Name, Service: ss.svc.Name, Release: r.Number}
	if row.BaseImage == "" || row.BaseDigest == "" {
		return apitypes.ImageScan{}, invalid("%s's image follows no tag: it was deployed by digest, or built without a base image Dock knows; deploy it by tag (--image name:tag) to have it followed", ss)
	}
	if row.BaseCheckedAt != nil && now().Sub(time.Unix(*row.BaseCheckedAt, 0)) < tagCheckAgain {
		return toImageScan(row, c.imageName(r.ImageRef, use)), nil
	}
	srv, err := c.store.GetServer(ctx, ss.env.ServerID)
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	rt, err := c.runtimeFor(srv.ID)
	if err != nil {
		return apitypes.ImageScan{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, tagCheckTimeout)
	defer cancel()
	rows, err := c.checkTag(cctx, srv, rt, ss.project.OrgID, row.BaseImage)
	if err != nil {
		if CodeOf(err) == apitypes.CodeServerOffline {
			return apitypes.ImageScan{}, err
		}
		return apitypes.ImageScan{}, invalid("Dock couldn't ask the registry about %s: %v; check the org's credentials for its registry, or wait for the daily check", row.BaseImage, c.nodeErr(srv.ID, err))
	}
	for _, s := range rows {
		if s.ID == row.ID {
			row = s
		}
	}
	return toImageScan(row, c.imageName(r.ImageRef, use)), nil
}

// SetAutoUpdate turns a service's automatic image updates on or off.
// Only a service whose live image follows a tag can turn them on. It
// applies at once: never staged, never a release. Scope: deploy.
func (c *Core) SetAutoUpdate(ctx context.Context, a Actor, ref ServiceRef, req apitypes.AutoUpdateRequest) (apitypes.Service, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "changing image updates")
	if err != nil {
		return apitypes.Service{}, err
	}
	if req.AutoUpdate {
		// What the live release runs decides, whatever made it: a restart
		// or a rollback of an image deployed by tag still follows it.
		r, err := c.store.GetActiveRelease(ctx, ss.svc.ID)
		switch {
		case store.IsNotFound(err):
			return apitypes.Service{}, invalid("%s has no live release; deploy an image by tag first, then turn image updates on", ss)
		case err != nil:
			return apitypes.Service{}, err
		case c.dockNamed(r.ImageRef):
			return apitypes.Service{}, invalid("Dock builds %s's image, so there is no tag to follow; deploying it again builds on the newest base image", ss)
		case imageTag(r.ImageRef) == "":
			return apitypes.Service{}, invalid("%s's live image %s names no tag to follow; deploy it by tag (like postgres:17-alpine), then turn image updates on", ss, r.ImageRef)
		}
	}
	if err := c.store.SetServiceAutoUpdate(ctx, store.SetServiceAutoUpdateParams{AutoUpdate: boolInt(req.AutoUpdate), ID: ss.svc.ID}); err != nil {
		return apitypes.Service{}, err
	}
	summary := "image updates are manual"
	if req.AutoUpdate {
		summary = "image updates are automatic"
	}
	c.audit(ctx, a, "service.auto_update", &ss.project, &ss.env, ss.String(), summary)
	return c.GetService(ctx, a, ref)
}

// autoUpdateImages updates every service with auto_update on whose tag
// points at a newer image than it runs, one after another: a snapshot of
// its volumes, then a deploy of the newer image by digest. It runs at the
// end of the daily check (checkTags). A service is left as it is
// when its environment is frozen (locked for people too), while it
// deploys or has staged changes, when its last release already tried
// this image and failed, or when the snapshot fails. It starts the
// deploys and returns; each snapshot is waited for.
func (c *Core) autoUpdateImages(ctx context.Context) {
	rows, err := c.store.ListImageAutoUpdates(ctx)
	if err != nil {
		c.log.Error("list automatic image updates", "err", err)
		return
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		// A scan is kept by digest, so it may follow another tag of the
		// same image (postgres:alpine for postgres:17-alpine): only the
		// service's own tag counts.
		if imageTag(row.ImageRef) != row.BaseImage {
			continue
		}
		c.safely("updating a service's image", func() {
			c.autoUpdate(ctx, row.ServiceID, row.BaseImage+"@"+row.BaseLatest)
		})
	}
}

// autoUpdate updates one service to image (tag@digest), as
// autoUpdateImages says.
func (c *Core) autoUpdate(ctx context.Context, serviceID int64, image string) {
	ss, err := c.scopeOf(ctx, serviceID)
	if err != nil {
		c.log.Warn("load a service to update its image", "service", serviceID, "err", err)
		return
	}
	// Decided from the service as it is now: it may have been turned off,
	// archived or stopped since the list was read.
	if ss.svc.AutoUpdate == 0 || ss.svc.ArchivedAt != nil || ss.svc.StoppedAt != nil {
		return
	}
	if lockHolds(ss.env, now()) && ss.env.LockPeople {
		return // frozen: nothing changes there, not even on its own
	}
	if last, err := c.store.ListReleases(ctx, store.ListReleasesParams{ServiceID: ss.svc.ID, Limit: 1}); err == nil && len(last) == 1 {
		switch l := last[0]; {
		case !apitypes.ReleaseStatus(l.Status).Done():
			return // deploying: the next check looks again
		case l.Status == string(apitypes.ReleaseFailed) && l.ImageRef == image:
			return // tried and failed: Update by hand, or a newer image, tries again
		}
	}
	// An image deploy takes the service's staged changes live with it;
	// nobody asked for that here, so it waits until they are applied.
	if pc, err := c.pendingChanges(ctx, c.store.Queries, ss.envScope, false); err != nil || pc.CountFor(ss.svc.Name) > 0 {
		return
	}
	actor := Actor{Kind: "system", Name: autoUpdateActor, Scope: apitypes.ScopeDeploy}.InOrg(ss.project.OrgID)
	short := shortImage(image)
	if _, err := c.SnapshotBefore(ctx, actor, ss, apitypes.SnapshotBeforeDeploy, "before the automatic update to "+short); err != nil {
		c.audit(ctx, actor, "service.auto_update_skipped", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("didn't update to %s: the snapshot before it failed: %s", short, c.errorLine(err)))
		return
	}
	// The snapshot may have waited on a backup: someone may have deployed
	// another tag meanwhile, which this must not take back.
	if live, err := c.store.GetActiveRelease(ctx, ss.svc.ID); err != nil || imageTag(live.ImageRef) != imageTag(image) || live.ImageRef == image {
		return
	}
	rel, err := c.startRelease(ctx, actor, ss, releaseSpec{source: apitypes.SourceImage, imageRef: image})
	if err != nil {
		c.audit(ctx, actor, "service.auto_update_skipped", &ss.project, &ss.env, ss.String(),
			fmt.Sprintf("didn't update to %s: %s", short, c.errorLine(err)))
		return
	}
	c.audit(ctx, actor, "release.deploy", &ss.project, &ss.env, ss.String(),
		fmt.Sprintf("release %d from image %s, an automatic image update", rel.Number, rel.ImageRef))
}

// shortImage is ref with its digest cut to 7 characters, as the UI shows
// it: postgres:17-alpine@f9cf3e1.
func shortImage(ref string) string {
	name, digest, ok := strings.Cut(ref, "@sha256:")
	if !ok {
		return ref
	}
	return name + "@" + digest[:min(7, len(digest))]
}
