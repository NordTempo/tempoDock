package docker

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

// logBuffer is how many parsed log lines may wait for the consumer.
const logBuffer = 256

// Logs streams a managed container's stdout and stderr as lines.
func (r *Runtime) Logs(ctx context.Context, id string, opts node.LogOptions) (<-chan node.LogLine, error) {
	if _, err := r.inspect(ctx, id); err != nil {
		return nil, err
	}
	o := client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Follow:     opts.Follow,
	}
	if opts.Tail > 0 {
		o.Tail = strconv.Itoa(opts.Tail)
	}
	if !opts.Since.IsZero() {
		o.Since = fmt.Sprintf("%d.%09d", opts.Since.Unix(), opts.Since.Nanosecond())
	}
	rc, err := r.cli.ContainerLogs(ctx, id, o)
	if err != nil {
		return nil, fmt.Errorf("logs for container %s: %w", id, err)
	}

	// Buffered so the reader keeps parsing while the consumer writes a
	// batch to its client (core.ServiceLogs drains what is queued).
	ch := make(chan node.LogLine, logBuffer)
	go func() {
		defer close(ch)
		defer func() { _ = rc.Close() }()
		emit := func(l node.LogLine) error {
			select {
			case ch <- l:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		stdout := &lineWriter{stream: "stdout", emit: emit}
		stderr := &lineWriter{stream: "stderr", emit: emit}
		if _, err := stdcopy.StdCopy(stdout, stderr, rc); err != nil {
			return
		}
		if stdout.flush() == nil {
			_ = stderr.flush()
		}
	}()
	return ch, nil
}

// maxLogLine bounds one line, its timestamp included: past it the line is
// emitted cut on a rune boundary and the rest, up to its newline, is dropped, as the node link truncates
// lines (remote's maxLogText). Without it a container that never writes
// a newline grows the buffer without bound, and log drains follow every
// container all the time (ADR 0046).
const maxLogLine = 64 << 10

// lineWriter splits one demultiplexed stream into timestamped lines and
// hands each to emit. Docker prefixes every line with an RFC 3339 timestamp
// when timestamps are requested. It buffers at most maxLogLine bytes.
type lineWriter struct {
	stream string
	emit   func(node.LogLine) error
	buf    []byte
	// skip drops input up to the next newline: the rest of a line already
	// emitted cut at maxLogLine.
	skip bool
}

func (w *lineWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if w.skip {
			if i < 0 {
				return n, nil
			}
			w.skip, p = false, p[i+1:]
			continue
		}
		if i < 0 {
			if len(w.buf)+len(p) <= maxLogLine {
				w.buf = append(w.buf, p...)
				return n, nil
			}
			w.buf = append(w.buf, p[:maxLogLine-len(w.buf)]...)
			w.skip, p = true, nil
		} else {
			w.buf = append(w.buf, p[:min(i, max(0, maxLogLine-len(w.buf)))]...)
			p = p[i+1:]
		}
		line := string(truncateRunes(w.buf))
		w.buf = w.buf[:0]
		if err := w.emit(w.parse(line)); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// truncateRunes drops a rune cut short at the end of b, left by cutting a
// line at maxLogLine.
func truncateRunes(b []byte) []byte {
	if len(b) < maxLogLine {
		return b
	}
	for i := len(b) - 1; i >= max(0, len(b)-utf8.UTFMax); i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			return b
		}
	}
	return b
}

// flush emits a trailing line that had no newline.
func (w *lineWriter) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	line := string(w.buf)
	w.buf = nil
	return w.emit(w.parse(line))
}

func (w *lineWriter) parse(line string) node.LogLine {
	line = strings.TrimSuffix(line, "\r")
	l := node.LogLine{Stream: w.stream, Text: line}
	if ts, text, ok := strings.Cut(line, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			l.Time, l.Text = t, text
		}
	}
	return l
}
