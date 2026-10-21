package core

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"github.com/NordTempo/platform/internal/apitypes"
	"github.com/NordTempo/platform/internal/node"
	"github.com/NordTempo/platform/internal/store"
)

// Stream delivers live items until it ends, ctx is done or send fails.
// Operations returning a Stream check permissions and load what they need
// first, so errors surface before a client starts reading.
type Stream[T any] func(ctx context.Context, send func(T) error) error

// EventFilter narrows the event stream. Environment needs Project, and
// Service needs Environment.
type EventFilter struct {
	Project, Environment, Service string
}

// Events streams live events the actor may see. When the client is too
// slow and events were dropped, it receives an EventLagged event and
// should refetch what it shows.
func (c *Core) Events(ctx context.Context, a Actor, f EventFilter) (Stream[apitypes.Event], error) {
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
	return func(ctx context.Context, send func(apitypes.Event) error) error {
		sub := c.bus.subscribe(match)
		defer sub.close()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-c.ctx.Done():
				return nil
			case ev := <-sub.ch:
				if sub.takeLagged() {
					if err := send(apitypes.Event{Type: apitypes.EventLagged, Time: now()}); err != nil {
						return err
					}
				}
				if err := send(ev); err != nil {
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
	return func(ctx context.Context, send func(apitypes.BuildLogLine) error) error {
		// Subscribe before reading the file so no line falls in between;
		// line numbers (Seq) drop the duplicates.
		sub := c.bus.subscribe(func(e busEvent) bool {
			return e.serviceID == ss.svc.ID &&
				((e.ev.BuildLog != nil && e.ev.BuildLog.ReleaseNumber == number) ||
					(e.ev.Release != nil && e.ev.Release.Number == number))
		})
		defer sub.close()
		sent := 0
		catchUp := func() error {
			lines, err := readLines(c.buildLogPath(rel.ID))
			if err != nil {
				return err
			}
			for ; sent < len(lines); sent++ {
				if err := send(apitypes.BuildLogLine{ReleaseNumber: number, Seq: sent, Text: lines[sent]}); err != nil {
					return err
				}
			}
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

// ServiceLogs streams a release container's output from the runtime. Logs
// are read from Docker, never copied or stored by the platform.
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
	if rel.ContainerID == "" {
		return nil, notFound("release %d of %s never started a container; see its build log", rel.Number, ss)
	}
	return func(ctx context.Context, send func(apitypes.LogLine) error) error {
		lines, err := c.runtime(ss.env.ServerID).Logs(ctx, rel.ContainerID, node.LogOptions{Follow: q.Follow, Tail: q.Tail, Since: q.Since})
		if errors.Is(err, node.ErrNotFound) {
			return notFound("the container of release %d of %s is gone", rel.Number, ss)
		}
		if err != nil {
			return err
		}
		for l := range lines {
			if err := send(apitypes.LogLine{Time: l.Time, Stream: l.Stream, Text: l.Text}); err != nil {
				return err
			}
		}
		return nil
	}, nil
}
