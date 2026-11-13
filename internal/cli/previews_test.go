package cli

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
	"github.com/NordTempo/tempoDock/internal/core"
)

// An admin turns previews on for a service and sets its environment's
// numbers; a pull request's preview shows in `dock environment list` and
// `dock env previews`; closing it asks first (--yes off a terminal);
// turning the service's previews off turns them off.
func TestPreviews(t *testing.T) {
	setup(t)
	// GitHub answers nothing; the preview's build and comment fail
	// quietly, which this test doesn't look at.
	gh := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(gh.Close)
	srv := newTestServerWith(t, func(o *core.Options) {
		o.GitHubAPI = gh.URL
		o.Config.Hooks.PublicHost = "hooks.example.com"
	})
	ctx := context.Background()
	web := core.ServiceRef{Project: "shop", Environment: "staging", Service: "web"}
	if _, err := srv.core.CreateProject(ctx, sys, apitypes.CreateProjectRequest{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.core.CreateEnvironment(ctx, sys, "shop", apitypes.CreateEnvironmentRequest{Name: "staging"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.core.CreateService(ctx, sys, web.Env(), apitypes.CreateServiceRequest{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.core.GenerateDomain(ctx, sys, web); err != nil {
		t.Fatal(err)
	}
	conn, err := srv.core.GitConnect(ctx, sys, web, apitypes.GitConnectRequest{Repo: "acme/shop", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])

	code, stdout, stderr := exec(t, "env", "previews", "-p", "shop")
	if code != 0 || !strings.Contains(stdout, "No service of shop builds pull requests") {
		t.Fatalf("previews while off: exit %d\n%s%s", code, stdout, stderr)
	}
	sp := ok[apitypes.ServicePreviews](t, "service", "previews", "on", "-s", "web", "-e", "staging", "-p", "shop", "--copy", "PUBLIC_URL", "--forks")
	if !sp.Build || !sp.AllowForks || strings.Join(sp.CopyVariables, " ") != "PUBLIC_URL" {
		t.Fatalf("service previews on = %+v", sp)
	}
	// A flag alone changes only itself.
	if sp := ok[apitypes.ServicePreviews](t, "service", "previews", "-s", "web", "-e", "staging", "-p", "shop", "--forks=false"); !sp.Build || sp.AllowForks || len(sp.CopyVariables) != 1 {
		t.Fatalf("service previews --forks=false = %+v", sp)
	}
	ep := ok[apitypes.EnvironmentPreviews](t, "env", "previews", "set", "staging", "-p", "shop", "--max", "2", "--copy", "SENTRY_DSN")
	if ep.MaxPreviews != 2 || strings.Join(ep.CopyShared, " ") != "SENTRY_DSN" {
		t.Fatalf("env previews set = %+v", ep)
	}
	set := ok[apitypes.PreviewSettings](t, "env", "previews", "-p", "shop")
	if !set.Enabled || set.Base != "staging" || set.MaxPreviews != 2 || strings.Join(set.CopyVariables, " ") != "shared.SENTRY_DSN web.PUBLIC_URL" {
		t.Fatalf("previews = %+v", set)
	}

	body := `{"action":"opened","number":12,"pull_request":{"html_url":"https://github.com/acme/shop/pull/12","title":"Fix the checkout page for guests who pay by card",
		"updated_at":"2026-11-03T10:00:00Z","user":{"login":"kari"},
		"head":{"ref":"fix-checkout","sha":"abc1234def5678abc1234def5678abc1234def56","repo":{"id":7,"full_name":"acme/shop"}},
		"base":{"ref":"main","repo":{"id":7,"full_name":"acme/shop"}}},
		"repository":{"id":7,"full_name":"acme/shop"},"sender":{"login":"kari"}}`
	mac := hmac.New(sha256.New, []byte(conn.Secret))
	mac.Write([]byte(body))
	msg, err := srv.core.GitWebhook(ctx, core.GitDelivery{
		HookID: path.Base(conn.Connection.WebhookURL), Event: "pull_request", ID: "d-1",
		Signature: "sha256=" + hex.EncodeToString(mac.Sum(nil)), Body: []byte(body),
	})
	if err != nil || !strings.Contains(msg, "preview pr-12 created") {
		t.Fatalf("pull request event = %q, %v", msg, err)
	}

	code, stdout, stderr = exec(t, "environment", "list", "-p", "shop")
	if code != 0 || !strings.Contains(stdout, "PULL REQUEST") || !strings.Contains(stdout, "#12 acme/shop") {
		t.Fatalf("environment list: exit %d\n%s%s", code, stdout, stderr)
	}
	envs := ok[[]apitypes.Environment](t, "environment", "list", "-p", "shop")
	i := slices.IndexFunc(envs, func(e apitypes.Environment) bool { return e.Name == "pr-12" })
	if i < 0 || envs[i].Preview == nil || envs[i].Preview.Number != 12 || envs[i].Preview.Author != "kari" {
		t.Fatalf("environment list --json = %+v", envs)
	}
	code, stdout, stderr = exec(t, "env", "previews", "-p", "shop")
	if code != 0 || !strings.Contains(stdout, "staging/web") || !strings.Contains(stdout, "acme/shop@main") || !strings.Contains(stdout+stderr, "at most 2 open") ||
		!strings.Contains(stdout, "pr-12        #12 Fix the checkout page for guests wh…") || !strings.Contains(stdout, "web-shop-pr-12.") ||
		!strings.Contains(stdout, " abc1234\n") {
		t.Fatalf("previews: exit %d\n%s%s", code, stdout, stderr)
	}

	if code, _, stderr := exec(t, "env", "previews", "close", "pr-12", "-p", "shop"); code != int(output.ExitUsage) || !strings.Contains(stderr, "--yes") {
		t.Fatalf("close without --yes off a terminal: exit %d %s", code, stderr)
	}
	if pv := ok[apitypes.Preview](t, "environment", "previews", "close", "pr-12", "-p", "shop", "--yes"); pv.Status != apitypes.PreviewClosed || pv.ClosedAt == nil {
		t.Fatalf("close = %+v", pv)
	}
	if sp := ok[apitypes.ServicePreviews](t, "service", "previews", "off", "-s", "web", "-e", "staging", "-p", "shop"); sp.Build {
		t.Fatalf("service previews off = %+v", sp)
	}
	if set := ok[apitypes.PreviewSettings](t, "env", "previews", "-p", "shop"); set.Enabled || len(set.Previews) != 0 {
		t.Fatalf("previews after off = %+v", set)
	}
}
