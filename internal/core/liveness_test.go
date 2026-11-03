package core

import (
	"testing"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
	"github.com/NordTempo/tempoDock/internal/repoconfig"
	"github.com/NordTempo/tempoDock/internal/store"
)

func TestHealthCheckSettingsValidation(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	tests := []struct {
		name string
		req  apitypes.UpdateServiceRequest
		ok   bool
	}{
		{"interval off", apitypes.UpdateServiceRequest{HealthCheckIntervalSeconds: new(-1)}, true},
		{"interval max", apitypes.UpdateServiceRequest{HealthCheckIntervalSeconds: new(3600)}, true},
		{"interval below off", apitypes.UpdateServiceRequest{HealthCheckIntervalSeconds: new(-2)}, false},
		{"interval too long", apitypes.UpdateServiceRequest{HealthCheckIntervalSeconds: new(3601)}, false},
		{"probe timeout max", apitypes.UpdateServiceRequest{HealthCheckProbeTimeoutSeconds: new(300)}, true},
		{"probe timeout negative", apitypes.UpdateServiceRequest{HealthCheckProbeTimeoutSeconds: new(-1)}, false},
		{"probe timeout too long", apitypes.UpdateServiceRequest{HealthCheckProbeTimeoutSeconds: new(301)}, false},
		{"retries max", apitypes.UpdateServiceRequest{HealthCheckRetries: new(100)}, true},
		{"retries too many", apitypes.UpdateServiceRequest{HealthCheckRetries: new(101)}, false},
		{"start period max", apitypes.UpdateServiceRequest{HealthCheckStartPeriodSeconds: new(3600)}, true},
		{"start period negative", apitypes.UpdateServiceRequest{HealthCheckStartPeriodSeconds: new(-1)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.c.UpdateService(h.ctx, testSystem, web, tt.req)
			if tt.ok != (err == nil) || (err != nil && CodeOf(err) != apitypes.CodeValidation) {
				t.Fatalf("ok = %v, err = %v", tt.ok, err)
			}
		})
	}
	if _, err := h.c.CreateService(h.ctx, testSystem, web.Env(), apitypes.CreateServiceRequest{Name: "api", HealthCheckRetries: 101}); CodeOf(err) != apitypes.CodeValidation {
		t.Fatalf("create with 101 retries: %v", err)
	}
}

// A staged health check setting shows as a change, can be discarded, and
// once applied reaches the new release's container.
func TestStagedHealthCheckSetting(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{Port: 3000})
	h.deploy(web, "app:1")

	if _, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{HealthCheckIntervalSeconds: new(10)}); err != nil {
		t.Fatal(err)
	}
	pending, err := h.c.GetChanges(h.ctx, testSystem, web.Env(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.Settings) != 1 || pending.Settings[0] != (apitypes.SettingChange{Service: "web", Field: "health_check_interval_seconds", Old: "0", New: "10"}) {
		t.Fatalf("staged: %+v", pending.Settings)
	}
	discarded, err := h.c.DiscardChanges(h.ctx, testSystem, web.Env(), apitypes.DiscardChangesRequest{
		Settings: []apitypes.SettingRef{{Service: "web", Field: "health_check_interval_seconds"}},
	})
	if err != nil || discarded.Count != 0 {
		t.Fatalf("discard: %+v, %v", discarded, err)
	}

	if _, err := h.c.UpdateService(h.ctx, testSystem, web, apitypes.UpdateServiceRequest{HealthCheckIntervalSeconds: new(10), Apply: true}); err != nil {
		t.Fatal(err)
	}
	rels, err := h.c.ListReleases(h.ctx, testSystem, web, 10)
	if err != nil || len(rels) != 2 {
		t.Fatalf("a health check change needs a release: %d, %v", len(rels), err)
	}
	h.wait(web, rels[0].Number)
	svc, err := h.c.GetService(h.ctx, testSystem, web)
	if err != nil || svc.HealthCheckIntervalSeconds != 10 {
		t.Fatalf("live interval = %d, %v", svc.HealthCheckIntervalSeconds, err)
	}
	spec, ok := h.rt.Spec(h.ctr(web, rels[0].Number))
	if !ok || spec.Health == nil || spec.Health.Interval != 10*time.Second || spec.Health.Port != 3000 {
		t.Fatalf("container health: %+v", spec.Health)
	}
}

