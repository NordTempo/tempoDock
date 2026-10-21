package core

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/store"
)

// Stream delivers live items until it ends, ctx is done or send fails.
// Operations returning a Stream check permissions and load what they need
// first, so errors surface before a client starts reading. A stream calls
// ready once it is live (subscribed to the event bus, or attached to the
// runtime) and before its first send, so nothing published after a client
// sees "ready" is missed. An error before ready means nothing was sent.
// Each send delivers one or more items, written to the client together.
type Stream[T any] func(ctx context.Context, ready func(), send func(...T) error) error

// EventFilter narrows the event stream. Environment needs Project, and
// Service needs Environment.
type EventFilter struct {
	Project, Environment, Service string
}

// Events streams live events the actor may see. When the client is too
// slow and events were dropped, it receives an EventLagged event and
// should refetch what it shows. Events queued while the client was being
// written to are sent together.
func (c *Core) Events(ctx context.Context, a Actor, f EventFilter) (Stream[LiveEvent], error) {
	if err := a.require(apitypes.ScopeRead, "following events"); err != nil {
		return nil, err
	}
	switch {
	case f.Service != "":
		if _, err := c.serviceFor(ctx, a, ServiceRef(f), apitypes.ScopeRead, "following events"); err != nil {
			return nil, err
		}
	case f.Environment != "":
		if _, err := c.envFor(ctx, a, EnvRef{f.Project, f.Environment}, apitypes.ScopeRead, "following events"); err != nil {
			return nil, err
		}
	case f.Project != "":
		if _, err := c.projectFor(ctx, a, f.Project, apitypes.ScopeRead, "following events"); err != nil {
			return nil, err
		}
	}
	match := func(e busEvent) bool {
		if !a.canSee(e.projectID, e.envID) {
			return false
		}
		return (f.Project == "" || e.ev.Project == f.Project) &&
			(f.Environment == "" || e.ev.Environment == f.Environment) &&
			(f.Service == "" || e.ev.Service == f.Service)
	}
	return func(ctx context.Context, ready func(), send func(...LiveEvent) error) error {
		sub := c.bus.subscribe(match)
		defer sub.close()
		ready()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-c.ctx.Done():
				return nil
			case ev := <-sub.ch:
				if err := send(sub.drain(ev)...); err != nil {
					return err
				}
			}
		}
	}, nil
}

// BuildLog opens a release's build log (empty if there is none yet).
func (c *Core) BuildLog(ctx context.Context, a Actor, ref ServiceRef, number int64) (io.ReadCloser, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading build logs")
	if err != nil {
		return nil, err
	}
	rel, err := c.releaseByNumber(ctx, ss, number)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(c.buildLogPath(rel.ID))
	if errors.Is(err, os.ErrNotExist) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	return f, err
}

// FollowBuildLog streams a release's build log: what's written so far, then
// new lines until the build phase ends.
func (c *Core) FollowBuildLog(ctx context.Context, a Actor, ref ServiceRef, number int64) (Stream[apitypes.BuildLogLine], error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading build logs")
	if err != nil {
		return nil, err
	}
	rel, err := c.releaseByNumber(ctx, ss, number)
	if err != nil {
		return nil, err
	}
	building := func(status string) bool {
		return status == string(apitypes.ReleaseQueued) || status == string(apitypes.ReleaseBuilding)
	}
	return func(ctx context.Context, ready func(), send func(...apitypes.BuildLogLine) error) error {
		// Subscribe before reading the file so no line falls in between;
		// line numbers (Seq) drop the duplicates.
		sub := c.bus.subscribe(func(e busEvent) bool {
			return e.serviceID == ss.svc.ID &&
				((e.ev.BuildLog != nil && e.ev.BuildLog.ReleaseNumber == number) ||
					(e.ev.Release != nil && e.ev.Release.Number == number))
		})
		defer sub.close()
		ready()
		sent := 0
		catchUp := func() error {
			lines, err := readLines(c.buildLogPath(rel.ID))
			if err != nil {
				return err
			}
			if sent >= len(lines) {
				return nil
			}
			batch := make([]apitypes.BuildLogLine, 0, len(lines)-sent)
			for seq := sent; seq < len(lines); seq++ {
				batch = append(batch, apitypes.BuildLogLine{ReleaseNumber: number, Seq: seq, Text: lines[seq]})
			}
			if err := send(batch...); err != nil {
				return err
			}
			sent = len(lines)
			return nil
		}
		if err := catchUp(); err != nil {
			return err
		}
		cur, err := c.store.GetRelease(ctx, rel.ID)
		if err != nil || !building(cur.Status) {
			return err
		}
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-c.ctx.Done():
				return nil
			case ev := <-sub.ch:
				if sub.takeLagged() {
					if err := catchUp(); err != nil {
						return err
					}
				}
				switch {
				case ev.BuildLog != nil && ev.BuildLog.Seq >= sent:
					if err := send(*ev.BuildLog); err != nil {
						return err
					}
					sent = ev.BuildLog.Seq + 1
				case ev.Release != nil && !building(string(ev.Release.Status)):
					return catchUp()
				}
			}
		}
	}, nil
}

