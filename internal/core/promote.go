package core

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// promoteStep is one service of a promotion. Its release waits for the
// one before it and fails without starting when that one failed, so
// services deploy one at a time and stop at the first failure (ADR 0014
// section 4).
type promoteStep struct {
	service string
	// from is the source environment's active release; imageRef and
	// imageID are the image the new release runs.
	from              store.Release
	imageRef, imageID string
	prev              *promoteStep
	// done closes when the release finished; ok is set before.
	done chan struct{}
	ok   bool
}

// failed names the service whose failure stopped a promotion at p (p or
// one before it).
func (p *promoteStep) failed() string {
	if p.prev != nil && !p.prev.ok {
		return p.prev.failed()
	}
	return p.service
}

// Promote starts promoting the images running in req.From to req.To in
// the same project (ADR 0014 section 4): per service, a release in To with
// the same image (by registry digest) and To's own variables and settings,
// deployed through the normal health-checked path, one at a time in name
// order. Nothing is rebuilt. Services missing in To, without an active
// release in From, whose image can't reach To's server, or already
// deploying in To are skipped and listed. A release that can't be created
// stops the promotion: once earlier ones started, it and the services
// after it are skipped as failed; before that, its error is returned.
// Requires deploy on To and read on From; To's server must be online.
func (c *Core) Promote(ctx context.Context, a Actor, project string, req apitypes.PromoteRequest) (apitypes.PromoteResult, error) {
	out := apitypes.PromoteResult{From: req.From, To: req.To, Promoted: []apitypes.PromotedService{}, Skipped: []apitypes.SkippedService{}}
	if req.From == req.To {
		return out, invalid("promote copies releases between two environments; from and to are both %s", req.From)
	}
	src, err := c.envFor(ctx, a, EnvRef{project, req.From}, apitypes.ScopeRead, "promoting")
	if err != nil {
		return out, err
	}
	dst, err := c.envFor(ctx, a, EnvRef{project, req.To}, apitypes.ScopeDeploy, "promoting")
	if err != nil {
		return out, err
	}
	return c.promote(ctx, a, src, dst, req.Services, false)
}