func TestResolveConfigHealthCheckDefaults(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{HealthCheckTimeoutSeconds: 45})
	ss, err := h.c.serviceFor(h.ctx, testSystem, web, apitypes.ScopeRead, "testing")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := h.c.resolveConfig(h.ctx, h.store.Queries, ss, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HealthCheckInterval != 30 || cfg.HealthCheckProbeTimeout != 5 || cfg.HealthCheckRetries != 3 || cfg.HealthCheckStartPeriod != 45 {
		t.Fatalf("defaults: %+v", cfg)
	}

	// dock.toml wins over the service's settings.
	repo := &repoDeploy{HealthCheckIntervalSeconds: livenessOff, HealthCheckRetries: 7}
	cfg, err = h.c.resolveConfig(h.ctx, h.store.Queries, ss, repo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HealthCheckInterval != livenessOff || cfg.HealthCheckRetries != 7 {
		t.Fatalf("from dock.toml: %+v", cfg)
	}
}

func TestLiveness(t *testing.T) {
	want := func(port int, path string) *node.LivenessCheck {
		return &node.LivenessCheck{
			Port: port, Path: path, Interval: 30 * time.Second, Timeout: 5 * time.Second, Retries: 3,
			StartPeriod: 60 * time.Second, StartInterval: 2 * time.Second,
		}
	}
	tests := []struct {
		name string
		cfg  releaseConfig
		want *node.LivenessCheck
	}{
		// Zeros, as in a config sealed before the ongoing check existed.
		{"published HTTP", releaseConfig{Port: 3000, Published: true, HealthCheckPath: "/up", HealthCheckTimeout: 60}, want(3000, "/up")},
		{"published TCP", releaseConfig{Port: 8080, Published: true, HealthCheckTimeout: 60}, want(8080, "")},
		{"template database", releaseConfig{Port: 8080, HealthPort: 5432, HealthCheckTimeout: 60}, want(5432, "")},
		{"worker", releaseConfig{Port: 8080, HealthCheckTimeout: 60}, nil},
		{"off", releaseConfig{Port: 3000, Published: true, HealthCheckInterval: livenessOff}, nil},
		{"set", releaseConfig{
			Port: 3000, Published: true, HealthCheckInterval: 1, HealthCheckProbeTimeout: 9, HealthCheckRetries: 4, HealthCheckStartPeriod: 20, HealthCheckTimeout: 60,
		}, &node.LivenessCheck{Port: 3000, Interval: time.Second, Timeout: 9 * time.Second, Retries: 4, StartPeriod: 20 * time.Second, StartInterval: time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := liveness(tt.cfg)
			if (got == nil) != (tt.want == nil) || (got != nil && *got != *tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// dock.toml's `health_check_interval = "off"` reaches the service settings
// as -1, so the release gets no ongoing check.
func TestRepoHealthCheckIntervalOff(t *testing.T) {
	rc, err := repoconfig.Parse([]byte("[deploy]\nhealth_check_interval = \"off\"\nhealth_check_retries = 5\n"))
	if err != nil {
		t.Fatal(err)
	}
	var s store.Service
	repoDeployOf(&rc).apply(&s)
	if s.HealthCheckIntervalSeconds != livenessOff || s.HealthCheckRetries != 5 {
		t.Fatalf("interval %d, retries %d; want %d, 5", s.HealthCheckIntervalSeconds, s.HealthCheckRetries, livenessOff)
	}
}
