package mcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/mcpkit"
)

// Waiting for releases. Tools that start releases (deploy, redeploy, start,
// rollback, promote, apply) wait until each is active or failed, up to the
// caller's wait_seconds, and return the final state; wait_for_release
// does the same for a release already running. They follow the event bus
// like `dock deploy` follows /events: nothing polls.

const (
	defaultWait = 300 * time.Second
	maxWait     = 900 * time.Second
	// failureLogLines is how many container and build log lines explain a
	// failed release.
	failureLogLines = 40
)

// WaitInput is the wait_seconds argument of tools that start releases.
type WaitInput struct {
	WaitSeconds *int `json:"wait_seconds,omitempty" doc:"How long to wait for the releases to finish (active or failed), in seconds. Default 300, at most 900. 0 returns as soon as they are queued."`
}

func (w WaitInput) duration() (time.Duration, error) {
	if w.WaitSeconds == nil {
		return defaultWait, nil
	}
	d := time.Duration(*w.WaitSeconds) * time.Second
	if d < 0 || d > maxWait {
		return 0, mcpkit.Invalid("wait_seconds must be between 0 and %d", int(maxWait.Seconds()))
	}
	return d, nil
}

// errAllDone ends the event stream once every release is done.
var errAllDone = errors.New("all releases done")

type releaseKey struct {
	service string
	number  int64
}

