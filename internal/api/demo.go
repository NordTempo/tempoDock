package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The public demo (ADR 0100): one shared account every visitor is signed
// in as, on the fake runtime, started again from its seed every hour.

// Why demo mode refuses an operation, for its error.
const (
	demoSharedAccount = "everyone visiting the demo shares this account, so it stays as it is"
	demoSignIn        = "everyone visiting the demo signs in the same way, so that stays as it is"
	demoOtherHosts    = "the demo sends nothing to other hosts"
	demoDisk          = "the demo shares its server's disk, so it keeps no large files"
)

// demoRefused are the operations demo mode refuses, by operation ID, and
// why: what would lock the next visitor out of the demo or out of its
// account, and what would make the daemon connect to an address a visitor
// typed in (alert channels, log drains, mail, identity providers, buckets,
// Cloudflare, GitHub, releases), and what would store a large file on
// the server's disk. Everything else works, on the fake runtime, until
// the next reset.
var demoRefused = map[string]string{
	// The shared account.
	"change-password":           demoSharedAccount,
	"update-me":                 demoSharedAccount,
	"set-my-email":              demoSharedAccount,
	"start-totp":                demoSharedAccount,
	"add-totp":                  demoSharedAccount,
	"passkey-registration":      demoSharedAccount,
	"add-passkey":               demoSharedAccount,
	"remove-factor":             demoSharedAccount,
	"new-recovery-codes":        demoSharedAccount,
	"delete-user":               demoSharedAccount,
	"disable-user":              demoSharedAccount,
	"reset-password":            demoSharedAccount,
	"set-operator":              demoSharedAccount,
	"set-platform-role":         demoSharedAccount,
	"set-member-role":           demoSharedAccount,
	"remove-member":             demoSharedAccount,
	"leave-org":                 demoSharedAccount,
	"delete-org":                demoSharedAccount,
	"update-org":                demoSharedAccount,
	"set-console-org-member":    demoSharedAccount,
	"remove-console-org-member": demoSharedAccount,
	"suspend-console-org":       demoSharedAccount,
	"stop-console-org":          demoSharedAccount,
	"delete-console-org":        demoSharedAccount,
	// How everyone signs in.
	"set-sign-in-settings":  demoSignIn,
	"set-platform-security": demoSignIn,
	"set-org-two-factor":    demoSignIn,
	"set-access-model":      demoSignIn,
	"create-sso-provider":   demoSignIn,
	"update-sso-provider":   demoSignIn,
	// Connections to addresses a visitor names.
	"create-alert-channel":         demoOtherHosts,
	"test-alert-channel":           demoOtherHosts,
	"set-heartbeat":                demoOtherHosts,
	"test-heartbeat":               demoOtherHosts,
	"add-log-drain":                demoOtherHosts,
	"test-log-drain":               demoOtherHosts,
	"add-status-hook":              demoOtherHosts,
	"test-status-hook":             demoOtherHosts,
	"set-email":                    demoOtherHosts,
	"test-email":                   demoOtherHosts,
	"create-cloudflare":            demoOtherHosts,
	"update-cloudflare":            demoOtherHosts,
	"refresh-cloudflare":           demoOtherHosts,
	"retry-cloudflare":             demoOtherHosts,
	"create-cloudflare-redirect":   demoOtherHosts,
	"update-cloudflare-redirect":   demoOtherHosts,
	"retry-cloudflare-redirect":    demoOtherHosts,
	"create-storage":               demoOtherHosts,
	"update-storage":               demoOtherHosts,
	"configure-backups":            demoOtherHosts,
	"move-backups":                 demoOtherHosts,
	"run-control-plane-backup":     demoOtherHosts,
	"migrate-registry":             demoOtherHosts,
	"create-github-app":            demoOtherHosts,
	"set-github-app-client-secret": demoOtherHosts,
	"install-github-app":           demoOtherHosts,
	"connect-git":                  demoOtherHosts,
	"deploy-git":                   demoOtherHosts,
	"check-site-tracking":          demoOtherHosts,
	"start-update":                 demoOtherHosts,

	// Large files: a vulnerability database is up to 1 GB. Source uploads
	// are capped by the demo's max_upload_mb instead.
	"import-scan-database": demoDisk,
}

// demoRefusal is the error for an operation demo mode refuses, or nil.
// Until the demo is ready nobody can sign in to it, and its seed
// (cmd/dock-demo) may do anything.
func (s *Server) demoRefusal(ctx context.Context, op *huma.Operation) error {
	why, ok := demoRefused[op.OperationID]
	if !ok || !s.core.Demo() {
		return nil
	}
	if ready, err := s.core.DemoReady(ctx); err != nil || !ready {
		return err
	}
	return huma.Error403Forbidden("Not in the demo: " + why + ". Install Dock on your own server to try it.")
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
