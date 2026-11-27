package api

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
	"github.com/NordTempo/tempoDock/internal/node"
)

// Shells in a running container (`dock exec`, ADR 0111): `GET
// .../exec` upgrades to apitypes.ExecProtocol and carries runframe
// frames as a streamed run does, ending with an Exit frame holding
// apitypes.ExecEnd. Query parameters: `cmd` (repeated: the command and
// its arguments; none opens a shell), `tty`, `stdin`, `cols`, `rows`.
// Bearer tokens only; off in the public demo. A locked environment
// refuses tokens without filing an access request: a session can't wait
// for an approval, and there is nothing to run without it.

func registerExec(s *Server, a huma.API, mux *http.ServeMux) {
	mux.HandleFunc("GET "+servicePathT+"/exec", s.demoOff(s.streamExec))
	authed(s, a, huma.Operation{
		OperationID: "create-exec-ticket", Method: http.MethodPost, Path: servicePathT + "/exec-tickets",
		Summary: "Get a ticket for a shell", Tags: []string{"runs"},
		Description: "A single-use ticket, good for 30 seconds, that opens a shell in the service's running container over a WebSocket: " +
			"`GET " + servicePathT + "/exec/ws?cols=&rows=` with the subprotocols `" + apitypes.ExecProtocol + "` and the ticket, from the console's own origin. " +
			"The socket's binary messages carry runframe frames, as `dock exec` does. Checks what the session will: admin on the environment (the shell permission), the lock, a running container. " +
			"The session is audited as service.exec when it opens. With a token, `dock exec` (`GET .../exec`) needs no ticket.",
	}, func(ctx context.Context, actor core.Actor, in *ServicePath) (*out[apitypes.ExecTicket], error) {
		return reply(s.core.ExecTicket(ctx, actor, in.ref()))
	})
	mux.HandleFunc("GET "+servicePathT+"/exec/ws", s.demoOff(s.execSocket))
}

// streamExec serves one exec session: authenticate, open it in core
// (permission, lock, the audit), answer 101, then run it connected to the
// client's frames. Errors before the upgrade are problem responses.
func (s *Server) streamExec(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.upgradeActor(w, r, apitypes.ExecProtocol, "`dock exec` does")
	if !ok {
		return
	}
	q := r.URL.Query()
	size, bad := termSize(q)
	if len(bad) > 0 {
		s.writeProblem(w, notANumber(bad))
		return
	}
	tty, stdin := q.Get("tty") == "true", q.Get("stdin") == "true"
	ref := ServicePath{EnvPath: EnvPath{ProjectPath: ProjectPath{Project: r.PathValue("project")}, Environment: r.PathValue("environment")}, Service: r.PathValue("service")}
	ex, err := s.core.OpenExec(r.Context(), actor, ref.ref(), q["cmd"], tty)
	if err != nil {
		s.writeProblem(w, s.problem(err))
		return
	}
	conn, brw, ok := s.switchProtocols(w, apitypes.ExecProtocol)
	if !ok {
		ex.Abandon()
		return
	}
	defer func() { _ = conn.Close() }()
	serveFrames(r.Context(), conn, brw, tty, size, stdin, func(ctx context.Context, st *node.JobStream) any { return ex.Wait(ctx, st) })
}

// execSocket serves a browser's exec session: a WebSocket from the
// console's own origin presenting an exec ticket (ExecTicket) as its
// second subprotocol, whose binary messages carry runframe frames both
// ways. The session runs as the person the ticket was made for, checked
// again by OpenExec. Errors before the upgrade are problem responses.
func (s *Server) execSocket(w http.ResponseWriter, r *http.Request) {
	// Browsers don't apply CORS to WebSockets: a socket from another page
	// is refused here, whatever it presents.
	if origin := r.Header.Get("Origin"); origin == "" || !sameOrigin(origin, r.Host) {
		s.writeProblem(w, &apitypes.Error{Status: http.StatusForbidden, Title: http.StatusText(http.StatusForbidden), Code: apitypes.CodeForbidden,
			Detail: "a shell's WebSocket opens from the console's own page"})
		return
	}
	var ticket string
	for _, p := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		if p = strings.TrimSpace(p); p != apitypes.ExecProtocol {
			ticket = p
		}
	}
	size, bad := termSize(r.URL.Query())
	if len(bad) > 0 {
		s.writeProblem(w, notANumber(bad))
		return
	}
	ref := ServicePath{EnvPath: EnvPath{ProjectPath: ProjectPath{Project: r.PathValue("project")}, Environment: r.PathValue("environment")}, Service: r.PathValue("service")}
	actor, release, err := s.core.RedeemExecTicket(r.Context(), ticket, ref.ref())
	if err != nil {
		s.writeProblem(w, s.problem(err))
		return
	}
	ex, err := s.core.OpenExec(r.Context(), actor, ref.ref(), nil, true)
	if err != nil {
		s.writeProblem(w, s.problem(err))
		return
	}
	// A deploy between the ticket and now: the console showed another
	// release than the shell would open in.
	if ex.Release() != release {
		ex.Abandon()
		s.writeProblem(w, &apitypes.Error{Status: http.StatusConflict, Title: http.StatusText(http.StatusConflict), Code: apitypes.CodeConflict,
			Detail: "a deploy replaced the service's container since the shell was asked for; open it again"})
		return
	}
	// The server's header timeout set a deadline; a session has its own.
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{apitypes.ExecProtocol}, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		ex.Abandon()
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	conn := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer func() { _ = conn.Close() }()
	rw := bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	serveFrames(ctx, conn, rw, true, size, true, func(ctx context.Context, st *node.JobStream) any { return ex.Wait(ctx, st) })
}

// sameOrigin reports whether origin (a browser's Origin header) is the
// host the request reached.
func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, host)
}