// waitReleases waits until every release in rels is done or wait passes,
// sending each deploy step as a progress notification. rels must be in
// one environment. It returns the releases as they are then, with their
// progress lines, and failed ones with the logs that explain them.
func (h *handler) waitReleases(ctx context.Context, c *call, rels []apitypes.Release, wait time.Duration) ([]releaseView, error) {
	if len(rels) == 0 {
		return []releaseView{}, nil
	}
	env := core.EnvRef{Project: rels[0].Project, Environment: rels[0].Environment}
	pending := map[releaseKey]bool{}
	for _, r := range rels {
		if !r.Status.Done() {
			pending[releaseKey{r.Service, r.Number}] = true
		}
	}
	if len(pending) > 0 && wait > 0 {
		if err := h.follow(ctx, c, env, pending, wait); err != nil {
			return nil, err
		}
	}
	out := make([]releaseView, 0, len(rels))
	for _, r := range rels {
		v, err := h.releaseDetails(ctx, c.Actor, core.ServiceRef{Project: env.Project, Environment: env.Environment, Service: r.Service}, r.Number, failureLogLines)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// follow subscribes to the environment's events and returns when no
// release in pending is left unfinished, or wait passes. After
// subscribing it reads each release once, so a release that finished
// before the subscription isn't waited for in vain, and the steps a
// deploy took before it are sent as progress too. Steps are sent once
// each, by their sequence number.
func (h *handler) follow(ctx context.Context, c *call, env core.EnvRef, pending map[releaseKey]bool, wait time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	stream, err := h.core.Events(ctx, c.Actor, core.EventFilter{Project: env.Project, Environment: env.Environment})
	if err != nil {
		return err
	}
	var readErr error
	sent := map[releaseKey]int64{} // the last step sent per release
	step := func(k releaseKey, ev apitypes.ReleaseEvent) {
		if ev.Seq > sent[k] {
			sent[k] = ev.Seq
			c.Notify(fmt.Sprintf("%s #%d: %s", k.service, k.number, ev.Message))
		}
	}
	// refresh reads every pending release, sends its steps not sent yet
	// and drops the finished ones.
	refresh := func() bool {
		for k := range pending {
			r, err := h.core.GetRelease(ctx, c.Actor, core.ServiceRef{Project: env.Project, Environment: env.Environment, Service: k.service}, k.number)
			if err != nil {
				if ctx.Err() == nil {
					readErr = err
				}
				return false
			}
			for _, ev := range r.Events {
				step(k, ev)
			}
			if r.Status.Done() {
				delete(pending, k)
			}
		}
		return len(pending) == 0
	}
	err = stream(ctx, func() {
		if refresh() || readErr != nil {
			cancel()
		}
	}, func(evs ...core.LiveEvent) error {
		for _, ev := range evs {
			switch {
			case ev.Type == apitypes.EventLagged:
				if refresh() {
					return errAllDone
				}
				if readErr != nil {
					return readErr
				}
			case ev.Release != nil && pending[releaseKey{ev.Service, ev.Release.Number}]:
				if ev.Release.Status.Done() {
					delete(pending, releaseKey{ev.Service, ev.Release.Number})
					c.Notify(describeRelease(*ev.Release))
				}
			case ev.ReleaseEvent != nil && pending[releaseKey{ev.Service, ev.ReleaseEvent.ReleaseNumber}]:
				step(releaseKey{ev.Service, ev.ReleaseEvent.ReleaseNumber}, *ev.ReleaseEvent)
			}
		}
		if len(pending) == 0 {
			return errAllDone
		}
		return nil
	})
	switch {
	case readErr != nil:
		return readErr
	case err != nil && !errors.Is(err, errAllDone):
		return err
	}
	return nil
}

// releaseDetails returns a release with its progress lines and, when it
// failed, the last lines of its container's output and of its build.
func (h *handler) releaseDetails(ctx context.Context, a core.Actor, ref core.ServiceRef, number int64, lines int) (releaseView, error) {
	r, err := h.core.GetRelease(ctx, a, ref, number)
	if err != nil {
		return releaseView{}, err
	}
	v := viewRelease(r)
	if r.Status != apitypes.ReleaseFailed || lines == 0 {
		return v, nil
	}
	logs := &failureLogs{}
	// A release that never started a container has no container logs;
	// that's not an error here.
	if out, err := h.logLines(ctx, a, ref, core.LogQuery{Release: number, Tail: lines}, lines); err == nil {
		logs.Container = out
	} else if core.CodeOf(err) != apitypes.CodeNotFound {
		return v, err
	}
	if logs.Build, err = h.buildTail(ctx, a, ref, number, lines); err != nil {
		return v, err
	}
	if len(logs.Container)+len(logs.Build) > 0 {
		v.Logs = logs
	}
	return v, nil
}

// logLines reads up to max lines of a release's container output.
func (h *handler) logLines(ctx context.Context, a core.Actor, ref core.ServiceRef, q core.LogQuery, max int) ([]string, error) {
	stream, err := h.core.ServiceLogs(ctx, a, ref, q)
	if err != nil {
		return nil, err
	}
	out := []string{}
	err = stream(ctx, func() {}, func(ls ...apitypes.LogLine) error {
		for _, l := range ls {
			out = append(out, logLine(l))
		}
		if len(out) > max {
			out = out[len(out)-max:]
		}
		return nil
	})
	return out, err
}

// buildTail returns the last n lines of a release's build (or pull)
// output, reading the log through with n lines in memory.
func (h *handler) buildTail(ctx context.Context, a core.Actor, ref core.ServiceRef, number int64, n int) ([]string, error) {
	f, err := h.core.BuildLog(ctx, a, ref, number)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return tail(f, n)
}

// tail returns the last n lines of r, keeping at most n lines in memory.
// Lines over 64 KB are cut.
func tail(r io.Reader, n int) ([]string, error) {
	ring := make([]string, 0, n)
	next := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if len(ring) < n {
			ring = append(ring, line)
			continue
		}
		ring[next] = line
		next = (next + 1) % n
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return nil, err
	}
	return append(ring[next:len(ring):len(ring)], ring[:next]...), nil
}

// waitSummary describes releases after a wait: each one's state, and what
// to do about unfinished ones.
func waitSummary(action string, vs []releaseView) string {
	parts := make([]string, 0, len(vs))
	unfinished := 0
	for _, v := range vs {
		s := fmt.Sprintf("%s #%d %s", v.Service, v.Number, v.Status)
		if v.Status == apitypes.ReleaseFailed && v.ErrorCode != "" {
			s += " (" + string(v.ErrorCode) + ")"
		}
		if d := v.Diagnosis; v.Status == apitypes.ReleaseFailed && d != nil && d.Rule != "" {
			s += ", likely cause: " + d.Summary
		}
		if !v.Status.Done() {
			unfinished++
		}
		parts = append(parts, s)
	}
	out := action + ": " + strings.Join(parts, ", ")
	if len(vs) == 0 {
		out = action + ": no releases started"
	}
	if unfinished > 0 {
		out += ". Still deploying; call wait_for_release to keep waiting."
	}
	for _, v := range vs {
		if v.Status == apitypes.ReleaseFailed {
			out += ". A failed release never got traffic; the previous one still serves. Call diagnose for the cause and the evidence (or read error and logs), fix, deploy again; don't roll back."
			break
		}
	}
	return out
}
