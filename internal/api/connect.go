package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/core"
)

// `dock connect` (ADR 0023). GET .../connect is a regular operation: the
// caller's access and what tunnels reach. GET .../tunnel carries one TCP
// connection: an HTTP/1.1 upgrade to apitypes.TunnelProtocol, then raw
// bytes both ways until either side closes. It is a plain handler next to
// Huma (bytes, not JSON) and takes bearer tokens only: browsers can't send
// the upgrade, and the session cookie never opens a tunnel.

func registerConnect(s *Server, a huma.API, mux *http.ServeMux) {
	authed(s, a, huma.Operation{
		OperationID: "get-connect", Method: http.MethodGet, Path: servicePathT + "/connect",
		Summary: "Check access for `dock connect`", Tags: []string{"data"}, Errors: dataErrors,
		Description: "What tunnels to the service reach (port, and the owner's user and database for Postgres) and the caller's data access. " +
			"Tunnels are raw access: they need data access level write with sensitive visible, and on production `confirm` set to the environment's name. " +
			"Each tunnelled connection is `GET " + servicePathT + "/tunnel` with `Connection: Upgrade` and `Upgrade: " + apitypes.TunnelProtocol + "` (bearer token only), answered with 101 and then raw bytes.",
	}, func(ctx context.Context, actor core.Actor, in *struct {
		ServicePath
		Confirm string `query:"confirm" doc:"The environment's name; needed on production"`
		Reveal  bool   `query:"reveal" doc:"Add the connection URL with its password; audited as data.reveal"`
	}) (*out[apitypes.ConnectInfo], error) {
		return reply(s.core.ConnectInfo(ctx, actor, in.ref(), core.ConnectRequest{Confirm: in.Confirm, Reveal: in.Reveal}))
	})
	mux.HandleFunc("GET "+servicePathT+"/tunnel", s.tunnel)
}

// tunnel serves one tunnel connection: authenticate, open the tunnel in
// core (access, dial, audit), answer 101 and pipe until either side ends.
// Errors before the upgrade are problem responses.
func (s *Server) tunnel(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor != 1 || !headerHas(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), apitypes.TunnelProtocol) {
		w.Header().Set("Upgrade", apitypes.TunnelProtocol)
		s.writeProblem(w, &apitypes.Error{Status: http.StatusUpgradeRequired, Title: http.StatusText(http.StatusUpgradeRequired), Code: apitypes.CodeValidation,
			Detail: "a tunnel is an HTTP/1.1 upgrade: send `Connection: Upgrade` and `Upgrade: " + apitypes.TunnelProtocol + "`; `dock connect` does"})
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		s.writeProblem(w, &apitypes.Error{Status: http.StatusUnauthorized, Title: http.StatusText(http.StatusUnauthorized), Code: apitypes.CodeUnauthorized,
			Detail: "send `Authorization: Bearer <token>`"})
		return
	}
	actor, err := s.core.AuthenticateToken(r.Context(), strings.TrimSpace(token), r.URL.Query().Get("org"))
	if err != nil {
		s.writeProblem(w, s.problem(err))
		return
	}
	from := r.RemoteAddr
	if host, _, err := net.SplitHostPort(from); err == nil {
		from = host
	}
	ref := core.ServiceRef{Project: r.PathValue("project"), Environment: r.PathValue("environment"), Service: r.PathValue("service")}
	t, err := s.core.OpenTunnel(r.Context(), actor, ref, core.TunnelRequest{Confirm: r.URL.Query().Get("confirm"), From: from})
	if err != nil {
		s.writeProblem(w, s.problem(err))
		return
	}
	conn, brw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		t.Close()
		s.opts.Logger.Error("api: tunnel hijack", "err", err)
		return
	}
	// The server's header timeout set a deadline; the tunnel has none.
	_ = conn.SetDeadline(time.Time{})
	if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + apitypes.TunnelProtocol + "\r\n\r\n")); err != nil {
		_ = conn.Close()
		t.Close()
		return
	}
	var head []byte
	if n := brw.Reader.Buffered(); n > 0 {
		head, _ = brw.Peek(n)
	}
	t.Pipe(conn, head)
}

// headerHas reports whether a comma-separated header lists token.
func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for p := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), token) {
				return true
			}
		}
	}
	return false
}

// writeProblem writes an API error outside Huma.
func (s *Server) writeProblem(w http.ResponseWriter, err error) {
	ae, ok := errors.AsType[*apitypes.Error](err)
	if !ok {
		ae = &apitypes.Error{Status: http.StatusInternalServerError, Title: http.StatusText(http.StatusInternalServerError), Code: apitypes.CodeInternal, Detail: "internal error; see the daemon log"}
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(ae)
}
