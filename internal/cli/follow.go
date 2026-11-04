package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Live progress. Commands that start releases (deploy, rollback, restart,
// service start, apply) open the /events stream *before* the request that
// starts them, so no step is missed, then print build output and deploy
// steps until every release is active or failed. The stream is the only source of progress;
// a slow refetch is the safety net for a dropped stream or missed events.

// refetchEvery is how often unfinished releases are fetched again while
// the event stream is healthy, in case an event was missed.
const refetchEvery = 15 * time.Second

// watcher is an open /events stream.
type watcher struct {
	g      *globals
	s      *session
	events <-chan apitypes.Event
	cancel context.CancelFunc
}

// watch opens the event stream for t (a project, environment or service).
func (g *globals) watch(ctx context.Context, s *session, t target) (*watcher, error) {
	q := url.Values{"project": {t.Project}}
	if t.Environment != "" {
		q.Set("environment", t.Environment)
	}
	if t.Service != "" {
		q.Set("service", t.Service)
	}
	ctx, cancel := context.WithCancel(ctx)
	r, err := s.api.Events(ctx, apiPrefix+"/events?"+q.Encode())
	if err != nil {
		cancel()
		return nil, err
	}
	events := make(chan apitypes.Event, 256)
	go func() {
		defer close(events)
		defer func() { _ = r.Close() }()
		_ = apiclient.Each(r, func(ev apitypes.Event) error {
			select {
			case events <- ev:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return &watcher{g: g, s: s, events: events, cancel: cancel}, nil
}

// close stops the stream. Safe on a nil watcher.
func (w *watcher) close() {
	if w != nil {
		w.cancel()
	}
}

// releaseKey identifies an open release among an apply's several services.
type releaseKey struct {
	service string
	number  int64
}

// releasePath is r's API path.
func releasePath(r apitypes.Release) string {
	return releaseTarget(r).servicePath() + "/releases/" + itoa(r.Number)
}

// follow prints the progress of rels until each is done, and returns them
// fetched once more at the end (with their events). Several releases (from
// an apply) get their lines prefixed with the service name.
func (w *watcher) follow(ctx context.Context, rels []apitypes.Release) ([]apitypes.Release, error) {
	io := w.g.io
	open := map[releaseKey]int{}
	for i, r := range rels {
		if !r.Status.Done() {
			open[releaseKey{r.Service, r.Number}] = i
		}
	}
	prefix := func(service string) string {
		if len(rels) > 1 {
			return service + ": "
		}
		return ""
	}
	refetch := func() {
		for k, i := range open {
			r, err := get[apitypes.Release](ctx, w.s, releasePath(rels[i]))
			if err != nil {
				continue // the next refetch or event will do
			}
			rels[i] = r
			if r.Status.Done() {
				delete(open, k)
			}
		}
	}

	tick := time.NewTicker(refetchEvery)
	defer tick.Stop()
	events := w.events
	for len(open) > 0 {
		select {
		case <-ctx.Done():
			return rels, ctx.Err()
		case <-tick.C:
			refetch()
		case ev, ok := <-events:
			if !ok {
				events = nil
				io.Warnf("Lost the live event stream; checking progress every few seconds.")
				tick.Reset(2 * time.Second)
				continue
			}
			switch ev.Type {
			case apitypes.EventProjectRenamed:
				if rename := ev.ProjectRenamed; rename != nil {
					for i := range rels {
						rels[i].Project = rename.Project.Name
					}
				}
			case apitypes.EventBuildLog:
				if _, ok := open[releaseKey{ev.Service, ev.BuildLog.ReleaseNumber}]; ok {
					io.Detail(prefix(ev.Service) + ev.BuildLog.Text)
				}
			case apitypes.EventReleaseEvent:
				if _, ok := open[releaseKey{ev.Service, ev.ReleaseEvent.ReleaseNumber}]; ok {
					io.Progressf("%s%s", prefix(ev.Service), ev.ReleaseEvent.Message)
				}
			case apitypes.EventRelease:
				k := releaseKey{ev.Service, ev.Release.Number}
				if i, ok := open[k]; ok {
					rels[i] = *ev.Release
					if ev.Release.Status.Done() {
						delete(open, k)
					}
				}
			case apitypes.EventLagged:
				io.Warnf("Fell behind the server and missed some progress output.")
				refetch()
			}
		}
	}
	for i := range rels {
		if r, err := get[apitypes.Release](ctx, w.s, releasePath(rels[i])); err == nil {
			rels[i] = r
		}
	}
	return rels, nil
}

// interrupted turns giving up on following (Ctrl-C) into a clear message:
// the server carries on regardless.
func interrupted(err error, rels []apitypes.Release) error {
	if !errors.Is(err, context.Canceled) || len(rels) == 0 {
		return err
	}
	hint := fmt.Sprintf("Follow it again with `dock release wait %d %s`.", rels[0].Number, releaseTarget(rels[0]).flags())
	if len(rels) > 1 {
		t := releaseTarget(rels[0])
		hint = fmt.Sprintf("Check on them with `dock status -a %s`.", target{Project: t.Project, Environment: t.Environment}.flags())
	}
	return &output.Error{
		Exit:    output.ExitError,
		Message: "stopped following; the deploy continues on the server",
		Hint:    hint,
	}
}

// releaseFailure explains a failed release and what to do next, or returns
// nil when r didn't fail.
func releaseFailure(ctx context.Context, s *session, r apitypes.Release) error {
	if r.Status != apitypes.ReleaseFailed {
		return nil
	}
	serving := "Nothing else is serving this service."
	t := releaseTarget(r)
	if svc, err := get[apitypes.Service](ctx, s, t.servicePath()); err == nil {
		switch {
		case svc.StoppedAt != nil:
			serving = "The service is still stopped; its domains answer 503."
		case svc.ActiveRelease != nil:
			serving = fmt.Sprintf("Release %d is still serving; traffic was not switched.", *svc.ActiveRelease)
		}
	}
	see := fmt.Sprintf("See what happened: `dock release show %d %s`.", r.Number, t.flags())
	if r.ErrorCode == apitypes.CodeBuildFailed {
		see = fmt.Sprintf("See the build log: `dock logs --build --release %d %s`.", r.Number, t.flags())
	}
	code := r.ErrorCode
	if code == "" {
		code = apitypes.CodeDeployFailed
	}
	return &output.Error{
		Exit:    output.ExitDeployFailed,
		Code:    string(code),
		Message: fmt.Sprintf("release %d of %s failed: %s", r.Number, r.Service, r.Error),
		Hint:    serving + " " + see,
	}
}

// deployInProgress gives a deploy_in_progress error a hint that waits for
// the deploy holding t: `dock release wait N -p -e -s`, with N when the
// message names the release ("release 12 of shop/production/web is already
// deploying"). Other errors are returned as they are.
func deployInProgress(err error, t target) error {
	e, ok := errors.AsType[*output.Error](err)
	if !ok || e.Code != string(apitypes.CodeDeployInProgress) {
		return err
	}
	wait := "dock release wait"
	if _, rest, ok := strings.Cut(e.Error(), "release "); ok {
		if num, _, ok := strings.Cut(rest, " of "); ok {
			if n, err := strconv.ParseInt(num, 10, 64); err == nil {
				wait += " " + itoa(n)
			}
		}
	}
	c := *e
	c.Hint = fmt.Sprintf("Wait for it with `%s %s`, then try again.", wait, t.flags())
	return &c
}

// startAndFollow runs start (which starts one release) and follows it
// unless detach is set. The service's events are watched before start.
func (g *globals) startAndFollow(ctx context.Context, s *session, t target, detach bool, start func() (apitypes.Release, error)) error {
	var w *watcher
	if !detach {
		var err error
		if w, err = g.watch(ctx, s, t); err != nil {
			return err
		}
		defer w.close()
	}
	rel, err := start()
	if err != nil {
		return deployInProgress(err, t)
	}
	if detach {
		return g.io.Print(rel, func() error {
			g.io.Successf("Release %d of %s queued (%s)", rel.Number, t, rel.Source)
			g.io.Progressf("Follow it with `dock release wait %d %s`.", rel.Number, releaseTarget(rel).flags())
			return nil
		})
	}
	rels, err := w.follow(ctx, []apitypes.Release{rel})
	if err != nil {
		return interrupted(err, rels)
	}
	return g.finishRelease(ctx, s, rels[0])
}

// finishRelease prints a finished release: its JSON, or where it is live.
// A failed release is a deploy_failed error (after its JSON, with --json).
// Its JSON carries the end of its logs; humans see the end of its output,
// since its build log was either streamed already or is named by the hint.
func (g *globals) finishRelease(ctx context.Context, s *session, r apitypes.Release) error {
	v := g.viewRelease(ctx, s, r, defaultLogLines)
	err := g.io.Print(v, func() error {
		switch r.Status {
		case apitypes.ReleaseActive:
			g.printLive(ctx, s, r)
		case apitypes.ReleaseSuperseded:
			g.io.Successf("Release %d of %s went live and has since been superseded", r.Number, r.Service)
		case apitypes.ReleaseFailed:
			if v.Logs != nil && len(v.Logs.Container) > 0 {
				g.io.Progressf("Last output of release %d:", r.Number)
				for _, l := range v.Logs.Container {
					g.io.Detail(l)
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return releaseFailure(ctx, s, r)
}

// printLive says that r is live and lists the URLs it serves.
func (g *globals) printLive(ctx context.Context, s *session, r apitypes.Release) {
	g.io.Successf("Release %d of %s is live", r.Number, r.Service)
	domains, err := get[[]apitypes.Domain](ctx, s, releaseTarget(r).servicePath()+"/domains")
	if err != nil {
		return // nice to have
	}
	for _, d := range domains {
		if d.RedirectTo == "" {
			_, _ = fmt.Fprintln(g.io.Out, "  "+d.URL)
		}
	}
}

func addDetachFlag(cmd *cobra.Command, detach *bool) {
	cmd.Flags().BoolVarP(detach, "detach", "d", false, "return once started instead of following progress")
}

func addDeployFlags(cmd *cobra.Command, deploy, detach *bool) {
	cmd.Flags().BoolVar(deploy, "deploy", false, "apply the environment's staged changes now (redeploys affected services)")
	addDetachFlag(cmd, detach)
}

func usageError(msg, hint string) error {
	return &output.Error{Exit: output.ExitUsage, Message: msg, Hint: hint}
}
