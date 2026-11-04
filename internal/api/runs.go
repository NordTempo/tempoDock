package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/runframe"
)

// One-off commands (`dock run`, ADR 0048). POST .../runs runs one in the
// background (the UI); GET .../run streams one: an HTTP/1.1 upgrade to
// apitypes.RunProtocol, then runframe frames both ways, as `dock run`
// uses it. Like tunnels, the stream takes bearer tokens only.

// runCommandInput is run-command's input. A streamed run a locked
// environment refuses is filed as this operation (runCommandOp), so
// approving it runs the command in the background.
type runCommandInput struct {
	ServicePath
	Body apitypes.RunCommandRequest
}

// runCommandOp is POST .../runs.
var runCommandOp = huma.Operation{OperationID: "run-command", Summary: "Run a command"}

func registerRuns(s *Server, a huma.API, mux *http.ServeMux) {
	tags := []string{"runs"}
	authed(s, a, huma.Operation{
		OperationID: runCommandOp.OperationID, Method: http.MethodPost, Path: servicePathT + "/runs",
		Summary: runCommandOp.Summary, Tags: tags, DefaultStatus: http.StatusAccepted,
		Description: "Runs a one-off command in the background in the service's active image: its resolved variables, CPU and memory limits and its environment's private network, with `/bin/sh -c`. " +
			"The answer is the run, running; run events follow it. Audited as service.run with the exit code. Scope: deploy. " +
			"To stream output and input (a console), `GET " + servicePathT + "/run` with `Connection: Upgrade` and `Upgrade: " + apitypes.RunProtocol + "` and the same fields as query parameters, plus `tty`, `stdin`, `cols` and `rows` (bearer token only); `dock run` does.",
	}, func(ctx context.Context, actor core.Actor, in *runCommandInput) (*out[apitypes.CommandRun], error) {
		return reply(s.core.StartRun(ctx, actor, in.ref(), in.Body))
	})
	authed(s, a, huma.Operation{
		OperationID: "list-runs", Method: http.MethodGet, Path: servicePathT + "/runs",
		Summary: "List a service's runs", Tags: tags,
		Description: "Its latest one-off commands, newest first, without their output.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[[]apitypes.CommandRun], error) {
		return reply(s.core.ListRuns(ctx, actor, in.ref()))
	})
	authed(s, a, huma.Operation{
		OperationID: "get-run", Method: http.MethodGet, Path: servicePathT + "/runs/{id}",
		Summary: "Get a run", Tags: tags,
		Description: "One run with the tail of its output (none for terminal sessions). Scope: deploy, as output can show the service's variables.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		ID int64 `path:"id"`
	}) (*out[apitypes.CommandRun], error) {
		return reply(s.core.GetRun(ctx, actor, in.ref(), in.ID))
	})
	mux.HandleFunc("GET "+servicePathT+"/run", s.streamRun)
}

