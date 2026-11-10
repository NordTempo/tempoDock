package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NordTempo/tempoDock/internal/apitypes"
	"github.com/NordTempo/tempoDock/internal/cli/output"
	"github.com/NordTempo/tempoDock/internal/clientconfig"
)

// Drains are added, listed without their secrets, tested and removed by
// name from the CLI; a failing test exits 7.
func TestLogDrainCommands(t *testing.T) {
	setup(t)
	srv := newTestServer(t)
	srv.core.AllowLoopbackLogDrains(true) // the endpoint listens on 127.0.0.1
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	t.Cleanup(sink.Close)
	t.Setenv(clientconfig.EnvURL, srv.url)
	t.Setenv(clientconfig.EnvToken, srv.tokens[apitypes.ScopeAdmin])
	exec(t, "project", "create", "shop")
	env := []string{"-p", "shop", "-e", "production"}

	d := ok[apitypes.LogDrain](t, append([]string{"logs", "drain", "add", "http", sink.URL + "/in?key=x", "--header", "X-Api-Key=s3cret"}, env...)...)
	if d.Name != "http" || d.Target != sink.URL {
		t.Fatalf("added = %+v", d)
	}
	ok[apitypes.LogDrain](t, append([]string{"logs", "drain", "add", "http", sink.URL, "--name", "nokey"}, env...)...)
	drains := ok[[]apitypes.LogDrain](t, append([]string{"logs", "drain", "list"}, env...)...)
	if len(drains) != 2 || drains[0].Name != "http" || strings.Contains(drains[0].Target, "key") {
		t.Fatalf("drains = %+v", drains)
	}
	if res := ok[apitypes.LogDrainTest](t, append([]string{"logs", "drain", "test", "http"}, env...)...); !res.OK || res.Drain.Status.Sent != 1 {
		t.Fatalf("test = %+v", res)
	}
	if code, _, stderr := exec(t, append([]string{"logs", "drain", "test", "nokey"}, env...)...); code != int(output.ExitUnreachable) || !strings.Contains(stderr, "401") {
		t.Fatalf("failing test: exit %d %q", code, stderr)
	}
	exec(t, append([]string{"logs", "drain", "remove", itoa(d.ID), "--yes"}, env...)...)
	if code, _, _ := exec(t, append([]string{"logs", "drain", "remove", "http", "--yes"}, env...)...); code != int(output.ExitNotFound) {
		t.Fatalf("removing a removed drain: exit %d", code)
	}
}
