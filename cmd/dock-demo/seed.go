package main

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
)

// The demo's people and hostnames. Hostnames are under example.com,
// which nobody owns, so no visitor's click reaches a real site.
const (
	// seedUser is the first user: Ada builds Acme's projects, so the
	// audit log and releases name her.
	seedUser = "ada@acme.example"
	// pendingDomain waits for DNS forever (DOCK_FAKE_PENDING_DOMAINS), so
	// the demo shows a domain that isn't pointed at Dock yet.
	pendingDomain = "blog.example.com"
	// shopHost is the shop's domain, the one with analytics.
	shopHost = "shop.example.com"
)

// seedConfig is what seed needs from run.
type seedConfig struct {
	URL      string
	Password string
	// DemoUser is [demo] user, created last.
	DemoUser string
	// Spool is DOCK_FAKE_SPOOL: seed writes the shop's visits there
	// before analytics starts reading it.
	Spool string
}

// seed fills a fresh demo through the API: an org with three projects
// (a shop, a blog, a docs site) and an archived one, people, tokens,
// analytics, status pages and a locked environment with a waiting access
// request. The demo's user comes last: once it exists the demo is ready.
func seed(ctx context.Context, log *slog.Logger, sc seedConfig) error {
	if err := waitHealthy(ctx, sc.URL); err != nil {
		return err
	}
	start := time.Now()
	c, err := newClient(ctx, sc.URL, seedUser, sc.Password)
	if err != nil {
		return err
	}
	s := &seeder{c: c, ctx: ctx}
	s.org()
	s.shop()
	s.blog()
	s.smaller()
	s.people()
	s.analytics(sc.Spool)
	s.statusPages()
	s.lockBlog()
	if s.err != nil {
		return s.err
	}
	// Last: the demo is ready once its user exists.
	if err := c.do(ctx, http.MethodPost, "/users", apitypes.CreateUserRequest{
		Email: sc.DemoUser, Name: "Demo", Password: rand.Text(), Role: apitypes.ScopeAdmin, Operator: true,
	}, nil); err != nil {
		return err
	}
	log.Info("seeded the demo", "took", time.Since(start).Round(time.Millisecond))
	return nil
}

// seeder makes the seed's calls, keeping the first error: the demo is
// seeded in full or not at all.
type seeder struct {
	c   *client
	ctx context.Context
	err error
	// webRelease and webService are shop/production/web's live release
	// and its service, which the shop's visits name.
	webRelease, webService int64
}

// call is client.do, skipped after an error.
func (s *seeder) call(method, path string, body, out any) {
	if s.err == nil {
		s.err = s.c.do(s.ctx, method, path, body, out)
	}
}

// svc is a service's API path.
func svc(project, env, name string) string {
	return fmt.Sprintf("/projects/%s/environments/%s/services/%s", project, env, name)
}

// service creates a service and returns its ID.
func (s *seeder) service(project, env string, req apitypes.CreateServiceRequest) int64 {
	var out apitypes.Service
	s.call(http.MethodPost, fmt.Sprintf("/projects/%s/environments/%s/services", project, env), req, &out)
	return out.ID
}

// deploy deploys image to a service and returns the release's ID.
func (s *seeder) deploy(path, image string) int64 {
	var out apitypes.Release
	s.call(http.MethodPost, path+"/releases", apitypes.DeployImageRequest{Image: image}, &out)
	return out.ID
}

// org names the org, the seed user and the server.
func (s *seeder) org() {
	s.call(http.MethodPatch, "/me", apitypes.UpdateMeRequest{Name: "Ada Lovelace"}, nil)
	s.call(http.MethodPatch, "/org", apitypes.UpdateOrgRequest{Name: "Acme", Slug: "acme"}, nil)
	// Acme from now on, also once the seed user is in a second org.
	s.c.org = "acme"
	// The control plane is named after its host, a container ID here.
	var servers []apitypes.Server
	s.call(http.MethodGet, "/servers", nil, &servers)
	if s.err == nil && len(servers) > 0 {
		name := "acme-1"
		s.call(http.MethodPatch, "/servers/"+url.PathEscape(servers[0].Name), apitypes.UpdateServerRequest{Name: &name}, nil)
	}
}

