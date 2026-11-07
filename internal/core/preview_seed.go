package core

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Preview seeds (ADR 0056): a preview's stateful services start with new,
// empty volumes, unless the settings name them in seed_from_snapshot. A
// seeded service's volumes are filled from its base service's latest
// ready snapshot (ADR 0047) before its first deploy: the snapshot is
// copied into the preview's own new volumes, the base's are never
// mounted. The copy runs in the background, so the webhook answers at
// once; the service is promoted when it is done. Without a snapshot the
// service starts empty, and the preview's last event says so.

// previewSeeds checks the services previews seed from snapshots: each a
// live service of the base with a volume. Returns the JSON list stored in
// preview_settings.seed_from_snapshot, sorted and without duplicates.
func (c *Core) previewSeeds(ctx context.Context, base envScope, names []string) (string, []string, error) {
	out := make([]string, 0, len(names))
	if len(names) > 0 {
		svcs, err := c.store.ListServices(ctx, base.env.ID)
		if err != nil {
			return "", nil, err
		}
		vols, err := c.store.ListEnvironmentVolumes(ctx, base.env.ID)
		if err != nil {
			return "", nil, err
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.Name == name && s.ArchivedAt == nil })
			if i < 0 {
				return "", nil, invalid("%s has no service %q to seed previews from; name a service with a volume, e.g. db", base, name)
			}
			if !slices.ContainsFunc(vols, func(v store.Volume) bool { return v.ServiceID == svcs[i].ID && !v.Pending }) {
				return "", nil, invalid("%s has no volume, so there is nothing to seed previews with; seed a database or another service with a volume", svcScope{base, svcs[i]})
			}
			out = append(out, name)
		}
	}
	slices.Sort(out)
	out = slices.Compact(out)
	list, err := json.Marshal(out)
	return string(list), out, err
}

// seedLiterals says which base variables a preview copies for its seeded
// services: a template database's generated literals (user, password,
// database), which the copied data files only open with. Used by
// createPreview's clone rules next to copy_variables.
func seedLiterals(svcs []store.Service, set store.PreviewSetting) func(apitypes.VariableRef) bool {
	seeds := copyList(set.SeedFromSnapshot)
	keys := map[apitypes.VariableRef]bool{}
	for _, s := range svcs {
		t, ok := templateOf(s)
		if !ok || s.ArchivedAt != nil || !slices.Contains(seeds, s.Name) {
			continue
		}
		for k, v := range t.vars(s.Name, "") {
			if !strings.Contains(v, "${{") {
				keys[apitypes.VariableRef{Service: s.Name, Key: k}] = true
			}
		}
	}
	return func(v apitypes.VariableRef) bool { return keys[v] }
}

// seedPreview starts seeding a new preview's services named in set's
// seed_from_snapshot and returns promoted (the base services createPreview
// promotes now) without them: each seeded one is promoted on its own in
// the background, once its volumes are filled.
func (c *Core) seedPreview(a Actor, base, dst envScope, previewID int64, set store.PreviewSetting, svcs []store.Service, promoted []string) []string {
	seeds := copyList(set.SeedFromSnapshot)
	rest := make([]string, 0, len(promoted))
	for _, name := range promoted {
		i := slices.IndexFunc(svcs, func(s store.Service) bool { return s.Name == name && s.ArchivedAt == nil })
		if i < 0 || !slices.Contains(seeds, name) {
			rest = append(rest, name)
			continue
		}
		from := svcScope{base, svcs[i]}
		c.wg.Go(func() { c.seedService(a, from, dst, previewID) })
	}
	return rest
}

// seedService fills from's clone in the preview dst from from's latest
// snapshot, then promotes it. When there is nothing to copy or the copy
// fails, the clone is promoted with empty volumes; either way the
// preview's last event says which.
func (c *Core) seedService(a Actor, from svcScope, dst envScope, previewID int64) {
	ctx := c.ctx
	msg, err := c.fillPreviewSeed(ctx, from, dst)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		msg = fmt.Sprintf("%s starts empty: %s", from.svc.Name, errorLine(err))
	}
	c.audit(ctx, System, "preview.seed", &dst.project, &dst.env, dst.String()+"/"+from.svc.Name, msg)
	c.setPreviewEvent(previewID, msg)
	if _, err := c.promote(ctx, a, from.envScope, dst, []string{from.svc.Name}, true); err != nil {
		c.setPreviewEvent(previewID, fmt.Sprintf("%s wasn't deployed: %s", from.svc.Name, errorLine(err)))
	}
	c.refreshPreview(previewID)
}

// fillPreviewSeed copies from's newest ready snapshot on dst's server into
// the volumes of from's clone in dst, which nothing runs on yet. The base
// service is claimed meanwhile, so the snapshot can't be deleted or
// pruned under the copy.
func (c *Core) fillPreviewSeed(ctx context.Context, from svcScope, dst envScope) (string, error) {
	snap, err := c.store.GetLatestReadySnapshot(ctx, store.GetLatestReadySnapshotParams{ServiceID: from.svc.ID, ServerID: dst.env.ServerID})
	if store.IsNotFound(err) {
		return "", notFound("%s has no snapshot on this preview's server; take one (`dock volume snapshot`) and the next preview is seeded from it", from)
	}
	if err != nil {
		return "", err
	}
	svc, err := c.store.GetServiceByName(ctx, store.GetServiceByNameParams{EnvironmentID: dst.env.ID, Name: from.svc.Name})
	if err != nil {
		return "", err
	}
	to := svcScope{dst, svc}
	// The clone's volumes are staged until its first deploy applies them.
	vols, err := c.store.ListVolumes(ctx, svc.ID)
	if err != nil {
		return "", err
	}
	mounts, err := snapshotMounts(snap, vols, to.String())
	if err != nil {
		return "", err
	}
	rt, err := c.runtimeFor(dst.env.ServerID)
	if err != nil {
		return "", err
	}
	// A backup of the base may hold it a while, e.g. the one a new
	// stateful service's first release starts.
	if !c.claimWithin(ctx, from.svc.ID, snapshotBeforeWait) {
		return "", conflict("a backup, snapshot or restore of %s ran past %s; the next preview is seeded", from, snapshotBeforeWait)
	}
	defer c.releaseService(from.svc.ID)
	// Nothing deploys the clone while its volumes fill (volume_writers.go).
	if err := c.lockForRestore(ctx, to, volumeRestore{what: fmt.Sprintf("the seed from snapshot %d", snap.ID), watch: "`dock env previews`"}); err != nil {
		return "", err
	}
	defer c.unlockVolumes(to.svc.ID)
	for _, m := range mounts {
		if err := rt.EnsureVolume(ctx, m.Volume, c.labels(to, 0)); err != nil {
			return "", fmt.Errorf("create volume: %w", c.nodeErr(dst.env.ServerID, err))
		}
	}
	if err := c.copySnapshot(ctx, to, snap, mounts); err != nil {
		return "", fmt.Errorf("copy snapshot %d: %w", snap.ID, err)
	}
	return fmt.Sprintf("%s seeded from snapshot %d of %s", from.svc.Name, snap.ID, from), nil
}
