package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The public demo (ADR 0100): one shared account every visitor is signed
// in as, on the fake runtime, started again from its seed every hour. It
// is view-only: visitors look around a small company's mock data and
// change nothing, so nothing they send is kept, run or passed on.

// demoAllowed are the operations besides GETs the demo lets visitors
// call: signing in and out, and reads sent as POST.
var demoAllowed = map[string]bool{
	"demo-sign-in":      true,
	"logout":            true,
	"check-request":     true, // whether an access request can still be decided
	"plan-project-file": true, // what applying a project file would change
}

// demoViewOnly is what the demo answers everything that changes something.
const demoViewOnly = "Not in the demo: it is view-only, so look around as much as you like. Install Dock on your own server to try it."

// demoRefusal refuses, in the demo, every operation that isn't a GET or in
// demoAllowed. Until the demo is ready nobody can sign in to it, and its
// seed (cmd/dock-demo) may do anything.
func (s *Server) demoRefusal(ctx context.Context, op *huma.Operation) error {
	if !s.core.Demo() || op.Method == http.MethodGet || demoAllowed[op.OperationID] {
		return nil
	}
	if ready, err := s.core.DemoReady(ctx); err != nil || !ready {
		return err
	}
	return huma.Error403Forbidden(demoViewOnly)
}

// demoOff wraps a raw handler that acts on a service (`dock run`, tunnels)
// so the demo refuses it: those are GETs that do things. It checks per
// request: routes are also registered without a core, for the spec.
func (s *Server) demoOff(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.core.Demo() {
			s.writeProblem(w, &apitypes.Error{Status: http.StatusForbidden, Title: http.StatusText(http.StatusForbidden), Code: apitypes.CodeForbidden, Detail: demoViewOnly})
			return
		}
		h(w, r)
	}
}

// demoInput is how a demo sign-in arrived.
type demoInput struct{ client client }

// Resolve records how the request arrived.
func (in *demoInput) Resolve(ctx huma.Context) []error {
	in.client = clientOf(ctx)
	return nil
}

func registerDemo(s *Server, a huma.API) {
	tags := []string{"auth"}
	public(a, huma.Operation{
		OperationID: "demo-status", Method: http.MethodGet, Path: Prefix + "/auth/demo",
		Summary: "Whether the demo is ready", Tags: tags,
		Description: "Only on the public demo (ADR 0100), 404 elsewhere. 503 while it is being seeded: the demo's health check, so a new demo takes traffic once it is ready.",
		Errors:      []int{http.StatusNotFound, http.StatusServiceUnavailable},
	}, func(ctx context.Context, _ *struct{}) (*out[apitypes.DemoStatus], error) {
		ready, err := s.core.DemoReady(ctx)
		if err != nil {
			return nil, s.problem(err)
		}
		if !ready {
			return nil, huma.Error503ServiceUnavailable("the demo is starting; try again in a few seconds")
		}
		return reply(apitypes.DemoStatus{Ready: true}, nil)
	})
	public(a, huma.Operation{
		OperationID: "demo-sign-in", Method: http.MethodPost, Path: Prefix + "/auth/demo",
		Summary: "Sign in to the demo", Tags: tags,
		Description: "Only on the public demo (ADR 0100): sets a session cookie for the demo's shared account, no password needed. 404 elsewhere, 503 while the demo is starting.",
		Errors:      []int{http.StatusNotFound, http.StatusServiceUnavailable},
	}, func(ctx context.Context, in *demoInput) (*loginOutput, error) {
		res, err := s.core.DemoSignIn(ctx, in.client.ip)
		if err != nil {
			return nil, s.problem(err)
		}
		return in.client.signedIn(res), nil
	})
}
