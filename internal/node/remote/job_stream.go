package remote

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/runframe"
)

// RunJob with a Stream over the node link (`dock run`, ADR 0048), protocol
// 16: one full-duplex call per job, like Dial. The request is the job's
// JSON line, then runframe frames of its input (Stdin, CloseStdin,
// Resize). The node answers 200 once it has the job, then frames of its
// output (Stdout, Stderr) and one Exit frame with how it ended
// (jobStreamEnd). There is no call timeout: the job's own ctx bounds it,
// and canceling it resets the call, which removes the job on the node.
// A dropped link ends the call with ErrOffline; the node's job goes with
// its request's context.

// StreamJobProtocol is the first node protocol that serves
// run-job-stream.
const StreamJobProtocol = 16

// jobStreamRequest is run-job-stream's argument.
type jobStreamRequest struct {
	jobRequest
	TTY   bool   `json:"tty,omitempty"`
	Cols  uint16 `json:"cols,omitempty"`
	Rows  uint16 `json:"rows,omitempty"`
	Stdin bool   `json:"stdin,omitempty"`
}

// jobStreamEnd is the Exit frame's payload.
type jobStreamEnd struct {
	ExitCode int    `json:"exit_code"`
	Error    *Error `json:"error,omitempty"`
}

// streamJob runs a job with a Stream on the node (RunJob).
func (c *Client) streamJob(ctx context.Context, spec node.JobSpec) (node.JobResult, error) {
	if err := c.needProtocol(ctx, StreamJobProtocol, "run commands (`dock run`)"); err != nil {
		return node.JobResult{}, err
	}
	s, err := c.session(ctx)
	if err != nil {
		return node.JobResult{}, err
	}
	st := spec.Stream
	req := jobStreamRequest{jobRequest: toJobRequest(spec), TTY: st.TTY, Cols: st.Size.Cols, Rows: st.Size.Rows, Stdin: st.Stdin != nil}
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	up, upW := io.Pipe()
	body, err := open(callCtx, s, opRunJobStream, req, up)
	if err != nil {
		_ = upW.Close()
		return node.JobResult{}, err
	}
	// Canceling (or returning, through the deferred cancel) closes both
	// bodies: the transport doesn't watch ctx while it waits for input.
	// The input ends with the cancel's error, not a clean EOF: the node
	// must see the call reset, not a job whose input simply ended.
	context.AfterFunc(callCtx, func() { _, _ = upW.CloseWithError(context.Canceled), body.Close() })
	ended := make(chan struct{})
	defer close(ended)
	go sendJobInput(runframe.NewWriter(upW), upW, st, ended)

	var tail tailWriter
	stdout := teeTail(st.Stdout, &tail)
	stderr := teeTail(cmp.Or(st.Stderr, st.Stdout), &tail)
	fr := runframe.NewReader(body)
	for {
		t, p, err := fr.Next()
		if err != nil {
			if ctx.Err() != nil {
				return node.JobResult{Output: tail.String()}, ctx.Err()
			}
			return node.JobResult{Output: tail.String()}, offline(ctx, s.name(), err)
		}
		var werr error
		switch t {
		case runframe.Stdout:
			_, werr = stdout.Write(p)
		case runframe.Stderr:
			_, werr = stderr.Write(p)
		case runframe.Exit:
			var end jobStreamEnd
			if err := json.Unmarshal(p, &end); err != nil {
				return node.JobResult{Output: tail.String()}, fmt.Errorf("server %s: bad job end: %w", s.name(), err)
			}
			res := node.JobResult{ExitCode: end.ExitCode, Output: tail.String()}
			if end.Error != nil {
				return res, end.Error
			}
			if err := ctx.Err(); err != nil {
				// Canceled while the end was on its way: the job was
				// stopped, however it ended on the node.
				return res, err
			}
			return res, nil
		}
		if werr != nil {
			// The caller stopped taking output: returning resets the call
			// and the job ends on the node.
			return node.JobResult{Output: tail.String()}, fmt.Errorf("job %s output: %w", spec.Name, werr)
		}
	}
}

