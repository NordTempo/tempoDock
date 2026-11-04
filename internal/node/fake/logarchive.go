package fake

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/NordTempo/tempoDock/internal/node"
)

// fakeArchive is one release's archived lines (ADR 0046), held in memory.
// Its size is the bytes of its texts (one per line more), standing in for
// the compressed file's.
type fakeArchive struct {
	lines []node.LogLine
	bytes int64
	at    time.Time
}

// FailArchiveLogs makes ArchiveLogs fail with err (nil to succeed again),
// like a node too old to keep logs.
func (r *Runtime) FailArchiveLogs(err error) { r.mu.Lock(); r.archiveErr = err; r.mu.Unlock() }

// AddLogArchive archives lines for a service and release (IDs) directly,
// as if a container of it had been removed.
func (r *Runtime) AddLogArchive(service, release string, lines ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ls := make([]node.LogLine, len(lines))
	for i, l := range lines {
		ls[i] = node.LogLine{Time: time.Now(), Stream: "stdout", Text: l}
	}
	r.appendArchiveLocked(service, release, ls)
}

// AgeLogArchive makes a release's archive look last written d ago.
func (r *Runtime) AgeLogArchive(service, release string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.archives[service][release]; a != nil {
		a.at = a.at.Add(-d)
	}
}

// LogArchives lists the archives as "<service>/<release>", sorted.
func (r *Runtime) LogArchives() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for svc, rels := range r.archives {
		for rel := range rels {
			out = append(out, svc+"/"+rel)
		}
	}
	slices.Sort(out)
	return out
}

func (r *Runtime) appendArchiveLocked(service, release string, lines []node.LogLine) int64 {
	if r.archives[service] == nil {
		r.archives[service] = map[string]*fakeArchive{}
	}
	a := r.archives[service][release]
	if a == nil {
		a = &fakeArchive{}
		r.archives[service][release] = a
	}
	var n int64
	for _, l := range lines {
		n += int64(len(l.Text)) + 1
	}
	a.lines = append(a.lines, lines...)
	a.bytes += n
	a.at = time.Now()
	return n
}

// ArchiveLogs implements node.Containers, recorded as "archive logs
// <name> max=<bytes> age=<duration>": the container's lines are kept by
// its labels' service and release, then the service's archives are held
// to limits like the real runtime's.
func (r *Runtime) ArchiveLogs(_ context.Context, id string, limits node.LogLimits) (node.LogArchive, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.containers[id]
	if !ok {
		return node.LogArchive{}, fmt.Errorf("container %s: %w", id, node.ErrNotFound)
	}
	r.op("archive logs %s max=%d age=%s", c.Name, limits.MaxBytes, limits.MaxAge)
	out := node.LogArchive{Service: c.Labels.Service, Release: c.Labels.Release}
	if r.archiveErr != nil {
		return out, r.archiveErr
	}
	if out.Service == "" || out.Release == "" {
		return out, fmt.Errorf("container %s has no service or release label; its logs aren't kept", id)
	}
	out.Bytes = r.appendArchiveLocked(out.Service, out.Release, slices.Clone(r.logs[id]))
	r.enforceLocked(out.Service, limits, time.Now())
	return out, nil
}

// enforceLocked drops a service's archives past limits, oldest first.
func (r *Runtime) enforceLocked(service string, limits node.LogLimits, now time.Time) node.PruneResult {
	var out node.PruneResult
	rels := r.archives[service]
	ids := slices.SortedFunc(maps.Keys(rels), func(a, b string) int { return cmp.Compare(rels[b].at.UnixNano(), rels[a].at.UnixNano()) })
	var total int64
	for _, id := range ids {
		a := rels[id]
		total += a.bytes
		if (limits.MaxAge > 0 && now.Sub(a.at) > limits.MaxAge) || (limits.MaxBytes > 0 && total > limits.MaxBytes) {
			total -= a.bytes
			delete(rels, id)
			out.Removed++
			out.ReclaimedBytes += a.bytes
		}
	}
	return out
}

// ArchivedLogs implements node.Containers.
func (r *Runtime) ArchivedLogs(_ context.Context, q node.LogArchiveQuery) (<-chan node.LogLine, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.archives[q.Service][q.Release]
	if a == nil {
		return nil, fmt.Errorf("logs of release %s: %w", q.Release, node.ErrNotFound)
	}
	lines := slices.DeleteFunc(slices.Clone(a.lines), func(l node.LogLine) bool { return l.Time.Before(q.Since) })
	if q.Tail > 0 && len(lines) > q.Tail {
		lines = lines[len(lines)-q.Tail:]
	}
	ch := make(chan node.LogLine, len(lines))
	for _, l := range lines {
		ch <- l
	}
	close(ch)
	return ch, nil
}

// PruneLogs implements node.Containers, recorded as "prune logs
// keep=<ids> max=<bytes> age=<duration>".
func (r *Runtime) PruneLogs(_ context.Context, p node.LogPrune) (node.PruneResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.op("prune logs keep=%s max=%d age=%s", strings.Join(p.KeepServices, ","), p.Limits.MaxBytes, p.Limits.MaxAge)
	var out node.PruneResult
	now := time.Now()
	for svc, rels := range r.archives {
		if slices.Contains(p.KeepServices, svc) {
			res := r.enforceLocked(svc, p.Limits, now)
			out.Removed += res.Removed
			out.ReclaimedBytes += res.ReclaimedBytes
			continue
		}
		// Like the real runtime, a service archived to lately stays.
		if slices.ContainsFunc(slices.Collect(maps.Values(rels)), func(a *fakeArchive) bool { return now.Sub(a.at) < time.Hour }) {
			continue
		}
		for _, a := range rels {
			out.Removed++
			out.ReclaimedBytes += a.bytes
		}
		delete(r.archives, svc)
	}
	return out, nil
}