// promote promotes from src to dst once the actor's access is checked
// (Promote, and CloneEnvironment with deploy). names limits it to those
// services (empty: all). applyStaged makes each release take its
// service's own staged changes live with it, as an image deploy does: a
// clone's volumes stay staged until its services first deploy.
func (c *Core) promote(ctx context.Context, a Actor, src, dst envScope, names []string, applyStaged bool) (apitypes.PromoteResult, error) {
	out := apitypes.PromoteResult{From: src.env.Name, To: dst.env.Name, Promoted: []apitypes.PromotedService{}, Skipped: []apitypes.SkippedService{}}
	if _, err := c.runtimeFor(dst.env.ServerID); err != nil {
		return out, err
	}
	srcSvcs, err := c.store.ListServices(ctx, src.env.ID)
	if err != nil {
		return out, err
	}
	dstSvcs, err := c.store.ListServices(ctx, dst.env.ID)
	if err != nil {
		return out, err
	}
	actives, err := c.store.ListPromotableReleases(ctx, src.env.ID)
	if err != nil {
		return out, err
	}
	live := func(svcs []store.Service, name string) (store.Service, bool) {
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.Name == name && s.ArchivedAt == nil })
		if i < 0 {
			return store.Service{}, false
		}
		return svcs[i], true
	}
	for _, name := range names {
		if _, ok := live(srcSvcs, name); !ok {
			return out, notFound("service %s not found in %s", name, src)
		}
	}
	if len(names) == 0 {
		for _, s := range srcSvcs {
			if s.ArchivedAt == nil {
				names = append(names, s.Name)
			}
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)

	skip := func(name string, reason apitypes.SkipReason, format string, args ...any) {
		out.Skipped = append(out.Skipped, apitypes.SkippedService{Service: name, Reason: reason, Message: fmt.Sprintf(format, args...)})
	}
	sameServer := src.env.ServerID == dst.env.ServerID
	var prev *promoteStep
	// failed is an error reading back a release that started; it is
	// returned once the promotion is audited.
	var failed error
	for n, name := range names {
		i := slices.IndexFunc(actives, func(r store.ListPromotableReleasesRow) bool { return r.ServiceName == name })
		if i < 0 {
			skip(name, apitypes.SkipNoActiveRelease, "%s has no active release in %s", name, src.env.Name)
			continue
		}
		from := actives[i].Release
		target, ok := live(dstSvcs, name)
		if !ok {
			skip(name, apitypes.SkipMissingInTarget, "%s doesn't exist in %s; promote never creates services, so create it there first", name, dst.env.Name)
			continue
		}
		ref, id, ok := c.promotedImage(from, sameServer)
		if !ok {
			skip(name, apitypes.SkipNoDigest, "release %d of %s in %s isn't in the registry, and %s runs on another server; deploy it to %s again with the registry enabled, then promote",
				from.Number, name, src.env.Name, dst.env.Name, src.env.Name)
			continue
		}
		step := &promoteStep{service: name, from: from, imageRef: ref, imageID: id, prev: prev, done: make(chan struct{})}
		ss := svcScope{dst, target}
		rel, err := c.startRelease(ctx, a, ss, releaseSpec{source: apitypes.SourcePromote, promote: step, applyStaged: applyStaged})
		if CodeOf(err) == apitypes.CodeDeployInProgress {
			skip(name, apitypes.SkipDeployInProgress, "%s is deploying in %s already; promote it when that finished", name, dst.env.Name)
			continue
		}
		if err != nil && prev == nil {
			return out, err // nothing started
		}
		if err != nil {
			// Earlier releases are deploying: stop here, as a failed
			// release would stop the ones after it.
			skip(name, apitypes.SkipFailed, "%s couldn't start in %s: %v", name, dst.env.Name, err)
			for _, rest := range names[n+1:] {
				skip(rest, apitypes.SkipFailed, "not promoted: %s failed first", name)
			}
			break
		}
		prev = step
		c.releaseEvent(ctx, ss, rel, fmt.Sprintf("Promoting release %d of %s (%s)", from.Number, src.env.Name, ref))
		r, err := c.toRelease(ctx, ss, rel, false)
		if err != nil {
			failed = err
			break
		}
		out.Promoted = append(out.Promoted, apitypes.PromotedService{Service: name, FromRelease: from.Number, Release: r})
	}
	c.audit(ctx, a, "release.promote", &dst.project, &dst.env, dst.String(), promoteSummary(out))
	return out, failed
}

// promotedImage is the image a promoted release runs: the source
// release's by registry digest; a CI image's reference as it is; or, on
// the same server, the image that is already there. ok is false when the
// image can't reach the target's server.
func (c *Core) promotedImage(from store.Release, sameServer bool) (ref, id string, ok bool) {
	switch {
	case from.ImageDigest != "" && c.reg != nil:
		return c.registryRef(from.ImageRef, from.ImageDigest), "", true
	case sameServer:
		return from.ImageRef, from.ImageID, true
	case !c.ownImage(from.ImageRef):
		return from.ImageRef, "", true // pulled from its registry
	}
	return "", "", false
}

// promoteSummary is a promotion's audit line.
func promoteSummary(r apitypes.PromoteResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "promoted from %s:", r.From)
	if len(r.Promoted) == 0 {
		b.WriteString(" nothing")
	}
	for i, p := range r.Promoted {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, " %s (release %d as %d)", p.Service, p.FromRelease, p.Release.Number)
	}
	for _, s := range r.Skipped {
		fmt.Fprintf(&b, "; skipped %s (%s)", s.Service, s.Reason)
	}
	return b.String()
}
