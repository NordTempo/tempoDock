package docker

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"

	"github.com/NordTempo/tempoDock/internal/node"
)

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

	ch := make(chan node.LogLine)
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

// lineWriter splits one demultiplexed stream into timestamped lines and
// hands each to emit. Docker prefixes every line with an RFC 3339 timestamp
// when timestamps are requested.
type lineWriter struct {
	stream string
	emit   func(node.LogLine) error
	buf    []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			return len(p), nil
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if err := w.emit(w.parse(line)); err != nil {
			return 0, err
		}
	}
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
