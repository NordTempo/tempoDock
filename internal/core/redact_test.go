package core

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/node"
)

// The fake's pull log echoes the image reference, standing in for a build
// that echoes a build argument.
const (
	leakedKey   = "hunter22-token"
	leakedImage = "registry.example.com/app:" + leakedKey
)

// readerActor may read everything in the test org but reveal nothing.
var readerActor = Actor{Kind: "token", OrgID: 1, Name: "ro", Scope: apitypes.ScopeRead}

// secretService creates web with a secret of its own, one through a
// reference to an org variable and a short value, deploys it and has it
// print them all.
func secretService(t *testing.T, h *harness) ServiceRef {
	t.Helper()
	web := h.service("web", apitypes.CreateServiceRequest{})
	if _, err := h.c.SetOrgVariable(h.ctx, testSystem, "STRIPE_KEY", "sk_live_org123"); err != nil {
		t.Fatal(err)
	}
	set := map[string]string{"API_KEY": leakedKey, "STRIPE": "${{org.STRIPE_KEY}}", "MODE": "on"}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: set}); err != nil {
		t.Fatal(err)
	}
	if rel := h.deploy(web, leakedImage); rel.Status != apitypes.ReleaseActive {
		t.Fatalf("release is %s", rel.Status)
	}
	h.rt.AddLogs(h.ctr(web, 1), "API_KEY="+leakedKey+" stripe=sk_live_org123 mode=on")
	return web
}

