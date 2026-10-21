package cli

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/apiclient"
	"github.com/NordTempo/tempoDock/internal/cli/output"
)

// Live progress. Commands that start releases (deploy, rollback, restart,
// apply) open the /events stream *before* the request that starts them, so
// no step is missed, then print build output and deploy steps until every
// release is active or failed. The stream is the only source of progress;
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

type releaseKey struct {
	service string
	number  int64
}

func releasePath(r apitypes.Release) string {
	return target{r.Project, r.Environment, r.Service}.servicePath() + "/releases/" + itoa(r.Number)
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
	return &output.Error{
		Exit:    output.ExitError,
		Message: "stopped following; the deploy continues on the server",
		Hint:    fmt.Sprintf("Check on it with `dock release show %d -s %s`.", rels[0].Number, rels[0].Service),
	}
}

// releaseFailure explains a failed release and what to do next, or returns
// nil when r didn't fail.
func releaseFailure(ctx context.Context, s *session, r apitypes.Release) error {
	if r.Status != apitypes.ReleaseFailed {
		return nil
	}
	serving := "Nothing else is serving this service."
	t := target{r.Project, r.Environment, r.Service}
	if svc, err := get[apitypes.Service](ctx, s, t.servicePath()); err == nil && svc.ActiveRelease != nil {
		serving = fmt.Sprintf("Release %d is still serving; traffic was not switched.", *svc.ActiveRelease)
	}
	see := fmt.Sprintf("See what happened: `dock release show %d`.", r.Number)
	if r.ErrorCode == apitypes.CodeBuildFailed {
		see = fmt.Sprintf("See the build log: `dock logs --build --release %d`.", r.Number)
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
		return err
	}
	if detach {
		return g.io.Print(rel, func() error {
			g.io.Successf("Release %d of %s queued (%s)", rel.Number, t, rel.Source)
			g.io.Progressf("Follow it with `dock release show %d`.", rel.Number)
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
func (g *globals) finishRelease(ctx context.Context, s *session, r apitypes.Release) error {
	err := g.io.Print(r, func() error {
		if r.Status != apitypes.ReleaseActive {
			return nil
		}
		g.io.Successf("Release %d of %s is live", r.Number, r.Service)
		domains, err := get[[]apitypes.Domain](ctx, s, target{r.Project, r.Environment, r.Service}.servicePath()+"/domains")
		if err != nil {
			return nil // nice to have
		}
		for _, d := range domains {
			if d.RedirectTo == "" {
				_, _ = fmt.Fprintln(g.io.Out, "  https://"+d.Hostname)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return releaseFailure(ctx, s, r)
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