// readLines returns the complete (newline-terminated) lines of a file.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var lines []string
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return lines, nil // EOF; a partial last line is still being written
		}
		lines = append(lines, strings.TrimSuffix(line, "\n"))
	}
}

// LogQuery selects service log lines. Release 0 means the active release.
type LogQuery struct {
	Release int64
	Follow  bool
	Tail    int
	Since   time.Time
}

// savedLogLines is how much of a failed release's container output is kept.
const savedLogLines = 200

func (c *Core) containerLogPath(releaseID int64) string {
	return filepath.Join(c.cfg.DataDir, "builds", strconv.FormatInt(releaseID, 10)+"-container.jsonl")
}

// saveContainerLogs keeps the last lines of a failed release's container
// (one JSON LogLine per line) before the container is removed, so its logs
// still explain why it failed. Best effort.
func (c *Core) saveContainerLogs(ctx context.Context, serverID, releaseID int64) {
	rt := c.runtime(serverID)
	ctrs, err := rt.List(ctx)
	if err != nil {
		return
	}
	cs := ofRelease(ctrs, releaseID)
	if len(cs) == 0 {
		return
	}
	lines, err := rt.Logs(ctx, cs[0].ID, node.LogOptions{Tail: savedLogLines})
	if err != nil {
		c.log.Warn("save container logs", "release", releaseID, "err", err)
		return
	}
	f, err := os.OpenFile(c.containerLogPath(releaseID), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		c.log.Warn("save container logs", "release", releaseID, "err", err)
		return
	}
	defer func() { _ = f.Close() }()
	enc := json.NewEncoder(f)
	for l := range lines {
		_ = enc.Encode(apitypes.LogLine{Time: l.Time, Stream: l.Stream, Text: l.Text})
	}
}

// readSavedLogs reads what saveContainerLogs kept; nil if nothing was.
func readSavedLogs(path string) ([]apitypes.LogLine, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := []apitypes.LogLine{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		var l apitypes.LogLine
		if json.Unmarshal([]byte(line), &l) == nil {
			out = append(out, l)
		}
	}
	return out, nil
}

// ServiceLogs streams a release container's output from the runtime. Logs
// are read from Docker; the platform only keeps the last lines of failed
// releases' containers (saveContainerLogs), which are served instead.
func (c *Core) ServiceLogs(ctx context.Context, a Actor, ref ServiceRef, q LogQuery) (Stream[apitypes.LogLine], error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading logs")
	if err != nil {
		return nil, err
	}
	var rel store.Release
	if q.Release != 0 {
		rel, err = c.releaseByNumber(ctx, ss, q.Release)
	} else {
		rel, err = c.store.GetActiveRelease(ctx, ss.svc.ID)
		if store.IsNotFound(err) {
			return nil, notFound("%s has no active release; pass a release number to see a failed one", ss)
		}
	}
	if err != nil {
		return nil, err
	}
	if saved, err := readSavedLogs(c.containerLogPath(rel.ID)); err != nil {
		return nil, err
	} else if saved != nil && rel.Status == string(apitypes.ReleaseFailed) {
		if q.Tail > 0 && len(saved) > q.Tail {
			saved = saved[len(saved)-q.Tail:]
		}
		return func(_ context.Context, ready func(), send func(...apitypes.LogLine) error) error {
			ready()
			lines := slices.DeleteFunc(saved, func(l apitypes.LogLine) bool { return l.Time.Before(q.Since) })
			if len(lines) == 0 {
				return nil
			}
			return send(lines...)
		}, nil
	}
	if rel.ContainerID == "" {
		return nil, notFound("release %d of %s never started a container; see its build log", rel.Number, ss)
	}
	return func(ctx context.Context, ready func(), send func(...apitypes.LogLine) error) error {
		lines, err := c.runtime(ss.env.ServerID).Logs(ctx, rel.ContainerID, node.LogOptions{Follow: q.Follow, Tail: q.Tail, Since: q.Since})
		if errors.Is(err, node.ErrNotFound) {
			return notFound("the container of release %d of %s is gone", rel.Number, ss)
		}
		if err != nil {
			return err
		}
		ready()
		for l := range lines {
			if err := send(apitypes.LogLine{Time: l.Time, Stream: l.Stream, Text: l.Text}); err != nil {
				return err
			}
		}
		return nil
	}, nil
}
