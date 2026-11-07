package core

import (
	"sync"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/errparse"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Errors in containers' logs (ADR 0062, "One place for errors"). Every
// line a log follower reads (drains_deliver.go) goes through its
// errorFollower: an errparse.Assembler that turns lines into errors, one
// per stack trace however many lines it took. A plain line costs a byte
// scan and an uncontended lock (BenchmarkErrorFollowerLine). Each error,
// its secret values masked, goes into the error sink (errors_ingest.go)
// without blocking; the statistics tick writes the sink's issues.

// errorQuiet is how long an open error may wait for its next line before
// the tick ends it: an error line followed by silence is still an error.
const errorQuiet = 2 * time.Second

// errorFollower is a follower's error state. The follower feeds it every
// line; the statistics tick ends an error that waited errorQuiet for a
// line that never came (flushQuietErrors), hence the lock.
type errorFollower struct {
	mu  sync.Mutex
	asm errparse.Assembler
	// started is when the open error's first line was written; touched
	// when its last was.
	started, touched time.Time
	// src labels and masks the container's lines, and names its service.
	src *logSource
}

// line feeds one line and sinks the error it ends, if any.
func (f *errorFollower) line(c *Core, l node.LogLine) {
	f.mu.Lock()
	wasOpen := f.asm.Open()
	ev, done := f.asm.Line(l.Text)
	at := f.started
	if f.asm.Open() {
		if done || !wasOpen {
			f.started = l.Time
		}
		f.touched = l.Time
	}
	f.mu.Unlock()
	if done {
		c.sinkLogError(f.src, ev, at)
	}
}

// end sinks the open error when the container's log stream ends.
func (f *errorFollower) end(c *Core) {
	f.mu.Lock()
	ev, done := f.asm.Flush()
	at := f.started
	f.mu.Unlock()
	if done {
		c.sinkLogError(f.src, ev, at)
	}
}

// flushQuietErrors ends every follower's open error whose last line was
// written errorQuiet before now. The statistics tick calls it before
// writing the sink.
func (c *Core) flushQuietErrors(now time.Time) {
	type ended struct {
		src *logSource
		ev  errparse.Event
		at  time.Time
	}
	var out []ended
	m := c.logDrains
	m.mu.Lock()
	for _, f := range m.follows {
		e := &f.errs
		e.mu.Lock()
		if e.asm.Open() && now.Sub(e.touched) >= errorQuiet {
			if ev, ok := e.asm.Flush(); ok {
				out = append(out, ended{e.src, ev, e.started})
			}
		}
		e.mu.Unlock()
	}
	m.mu.Unlock()
	for _, e := range out {
		c.sinkLogError(e.src, e.ev, e.at)
	}
}

// sinkLogError masks a container's error and puts it in the sink, under
// its service and release.
func (c *Core) sinkLogError(src *logSource, ev errparse.Event, at time.Time) {
	if src == nil || src.serviceID == 0 {
		return
	}
	title, _ := src.red.line(ev.Title)
	culprit, _ := src.red.line(ev.Culprit)
	sample, _ := src.red.line(ev.Sample)
	if at.IsZero() {
		at = now()
	}
	c.issues.add(src.serviceID, ev.Fingerprint, pendingError{
		source: apitypes.ErrorFromLogs, title: title, culprit: culprit, sample: sample,
		count: 1, first: at, last: at, release: src.Release,
	})
}
