package remote

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

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
	if spec.Sync {
		if err := c.needProtocol(ctx, MoveSyncProtocol, "copy volumes while their service runs"); err != nil {
			return node.JobResult{}, err
		}
	}
	s, err := c.session(ctx)
	if err != nil {
		return node.JobResult{}, err
	}
	st := spec.Stream
	req := jobStreamRequest{jobRequest: toJobRequest(spec), TTY: st.TTY, Cols: st.Size.Cols, Rows: st.Size.Rows, Stdin: st.Stdin != nil}
	var tail tailWriter
	code, err := streamCall(ctx, s, opRunJobStream, req, st, &tail)
	res := node.JobResult{ExitCode: code, Output: tail.String()}
	if errors.Is(err, errStreamOutput) {
		err = fmt.Errorf("job %s output: %w", spec.Name, err)
	}
	return res, err
}

// errStreamOutput marks a streamCall whose caller stopped taking output.
var errStreamOutput = errors.New("the caller stopped taking output")

// streamCall is one full-duplex call carrying a stream (run-job-stream,
// exec): req, then st's input as frames up; output frames down into st
// (and tail, when set) until the Exit frame, whose exit code it returns.
// Returning resets the call, which ends the work on the node.
func streamCall(ctx context.Context, s *session, op string, req any, st *node.JobStream, tail *tailWriter) (int, error) {
	callCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	up, upW := io.Pipe()
	body, err := open(callCtx, s, op, req, up)
	if err != nil {
		_ = upW.Close()
		return 0, err
	}
	// Canceling (or returning, through the deferred cancel) closes both
	// bodies: the transport doesn't watch ctx while it waits for input.
	// The input ends with the cancel's error, not a clean EOF: the node
	// must see the call reset, not a stream whose input simply ended.
	context.AfterFunc(callCtx, func() { _, _ = upW.CloseWithError(context.Canceled), body.Close() })
	ended := make(chan struct{})
	defer close(ended)
	go sendJobInput(runframe.NewWriter(upW), upW, st, ended)

	stdout, stderr := st.Stdout, cmp.Or(st.Stderr, st.Stdout)
	if tail != nil {
		stdout, stderr = teeTail(stdout, tail), teeTail(stderr, tail)
	}
	fr := runframe.NewReader(body)
	for {
		t, p, err := fr.Next()
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			return 0, offline(ctx, s.name(), err)
		}
		var werr error
		switch t {
		case runframe.Stdout:
			_, werr = writeTo(stdout, p)
		case runframe.Stderr:
			_, werr = writeTo(stderr, p)
		case runframe.Exit:
			var end jobStreamEnd
			if err := json.Unmarshal(p, &end); err != nil {
				return 0, fmt.Errorf("server %s: bad stream end: %w", s.name(), err)
			}
			if end.Error != nil {
				return end.ExitCode, end.Error
			}
			if err := ctx.Err(); err != nil {
				// Canceled while the end was on its way: the work was
				// stopped, however it ended on the node.
				return end.ExitCode, err
			}
			return end.ExitCode, nil
		}
		if werr != nil {
			// The caller stopped taking output: returning resets the call
			// and the work ends on the node.
			return 0, fmt.Errorf("%w: %w", errStreamOutput, werr)
		}
	}
}

// writeTo writes p to w, if any.
func writeTo(w io.Writer, p []byte) (int, error) {
	if w == nil {
		return len(p), nil
	}
	return w.Write(p)
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
		serveStream(w, r, up, a.TTY, a.Cols, a.Rows, a.Stdin, func(st *node.JobStream) (int, error) {
			spec := a.node()
			spec.Stream = st
			res, err := rt.RunJob(r.Context(), spec)
			return res.ExitCode, err
		})
	}
}

// serveStream answers a stream call on the node (run-job-stream, exec):
// 200, then run's output as frames while it reads its input from up's
// frames, then one Exit frame with how it ended.
func serveStream(w http.ResponseWriter, r *http.Request, up io.Reader, tty bool, cols, rows uint16, stdin bool, run func(*node.JobStream) (int, error)) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		return
	}
	// The runtime may still be writing when run returns (a job's or a
	// session's output copy, once its caller's end frees it): out takes
	// no write after the end, which the handler's return would make a
	// write to a finished response.
	out := &endedWriter{w: flushWriter{w, rc}}
	defer out.end()
	fw := runframe.NewWriter(out)
	resize := make(chan node.TermSize, 1)
	st := &node.JobStream{
		Stdout: fw.Stream(runframe.Stdout), Stderr: fw.Stream(runframe.Stderr),
		TTY: tty, Size: node.TermSize{Cols: cols, Rows: rows}, Resize: resize,
	}
	var stdinW *io.PipeWriter
	if stdin {
		var pr *io.PipeReader
		pr, stdinW = io.Pipe()
		st.Stdin = pr
	}
	go receiveJobInput(runframe.NewReader(up), stdinW, resize)
	code, err := run(st)
	if stdinW != nil {
		_ = stdinW.Close() // ends the runtime's copy of the input
	}
	end := jobStreamEnd{ExitCode: code}
	if err != nil {
		end.Error = wireError(err)
	}
	raw, _ := json.Marshal(end)
	_ = fw.Frame(runframe.Exit, raw)
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

// endedWriter passes writes to w until end, then refuses them. end waits
// for a write in progress.
type endedWriter struct {
	mu    sync.Mutex
	w     io.Writer
	ended bool
}

func (e *endedWriter) Write(p []byte) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ended {
		return 0, io.ErrClosedPipe
	}
	return e.w.Write(p)
}

func (e *endedWriter) end() {
	e.mu.Lock()
	e.ended = true
	e.mu.Unlock()
}
