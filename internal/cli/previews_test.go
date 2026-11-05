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

// An admin turns previews on; a pull request's preview shows in
// `dock environment list` and `dock env previews`; closing it asks first
// (--yes off a terminal); disabling turns them off.
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
	if code != 0 || !strings.Contains(stdout, "Previews of pull requests are off for shop") {
		t.Fatalf("previews while off: exit %d\n%s%s", code, stdout, stderr)
	}
	set := ok[apitypes.PreviewSettings](t, "env", "previews", "enable", "staging", "-p", "shop", "--max", "2", "--copy", "web.PUBLIC_URL", "--copy", "shared.SENTRY_DSN", "--forks")
	if !set.Enabled || set.Base != "staging" || set.MaxPreviews != 2 || !set.AllowForks || strings.Join(set.CopyVariables, " ") != "shared.SENTRY_DSN web.PUBLIC_URL" {
		t.Fatalf("enable = %+v", set)
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
	if code != 0 || !strings.Contains(stdout, "on, cloned from staging") || !strings.Contains(stdout, "shared.SENTRY_DSN, web.PUBLIC_URL") ||
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
	if set := ok[apitypes.PreviewSettings](t, "env", "previews", "disable", "-p", "shop"); set.Enabled || len(set.Previews) != 0 {
		t.Fatalf("disable = %+v", set)
	}
}