// sendJobInput frames a job's input and terminal sizes into the call's
// request body until both end or the job ended.
func sendJobInput(fw *runframe.Writer, w *io.PipeWriter, st *node.JobStream, ended <-chan struct{}) {
	if st.Stdin != nil {
		go func() {
			_, err := io.Copy(fw.Stream(runframe.Stdin), st.Stdin)
			if err == nil {
				_ = fw.Frame(runframe.CloseStdin, nil)
			}
		}()
	}
	defer func() { _ = w.Close() }()
	if !st.TTY || st.Resize == nil {
		<-ended
		return
	}
	for {
		select {
		case sz, ok := <-st.Resize:
			if !ok {
				<-ended
				return
			}
			if fw.Resize(sz.Cols, sz.Rows) != nil {
				return
			}
		case <-ended:
			return
		}
	}
}

// serveRunJobStream serves run-job-stream on the node.
func serveRunJobStream(rt node.Runtime) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, up, err := readArgs[jobStreamRequest](r)
		if err != nil {
			newResponder(w).fail(&Error{Code: codeBadRequest, Message: err.Error()})
			return
		}
		rc := http.NewResponseController(w)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if err := rc.Flush(); err != nil {
			return
		}
		fw := runframe.NewWriter(flushWriter{w, rc})
		spec := a.node()
		resize := make(chan node.TermSize, 1)
		st := &node.JobStream{
			Stdout: fw.Stream(runframe.Stdout), Stderr: fw.Stream(runframe.Stderr),
			TTY: a.TTY, Size: node.TermSize{Cols: a.Cols, Rows: a.Rows}, Resize: resize,
		}
		var stdinW *io.PipeWriter
		if a.Stdin {
			var pr *io.PipeReader
			pr, stdinW = io.Pipe()
			st.Stdin = pr
		}
		spec.Stream = st
		go receiveJobInput(runframe.NewReader(up), stdinW, resize)
		res, err := rt.RunJob(r.Context(), spec)
		if stdinW != nil {
			_ = stdinW.Close() // ends the runtime's copy into the job
		}
		end := jobStreamEnd{ExitCode: res.ExitCode}
		if err != nil {
			end.Error = wireError(err)
		}
		raw, _ := json.Marshal(end)
		_ = fw.Frame(runframe.Exit, raw)
	}
}

// receiveJobInput reads the control plane's input frames into the job's
// stdin (nil: it takes none) and terminal size changes. The request body
// ending (the call is over) ends it.
func receiveJobInput(fr *runframe.Reader, stdin *io.PipeWriter, resize chan node.TermSize) {
	defer close(resize)
	for {
		t, p, err := fr.Next()
		if err != nil {
			if stdin != nil {
				_ = stdin.CloseWithError(err)
			}
			return
		}
		switch t {
		case runframe.Stdin:
			if stdin != nil {
				if _, err := stdin.Write(p); err != nil && !errors.Is(err, io.ErrClosedPipe) {
					return
				}
			}
		case runframe.CloseStdin:
			if stdin != nil {
				_ = stdin.Close()
			}
		case runframe.Resize:
			cols, rows, err := runframe.ParseResize(p)
			if err != nil {
				continue
			}
			// Only the latest size matters: drop one still unread.
			select {
			case <-resize:
			default:
			}
			resize <- node.TermSize{Cols: cols, Rows: rows}
		}
	}
}

// tailWriter keeps the last node.MaxJobOutput bytes written to it, the
// result's Output.
type tailWriter struct{ b []byte }

// Write trims before it appends, so the buffer never holds more than
// node.MaxJobOutput bytes, however large one write is.
func (t *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= node.MaxJobOutput {
		p, t.b = p[len(p)-node.MaxJobOutput:], t.b[:0]
	} else if over := len(t.b) + len(p) - node.MaxJobOutput; over > 0 {
		t.b = t.b[:copy(t.b, t.b[over:])]
	}
	t.b = append(t.b, p...)
	return n, nil
}

func (t *tailWriter) String() string { return string(t.b) }

// teeTail writes to w (if any) and the tail.
func teeTail(w io.Writer, tail *tailWriter) io.Writer {
	if w == nil {
		return tail
	}
	return io.MultiWriter(w, tail)
}
