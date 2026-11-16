package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Jobs in project files (ADR 0049, 0052). A file service's jobs are set
// at once when the file is applied, like `dock job set`: jobs aren't
// staged. A job of the service that the file leaves out is kept, and
// removed with --prune, like variables and domains. Jobs from the
// service's dock.toml (source repo) are the release's: a file never
// changes or removes them, and a file job of the same name is an error.

// jobOp is a job a project file sets, or (remove) removes, at once.
type jobOp struct {
	service, name string
	// req is checked, with its defaults filled in.
	req    apitypes.SetJobRequest
	remove bool
	// id is the removed job's.
	id int64
}

// planJobs compares a file service's jobs with the live ones. A service
// that doesn't exist yet (or whose creation is staged) can't have jobs:
// they are set once it is created, by --deploy or by applying the file
// again. running reports whether a job's run is in flight, which --prune
// doesn't remove.
func (ep *envPlan) planJobs(line func(*[]apitypes.PlanChange, apitypes.PlanChange), service string, fs apitypes.ServiceExport, ls *liveSvc, prune bool, running func(int64) bool) error {
	ch := func(action apitypes.ChangeAction, name, note string) apitypes.PlanChange {
		return apitypes.PlanChange{Service: service, Kind: apitypes.PlanJob, Action: action, Name: name, Note: note}
	}
	have := make(map[string]store.ServiceJob, len(ls.jobs))
	fromRepo := 0
	for _, j := range ls.jobs {
		have[j.Name] = j
		if apitypes.JobSource(j.Source) == apitypes.JobFromRepo {
			fromRepo++
		}
	}
	pending := ls.row.ID == 0 || ls.row.Staged == "create"
	inFile := make(map[string]bool, len(fs.Jobs))
	for _, fj := range slices.SortedFunc(slices.Values(fs.Jobs), func(x, y apitypes.JobExport) int { return cmp.Compare(x.Name, y.Name) }) {
		inFile[fj.Name] = true
		req := apitypes.SetJobRequest{
			Schedule: strings.TrimSpace(fj.Schedule), Timezone: fj.Timezone, Command: fj.Command,
			TimeoutSeconds: fj.TimeoutSeconds, Overlap: fj.Overlap, Enabled: fj.Enabled,
		}
		if err := checkJob(fj.Name, &req); err != nil {
			return invalid("job %s of service %s: %v", fj.Name, service, err)
		}
		enabled := req.Enabled == nil || *req.Enabled
		cur, ok := have[fj.Name]
		if ok && apitypes.JobSource(cur.Source) == apitypes.JobFromRepo {
			return invalid("job %s of service %s is in the project file and in %s's dock.toml; rename the project file's job or remove it from dock.toml", fj.Name, service, service)
		}
		c := ch(apitypes.ChangeAdd, fj.Name, "")
		c.New = jobText(req.Schedule, req.Timezone, req.Command, req.TimeoutSeconds, req.Overlap, enabled)
		switch {
		case pending:
			c.Note = "set once the service is created: with --deploy, or by applying the file again after `dock changes apply`"
			line(&ep.manual, c)
			continue
		case !ok:
			c.Note = "set at once; jobs aren't staged"
		default:
			c.Old = jobText(cur.Schedule, cur.Timezone, cur.Command, int(cur.TimeoutSeconds), apitypes.JobOverlap(cur.Overlap), cur.Enabled)
			if c.Old == c.New {
				continue
			}
			c.Action, c.Note = apitypes.ChangeUpdate, "set at once; jobs aren't staged"
		}
		line(&ep.changes, c)
		ep.jobs = append(ep.jobs, jobOp{service: service, name: fj.Name, req: req})
	}
	total := fromRepo + len(fs.Jobs)
	for _, j := range ls.jobs {
		if inFile[j.Name] || apitypes.JobSource(j.Source) == apitypes.JobFromRepo {
			continue
		}
		c := ch(apitypes.ChangeRemove, j.Name, "")
		c.Old = jobText(j.Schedule, j.Timezone, j.Command, int(j.TimeoutSeconds), apitypes.JobOverlap(j.Overlap), j.Enabled)
		switch {
		case prune && running(j.ID):
			c.Note = "running; apply the file again once its run ended"
			line(&ep.manual, c)
			total++
		case prune:
			c.Note = "removed at once, with its history"
			line(&ep.changes, c)
			ep.jobs = append(ep.jobs, jobOp{service: service, name: j.Name, remove: true, id: j.ID})
		default:
			c.Note = "not in the file; kept (--prune removes it)"
			line(&ep.kept, c)
			total++
		}
	}
	if total > apitypes.MaxJobsPerService {
		return invalid("service %s would have %d jobs, the most a service may have is %d; remove some from the file, or remove the others with --prune", service, total, apitypes.MaxJobsPerService)
	}
	return nil
}