// shop is the busiest project: five services in production with a
// release history, domains, variables with a staged change, uptime
// checks, jobs, a volume and a snapshot, and a staging environment.
func (s *seeder) shop() {
	s.call(http.MethodPost, "/projects", apitypes.CreateProjectRequest{Name: "shop"}, nil)
	s.call(http.MethodPost, "/projects/shop/environments", apitypes.CreateEnvironmentRequest{Name: "staging", Kind: apitypes.KindNonProduction}, nil)

	s.webService = s.service("shop", "production", apitypes.CreateServiceRequest{Name: "web", Image: "ghcr.io/acme/shop-web:1.0.0", Port: 3000, HealthCheckPath: "/healthz"})
	s.service("shop", "production", apitypes.CreateServiceRequest{Name: "api", Image: "ghcr.io/acme/shop-api:2.3.0", Port: 8080, HealthCheckPath: "/health"})
	s.service("shop", "production", apitypes.CreateServiceRequest{Name: "worker", Image: "ghcr.io/acme/shop-worker:2.3.0", StartCommand: "node worker.js"})
	s.service("shop", "production", apitypes.CreateServiceRequest{Name: "db", Template: apitypes.TemplatePostgres})
	s.service("shop", "production", apitypes.CreateServiceRequest{Name: "cache", Template: apitypes.TemplateRedis})

	web, api := svc("shop", "production", "web"), svc("shop", "production", "api")
	s.call(http.MethodPatch, "/projects/shop/environments/production/variables", apitypes.SetVariablesRequest{
		Service: "web", Apply: true,
		Set: map[string]string{"NODE_ENV": "production", "API_URL": "https://api." + shopHost, "STRIPE_PUBLIC_KEY": "pk_live_51Hx0demo", "SESSION_SECRET": "demo-" + rand.Text()},
	}, nil)
	s.call(http.MethodPatch, "/projects/shop/environments/production/variables", apitypes.SetVariablesRequest{
		Service: "api", Apply: true,
		Set: map[string]string{"DATABASE_URL": "${{db.DATABASE_URL}}", "REDIS_URL": "${{cache.REDIS_URL}}", "LOG_LEVEL": "info"},
	}, nil)
	for _, v := range []string{"1.0.1", "1.1.0", "1.2.0", "1.2.1", "1.3.0"} {
		s.webRelease = s.deploy(web, "ghcr.io/acme/shop-web:"+v)
	}
	for _, v := range []string{"2.3.1", "2.4.0"} {
		s.deploy(api, "ghcr.io/acme/shop-api:"+v)
	}
	s.deploy(svc("shop", "production", "worker"), "ghcr.io/acme/shop-worker:2.4.0")

	s.call(http.MethodPost, web+"/domains", apitypes.AddDomainRequest{Hostname: shopHost}, nil)
	s.call(http.MethodPost, web+"/domains", apitypes.AddDomainRequest{Hostname: "www." + shopHost, RedirectTo: shopHost}, nil)
	s.call(http.MethodPost, web+"/domains/generate", nil, nil)
	s.call(http.MethodPost, api+"/domains", apitypes.AddDomainRequest{Hostname: "api." + shopHost}, nil)
	s.call(http.MethodPost, web+"/uptime-checks", apitypes.AddUptimeCheckRequest{URL: "https://" + shopHost + "/healthz", IntervalSeconds: 60}, nil)
	s.call(http.MethodPost, api+"/uptime-checks", apitypes.AddUptimeCheckRequest{URL: "https://api." + shopHost + "/health", IntervalSeconds: 30, ExpectStatus: "200"}, nil)
	off := false
	s.call(http.MethodPut, api+"/jobs/cleanup", apitypes.SetJobRequest{Command: "node scripts/cleanup.js", Schedule: "0 3 * * *"}, nil)
	s.call(http.MethodPut, api+"/jobs/digest", apitypes.SetJobRequest{Command: "node scripts/digest.js", Schedule: "*/15 * * * *", Enabled: &off}, nil)
	s.call(http.MethodPost, web+"/volumes", apitypes.AddVolumeRequest{Name: "uploads", MountPath: "/app/uploads", Apply: true}, nil)
	s.call(http.MethodPost, svc("shop", "production", "db")+"/snapshots", apitypes.CreateSnapshotRequest{Note: "before the orders migration"}, nil)
	s.call(http.MethodPost, api+"/runs", apitypes.RunCommandRequest{Command: "node scripts/migrate.js"}, nil)
	s.call(http.MethodPost, api+"/jobs/cleanup/runs", nil, nil)
	// A change waiting to be applied, for the Apply bar.
	s.call(http.MethodPatch, "/projects/shop/environments/production/variables", apitypes.SetVariablesRequest{
		Service: "web", Set: map[string]string{"FEATURE_CHECKOUT_V2": "true"}, Unset: []string{"STRIPE_PUBLIC_KEY"},
	}, nil)

	s.service("shop", "staging", apitypes.CreateServiceRequest{Name: "web", Image: "ghcr.io/acme/shop-web:1.4.0-rc1", Port: 3000})
	s.service("shop", "staging", apitypes.CreateServiceRequest{Name: "db", Template: apitypes.TemplatePostgres})
	// A worker that keeps crashing, for Needs attention and its diagnosis.
	s.service("shop", "staging", apitypes.CreateServiceRequest{Name: "worker", Image: "ghcr.io/acme/shop-worker:2.5.0-rc1", StartCommand: "node worker.js"})
	s.call(http.MethodPatch, "/projects/shop/environments/staging/variables", apitypes.SetVariablesRequest{
		Service: "worker", Apply: true, Set: map[string]string{"DEMO_CRASH_LOOP": "1"},
	}, nil)
}

