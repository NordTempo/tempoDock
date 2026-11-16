package core

import (
	"context"
	"fmt"
	"sync"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/store"
)

// deployRuns holds the deploys running on this control plane by release
// ID, so CancelRelease can stop one (ADR 0086). launch tracks a release
// before its goroutine starts and deploy forgets it once the outcome is
// recorded. Only in memory: a deploy in flight across a restart is failed
// by recovery.
type deployRuns struct {
	mu   sync.Mutex
	runs map[int64]*deployRun
}

// deployRun is one running deploy: its context's cancel func, where it
// stands, and who canceled it.
type deployRun struct {
	cancel context.CancelFunc
	// done closes once the release's outcome is recorded.
	done chan struct{}

	mu    sync.Mutex
	state deployRunState
	by    string
}

// deployRunState is where a deploy stands as far as canceling goes.
type deployRunState int

const (
	// runRunning: before the cutover; a cancel stops it.
	runRunning deployRunState = iota
	// runCanceled: a cancel was asked; the deploy fails as canceled.
	runCanceled
	// runSwitching: the cutover began; too late to cancel.
	runSwitching
	// runFinished: its outcome is being recorded.
	runFinished
)

// canceledError ends a deploy that was canceled (ADR 0086): by someone
// (CancelRelease), or a promotion step whose earlier step was. Its
// release is recorded with CodeDeployCanceled.
type canceledError struct{ msg string }

func (e *canceledError) Error() string { return e.msg }

// track registers a release's deploy and returns its context, derived
// from the daemon's.
func (d *deployRuns) track(parent context.Context, releaseID int64) (context.Context, *deployRun) {
	ctx, cancel := context.WithCancel(parent)
	run := &deployRun{cancel: cancel, done: make(chan struct{})}
	d.mu.Lock()
	if d.runs == nil {
		d.runs = map[int64]*deployRun{}
	}
	d.runs[releaseID] = run
	d.mu.Unlock()
	return ctx, run
}

// forget removes a finished deploy and wakes whoever waits for it.
func (d *deployRuns) forget(releaseID int64, run *deployRun) {
	d.mu.Lock()
	if d.runs[releaseID] == run {
		delete(d.runs, releaseID)
	}
	d.mu.Unlock()
	run.cancel()
	close(run.done)
}

// get is a release's running deploy, or nil.
func (d *deployRuns) get(releaseID int64) *deployRun {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runs[releaseID]
}

// switching is asked right before the cutover: false if the deploy was
// canceled, else no cancel is taken from now on. This is what makes a
// cancel racing the switch-over safe: either the cancel wins and traffic
// never moves, or the cutover wins and the cancel is refused.
func (r *deployRun) switching() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == runCanceled {
		return false
	}
	r.state = runSwitching
	return true
}

// finish ends the deploy's cancelable life and says whether, and by
// whom, it was canceled.
func (r *deployRun) finish() (by string, canceled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	canceled = r.state == runCanceled
	r.state = runFinished
	return r.by, canceled
}

// stop cancels the deploy for by, or says why it can't be.
func (r *deployRun) stop(by string, ss svcScope, number int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch r.state {
	case runRunning:
		r.state, r.by = runCanceled, by
		r.cancel()
	case runSwitching:
		return conflict("release %d of %s is already switching traffic, too late to cancel; once it is live, roll back with `dock rollback -s %s -e %s` if needed", number, ss, ss.svc.Name, ss.env.Name)
	case runFinished:
		return conflict("release %d of %s already finished; nothing to cancel", number, ss)
	}
	return nil // runCanceled: canceled already
}

// CancelRelease stops a release of a service that is deploying (ADR
// 0086): queued, building, pushing, pulling, running its pre-deploy
// command, starting or health checking, anything before the cutover.
// What it started is removed, the release reads as canceled, the release
// that served keeps serving (a stateful service's is started again), and
// the changes it carried are staged again. Number 0 is the release that
// is deploying. It returns once the release is canceled, or with the
// release as it is when ctx ends first.
func (c *Core) CancelRelease(ctx context.Context, a Actor, ref ServiceRef, number int64) (apitypes.Release, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeDeploy, "canceling a deploy")
	if err != nil {
		return apitypes.Release{}, err
	}
	var rel store.Release
	if number == 0 {
		if rel, err = c.store.GetInFlightRelease(ctx, ss.svc.ID); store.IsNotFound(err) {
			return apitypes.Release{}, conflict("no deploy of %s is running; nothing to cancel", ss)
		}
		number = rel.Number
	} else {
		rel, err = c.releaseByNumber(ctx, ss, number)
	}
	if err != nil {
		return apitypes.Release{}, err
	}
	if s := releaseStatus(rel); s.Done() {
		hint := ""
		if s == apitypes.ReleaseActive {
			hint = fmt.Sprintf("; to go back, roll back with `dock rollback -s %s -e %s`", ss.svc.Name, ss.env.Name)
		}
		return apitypes.Release{}, conflict("release %d of %s already finished (%s); nothing to cancel%s", number, ss, s, hint)
	}
	run := c.deploys.get(rel.ID)
	if run == nil {
		// In flight in the database but not running here: queued this
		// instant, or the daemon just started and recovery fails it.
		return apitypes.Release{}, conflict("release %d of %s is just starting, or ending after a daemon restart; try again in a moment", number, ss)
	}
	if err := run.stop(a.Name, ss, number); err != nil {
		return apitypes.Release{}, err
	}
	c.audit(ctx, a, "release.cancel", &ss.project, &ss.env, ss.String(), fmt.Sprintf("release %d canceled while %s", number, rel.Status))
	select {
	case <-run.done:
	case <-ctx.Done():
	}
	if rel, err = c.store.GetRelease(context.WithoutCancel(ctx), rel.ID); err != nil {
		return apitypes.Release{}, err
	}
	return c.toRelease(context.WithoutCancel(ctx), ss, rel, false)
}

// releaseStatus is a release's status as the API reports it: a canceled
// one is stored as failed with CodeDeployCanceled (ADR 0086).
func releaseStatus(r store.Release) apitypes.ReleaseStatus {
	if r.ErrorCode == string(apitypes.CodeDeployCanceled) {
		return apitypes.ReleaseCanceled
	}
	return apitypes.ReleaseStatus(r.Status)
}