// jobText is a job's settings as one plan value: `0 3 * * * UTC: cmd`,
// with its timeout, overlap and off when they aren't the defaults.
func jobText(schedule, tz, command string, timeout int, overlap apitypes.JobOverlap, enabled bool) string {
	s := schedule + " " + tz + ": " + cleanLine(command, 80)
	var extra []string
	if timeout != apitypes.DefaultRunTimeoutSeconds {
		extra = append(extra, fmt.Sprintf("timeout %ds", timeout))
	}
	if overlap != apitypes.OverlapSkip {
		extra = append(extra, "overlap "+string(overlap))
	}
	if !enabled {
		extra = append(extra, "disabled")
	}
	if len(extra) > 0 {
		s += " [" + strings.Join(extra, ", ") + "]"
	}
	return s
}

// fileJobsDone is what applying a file's jobs did, for after its
// transaction: the jobs set and removed, by service.
type fileJobsDone struct {
	set     []svcJob
	removed []svcJob
}

type svcJob struct {
	ss  svcScope
	row store.ServiceJob
}

// setFileJobs sets and removes a plan's jobs in the caller's transaction.
// svcs are the environment's live services by name.
func (c *Core) setFileJobs(ctx context.Context, q *store.Queries, a Actor, es envScope, svcs map[string]*liveSvc, ops []jobOp) (fileJobsDone, error) {
	var done fileJobsDone
	at := now()
	for _, op := range ops {
		ss := svcScope{es, svcs[op.service].row}
		if op.remove {
			if err := q.DeleteServiceJobRuns(ctx, &op.id); err != nil {
				return done, err
			}
			if err := q.DeleteServiceJob(ctx, op.id); err != nil {
				return done, err
			}
			done.removed = append(done.removed, svcJob{ss, store.ServiceJob{ID: op.id, Name: op.name}})
			continue
		}
		row, err := q.UpsertServiceJob(ctx, store.UpsertServiceJobParams{
			ServiceID: ss.svc.ID, Name: op.name, Schedule: op.req.Schedule, Timezone: op.req.Timezone, Command: op.req.Command,
			TimeoutSeconds: int64(op.req.TimeoutSeconds), Overlap: string(op.req.Overlap), Enabled: op.req.Enabled == nil || *op.req.Enabled,
			Source: string(apitypes.JobFromAPI), CreatedBy: a.Name, CreatedAt: at, UpdatedAt: at,
		})
		if err != nil {
			return done, err
		}
		done.set = append(done.set, svcJob{ss, row})
	}
	return done, nil
}

// finish publishes what setFileJobs did once its transaction committed,
// audits it as one entry per environment and wakes the scheduler.
func (d fileJobsDone) finish(ctx context.Context, c *Core, a Actor, es envScope) {
	if len(d.set)+len(d.removed) == 0 {
		return
	}
	var parts []string
	names := func(js []svcJob) string {
		out := make([]string, len(js))
		for i, j := range js {
			out[i] = j.ss.svc.Name + "/" + j.row.Name
		}
		return strings.Join(out, ", ")
	}
	for _, j := range d.set {
		_, _ = c.publishJob(ctx, j.ss, j.row)
	}
	if len(d.set) > 0 {
		parts = append(parts, "set "+names(d.set))
	}
	for _, j := range d.removed {
		c.resolveAlert(ctx, jobAlertKey(j.ss, j.row), now())
		c.publish(j.ss, apitypes.Event{Type: apitypes.EventJob, Job: &apitypes.Job{Name: j.row.Name}, Message: "removed"})
	}
	if len(d.removed) > 0 {
		parts = append(parts, "removed "+names(d.removed))
	}
	c.audit(ctx, a, "job.apply", &es.project, &es.env, es.String(), "project file jobs: "+strings.Join(parts, "; "))
	c.kickJobs()
}

// applyCreatedJobs sets the file's jobs of services a --deploy apply just
// created, which had none to set when it was staged. It replans only jobs,
// so jobs set before are unchanged and add no lines.
func (c *Core) applyCreatedJobs(ctx context.Context, a Actor, es envScope, fe apitypes.EnvironmentExport, prune bool) ([]apitypes.PlanChange, error) {
	var ep envPlan
	var done fileJobsDone
	err := c.store.Tx(ctx, func(q *store.Queries) error {
		le, err := loadLiveEnv(ctx, q, c, es.env)
		if err != nil {
			return err
		}
		line := func(to *[]apitypes.PlanChange, ch apitypes.PlanChange) {
			ch.Environment = fe.Name
			*to = append(*to, ch)
		}
		for _, fs := range slices.SortedFunc(slices.Values(fe.Services), func(x, y apitypes.ServiceExport) int { return cmp.Compare(x.Name, y.Name) }) {
			ls := le.svcs[fs.Name]
			if ls == nil || len(fs.Jobs) == 0 {
				continue
			}
			if err := ep.planJobs(line, fs.Name, fs, ls, prune, c.jobRunning); err != nil {
				return err
			}
		}
		done, err = c.setFileJobs(ctx, q, a, es, le.svcs, ep.jobs)
		return err
	})
	if err != nil {
		return nil, err
	}
	done.finish(ctx, c, a, es)
	return ep.changes, nil
}