// blog is a Ghost blog on MySQL whose domain waits for DNS.
func (s *seeder) blog() {
	s.call(http.MethodPost, "/projects", apitypes.CreateProjectRequest{Name: "blog"}, nil)
	s.call(http.MethodPost, "/projects/blog/environments", apitypes.CreateEnvironmentRequest{Name: "staging", Kind: apitypes.KindNonProduction}, nil)
	s.service("blog", "production", apitypes.CreateServiceRequest{Name: "ghost", Image: "ghost:5", Port: 2368})
	s.service("blog", "production", apitypes.CreateServiceRequest{Name: "mysql", Template: apitypes.TemplateMySQL})
	s.service("blog", "staging", apitypes.CreateServiceRequest{Name: "ghost", Image: "ghost:5", Port: 2368})
	ghost := svc("blog", "production", "ghost")
	s.deploy(ghost, "ghost:5")
	s.call(http.MethodPost, ghost+"/domains", apitypes.AddDomainRequest{Hostname: pendingDomain}, nil)
}

// smaller adds a docs site and an archived project.
func (s *seeder) smaller() {
	s.call(http.MethodPost, "/projects", apitypes.CreateProjectRequest{Name: "docs"}, nil)
	s.service("docs", "production", apitypes.CreateServiceRequest{Name: "web", Image: "nginx:1.27", Port: 80})
	s.call(http.MethodPost, "/projects", apitypes.CreateProjectRequest{Name: "analytics-api"}, nil)
	s.service("analytics-api", "production", apitypes.CreateServiceRequest{Name: "collector", Image: "ghcr.io/acme/collector:0.9.2", Port: 8000})
	s.call(http.MethodPost, "/projects/analytics-api/archive", nil, nil)
}

// people adds members, an invite, tokens, org settings and a second org.
func (s *seeder) people() {
	s.call(http.MethodPost, "/users", apitypes.CreateUserRequest{Email: "linus@acme.example", Name: "Linus Berg", Password: rand.Text(), Role: apitypes.ScopeDeploy}, nil)
	s.call(http.MethodPost, "/users", apitypes.CreateUserRequest{Email: "grace@acme.example", Name: "Grace Hopper", Password: rand.Text(), Role: apitypes.ScopeRead}, nil)
	s.call(http.MethodPost, "/org/invites", apitypes.CreateInviteRequest{Email: "margaret@acme.example", Role: apitypes.ScopeDeploy}, nil)
	s.call(http.MethodPost, "/tokens", apitypes.CreateTokenRequest{Name: "github-actions", Scope: apitypes.ScopeDeploy, Project: "shop", ExpiresInDays: 90}, nil)
	s.call(http.MethodPost, "/tokens", apitypes.CreateTokenRequest{Name: "grafana", Scope: apitypes.ScopeRead}, nil)
	s.call(http.MethodPut, "/org/registry-credentials/ghcr.io", apitypes.SetRegistryCredentialRequest{Username: "acme-bot", Password: "ghp_demo" + rand.Text()}, nil)
	s.call(http.MethodPut, "/org/variables/SENTRY_DSN", apitypes.SetOrgVariableRequest{Value: "https://demo@o1.ingest.sentry.io/1"}, nil)
	// Email alerts: the demo has no mail server, so nothing is sent.
	s.call(http.MethodPost, "/alerts/channels", apitypes.CreateAlertChannelRequest{Name: "ops", Kind: apitypes.ChannelEmail, To: []string{"ops@acme.example"}}, nil)
	s.call(http.MethodPost, "/orgs", apitypes.CreateOrgRequest{Slug: "side-projects", Name: "Side projects"}, nil)
}