// streamRun serves one streamed command: authenticate, open the run in
// core (permission, checks, the record), answer 101, then run it with
// its input and terminal size from the client's frames and its output
// to them, and end with an Exit frame holding the run. Errors before the
// upgrade are problem responses. The client going away cancels the run.
func (s *Server) streamRun(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor != 1 || !headerHas(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), apitypes.RunProtocol) {
		w.Header().Set("Upgrade", apitypes.RunProtocol)
		s.writeProblem(w, &apitypes.Error{Status: http.StatusUpgradeRequired, Title: http.StatusText(http.StatusUpgradeRequired), Code: apitypes.CodeValidation,
			Detail: "a streamed run is an HTTP/1.1 upgrade: send `Connection: Upgrade` and `Upgrade: " + apitypes.RunProtocol + "`; `dock run` does. Without streaming, POST to .../runs"})
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		s.writeProblem(w, &apitypes.Error{Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Code: apitypes.CodeUnauthorized,
			Detail: "send `Authorization: Bearer <token>`"})
		return
	}
	q := r.URL.Query()
	actor, err := s.core.AuthenticateToken(r.Context(), strings.TrimSpace(token), q.Get("org"))
	if err != nil {
		s.writeProblem(w, s.problem(err))
		return
	}
	req := apitypes.RunCommandRequest{Command: q.Get("command"), Volumes: apitypes.RunVolumes(q.Get("volumes")), NoSnapshot: q.Get("no_snapshot") == "true"}
	tty, stdin := q.Get("tty") == "true", q.Get("stdin") == "true"
	var size node.TermSize
	var bad []string
	if v := q.Get("timeout_seconds"); v != "" {
		if req.TimeoutSeconds, err = strconv.Atoi(v); err != nil {
			bad = append(bad, "timeout_seconds")
		}
	}
	for name, dst := range map[string]*uint16{"cols": &size.Cols, "rows": &size.Rows} {
		if v := q.Get(name); v != "" {
			n, err := strconv.ParseUint(v, 10, 16)
			if err != nil {
				bad = append(bad, name)
			}
			*dst = uint16(n)
		}
	}
	if len(bad) > 0 {
		s.writeProblem(w, &apitypes.Error{Status: http.StatusBadRequest, Title: http.StatusText(http.StatusBadRequest), Code: apitypes.CodeValidation,
			Detail: "not a number: " + strings.Join(bad, ", ")})
		return
	}
	in := runCommandInput{ServicePath: ServicePath{EnvPath: EnvPath{ProjectPath: ProjectPath{Project: r.PathValue("project")}, Environment: r.PathValue("environment")}, Service: r.PathValue("service")}, Body: req}
	run, err := s.core.OpenRun(r.Context(), actor, in.ref(), req, tty)
	if err != nil {
		// A terminal can't wait for an approval: the request runs it in
		// the background, as POST .../runs would.
		ctx := r.Context()
		if reason := strings.TrimSpace(r.Header.Get(reasonHeader)); reason != "" {
			ctx = core.WithReason(ctx, reason[:min(len(reason), maxReason)])
		}
		s.writeProblem(w, s.problem(s.fileRequest(ctx, actor, runCommandOp, &in, err)))
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		run.Abandon()
		s.opts.Logger.Error("api: run hijack", "err", err)
		return
	}
	defer func() { _ = conn.Close() }()
	// The server's header timeout set a deadline; a run has its own.
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + apitypes.RunProtocol + "\r\n\r\n")); err != nil {
		run.Abandon()
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	fw := runframe.NewWriter(conn)
	resize := make(chan node.TermSize, 1)
	st := &node.JobStream{Stdout: fw.Stream(runframe.Stdout), Stderr: fw.Stream(runframe.Stderr), TTY: tty, Size: size, Resize: resize}
	var stdinW *io.PipeWriter
	if stdin {
		var pr *io.PipeReader
		pr, stdinW = io.Pipe()
		st.Stdin = pr
	}
	go readRunInput(runframe.NewReader(brw.Reader), stdinW, resize, cancel)
	res := run.Wait(ctx, st)
	if stdinW != nil {
		_ = stdinW.Close()
	}
	raw, _ := json.Marshal(res)
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_ = fw.Frame(runframe.Exit, raw)
}

// readRunInput reads a streamed run's input frames from the client: its
// stdin (nil: it takes none) and terminal sizes. The connection ending
// means the client went away: cancel stops the run.
func readRunInput(fr *runframe.Reader, stdin *io.PipeWriter, resize chan node.TermSize, cancel context.CancelFunc) {
	defer close(resize)
	for {
		t, p, err := fr.Next()
		if err != nil {
			if stdin != nil {
				_ = stdin.CloseWithError(err)
			}
			cancel()
			return
		}
		switch t {
		case runframe.Stdin:
			if stdin != nil {
				if _, err := stdin.Write(p); err != nil && !errors.Is(err, io.ErrClosedPipe) {
					cancel()
					return
				}
			}
		case runframe.CloseStdin:
			if stdin != nil {
				_ = stdin.Close()
			}
		case runframe.Resize:
			if cols, rows, err := runframe.ParseResize(p); err == nil {
				select {
				case <-resize: // only the latest size matters
				default:
				}
				resize <- node.TermSize{Cols: cols, Rows: rows}
			}
		}
	}
}
