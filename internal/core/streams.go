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
	"github.com/NordTempo/tempoDock/internal/datapanel"
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
// It is datapanel.Stream, so the data panel's streams are core's too.
type Stream[T any] = datapanel.Stream[T]

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
	var pid, eid, sid int64
	switch {
	case f.Service != "":
		ss, err := c.serviceFor(ctx, a, ServiceRef(f), apitypes.ScopeRead, "following events")
		if err != nil {
			return nil, err
		}
		pid, eid, sid = ss.project.ID, ss.env.ID, ss.svc.ID
	case f.Environment != "":
		es, err := c.envFor(ctx, a, EnvRef{f.Project, f.Environment}, apitypes.ScopeRead, "following events")
		if err != nil {
			return nil, err
		}
		pid, eid = es.project.ID, es.env.ID
	case f.Project != "":
		p, err := c.projectFor(ctx, a, f.Project, apitypes.ScopeRead, "following events")
		if err != nil {
			return nil, err
		}
		pid = p.ID
	}
	serverStats := a.readsServerStats()
	match := func(e busEvent) bool {
		// A project rename affects every environment, including scoped readers.
		envID := e.envID
		if e.ev.Type == apitypes.EventProjectRenamed {
			envID = a.EnvironmentID
		}
		if !a.canSee(e.orgID, e.projectID, envID) || (e.serverStats && !serverStats) {
			return false
		}
		return (pid == 0 || e.projectID == pid) &&
			(eid == 0 || e.envID == eid || e.ev.Type == apitypes.EventProjectRenamed) &&
			(sid == 0 || e.serviceID == sid || e.ev.Type == apitypes.EventProjectRenamed)
	}
	reveal := a.revealsSecrets()
	return func(ctx context.Context, ready func(), send func(...LiveEvent) error) error {
		sub := c.bus.subscribeAs(reveal, match)
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

// BuildLog opens a release's build log (empty if there is none yet), with
// secret values masked unless the actor may reveal them.
func (c *Core) BuildLog(ctx context.Context, a Actor, ref ServiceRef, number int64) (io.ReadCloser, error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading build logs")
	if err != nil {
		return nil, err
	}
	rel, err := c.releaseByNumber(ctx, ss, number)
	if err != nil {
		return nil, err
	}
	red, err := c.logRedactor(a, rel)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(c.buildLogPath(rel.ID))
	if errors.Is(err, os.ErrNotExist) {
		return io.NopCloser(strings.NewReader("")), nil
	}
	if err != nil {
		return nil, err
	}
	return red.reader(f), nil
}

// FollowBuildLog streams a release's build log: what's written so far, then
// new lines until the build phase ends. Secret values are masked unless the
// actor may reveal them.
func (c *Core) FollowBuildLog(ctx context.Context, a Actor, ref ServiceRef, number int64) (Stream[apitypes.BuildLogLine], error) {
	ss, err := c.serviceFor(ctx, a, ref, apitypes.ScopeRead, "reading build logs")
	if err != nil {
		return nil, err
	}
	rel, err := c.releaseByNumber(ctx, ss, number)
	if err != nil {
		return nil, err
	}
	red, err := c.logRedactor(a, rel)
	if err != nil {
		return nil, err
	}
	reveal := a.revealsSecrets()
	building := func(status string) bool {
		return status == string(apitypes.ReleaseQueued) || status == string(apitypes.ReleaseBuilding)
	}
	return func(ctx context.Context, ready func(), send func(...apitypes.BuildLogLine) error) error {
		// Subscribe before reading the file so no line falls in between;
		// line numbers (Seq) drop the duplicates.
		sub := c.bus.subscribeAs(reveal, func(e busEvent) bool {
			return e.serviceID == ss.svc.ID &&
				((e.ev.BuildLog != nil && e.ev.BuildLog.ReleaseNumber == number) ||
					(e.ev.Release != nil && e.ev.Release.Number == number))
		})
		defer sub.close()
		ready()
		// sent is the number of lines sent; pos is how far the file is read.
		sent := 0
		var pos logCursor
		catchUp := func() error {
			return pos.read(c.buildLogPath(rel.ID), func(first int, lines []string) error {
				// Skip lines already sent live.
				if skip := sent - first; skip > 0 {
					if skip >= len(lines) {
						return nil
					}
					first, lines = sent, lines[skip:]
				}
				batch := make([]apitypes.BuildLogLine, len(lines))
				for i, l := range lines {
					l, _ = red.line(l)
					batch[i] = apitypes.BuildLogLine{ReleaseNumber: number, Seq: first + i, Text: l}
				}
				if err := send(batch...); err != nil {
					return err
				}
				sent = first + len(lines)
				return nil
			})
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

// Build log catch-up batches hold at most logBatchLines lines and about
// logBatchBytes bytes (a batch ends with the line that reaches it), so
// following even a huge log takes a bounded amount of memory.
const (
	logBatchLines = 1000
	logBatchBytes = 64 << 10
)

// logCursor is a position in a build log: the byte offset and the number
// of the next line to read.
type logCursor struct {
	offset int64
	line   int
}

// read calls fn with the complete (newline-terminated) lines of the file
// at path from the cursor on, in batches (logBatchLines, logBatchBytes),
// along with the number of each batch's first line. The cursor moves past
// every batch fn accepts. A partial last line is still being written and
// is left for the next read. A missing file has no lines yet.
func (cur *logCursor) read(path string, fn func(first int, lines []string) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(cur.offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReaderSize(f, logBatchBytes)
	lines := make([]string, 0, logBatchLines)
	var size int64
	flush := func() error {
		if len(lines) == 0 {
			return nil
		}
		if err := fn(cur.line, lines); err != nil {
			return err
		}
		cur.offset += size
		cur.line += len(lines)
		lines, size = lines[:0], 0
		return nil
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return flush() // EOF, possibly after a partial line
		}
		lines = append(lines, line[:len(line)-1])
		size += int64(len(line))
		if len(lines) == logBatchLines || size >= logBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
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
func (c *Core) saveContainerLogs(ctx context.Context, ss svcScope, releaseID int64) {
	rt := c.runtime(ss.env.ServerID)
	ids, err := rt.ReleaseContainers(ctx, c.labels(ss, releaseID))
	if err != nil || len(ids) == 0 {
		return
	}
	lines, err := rt.Logs(ctx, ids[0], node.LogOptions{Tail: savedLogLines})
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
// Secret values are masked unless the actor may reveal them.
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
	red, err := c.logRedactor(a, rel)
	if err != nil {
		return nil, err
	}
	logLine := func(l node.LogLine) apitypes.LogLine {
		text, _ := red.line(l.Text)
		return apitypes.LogLine{Time: l.Time, Stream: l.Stream, Text: text}
	}
	if saved, err := readSavedLogs(c.containerLogPath(rel.ID)); err != nil {
		return nil, err
	} else if saved != nil && rel.Status == string(apitypes.ReleaseFailed) {
		if q.Tail > 0 && len(saved) > q.Tail {
			saved = saved[len(saved)-q.Tail:]
		}
		for i := range saved {
			saved[i].Text, _ = red.line(saved[i].Text)
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
			return c.nodeErr(ss.env.ServerID, err)
		}
		ready()
		// Lines queued behind the first go out in the same write.
		batch := make([]apitypes.LogLine, 0, logSendBatch)
		for l := range lines {
			batch = append(batch[:0], logLine(l))
			open := true
		drain:
			for open && len(batch) < logSendBatch {
				select {
				case l, ok := <-lines:
					if open = ok; ok {
						batch = append(batch, logLine(l))
					}
				default:
					break drain
				}
			}
			if err := send(batch...); err != nil {
				return err
			}
			if !open {
				return nil
			}
		}
		return nil
	}, nil
}

// logSendBatch is the most service log lines sent in one write.
const logSendBatch = 256