// analytics writes a day of the shop's visits to the spool, then turns
// analytics on for its web service, which starts the collector reading
// them.
func (s *seeder) analytics(spool string) {
	if s.err != nil {
		return
	}
	// The demo's first and only site gets ID 1.
	if s.err = writeVisits(spool, visitsFor{Site: 1, Service: s.webService, Release: s.webRelease, Host: shopHost}, time.Now()); s.err != nil {
		return
	}
	s.call(http.MethodPut, "/projects/shop/environments/production/services/web/analytics", nil, nil)
	var sites []apitypes.Site
	s.call(http.MethodGet, "/projects/shop/sites", nil, &sites)
	if s.err != nil || len(sites) == 0 {
		return
	}
	site := "/projects/shop/sites/" + url.PathEscape(sites[0].Name)
	s.call(http.MethodPost, site+"/funnels", apitypes.CreateFunnelRequest{Name: "Checkout", Steps: []apitypes.GoalStep{
		{Kind: "page", Match: "/products"}, {Kind: "page", Match: "/cart"}, {Kind: "event", Match: "purchase"},
	}}, nil)
	s.call(http.MethodPost, site+"/goals", apitypes.CreateGoalRequest{Name: "Purchase", GoalStep: apitypes.GoalStep{Kind: "event", Match: "purchase"}}, nil)
}

// statusPages publishes the shop's status page with an open incident.
func (s *seeder) statusPages() {
	s.call(http.MethodPost, "/status-pages", apitypes.CreateStatusPageRequest{Slug: "shop", Title: "Shop status"}, nil)
	s.call(http.MethodPut, "/status-pages/shop/components", apitypes.SetStatusComponentsRequest{Components: []apitypes.StatusComponentInput{
		{Name: "Storefront", Group: "Shop", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/web"}}},
		{Name: "API", Group: "Shop", Sources: []apitypes.StatusSourceRef{{Service: "shop/production/api"}}},
		{Name: "Blog", Sources: []apitypes.StatusSourceRef{{Service: "blog/production/ghost"}}},
	}}, nil)
	published := true
	s.call(http.MethodPatch, "/status-pages/shop", apitypes.UpdateStatusPageRequest{Published: &published}, nil)
	s.call(http.MethodPost, "/status-pages/shop/incidents", apitypes.OpenIncidentRequest{
		Title: "Slow checkout for some customers", Message: "Payments take longer than usual. We are looking into it.",
		Components: []apitypes.StatusImpact{{Component: "Storefront", Impact: apitypes.StatusDegraded}},
	}, nil)
}

// lockBlog freezes the blog's production for a launch, then has a deploy
// token ask to restart it: an access request waiting for a person.
func (s *seeder) lockBlog() {
	s.call(http.MethodPost, "/projects/blog/environments/production/lock", apitypes.LockEnvironmentRequest{Reason: "Launch week freeze"}, nil)
	var tok apitypes.CreatedToken
	s.call(http.MethodPost, "/tokens", apitypes.CreateTokenRequest{Name: "deploy-bot", Scope: apitypes.ScopeDeploy, Project: "blog"}, &tok)
	if s.err != nil {
		return
	}
	bot := &client{base: s.c.base, http: &http.Client{Timeout: 30 * time.Second}, token: tok.Secret}
	// A locked environment turns the token's restart into a request: 423.
	err := bot.do(s.ctx, http.MethodPost, svc("blog", "production", "ghost")+"/restart", nil, nil)
	if ae, ok := errors.AsType[*apiError](err); !ok || ae.Status != http.StatusLocked {
		s.err = fmt.Errorf("file the blog's access request: want 423 Locked, got %w", err)
	}
}