// serviceLogTexts reads a service's active release's container output as a.
func serviceLogTexts(t *testing.T, h *harness, a Actor, ref ServiceRef) []string {
	t.Helper()
	stream, err := h.c.ServiceLogs(h.ctx, a, ref, LogQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	err = stream(h.ctx, func() {}, func(ls ...apitypes.LogLine) error {
		for _, l := range ls {
			got = append(got, l.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Someone who can't reveal secret values sees them masked in container
// output; an admin sees it as printed.
func TestServiceLogsMaskSecrets(t *testing.T) {
	h := newHarness(t)
	web := secretService(t, h)
	if got := serviceLogTexts(t, h, readerActor, web); !slices.Equal(got, []string{"API_KEY=***** stripe=***** mode=on"}) {
		t.Errorf("reader sees %q", got)
	}
	deployer := readerActor
	deployer.Scope = apitypes.ScopeDeploy
	if got := serviceLogTexts(t, h, deployer, web); !slices.Equal(got, []string{"API_KEY=***** stripe=***** mode=on"}) {
		t.Errorf("deployer sees %q", got)
	}
	if got := serviceLogTexts(t, h, testSystem, web); !slices.Equal(got, []string{"API_KEY=" + leakedKey + " stripe=sk_live_org123 mode=on"}) {
		t.Errorf("admin sees %q", got)
	}
}

// The lines kept from a failed release's container are masked too.
func TestSavedLogsMaskSecrets(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	set := map[string]string{"API_KEY": leakedKey}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: set}); err != nil {
		t.Fatal(err)
	}
	release := h.rt.HoldHealthChecks()
	defer release()
	rel, err := h.c.DeployImage(h.ctx, testSystem, web, apitypes.DeployImageRequest{Image: "app:1"})
	if err != nil {
		t.Fatal(err)
	}
	h.waitStatus(web, rel.Number, apitypes.ReleaseHealthChecking)
	h.rt.AddLogs(h.ctr(web, 1), "bad key "+leakedKey)
	h.rt.FailHealthCheck(func(node.ContainerSpec) error { return errors.New("connection refused") })
	release()
	if r := h.wait(web, rel.Number); r.Status != apitypes.ReleaseFailed {
		t.Fatalf("release %s", r.Status)
	}
	stream, err := h.c.ServiceLogs(h.ctx, readerActor, web, LogQuery{Release: rel.Number})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	_ = stream(h.ctx, func() {}, func(ls ...apitypes.LogLine) error {
		for _, l := range ls {
			got = append(got, l.Text)
		}
		return nil
	})
	if !slices.Equal(got, []string{"bad key *****"}) {
		t.Errorf("reader sees %q", got)
	}
}

// Build logs are masked the same way, whether read whole or followed.
func TestBuildLogMasksSecrets(t *testing.T) {
	h := newHarness(t)
	web := secretService(t, h)
	read := func(a Actor) string {
		t.Helper()
		f, err := h.c.BuildLog(h.ctx, a, web, 1)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		b, err := io.ReadAll(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	follow := func(a Actor) []string {
		t.Helper()
		stream, err := h.c.FollowBuildLog(h.ctx, a, web, 1)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		err = stream(h.ctx, func() {}, func(ls ...apitypes.BuildLogLine) error {
			for _, l := range ls {
				got = append(got, l.Text)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := read(readerActor); strings.Contains(got, leakedKey) || !strings.Contains(got, "Pulled registry.example.com/app:*****\n") {
		t.Errorf("reader reads %q", got)
	}
	if got := follow(readerActor); !slices.Contains(got, "Pulled registry.example.com/app:*****") || slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, leakedKey) }) {
		t.Errorf("reader follows %q", got)
	}
	if got := read(testSystem); !strings.Contains(got, "Pulled "+leakedImage+"\n") {
		t.Errorf("admin reads %q", got)
	}
	if got := follow(testSystem); !slices.Contains(got, "Pulled "+leakedImage) {
		t.Errorf("admin follows %q", got)
	}
}

// Live build log lines (the event stream `dock deploy` follows) reach
// readers masked and admins as written, each exactly once.
func TestBuildLogEventsMaskSecrets(t *testing.T) {
	h := newHarness(t)
	web := h.service("web", apitypes.CreateServiceRequest{})
	set := map[string]string{"API_KEY": leakedKey}
	if _, err := h.c.SetVariables(h.ctx, testSystem, web.Env(), apitypes.SetVariablesRequest{Service: "web", Set: set}); err != nil {
		t.Fatal(err)
	}
	watch := func(a Actor) func() []string {
		stream, err := h.c.Events(h.ctx, a, EventFilter{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(h.ctx)
		var mu sync.Mutex
		var lines []string
		ready, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			_ = stream(ctx, func() { close(ready) }, func(evs ...LiveEvent) error {
				mu.Lock()
				defer mu.Unlock()
				for _, e := range evs {
					if e.BuildLog != nil {
						lines = append(lines, e.BuildLog.Text)
					}
				}
				return nil
			})
		}()
		<-ready
		// got waits for the pull line, then stops watching.
		return func() []string {
			defer func() { cancel(); <-done }()
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
				mu.Lock()
				got := slices.Clone(lines)
				mu.Unlock()
				if slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "Pulled") }) {
					return got
				}
			}
			t.Fatal("no build log events")
			return nil
		}
	}
	reader, admin := watch(readerActor), watch(testSystem)
	h.deploy(web, leakedImage)
	if got := reader(); !slices.Equal(got, []string{"Pulled registry.example.com/app:*****"}) {
		t.Errorf("reader got %q", got)
	}
	if got := admin(); !slices.Equal(got, []string{"Pulled " + leakedImage}) {
		t.Errorf("admin got %q", got)
	}
}

// A secret split across reads of the underlying file is still masked, as
// is one at the very end; text between secrets comes through unchanged.
func TestRedactReaderAcrossReads(t *testing.T) {
	red := newRedactor(map[string]string{"A": "s3cr3t-value", "B": "s3cr3t-value-longer", "PORT": "8080123", "C": "tiny"})
	in := "start s3cr3t-value mid s3cr3t-value-longer x\ntiny 8080123 s3cr3t-value"
	want := "start ***** mid ***** x\ntiny 8080123 *****"
	for name, src := range map[string]io.Reader{
		"one byte": iotest.OneByteReader(strings.NewReader(in)),
		"halves":   iotest.HalfReader(strings.NewReader(in)),
		"whole":    strings.NewReader(in),
	} {
		got, err := io.ReadAll(red.reader(io.NopCloser(src)))
		if err != nil || string(got) != want {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	if err := iotest.TestReader(red.reader(io.NopCloser(strings.NewReader(in))), []byte(want)); err != nil {
		t.Error(err)
	}
}
