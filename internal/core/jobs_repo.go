package core

import (
	"context"
	"fmt"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// syncRepoJobs makes a newly active release's dock.toml jobs the
// service's file jobs (ADR 0054): added or changed to match the file,
// removed once the file no longer has them. A release whose source had
// no dock.toml (an image deploy) leaves them. A file job takes over a
// UI job of its name; a file job's on or off is kept, it's the UI's.
func (c *Core) syncRepoJobs(ctx context.Context, ss svcScope, cfg releaseConfig) {
	if cfg.Repo == nil || !cfg.Repo.JobsFromFile {
		return
	}
	changed, err := c.applyRepoJobs(ctx, ss, cfg.Repo.Jobs)
	if err != nil {
		c.log.Error("apply dock.toml jobs", "service", ss.String(), "err", err)
	}
	if changed {
		c.kickJobs() // a deploy that left the jobs as they were wakes nothing
	}
}

// applyRepoJobs makes the service's file jobs want and reports whether
// any changed.
func (c *Core) applyRepoJobs(ctx context.Context, ss svcScope, want []repoJob) (bool, error) {
	have, err := c.store.ListServiceJobs(ctx, ss.svc.ID)
	if err != nil {
		return false, err
	}
	byName := make(map[string]store.ServiceJob, len(have))
	for _, j := range have {
		byName[j.Name] = j
	}
	inFile := make(map[string]bool, len(want))
	var changed []string
	at := now()
	for _, j := range want {
		inFile[j.Name] = true
		req := apitypes.SetJobRequest{Schedule: j.Schedule, Timezone: j.Timezone, Command: j.Command, TimeoutSeconds: j.TimeoutSeconds, Overlap: apitypes.JobOverlap(j.Overlap)}
		if err := checkJob(j.Name, &req); err != nil {
			// dock.toml was checked when it was read; a timezone the host
			// lost since.
			c.log.Warn("dock.toml job skipped", "service", ss.String(), "job", j.Name, "err", err)
			continue
		}
		cur, ok := byName[j.Name]
		enabled := !ok || cur.Enabled
		if ok && apitypes.JobSource(cur.Source) == apitypes.JobFromRepo && cur.Schedule == req.Schedule && cur.Timezone == req.Timezone &&
			cur.Command == req.Command && int(cur.TimeoutSeconds) == req.TimeoutSeconds && cur.Overlap == string(req.Overlap) {
			continue // unchanged: its due times go on from where they were
		}
		row, err := c.store.UpsertServiceJob(ctx, store.UpsertServiceJobParams{
			ServiceID: ss.svc.ID, Name: j.Name, Schedule: req.Schedule, Timezone: req.Timezone, Command: req.Command,
			TimeoutSeconds: int64(req.TimeoutSeconds), Overlap: string(req.Overlap), Enabled: enabled, Source: string(apitypes.JobFromRepo),
			CreatedBy: "dock.toml", CreatedAt: at, UpdatedAt: at,
		})
		if err != nil {
			return false, err
		}
		changed = append(changed, j.Name)
		_, _ = c.publishJob(ctx, ss, row)
	}
	var removed []string
	for _, j := range have {
		if apitypes.JobSource(j.Source) != apitypes.JobFromRepo || inFile[j.Name] {
			continue
		}
		if err := c.deleteJob(ctx, j.ID); err != nil {
			return false, err
		}
		c.resolveAlert(ctx, jobAlertKey(ss, j), now())
		c.publish(ss, apitypes.Event{Type: apitypes.EventJob, Job: &apitypes.Job{Name: j.Name}, Message: "removed"})
		removed = append(removed, j.Name)
	}
	if len(changed)+len(removed) > 0 {
		var parts []string
		if len(changed) > 0 {
			parts = append(parts, "set "+strings.Join(changed, ", "))
		}
		if len(removed) > 0 {
			parts = append(parts, "removed "+strings.Join(removed, ", "))
		}
		c.audit(ctx, System.InOrg(ss.project.OrgID), "job.sync", &ss.project, &ss.env, ss.String(), fmt.Sprintf("dock.toml jobs: %s", strings.Join(parts, "; ")))
	}
	return len(changed)+len(removed) > 0, nil
}
