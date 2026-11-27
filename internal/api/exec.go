package api

import (
	"context"
	"net/http"

	"github.com/NordTempo/tempoDock/internal/apitypes"
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

func registerExec(s *Server, mux *http.ServeMux) {
	mux.HandleFunc("GET "+servicePathT+"/exec", s.demoOff(s.streamExec))
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
